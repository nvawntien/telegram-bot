package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/nvawntien/telegram-bot/internal/app"
	"github.com/nvawntien/telegram-bot/internal/domain"
	"github.com/nvawntien/telegram-bot/internal/postgres/generated"
)

func (s *AppStore) AcceptPayment(ctx context.Context, command app.AcceptPaymentCommand, acceptedAt time.Time, reservationTTL time.Duration) (app.PaymentAcceptanceResult, error) {
	var result app.PaymentAcceptanceResult
	err := s.transactor.WithinTransaction(ctx, func(ctx context.Context, queries *generated.Queries) error {
		if command.PaymentEventID > 0 {
			event, err := queries.LockPaymentEvent(ctx, command.PaymentEventID)
			if err != nil {
				return err
			}
			if event.ProcessingStatus == "completed" || event.ProcessingStatus == "review" {
				result.Decision = "duplicate"
				return nil
			}
			if event.ProcessingStatus != "processing" {
				return app.ErrConflict
			}
		}

		existing, err := queries.GetPaymentByProviderTransaction(ctx, generated.GetPaymentByProviderTransactionParams{
			Provider: command.Provider, ProviderTransactionID: optionalText(command.ProviderTransactionID),
		})
		if err == nil {
			return handleExistingPayment(ctx, queries, command, existing, acceptedAt, &result)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}

		order, err := queries.LockOrderByPaymentReference(ctx, command.Reference)
		if errors.Is(err, pgx.ErrNoRows) {
			return recordPaymentReview(ctx, queries, command, nil, 0, "unknown_reference", acceptedAt, &result)
		}
		if err != nil {
			return err
		}
		result.Target = "order"
		result.OrderID = order.ID

		reviewReason := orderReviewReason(order, command, acceptedAt)
		if reviewReason != "" {
			payment, err := insertOrderPayment(ctx, queries, command, order, "review", acceptedAt)
			if err != nil {
				return err
			}
			return recordPaymentReview(ctx, queries, command, &payment, order.ID, reviewReason, acceptedAt, &result)
		}

		payment, err := insertOrderPayment(ctx, queries, command, order, "confirmed", acceptedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			existing, findErr := queries.GetPaymentByProviderTransaction(ctx, generated.GetPaymentByProviderTransactionParams{
				Provider: command.Provider, ProviderTransactionID: optionalText(command.ProviderTransactionID),
			})
			if findErr != nil {
				return findErr
			}
			return handleExistingPayment(ctx, queries, command, existing, acceptedAt, &result)
		}
		if err != nil {
			return err
		}
		result.PaymentID = payment.ID

		paidOrder, err := queries.MarkOrderPaidGuarded(ctx, generated.MarkOrderPaidGuardedParams{
			PaidAt: requiredTimestamp(acceptedAt), ID: order.ID, ExpectedVersion: order.Version,
		})
		if err != nil {
			return err
		}
		if err := insertPaymentOrderHistory(ctx, queries, command, order.ID, domain.OrderStatusPendingPayment, domain.OrderStatusPaid, "payment_accepted"); err != nil {
			return err
		}
		reservingOrder, err := queries.UpdateOrderStatusGuarded(ctx, generated.UpdateOrderStatusGuardedParams{
			NewStatus: string(domain.OrderStatusReserving), ID: order.ID,
			ExpectedStatus: string(domain.OrderStatusPaid), ExpectedVersion: paidOrder.Version,
		})
		if err != nil {
			return err
		}
		if err := insertPaymentOrderHistory(ctx, queries, command, order.ID, domain.OrderStatusPaid, domain.OrderStatusReserving, "inventory_claim_started"); err != nil {
			return err
		}
		inventoryIDs, err := queries.ClaimExactAvailableInventory(ctx, generated.ClaimExactAvailableInventoryParams{
			OrderID: requiredInt8(order.ID), ReservedUntil: requiredTimestamp(acceptedAt.Add(reservationTTL)),
			ProductID: order.ProductID, Quantity: order.Quantity,
		})
		if err != nil {
			return err
		}
		if len(inventoryIDs) != int(order.Quantity) {
			outOfStock, err := queries.UpdateOrderStatusGuarded(ctx, generated.UpdateOrderStatusGuardedParams{
				NewStatus: string(domain.OrderStatusOutOfStock), ID: order.ID,
				ExpectedStatus: string(domain.OrderStatusReserving), ExpectedVersion: reservingOrder.Version,
			})
			if err != nil {
				return err
			}
			_ = outOfStock
			if err := insertPaymentOrderHistory(ctx, queries, command, order.ID, domain.OrderStatusReserving, domain.OrderStatusOutOfStock, "post_payment_inventory_insufficient"); err != nil {
				return err
			}
			return recordPaymentReview(ctx, queries, command, &payment, order.ID, "out_of_stock", acceptedAt, &result)
		}
		for _, inventoryID := range inventoryIDs {
			if err := queries.InsertOrderInventoryMapping(ctx, generated.InsertOrderInventoryMappingParams{OrderID: order.ID, OrderItemID: order.OrderItemID, InventoryItemID: inventoryID}); err != nil {
				return err
			}
		}
		allocation, err := queries.InsertPaymentAllocation(ctx, generated.InsertPaymentAllocationParams{
			PaymentID: payment.ID, TargetType: "order", TargetID: order.ID, AmountVnd: command.Amount.Int64(),
		})
		if err != nil {
			return err
		}
		_ = allocation
		result.Decision = "accepted"
		result.Claimed = len(inventoryIDs)
		if err := completePaymentEvent(ctx, queries, command.PaymentEventID, "completed", order.ID, 0, acceptedAt, "", ""); err != nil {
			return err
		}
		return insertPaymentAudit(ctx, queries, command, payment.ID, order.ID, result.Decision, "")
	})
	return result, err
}

