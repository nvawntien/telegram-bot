package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/nvawntien/telegram-bot/internal/app"
	"github.com/nvawntien/telegram-bot/internal/domain"
	"github.com/nvawntien/telegram-bot/internal/postgres/generated"
)

func (s *AppStore) ClaimInventory(ctx context.Context, request app.InventoryClaimRequest) (app.InventoryClaimResult, error) {
	result := app.InventoryClaimResult{OrderID: request.OrderID}
	err := s.transactor.WithinTransaction(ctx, func(ctx context.Context, queries *generated.Queries) error {
		order, err := queries.LockOrderForUpdate(ctx, request.OrderID)
		if errors.Is(err, pgx.ErrNoRows) {
			return app.ErrNotFound
		}
		if err != nil {
			return err
		}
		if domain.OrderStatus(order.Status) != domain.OrderStatusReserving {
			return app.ErrInvalidInventoryState
		}
		orderItem, err := queries.LockOrderItemForInventory(ctx, generated.LockOrderItemForInventoryParams{
			OrderItemID: request.OrderItemID, OrderID: request.OrderID, ProductID: request.ProductID,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return app.ErrNotFound
		}
		if err != nil {
			return err
		}
		if orderItem.Quantity != request.Quantity {
			return app.ErrInvalidInput
		}
		ids, err := queries.ClaimAvailableInventory(ctx, generated.ClaimAvailableInventoryParams{
			OrderID:       pgtype.Int8{Int64: request.OrderID, Valid: true},
			ReservedUntil: pgtype.Timestamptz{Time: request.ReservedUntil, Valid: true},
			ProductID:     request.ProductID, Quantity: request.Quantity,
		})
		if err != nil {
			return err
		}
		if len(ids) != int(request.Quantity) {
			return app.ErrInsufficientInventory
		}
		for _, inventoryID := range ids {
			if err := queries.InsertOrderInventoryMapping(ctx, generated.InsertOrderInventoryMappingParams{
				OrderID: request.OrderID, OrderItemID: request.OrderItemID, InventoryItemID: inventoryID,
			}); err != nil {
				return err
			}
		}
		if err := insertSystemInventoryAudit(ctx, queries, "inventory.reserved", "order", request.OrderID,
			map[string]any{
				"order_id": request.OrderID, "order_item_id": request.OrderItemID,
				"product_id": request.ProductID, "claimed_count": len(ids), "inventory_item_ids": ids,
			}, request.RequestID); err != nil {
			return err
		}
		result.InventoryItemIDs = append([]int64(nil), ids...)
		result.Count = len(ids)
		return nil
	})
	return result, err
}

func (s *AppStore) ReleaseInventory(ctx context.Context, request app.InventoryReleaseRequest) (app.InventoryReleaseResult, error) {
	result := app.InventoryReleaseResult{OrderID: request.OrderID, Reason: request.Reason}
	err := s.transactor.WithinTransaction(ctx, func(ctx context.Context, queries *generated.Queries) error {
		order, err := queries.LockOrderForUpdate(ctx, request.OrderID)
		if errors.Is(err, pgx.ErrNoRows) {
			return app.ErrNotFound
		}
		if err != nil {
			return err
		}
		expectedReason, safe := domain.InventoryReleaseReasonForOrder(domain.OrderStatus(order.Status))
		if !safe {
			return app.ErrUnsafeReservationRelease
		}
		if request.Reason != expectedReason {
			return app.ErrInvalidInput
		}
		released, err := releaseInventoryForOrder(ctx, queries, request.OrderID, request.Reason, request.RequestID)
		if err != nil {
			return err
		}
		result.Released = released
		return nil
	})
	return result, err
}

func (s *AppStore) RecoverExpiredReservation(
	ctx context.Context,
	orderID int64,
	expiredAt time.Time,
	requestID string,
) (app.InventoryRecoveryResult, error) {
	result := app.InventoryRecoveryResult{OrderID: orderID}
	err := s.transactor.WithinTransaction(ctx, func(ctx context.Context, queries *generated.Queries) error {
		order, err := queries.LockOrderForUpdate(ctx, orderID)
		if errors.Is(err, pgx.ErrNoRows) {
			return app.ErrNotFound
		}
		if err != nil {
			return err
		}
		status := domain.OrderStatus(order.Status)
		result.OrderStatus = status
		expiredCount, err := queries.CountExpiredReservationsByOrder(ctx, generated.CountExpiredReservationsByOrderParams{
			OrderID:   pgtype.Int8{Int64: orderID, Valid: true},
			ExpiredAt: pgtype.Timestamptz{Time: expiredAt, Valid: true},
		})
		if err != nil || expiredCount == 0 {
			return err
		}
		reason, safe := domain.InventoryReleaseReasonForOrder(status)
		if safe {
			released, err := releaseInventoryForOrder(ctx, queries, orderID, reason, requestID)
			if err != nil {
				return err
			}
			result.Released = released
			return nil
		}
		_, err = queries.InsertInventoryRecoveryAuditOnce(ctx, generated.InsertInventoryRecoveryAuditOnceParams{
			OrderID: orderID, OrderStatus: order.Status, RequestID: optionalText(requestID),
		})
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		result.RecoveryRequired = true
		return nil
	})
	return result, err
}

func releaseInventoryForOrder(
	ctx context.Context,
	queries *generated.Queries,
	orderID int64,
	reason domain.InventoryReleaseReason,
	requestID string,
) (int, error) {
	ids, err := queries.ListReservedInventoryIDsByOrder(ctx, pgtype.Int8{Int64: orderID, Valid: true})
	if err != nil {
		return 0, err
	}
	if len(ids) == 0 {
		return 0, nil
	}
	released, err := queries.ReleaseReservedInventoryByOrder(ctx, pgtype.Int8{Int64: orderID, Valid: true})
	if err != nil {
		return 0, err
	}
	if len(released) != len(ids) {
		return 0, app.ErrReservationNotOwned
	}
	mappings, err := queries.MarkOrderInventoryMappingsReleased(ctx, generated.MarkOrderInventoryMappingsReleasedParams{
		ReleaseReason: optionalText(string(reason)), OrderID: orderID, InventoryItemIds: released,
	})
	if err != nil {
		return 0, err
	}
	if mappings != int64(len(released)) {
		return 0, app.ErrReservationNotOwned
	}
	if err := insertSystemInventoryAudit(ctx, queries, "inventory.released", "order", orderID,
		map[string]any{
			"order_id": orderID, "released_count": len(released),
			"inventory_item_ids": released, "reason": reason,
		}, requestID); err != nil {
		return 0, err
	}
	return len(released), nil
}

func insertSystemInventoryAudit(
	ctx context.Context,
	queries *generated.Queries,
	action, resourceType string,
	resourceID int64,
	after any,
	requestID string,
) error {
	afterJSON, err := marshalAuditValue(after)
	if err != nil {
		return err
	}
	_, err = queries.InsertAuditLog(ctx, generated.InsertAuditLogParams{
		ActorType: "system", Action: action, ResourceType: resourceType,
		ResourceID: pgtype.Int8{Int64: resourceID, Valid: true},
		AfterData:  afterJSON, RequestID: optionalText(requestID),
	})
	return err
}
