package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/nvawntien/telegram-bot/internal/app"
	"github.com/nvawntien/telegram-bot/internal/domain"
	"github.com/nvawntien/telegram-bot/internal/postgres/generated"
)

func createDeliveryHandoffWithinTransaction(
	ctx context.Context,
	queries *generated.Queries,
	orderID int64,
	createdAt time.Time,
	maxAttempts int32,
	actorType string,
	actorID int64,
	requestID string,
) (bool, error) {
	if orderID <= 0 || maxAttempts <= 0 {
		return false, app.ErrInvalidInput
	}
	order, err := queries.LockOrderForDeliveryHandoff(ctx, orderID)
	if err != nil {
		return false, err
	}
	if domain.OrderStatus(order.Status) == domain.OrderStatusDelivering {
		if _, err := queries.GetDeliveryJobByOrder(ctx, requiredInt8(order.ID)); err != nil {
			return false, err
		}
		return false, nil
	}
	if domain.OrderStatus(order.Status) != domain.OrderStatusReserving || order.TelegramUserID <= 0 || order.Quantity <= 0 {
		return false, app.ErrInvalidDeliveryState
	}
	reserved, err := queries.CountExactReservedInventoryForOrder(ctx, order.ID)
	if err != nil {
		return false, err
	}
	if reserved != order.Quantity {
		return false, app.ErrDeliveryInventoryMismatch
	}
	job, err := queries.InsertDeliveryJob(ctx, generated.InsertDeliveryJobParams{
		OrderID: order.ID, MaxAttempts: maxAttempts, NextAttemptAt: requiredTimestamp(createdAt),
		RecipientChatID: order.TelegramUserID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		if _, findErr := queries.GetDeliveryJobByOrder(ctx, requiredInt8(order.ID)); findErr != nil {
			return false, findErr
		}
		return false, nil
	}
	if err != nil {
		return false, err
	}
	delivering, err := queries.MarkOrderDeliveringGuarded(ctx, generated.MarkOrderDeliveringGuardedParams{
		StartedAt: requiredTimestamp(createdAt), OrderID: order.ID, ExpectedVersion: order.Version,
	})
	if err != nil {
		return false, err
	}
	if _, err := queries.InsertOrderStatusHistory(ctx, generated.InsertOrderStatusHistoryParams{
		OrderID: order.ID, FromStatus: optionalText(string(domain.OrderStatusReserving)),
		ToStatus: string(domain.OrderStatusDelivering), ReasonCode: optionalText("delivery_job_created"),
		ActorType: actorType, ActorID: optionalInt8(actorID), RequestID: optionalText(requestID),
	}); err != nil {
		return false, err
	}
	auditActorType := actorType
	auditActorID := optionalInt8(actorID)
	if actorType == "provider" {
		auditActorType = "system"
		auditActorID = pgtype.Int8{}
	}
	after, err := json.Marshal(map[string]any{
		"delivery_job_id": job.ID, "order_id": order.ID, "status": delivering.Status,
		"inventory_count": reserved,
	})
	if err != nil {
		return false, err
	}
	if _, err := queries.InsertAuditLog(ctx, generated.InsertAuditLogParams{
		ActorType: auditActorType, ActorID: auditActorID, Action: "delivery.job_created",
		ResourceType: "delivery_job", ResourceID: requiredInt8(job.ID), AfterData: after,
		RequestID: optionalText(requestID),
	}); err != nil {
		return false, err
	}
	return true, nil
}

func (s *AppStore) BackfillDeliveryJobs(ctx context.Context, now time.Time, batchSize, maxAttempts int32) (int, error) {
	created := 0
	err := s.transactor.WithinTransaction(ctx, func(ctx context.Context, queries *generated.Queries) error {
		orderIDs, err := queries.ListDeliveryBackfillOrderIDs(ctx, batchSize)
		if err != nil {
			return err
		}
		for _, orderID := range orderIDs {
			inserted, err := createDeliveryHandoffWithinTransaction(
				ctx, queries, orderID, now, maxAttempts, "system", 0,
				fmt.Sprintf("delivery-backfill-%d", orderID),
			)
			if err != nil {
				return err
			}
			if inserted {
				created++
			}
		}
		return nil
	})
	return created, err
}

func (s *AppStore) RecoverStaleDeliveryJobs(ctx context.Context, now, staleBefore time.Time, batchSize int32) (app.DeliveryRecoveryResult, error) {
	result := app.DeliveryRecoveryResult{}
	err := s.transactor.WithinTransaction(ctx, func(ctx context.Context, queries *generated.Queries) error {
		jobs, err := queries.LockStaleDeliveryJobs(ctx, generated.LockStaleDeliveryJobsParams{StaleBefore: requiredTimestamp(staleBefore), BatchSize: batchSize})
		if err != nil {
			return err
		}
		for _, job := range jobs {
			switch job.ProcessingStage.String {
			case "claimed":
				if _, err := queries.MarkStaleDeliveryClaimRetryable(ctx, generated.MarkStaleDeliveryClaimRetryableParams{NextAttemptAt: requiredTimestamp(now), ID: job.ID}); err != nil {
					return err
				}
				result.Retryable++
			case "sending":
				if _, err := queries.MarkStaleDeliverySendAmbiguous(ctx, job.ID); err != nil {
					return err
				}
				if _, err := insertDeliveryAttempt(ctx, queries, job, "ambiguous", now, app.DeliveryFailure{
					Class: domain.DeliveryResultAmbiguous, Code: "stale_after_send_started",
					Summary: "send outcome requires manual verification", FailedAt: now,
				}, app.DeliverySendResult{}); err != nil {
					return err
				}
				if err := insertDeliveryAudit(ctx, queries, job.ID, job.DeliveryOrderID.Int64, "delivery.stale_send_ambiguous", "system", 0, "stale_after_send_started", ""); err != nil {
					return err
				}
				result.Ambiguous++
			default:
				return app.ErrInvalidDeliveryState
			}
		}
		return nil
	})
	return result, err
}

func (s *AppStore) ClaimDeliveryJobs(ctx context.Context, now time.Time, workerID string, batchSize int32) ([]app.DeliveryWorkItem, error) {
	rows, err := s.queries.ClaimDeliveryJobs(ctx, generated.ClaimDeliveryJobsParams{
		ClaimedAt: requiredTimestamp(now), WorkerID: optionalText(workerID), BatchSize: batchSize,
	})
	if err != nil {
		return nil, err
	}
	items := make([]app.DeliveryWorkItem, 0, len(rows))
	for _, row := range rows {
		items = append(items, app.DeliveryWorkItem{
			ID: row.ID, OrderID: row.DeliveryOrderID.Int64, RecipientChatID: row.RecipientChatID.Int64,
			Attempts: row.Attempts, MaxAttempts: row.MaxAttempts,
		})
	}
	return items, nil
}

func (s *AppStore) LoadClaimedDelivery(ctx context.Context, jobID int64, workerID string) (app.DeliveryEnvelope, []app.EncryptedDeliveryItem, error) {
	row, err := s.queries.LoadDeliveryEnvelope(ctx, jobID)
	if errors.Is(err, pgx.ErrNoRows) {
		return app.DeliveryEnvelope{}, nil, app.ErrDeliveryNotFound
	}
	if err != nil {
		return app.DeliveryEnvelope{}, nil, err
	}
	if row.JobStatus != string(domain.DeliveryJobProcessing) || row.ProcessingStage.String != "claimed" ||
		row.LockedBy.String != workerID || row.OrderStatus != string(domain.OrderStatusDelivering) ||
		!row.OrderID.Valid || !row.RecipientChatID.Valid || row.RecipientChatID.Int64 != row.TelegramUserID || row.Quantity <= 0 {
		return app.DeliveryEnvelope{}, nil, app.ErrInvalidDeliveryState
	}
	items, err := s.queries.ListEncryptedInventoryForDelivery(ctx, row.OrderID.Int64)
	if err != nil {
		return app.DeliveryEnvelope{}, nil, err
	}
	if len(items) != int(row.Quantity) {
		return app.DeliveryEnvelope{}, nil, app.ErrDeliveryInventoryMismatch
	}
	encrypted := make([]app.EncryptedDeliveryItem, 0, len(items))
	for _, item := range items {
		if !item.ReservedOrderID.Valid || item.ReservedOrderID.Int64 != row.OrderID.Int64 || item.Status != string(domain.InventoryStatusReserved) {
			return app.DeliveryEnvelope{}, nil, app.ErrDeliveryInventoryMismatch
		}
		encrypted = append(encrypted, app.EncryptedDeliveryItem{
			ID: item.ID, ProductID: item.ProductID,
			Ciphertext: append([]byte(nil), item.EncryptedPayload...), Nonce: append([]byte(nil), item.EncryptionNonce...),
			Format: item.EncryptionFormat, KeyVersion: item.EncryptionKeyVersion,
		})
	}
	return app.DeliveryEnvelope{
		JobID: row.DeliveryJobID, OrderID: row.OrderID.Int64,
		RecipientChatID: row.RecipientChatID.Int64, TelegramUserID: row.TelegramUserID,
		ProductName: row.ProductName, Quantity: row.Quantity,
		Attempts: row.Attempts, MaxAttempts: row.MaxAttempts,
	}, encrypted, nil
}

func (s *AppStore) BeginDeliveryAttempt(ctx context.Context, jobID int64, workerID string, attemptedAt time.Time) (app.DeliveryAttempt, error) {
	var result app.DeliveryAttempt
	err := s.transactor.WithinTransaction(ctx, func(ctx context.Context, queries *generated.Queries) error {
		job, err := queries.BeginDeliverySend(ctx, generated.BeginDeliverySendParams{
			SendAttemptedAt: requiredTimestamp(attemptedAt), ID: jobID, WorkerID: optionalText(workerID),
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return app.ErrDeliveryLeaseLost
		}
		if err != nil {
			return err
		}
		if _, err := queries.InsertDeliveryAttemptEvent(ctx, generated.InsertDeliveryAttemptEventParams{
			OrderID: job.DeliveryOrderID.Int64, DeliveryJobID: requiredInt8(job.ID), AttemptNumber: job.Attempts,
			Status: "started", StartedAt: requiredTimestamp(attemptedAt),
		}); err != nil {
			return err
		}
		result = app.DeliveryAttempt{Number: job.Attempts, JobID: job.ID, OrderID: job.DeliveryOrderID.Int64}
		return nil
	})
	return result, err
}

func (s *AppStore) FinalizeDeliverySuccess(ctx context.Context, jobID int64, workerID string, success app.DeliverySuccess) error {
	return s.transactor.WithinTransaction(ctx, func(ctx context.Context, queries *generated.Queries) error {
		job, err := queries.LockDeliveryJob(ctx, jobID)
		if err != nil {
			return err
		}
		if job.Status == string(domain.DeliveryJobCompleted) {
			return nil
		}
		if job.Status != string(domain.DeliveryJobProcessing) || job.ProcessingStage.String != "sending" || job.LockedBy.String != workerID {
			return app.ErrDeliveryLeaseLost
		}
		order, err := queries.LockOrderForDeliveryHandoff(ctx, job.DeliveryOrderID.Int64)
		if err != nil {
			return err
		}
		if order.Status != string(domain.OrderStatusDelivering) || success.Result.ChatID != job.RecipientChatID.Int64 || success.Result.MessageID <= 0 {
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
			DeliveredAt: requiredTimestamp(success.CompletedAt), OrderID: order.ID, ExpectedVersion: order.Version,
		}); err != nil {
			return err
		}
		if _, err := queries.MarkDeliveryCompleted(ctx, generated.MarkDeliveryCompletedParams{
			TelegramMessageID: requiredInt8(success.Result.MessageID), TelegramSentAt: requiredTimestamp(success.Result.SentAt),
			CompletedAt: requiredTimestamp(success.CompletedAt), ID: job.ID, WorkerID: optionalText(workerID),
		}); err != nil {
			return err
		}
		if _, err := insertDeliveryAttempt(ctx, queries, job, "succeeded", success.CompletedAt, app.DeliveryFailure{}, success.Result); err != nil {
			return err
		}
		if _, err := queries.InsertOrderStatusHistory(ctx, generated.InsertOrderStatusHistoryParams{
			OrderID: order.ID, FromStatus: optionalText(string(domain.OrderStatusDelivering)), ToStatus: string(domain.OrderStatusDelivered),
			ReasonCode: optionalText("telegram_delivery_confirmed"), ActorType: "system",
			RequestID: optionalText(fmt.Sprintf("delivery-job-%d", job.ID)),
		}); err != nil {
			return err
		}
		return insertDeliveryAudit(ctx, queries, job.ID, order.ID, "delivery.completed", "system", 0, "telegram_success", "")
	})
}

func (s *AppStore) FinalizeDeliveryFailure(ctx context.Context, jobID int64, workerID string, failure app.DeliveryFailure) error {
	return s.transactor.WithinTransaction(ctx, func(ctx context.Context, queries *generated.Queries) error {
		job, err := queries.LockDeliveryJob(ctx, jobID)
		if err != nil {
			return err
		}
		if job.Status != string(domain.DeliveryJobProcessing) || job.ProcessingStage.String != "sending" || job.LockedBy.String != workerID {
			return app.ErrDeliveryLeaseLost
		}
		status := failureAttemptStatus(failure.Class)
		if _, err := insertDeliveryAttempt(ctx, queries, job, status, failure.FailedAt, failure, app.DeliverySendResult{}); err != nil {
			return err
		}
		switch failure.Class {
		case domain.DeliveryResultRetryable:
			if _, err := queries.MarkDeliveryRetryable(ctx, generated.MarkDeliveryRetryableParams{
				NextAttemptAt: requiredTimestamp(failure.NextAttemptAt), ErrorCode: optionalText(failure.Code),
				ErrorDetail: optionalText(failure.Summary), ID: job.ID, WorkerID: optionalText(workerID),
			}); err != nil {
				return err
			}
			return insertDeliveryAudit(ctx, queries, job.ID, job.DeliveryOrderID.Int64, "delivery.retry_scheduled", "system", 0, failure.Code, "")
		case domain.DeliveryResultPermanent:
			if _, err := queries.MarkDeliveryPermanentFailed(ctx, generated.MarkDeliveryPermanentFailedParams{
				ErrorCode: optionalText(failure.Code), ErrorDetail: optionalText(failure.Summary),
				CompletedAt: requiredTimestamp(failure.FailedAt), ID: job.ID, WorkerID: optionalText(workerID),
			}); err != nil {
				return err
			}
			if err := markOrderDeliveryFailed(ctx, queries, job, failure.Code); err != nil {
				return err
			}
			return insertDeliveryAudit(ctx, queries, job.ID, job.DeliveryOrderID.Int64, "delivery.permanent_failed", "system", 0, failure.Code, "")
		case domain.DeliveryResultAmbiguous:
			if _, err := queries.MarkDeliveryAmbiguous(ctx, generated.MarkDeliveryAmbiguousParams{
				ErrorCode: optionalText(failure.Code), ErrorDetail: optionalText(failure.Summary), ID: job.ID,
			}); err != nil {
				return err
			}
			return insertDeliveryAudit(ctx, queries, job.ID, job.DeliveryOrderID.Int64, "delivery.ambiguous", "system", 0, failure.Code, "")
		default:
			return app.ErrInvalidDeliveryState
		}
	})
}

func (s *AppStore) FinalizeClaimedDeliveryFailure(ctx context.Context, jobID int64, workerID string, failure app.DeliveryFailure) error {
	return s.transactor.WithinTransaction(ctx, func(ctx context.Context, queries *generated.Queries) error {
		job, err := queries.LockDeliveryJob(ctx, jobID)
		if err != nil {
			return err
		}
		if job.Status != string(domain.DeliveryJobProcessing) || job.ProcessingStage.String != "claimed" || job.LockedBy.String != workerID {
			return app.ErrDeliveryLeaseLost
		}
		newStatus := "permanent_failed"
		completedAt := requiredTimestamp(failure.FailedAt)
		if failure.Class == domain.DeliveryResultRetryable {
			newStatus = "retryable_failed"
			completedAt = pgtype.Timestamptz{}
		}
		updated, err := queries.MarkClaimedDeliveryFailure(ctx, generated.MarkClaimedDeliveryFailureParams{
			NewStatus: newStatus, NextAttemptAt: requiredTimestamp(failure.NextAttemptAt),
			ErrorCode: optionalText(failure.Code), ErrorDetail: optionalText(failure.Summary),
			CompletedAt: completedAt, ID: job.ID, WorkerID: optionalText(workerID),
		})
		if err != nil {
			return err
		}
		if _, err := insertDeliveryAttempt(ctx, queries, updated, failureAttemptStatus(failure.Class), failure.FailedAt, failure, app.DeliverySendResult{}); err != nil {
			return err
		}
		if failure.Class == domain.DeliveryResultPermanent {
			if err := markOrderDeliveryFailed(ctx, queries, job, failure.Code); err != nil {
				return err
			}
		}
		return insertDeliveryAudit(ctx, queries, job.ID, job.DeliveryOrderID.Int64, "delivery.pre_send_failed", "system", 0, failure.Code, "")
	})
}

func (s *AppStore) PreserveAmbiguousSuccess(ctx context.Context, jobID int64, success app.DeliverySuccess, reason string) error {
	return s.transactor.WithinTransaction(ctx, func(ctx context.Context, queries *generated.Queries) error {
		job, err := queries.LockDeliveryJob(ctx, jobID)
		if err != nil {
			return err
		}
		if job.Status == string(domain.DeliveryJobCompleted) {
			return nil
		}
		if job.Status != string(domain.DeliveryJobProcessing) || job.ProcessingStage.String != "sending" {
			return app.ErrInvalidDeliveryState
		}
		if _, err := queries.MarkDeliveryAmbiguous(ctx, generated.MarkDeliveryAmbiguousParams{
			TelegramMessageID: requiredInt8(success.Result.MessageID), TelegramSentAt: requiredTimestamp(success.Result.SentAt),
			ErrorCode: optionalText(reason), ErrorDetail: optionalText("Telegram confirmed send but database finalization was not confirmed"), ID: job.ID,
		}); err != nil {
			return err
		}
		failure := app.DeliveryFailure{Class: domain.DeliveryResultAmbiguous, Code: reason, Summary: "confirmed send requires database reconciliation", FailedAt: success.CompletedAt}
		if _, err := insertDeliveryAttempt(ctx, queries, job, "ambiguous", success.CompletedAt, failure, success.Result); err != nil {
			return err
		}
		return insertDeliveryAudit(ctx, queries, job.ID, job.DeliveryOrderID.Int64, "delivery.finalize_ambiguous", "system", 0, reason, "")
	})
}

func insertDeliveryAttempt(ctx context.Context, queries *generated.Queries, job generated.OutboxEvent, status string, finishedAt time.Time, failure app.DeliveryFailure, result app.DeliverySendResult) (generated.DeliveryAttempt, error) {
	startedAt := job.SendAttemptedAt
	if !startedAt.Valid {
		startedAt = requiredTimestamp(finishedAt)
	}
	params := generated.InsertDeliveryAttemptEventParams{
		OrderID: job.DeliveryOrderID.Int64, DeliveryJobID: requiredInt8(job.ID), AttemptNumber: job.Attempts,
		Status: status, StartedAt: startedAt, FinishedAt: requiredTimestamp(finishedAt),
		HttpStatus: optionalInt4(failure.HTTPStatus), TelegramErrorCode: optionalInt4(failure.TelegramErrorCode),
		RetryAfterSeconds: durationSeconds(failure.RetryAfter), ErrorClass: optionalText(string(failure.Class)),
		ErrorCode: optionalText(failure.Code), ErrorDetail: optionalText(failure.Summary),
	}
	if result.ChatID > 0 {
		params.TelegramChatID = requiredInt8(result.ChatID)
	}
	if result.MessageID > 0 {
		params.TelegramMessageID = requiredInt8(result.MessageID)
	}
	return queries.InsertDeliveryAttemptEvent(ctx, params)
}

func markOrderDeliveryFailed(ctx context.Context, queries *generated.Queries, job generated.OutboxEvent, reason string) error {
	order, err := queries.MarkOrderDeliveryFailedGuarded(ctx, job.DeliveryOrderID.Int64)
	if errors.Is(err, pgx.ErrNoRows) {
		return app.ErrInvalidDeliveryState
	}
	if err != nil {
		return err
	}
	_, err = queries.InsertOrderStatusHistory(ctx, generated.InsertOrderStatusHistoryParams{
		OrderID: order.ID, FromStatus: optionalText(string(domain.OrderStatusDelivering)), ToStatus: string(domain.OrderStatusDeliveryFailed),
		ReasonCode: optionalText(reason), ActorType: "system", RequestID: optionalText(fmt.Sprintf("delivery-job-%d", job.ID)),
	})
	return err
}

func insertDeliveryAudit(ctx context.Context, queries *generated.Queries, jobID, orderID int64, action, actorType string, actorID int64, reason, requestID string) error {
	after, err := json.Marshal(map[string]any{"delivery_job_id": jobID, "order_id": orderID, "reason": reason})
	if err != nil {
		return err
	}
	_, err = queries.InsertAuditLog(ctx, generated.InsertAuditLogParams{
		ActorType: actorType, ActorID: optionalInt8(actorID), Action: action,
		ResourceType: "delivery_job", ResourceID: requiredInt8(jobID), AfterData: after,
		RequestID: optionalText(requestID),
	})
	return err
}

func failureAttemptStatus(class domain.DeliveryResultClass) string {
	switch class {
	case domain.DeliveryResultRetryable:
		return "retryable_failed"
	case domain.DeliveryResultPermanent:
		return "permanent_failed"
	default:
		return "ambiguous"
	}
}

func optionalInt4(value int32) pgtype.Int4 {
	return pgtype.Int4{Int32: value, Valid: value > 0}
}

func durationSeconds(value time.Duration) pgtype.Int4 {
	if value <= 0 {
		return pgtype.Int4{}
	}
	seconds := int64(math.Ceil(value.Seconds()))
	if seconds > math.MaxInt32 {
		seconds = math.MaxInt32
	}
	return pgtype.Int4{Int32: int32(seconds), Valid: true}
}

func configuredDeliveryMaxAttempts(values []int32) int32 {
	if len(values) > 0 && values[0] > 0 {
		return values[0]
	}
	return app.DefaultDeliveryMaxAttempts
}

var _ app.DeliveryRepository = (*AppStore)(nil)