func orderReviewReason(order generated.LockOrderByPaymentReferenceRow, command app.AcceptPaymentCommand, acceptedAt time.Time) string {
	if order.TotalVnd != command.Amount.Int64() {
		return "amount_mismatch"
	}
	if order.Currency != command.Currency {
		return "currency_mismatch"
	}
	status := domain.OrderStatus(order.Status)
	if status == domain.OrderStatusExpired || status == domain.OrderStatusCancelled || !order.ExpiresAt.Time.After(acceptedAt) {
		return "late_or_cancelled"
	}
	if status != domain.OrderStatusPendingPayment {
		return "competing_payment"
	}
	return ""
}

func insertOrderPayment(ctx context.Context, queries *generated.Queries, command app.AcceptPaymentCommand, order generated.LockOrderByPaymentReferenceRow, status string, acceptedAt time.Time) (generated.Payment, error) {
	confirmedAt := pgtype.Timestamptz{}
	if status == "confirmed" {
		confirmedAt = requiredTimestamp(acceptedAt)
	}
	return queries.InsertPayment(ctx, generated.InsertPaymentParams{
		OrderID: requiredInt8(order.ID), UserID: order.UserID, Purpose: "order", Provider: command.Provider,
		ProviderTransactionID: optionalText(command.ProviderTransactionID), PaymentReference: command.Reference,
		AmountVnd: command.Amount.Int64(), Status: status, ConfirmedAt: confirmedAt, OccurredAt: requiredTimestamp(command.OccurredAt),
	})
}

