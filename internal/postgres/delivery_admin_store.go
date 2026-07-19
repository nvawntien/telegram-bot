package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/nvawntien/telegram-bot/internal/app"
	"github.com/nvawntien/telegram-bot/internal/domain"
	"github.com/nvawntien/telegram-bot/internal/postgres/generated"
)

func (s *AppStore) ListDeliveryReviews(ctx context.Context, adminTelegramID int64, offset, limit int32) ([]app.DeliveryReviewItem, int64, error) {
	if _, err := authorizePaymentAdmin(ctx, s.queries, adminTelegramID); err != nil {
		return nil, 0, err
	}
	total, err := s.queries.CountDeliveryReviewJobs(ctx)
	if err != nil {
		return nil, 0, err
	}
	rows, err := s.queries.ListDeliveryReviewJobs(ctx, generated.ListDeliveryReviewJobsParams{PageOffset: offset, PageLimit: limit})
	if err != nil {
		return nil, 0, err
	}
	items := make([]app.DeliveryReviewItem, 0, len(rows))
	for _, row := range rows {
		items = append(items, mapDeliveryReviewFields(
			row.ID, row.DeliveryOrderID.Int64, row.Status, row.Attempts, row.MaxAttempts,
			row.RecipientChatID.Int64, row.TelegramMessageID.Int64, row.LastErrorCode.String,
			row.LastErrorDetail.String, row.CreatedAt.Time, row.UpdatedAt.Time, row.Version,
			row.ProductName, row.Quantity,
		))
	}
	return items, total, nil
}

func (s *AppStore) GetDeliveryReview(ctx context.Context, adminTelegramID, jobID int64) (app.DeliveryReviewDetail, error) {
	if _, err := authorizePaymentAdmin(ctx, s.queries, adminTelegramID); err != nil {
		return app.DeliveryReviewDetail{}, err
	}
	row, err := s.queries.GetDeliveryReviewJob(ctx, jobID)
	if errors.Is(err, pgx.ErrNoRows) {
		return app.DeliveryReviewDetail{}, app.ErrNotFound
	}
	if err != nil {
		return app.DeliveryReviewDetail{}, err
	}
	attemptRows, err := s.queries.ListDeliveryAttemptEvents(ctx, requiredInt8(jobID))
	if err != nil {
		return app.DeliveryReviewDetail{}, err
	}
	detail := app.DeliveryReviewDetail{DeliveryReviewItem: mapDeliveryReviewFields(
		row.ID, row.DeliveryOrderID.Int64, row.Status, row.Attempts, row.MaxAttempts,
		row.RecipientChatID.Int64, row.TelegramMessageID.Int64, row.LastErrorCode.String,
		row.LastErrorDetail.String, row.CreatedAt.Time, row.UpdatedAt.Time, row.Version,
		row.ProductName, row.Quantity,
	)}
	detail.AttemptsHistory = make([]app.DeliveryAttemptView, 0, len(attemptRows))
	for _, attempt := range attemptRows {
		detail.AttemptsHistory = append(detail.AttemptsHistory, app.DeliveryAttemptView{
			Number: attempt.AttemptNumber, Status: attempt.Status,
			HTTPStatus: attempt.HttpStatus.Int32, TelegramErrorCode: attempt.TelegramErrorCode.Int32,
			RetryAfterSeconds: attempt.RetryAfterSeconds.Int32,
			TelegramChatID:    attempt.TelegramChatID.Int64, TelegramMessageID: attempt.TelegramMessageID.Int64,
			ErrorClass: attempt.ErrorClass.String, ErrorCode: attempt.ErrorCode.String,
			ErrorDetail: attempt.ErrorDetail.String, StartedAt: attempt.StartedAt.Time,
			FinishedAt: attempt.FinishedAt.Time,
		})
	}
	return detail, nil
}

func (s *AppStore) ReconcileDeliveryForAdmin(ctx context.Context, adminTelegramID int64, staleBefore time.Time) (app.DeliveryReconciliation, error) {
	if _, err := authorizePaymentAdmin(ctx, s.queries, adminTelegramID); err != nil {
		return app.DeliveryReconciliation{}, err
	}
	return s.ReconcileDeliveryState(ctx, staleBefore)
}

func (s *AppStore) DeliveryQueueDepthForAdmin(ctx context.Context, adminTelegramID int64) (map[string]int64, error) {
	if _, err := authorizePaymentAdmin(ctx, s.queries, adminTelegramID); err != nil {
		return nil, err
	}
	rows, err := s.queries.CountDeliveryJobsByStatus(ctx)
	if err != nil {
		return nil, err
	}
	depths := make(map[string]int64, len(rows))
	for _, row := range rows {
		depths[row.Status] = row.JobCount
	}
	return depths, nil
}