func handleExistingPayment(ctx context.Context, queries *generated.Queries, command app.AcceptPaymentCommand, existing generated.Payment, acceptedAt time.Time, result *app.PaymentAcceptanceResult) error {
	result.PaymentID = existing.ID
	result.OrderID = existing.OrderID.Int64
	result.Target = existing.Purpose
	if existing.PaymentReference != command.Reference || existing.AmountVnd != command.Amount.Int64() || existing.Currency != command.Currency {
		return recordPaymentReview(ctx, queries, command, &existing, existing.OrderID.Int64, "transaction_conflict", acceptedAt, result)
	}
	allocation, err := queries.GetPaymentAllocation(ctx, existing.ID)
	if err == nil {
		result.Decision = "duplicate"
		result.Target = allocation.TargetType
		if allocation.TargetType == "order" {
			result.OrderID = allocation.TargetID
		}
		return completePaymentEvent(ctx, queries, command.PaymentEventID, "completed", result.OrderID, result.TopupID, acceptedAt, "", "")
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	return recordPaymentReview(ctx, queries, command, &existing, existing.OrderID.Int64, "duplicate_unallocated", acceptedAt, result)
}

func recordPaymentReview(ctx context.Context, queries *generated.Queries, command app.AcceptPaymentCommand, payment *generated.Payment, orderID int64, reason string, acceptedAt time.Time, result *app.PaymentAcceptanceResult) error {
	paymentID := int64(0)
	if payment != nil {
		paymentID = payment.ID
		result.PaymentID = payment.ID
	}
	_, err := queries.InsertPaymentReviewCase(ctx, generated.InsertPaymentReviewCaseParams{
		PaymentEventID: optionalInt8(command.PaymentEventID), PaymentID: optionalInt8(paymentID), OrderID: optionalInt8(orderID),
		Provider: command.Provider, ProviderTransactionID: optionalText(command.ProviderTransactionID),
		PaymentReference: command.Reference, AmountVnd: command.Amount.Int64(), Currency: command.Currency,
		OccurredAt: requiredTimestamp(command.OccurredAt), Reason: reason,
	})
	if err != nil {
		return err
	}
	result.Decision = "review"
	result.Reason = reason
	if result.Target == "" {
		result.Target = "unmatched"
	}
	if err := completePaymentEvent(ctx, queries, command.PaymentEventID, "review", orderID, 0, acceptedAt, reason, reason); err != nil {
		return err
	}
	return insertPaymentAudit(ctx, queries, command, paymentID, orderID, "review", reason)
}

func completePaymentEvent(ctx context.Context, queries *generated.Queries, eventID int64, status string, orderID, topupID int64, processedAt time.Time, errorCode, errorDetail string) error {
	if eventID <= 0 {
		return nil
	}
	_, err := queries.CompletePaymentEvent(ctx, generated.CompletePaymentEventParams{
		ProcessingStatus: status, RelatedOrderID: optionalInt8(orderID), RelatedWalletTopupID: optionalInt8(topupID),
		ProcessedAt: requiredTimestamp(processedAt), ProcessingError: optionalText(errorDetail),
		LastErrorCode: optionalText(errorCode), ID: eventID,
	})
	return err
}

func insertPaymentOrderHistory(ctx context.Context, queries *generated.Queries, command app.AcceptPaymentCommand, orderID int64, from, to domain.OrderStatus, reason string) error {
	_, err := queries.InsertOrderStatusHistory(ctx, generated.InsertOrderStatusHistoryParams{
		OrderID: orderID, FromStatus: optionalText(string(from)), ToStatus: string(to), ReasonCode: optionalText(reason),
		ActorType: command.Actor.Type, ActorID: optionalInt8(command.Actor.ID), RequestID: optionalText(command.RequestID),
	})
	return err
}

func insertPaymentAudit(ctx context.Context, queries *generated.Queries, command app.AcceptPaymentCommand, paymentID, orderID int64, decision, reason string) error {
	actorType := command.Actor.Type
	actorID := optionalInt8(command.Actor.ID)
	if actorType == "provider" {
		actorType = "system"
		actorID = pgtype.Int8{}
	}
	after, err := json.Marshal(map[string]any{"payment_id": paymentID, "order_id": orderID, "decision": decision, "reason": reason})
	if err != nil {
		return err
	}
	_, err = queries.InsertAuditLog(ctx, generated.InsertAuditLogParams{
		ActorType: actorType, ActorID: actorID, Action: "payment.acceptance_decided", ResourceType: "payment",
		ResourceID: optionalInt8(paymentID), AfterData: after, RequestID: optionalText(command.RequestID),
	})
	return err
}

func requiredInt8(value int64) pgtype.Int8 { return pgtype.Int8{Int64: value, Valid: true} }

func optionalInt8(value int64) pgtype.Int8 { return pgtype.Int8{Int64: value, Valid: value > 0} }

func requiredTimestamp(value time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: value, Valid: true}
}

var _ app.PaymentAcceptanceRepository = (*AppStore)(nil)