func (s *AppStore) RetryDelivery(ctx context.Context, command app.DeliveryResolutionCommand, resolvedAt time.Time) (app.DeliveryReviewItem, error) {
	var result app.DeliveryReviewItem
	err := s.transactor.WithinTransaction(ctx, func(ctx context.Context, queries *generated.Queries) error {
		admin, err := authorizePaymentAdmin(ctx, queries, command.AdminTelegramID)
		if err != nil {
			return err
		}
		if _, err := lockAndVerifySession(ctx, queries, admin.ID, command.Session, app.SessionDeliveryRetry); err != nil {
			return err
		}
		job, err := queries.LockDeliveryJob(ctx, command.JobID)
		if errors.Is(err, pgx.ErrNoRows) {
			return app.ErrDeliveryNotFound
		}
		if err != nil {
			return err
		}
		if job.Version != command.ExpectedVersion {
			return app.ErrStaleVersion
		}
		if job.Status != string(domain.DeliveryJobAmbiguous) && job.Status != string(domain.DeliveryJobManualReview) &&
			job.Status != string(domain.DeliveryJobPermanentFailed) && job.Status != string(domain.DeliveryJobRetryableFailed) {
			return app.ErrUnsafeDeliveryResolution
		}
		order, err := queries.LockOrderForDeliveryHandoff(ctx, job.DeliveryOrderID.Int64)
		if err != nil {
			return err
		}
		if order.Status == string(domain.OrderStatusDeliveryFailed) {
			if _, err := queries.UpdateOrderStatusGuarded(ctx, generated.UpdateOrderStatusGuardedParams{
				NewStatus: string(domain.OrderStatusDelivering), ID: order.ID,
				ExpectedStatus: string(domain.OrderStatusDeliveryFailed), ExpectedVersion: order.Version,
			}); err != nil {
				return err
			}
			if _, err := queries.InsertOrderStatusHistory(ctx, generated.InsertOrderStatusHistoryParams{
				OrderID: order.ID, FromStatus: optionalText(string(domain.OrderStatusDeliveryFailed)),
				ToStatus: string(domain.OrderStatusDelivering), ReasonCode: optionalText("manual_redelivery_verified"),
				ActorType: "admin", ActorID: requiredInt8(admin.ID), RequestID: optionalText(command.Meta.RequestID),
			}); err != nil {
				return err
			}
		} else if order.Status != string(domain.OrderStatusDelivering) {
			return app.ErrInvalidDeliveryState
		}
		updated, err := queries.ManualRetryDeliveryJob(ctx, generated.ManualRetryDeliveryJobParams{
			NextAttemptAt: requiredTimestamp(resolvedAt), Reason: optionalText(command.Reason),
			AdminID: requiredInt8(admin.ID), ResolvedAt: requiredTimestamp(resolvedAt),
			ID: job.ID, ExpectedVersion: job.Version,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return app.ErrStaleVersion
		}
		if err != nil {
			return err
		}
		if _, err := queries.InsertDeliveryAttemptEvent(ctx, generated.InsertDeliveryAttemptEventParams{
			OrderID: order.ID, DeliveryJobID: requiredInt8(job.ID), AttemptNumber: job.Attempts + 1,
			Status: "manual_resolution", ErrorClass: optionalText("manual"), ErrorCode: optionalText("verified_not_delivered"),
			ErrorDetail: optionalText(command.Reason), StartedAt: requiredTimestamp(resolvedAt), FinishedAt: requiredTimestamp(resolvedAt),
		}); err != nil {
			return err
		}
		if err := insertManualDeliveryAudit(ctx, queries, admin.ID, job, updated, "delivery.manual_retry", command); err != nil {
			return err
		}
		if err := finishSession(ctx, queries, admin.ID, command.Session); err != nil {
			return err
		}
		if err := completeReceipt(ctx, queries, command.Meta.UpdateID); err != nil {
			return err
		}
		result = deliveryReviewFromJob(updated, order.ProductName, order.Quantity)
		return nil
	})
	return result, mapConstraintError(err)
}

func (s *AppStore) CompleteDelivery(ctx context.Context, command app.DeliveryResolutionCommand, resolvedAt time.Time) (app.DeliveryReviewItem, error) {
	var result app.DeliveryReviewItem
	err := s.transactor.WithinTransaction(ctx, func(ctx context.Context, queries *generated.Queries) error {
		admin, err := authorizePaymentAdmin(ctx, queries, command.AdminTelegramID)
		if err != nil {
			return err
		}
		if _, err := lockAndVerifySession(ctx, queries, admin.ID, command.Session, app.SessionDeliveryComplete); err != nil {
			return err
		}
		job, err := queries.LockDeliveryJob(ctx, command.JobID)
		if errors.Is(err, pgx.ErrNoRows) {
			return app.ErrDeliveryNotFound
		}
		if err != nil {
			return err
		}
		if job.Version != command.ExpectedVersion {
			return app.ErrStaleVersion
		}
		if job.Status != string(domain.DeliveryJobAmbiguous) && job.Status != string(domain.DeliveryJobManualReview) {
			return app.ErrUnsafeDeliveryResolution
		}
		order, err := queries.LockOrderForDeliveryHandoff(ctx, job.DeliveryOrderID.Int64)
		if err != nil {
			return err
		}
		if order.Status != string(domain.OrderStatusDelivering) {
			return app.ErrInvalidDeliveryState
		}
		sold, err := queries.MarkExactReservedInventorySold(ctx, generated.MarkExactReservedInventorySoldParams{
			OrderID: requiredInt8(order.ID), ExpectedCount: order.Quantity,
		})
		if err != nil {
			return err
		}
		if len(sold) != int(order.Quantity) {
			return app.ErrDeliveryInventoryMismatch
		}
		if _, err := queries.MarkOrderDeliveredGuarded(ctx, generated.MarkOrderDeliveredGuardedParams{
			DeliveredAt: requiredTimestamp(resolvedAt), OrderID: order.ID, ExpectedVersion: order.Version,
		}); err != nil {
			return err
		}
		updated, err := queries.ManualCompleteDeliveryJob(ctx, generated.ManualCompleteDeliveryJobParams{
			TelegramMessageID: requiredInt8(command.TelegramMessageID), ResolvedAt: requiredTimestamp(resolvedAt),
			Reason: optionalText(command.Reason), AdminID: requiredInt8(admin.ID), ID: job.ID, ExpectedVersion: job.Version,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return app.ErrStaleVersion
		}
		if err != nil {
			return err
		}
		if _, err := queries.InsertDeliveryAttemptEvent(ctx, generated.InsertDeliveryAttemptEventParams{
			OrderID: order.ID, DeliveryJobID: requiredInt8(job.ID), AttemptNumber: job.Attempts + 1,
			Status: "manual_resolution", TelegramChatID: job.RecipientChatID,
			TelegramMessageID: requiredInt8(command.TelegramMessageID), ErrorClass: optionalText("manual"),
			ErrorCode: optionalText("verified_delivered"), ErrorDetail: optionalText(command.Reason),
			StartedAt: requiredTimestamp(resolvedAt), FinishedAt: requiredTimestamp(resolvedAt),
		}); err != nil {
			return err
		}
		if _, err := queries.InsertOrderStatusHistory(ctx, generated.InsertOrderStatusHistoryParams{
			OrderID: order.ID, FromStatus: optionalText(string(domain.OrderStatusDelivering)),
			ToStatus: string(domain.OrderStatusDelivered), ReasonCode: optionalText("manual_delivery_verified"),
			ActorType: "admin", ActorID: requiredInt8(admin.ID), RequestID: optionalText(command.Meta.RequestID),
		}); err != nil {
			return err
		}
		if err := insertManualDeliveryAudit(ctx, queries, admin.ID, job, updated, "delivery.manual_completed", command); err != nil {
			return err
		}
		if err := finishSession(ctx, queries, admin.ID, command.Session); err != nil {
			return err
		}
		if err := completeReceipt(ctx, queries, command.Meta.UpdateID); err != nil {
			return err
		}
		result = deliveryReviewFromJob(updated, order.ProductName, order.Quantity)
		return nil
	})
	return result, mapConstraintError(err)
}

func insertManualDeliveryAudit(ctx context.Context, queries *generated.Queries, adminID int64, before, after generated.OutboxEvent, action string, command app.DeliveryResolutionCommand) error {
	beforeData, err := json.Marshal(map[string]any{"status": before.Status, "version": before.Version})
	if err != nil {
		return err
	}
	afterData, err := json.Marshal(map[string]any{
		"status": after.Status, "version": after.Version, "reason": command.Reason,
		"telegram_message_id": command.TelegramMessageID,
	})
	if err != nil {
		return err
	}
	_, err = queries.InsertAuditLog(ctx, generated.InsertAuditLogParams{
		ActorType: "admin", ActorID: requiredInt8(adminID), Action: action,
		ResourceType: "delivery_job", ResourceID: requiredInt8(after.ID), BeforeData: beforeData, AfterData: afterData,
		RequestID: optionalText(command.Meta.RequestID), TelegramUpdateID: optionalInt8(command.Meta.UpdateID),
	})
	return err
}

func deliveryReviewFromJob(job generated.OutboxEvent, productName string, quantity int32) app.DeliveryReviewItem {
	return mapDeliveryReviewFields(
		job.ID, job.DeliveryOrderID.Int64, job.Status, job.Attempts, job.MaxAttempts,
		job.RecipientChatID.Int64, job.TelegramMessageID.Int64, job.LastErrorCode.String,
		job.LastErrorDetail.String, job.CreatedAt.Time, job.UpdatedAt.Time, job.Version,
		productName, quantity,
	)
}

func mapDeliveryReviewFields(id, orderID int64, status string, attempts, maxAttempts int32, chatID, messageID int64, errorCode, errorDetail string, createdAt, updatedAt time.Time, version int64, productName string, quantity int32) app.DeliveryReviewItem {
	return app.DeliveryReviewItem{
		ID: id, OrderID: orderID, Status: status, Attempts: attempts, MaxAttempts: maxAttempts,
		RecipientChatID: chatID, TelegramMessageID: messageID, ErrorCode: errorCode,
		ErrorDetail: errorDetail, CreatedAt: createdAt, UpdatedAt: updatedAt, Version: version,
		ProductName: productName, Quantity: quantity,
	}
}

var _ app.DeliveryAdminRepository = (*AppStore)(nil)
