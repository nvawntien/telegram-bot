package app

import (
	"context"
	"errors"
	"fmt"
	"html"
	"math"
	"math/rand/v2"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nvawntien/telegram-bot/internal/domain"
)

const DefaultDeliveryMaxAttempts int32 = 5

type DeliveryWorkItem struct {
	ID              int64
	OrderID         int64
	RecipientChatID int64
	Attempts        int32
	MaxAttempts     int32
}

type DeliveryEnvelope struct {
	JobID           int64
	OrderID         int64
	RecipientChatID int64
	TelegramUserID  int64
	ProductName     string
	Quantity        int32
	Attempts        int32
	MaxAttempts     int32
}

type EncryptedDeliveryItem struct {
	ID         int64
	ProductID  int64
	Ciphertext []byte
	Nonce      []byte
	Format     string
	KeyVersion int32
}

type DeliveryAttempt struct {
	Number  int32
	JobID   int64
	OrderID int64
}

type DeliverySendRequest struct {
	ChatID int64
	Text   string
}

type DeliverySendResult struct {
	ChatID    int64
	MessageID int64
	SentAt    time.Time
	Method    string
}

type DeliverySendError struct {
	Class             domain.DeliveryResultClass
	Code              string
	Summary           string
	HTTPStatus        int32
	TelegramErrorCode int32
	RetryAfter        time.Duration
}

func (e *DeliverySendError) Error() string {
	if e == nil || strings.TrimSpace(e.Code) == "" {
		return "Telegram delivery failed"
	}
	return "Telegram delivery failed: " + e.Code
}

type DeliveryFailure struct {
	Class             domain.DeliveryResultClass
	Code              string
	Summary           string
	HTTPStatus        int32
	TelegramErrorCode int32
	RetryAfter        time.Duration
	FailedAt          time.Time
	NextAttemptAt     time.Time
}

type DeliverySuccess struct {
	Result      DeliverySendResult
	CompletedAt time.Time
}

type DeliveryRecoveryResult struct {
	Retryable int
	Ambiguous int
}

type DeliveryRepository interface {
	BackfillDeliveryJobs(context.Context, time.Time, int32, int32) (int, error)
	RecoverStaleDeliveryJobs(context.Context, time.Time, time.Time, int32) (DeliveryRecoveryResult, error)
	ClaimDeliveryJobs(context.Context, time.Time, string, int32) ([]DeliveryWorkItem, error)
	LoadClaimedDelivery(context.Context, int64, string) (DeliveryEnvelope, []EncryptedDeliveryItem, error)
	BeginDeliveryAttempt(context.Context, int64, string, time.Time) (DeliveryAttempt, error)
	FinalizeDeliverySuccess(context.Context, int64, string, DeliverySuccess) error
	FinalizeDeliveryFailure(context.Context, int64, string, DeliveryFailure) error
	FinalizeClaimedDeliveryFailure(context.Context, int64, string, DeliveryFailure) error
	PreserveAmbiguousSuccess(context.Context, int64, DeliverySuccess, string) error
}

type DeliverySender interface {
	SendDelivery(context.Context, DeliverySendRequest) (DeliverySendResult, error)
}

type DeliveryMetrics interface {
	ObserveDeliveryJobCreated(result string)
	ObserveDeliveryJobClaimed(result string, count int)
	ObserveDeliveryAttempt(resultClass string, duration time.Duration)
	ObserveDeliveryCompleted(result string)
	ObserveDeliveryRetry(reason string)
	ObserveDeliveryAmbiguous(reason string)
	ObserveDeliveryPermanentFailure(reason string)
}

type DeliveryRetryPolicy struct {
	Base        time.Duration
	Max         time.Duration
	JitterRatio float64
	MaxAttempts int32
}

type DeliveryJob struct {
	repository        DeliveryRepository
	cipher            InventoryCipher
	sender            DeliverySender
	metrics           DeliveryMetrics
	policy            DeliveryRetryPolicy
	batchSize         int32
	lease             time.Duration
	staleScanInterval time.Duration
	lastStaleScan     time.Time
	jobTimeout        time.Duration
	messageLimit      int
	support           string
	workerID          string
	clock             func() time.Time
	random            func() float64
}

func NewDeliveryJob(
	repository DeliveryRepository,
	cipher InventoryCipher,
	sender DeliverySender,
	metrics DeliveryMetrics,
	policy DeliveryRetryPolicy,
	batchSize int32,
	lease time.Duration,
	jobTimeout time.Duration,
	messageLimit int,
	support string,
	workerID string,
) *DeliveryJob {
	return &DeliveryJob{
		repository: repository, cipher: cipher, sender: sender, metrics: metrics,
		policy: policy, batchSize: batchSize, lease: lease, staleScanInterval: lease,
		jobTimeout: jobTimeout, messageLimit: messageLimit,
		support: strings.TrimSpace(support), workerID: strings.TrimSpace(workerID),
		clock: time.Now, random: rand.Float64,
	}
}

func (j *DeliveryJob) WithStaleScanInterval(interval time.Duration) *DeliveryJob {
	if interval > 0 {
		j.staleScanInterval = interval
	}
	return j
}

func (j *DeliveryJob) RunOnce(ctx context.Context) (int, error) {
	if err := j.validate(); err != nil {
		return 0, err
	}
	now := j.clock()
	created, err := j.repository.BackfillDeliveryJobs(ctx, now, j.batchSize, j.policy.MaxAttempts)
	if err != nil {
		return 0, fmt.Errorf("backfill delivery jobs: %w", err)
	}
	if j.metrics != nil {
		result := "duplicate"
		if created > 0 {
			result = "created"
		}
		j.metrics.ObserveDeliveryJobCreated(result)
	}
	if j.lastStaleScan.IsZero() || now.Sub(j.lastStaleScan) >= j.staleScanInterval {
		recovered, err := j.repository.RecoverStaleDeliveryJobs(ctx, now, now.Add(-j.lease), j.batchSize)
		if err != nil {
			return 0, fmt.Errorf("recover stale delivery jobs: %w", err)
		}
		j.lastStaleScan = now
		if j.metrics != nil && recovered.Ambiguous > 0 {
			j.metrics.ObserveDeliveryAmbiguous("stale_after_send_started")
		}
	}
	jobs, err := j.repository.ClaimDeliveryJobs(ctx, now, j.workerID, j.batchSize)
	if err != nil {
		return 0, fmt.Errorf("claim delivery jobs: %w", err)
	}
	if j.metrics != nil {
		j.metrics.ObserveDeliveryJobClaimed("success", len(jobs))
	}
	for _, job := range jobs {
		jobCtx, cancel := context.WithTimeout(ctx, j.jobTimeout)
		err := j.process(jobCtx, job)
		cancel()
		if err != nil {
			return len(jobs), err
		}
	}
	return len(jobs), nil
}

func (j *DeliveryJob) process(ctx context.Context, job DeliveryWorkItem) error {
	envelope, encrypted, err := j.repository.LoadClaimedDelivery(ctx, job.ID, j.workerID)
	if err != nil {
		return j.failBeforeSend(ctx, job, "delivery_invariant_failed", err)
	}
	plaintext := make([][]byte, 0, len(encrypted))
	defer clearDeliveryPlaintext(plaintext)
	for _, item := range encrypted {
		value, decryptErr := j.cipher.Decrypt(ctx, item.ProductID, EncryptedInventoryPayload{
			Ciphertext: item.Ciphertext, Nonce: item.Nonce, KeyVersion: item.KeyVersion, Format: item.Format,
		})
		if decryptErr != nil {
			return j.failBeforeSend(ctx, job, "inventory_decryption_failed", decryptErr)
		}
		plaintext = append(plaintext, value)
	}
	message, err := BuildDeliveryMessage(envelope, plaintext, j.support, j.messageLimit)
	if err != nil {
		return j.failBeforeSend(ctx, job, "delivery_message_invalid", err)
	}
	attempt, err := j.repository.BeginDeliveryAttempt(ctx, job.ID, j.workerID, j.clock())
	if err != nil {
		return fmt.Errorf("begin delivery attempt: %w", err)
	}
	started := j.clock()
	result, sendErr := j.sender.SendDelivery(ctx, DeliverySendRequest{ChatID: envelope.RecipientChatID, Text: message})
	if sendErr == nil {
		if result.ChatID != envelope.RecipientChatID || result.MessageID <= 0 {
			sendErr = &DeliverySendError{Class: domain.DeliveryResultAmbiguous, Code: "invalid_success_evidence", Summary: "Telegram success response lacked trusted evidence"}
		} else {
			success := DeliverySuccess{Result: result, CompletedAt: j.clock()}
			if err := j.repository.FinalizeDeliverySuccess(ctx, job.ID, j.workerID, success); err != nil {
				preserveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
				defer cancel()
				_ = j.repository.PreserveAmbiguousSuccess(preserveCtx, job.ID, success, "database_finalize_failed")
				j.observeAttempt(domain.DeliveryResultAmbiguous, started)
				return fmt.Errorf("finalize confirmed delivery: %w", err)
			}
			j.observeAttempt(domain.DeliveryResultSuccess, started)
			if j.metrics != nil {
				j.metrics.ObserveDeliveryCompleted("success")
			}
			return nil
		}
	}
	failure := classifyDeliveryFailure(sendErr, attempt.Number, j.policy, j.clock(), j.random)
	if err := j.repository.FinalizeDeliveryFailure(ctx, job.ID, j.workerID, failure); err != nil {
		return fmt.Errorf("finalize delivery failure: %w", err)
	}
	j.observeAttempt(failure.Class, started)
	j.observeFailure(failure)
	return nil
}

func (j *DeliveryJob) failBeforeSend(ctx context.Context, job DeliveryWorkItem, code string, cause error) error {
	failure := DeliveryFailure{
		Class: domain.DeliveryResultPermanent, Code: code,
		Summary: "delivery validation failed before Telegram send", FailedAt: j.clock(),
	}
	if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		failure.Class = domain.DeliveryResultRetryable
		failure.Code = "cancelled_before_send"
		failure.Summary = "delivery stopped before Telegram send"
		failure.NextAttemptAt = j.clock().Add(DeliveryRetryDelay(j.policy, job.Attempts+1, 0, j.random))
	}
	if err := j.repository.FinalizeClaimedDeliveryFailure(ctx, job.ID, j.workerID, failure); err != nil {
		return fmt.Errorf("finalize pre-send delivery failure: %w", err)
	}
	j.observeFailure(failure)
	return nil
}

func (j *DeliveryJob) validate() error {
	if j.repository == nil || j.cipher == nil || j.sender == nil || j.batchSize <= 0 ||
		j.lease <= 0 || j.staleScanInterval <= 0 || j.jobTimeout <= 0 || j.messageLimit <= 0 || j.workerID == "" || j.policy.Base <= 0 ||
		j.policy.Max < j.policy.Base || j.policy.MaxAttempts <= 0 ||
		j.policy.JitterRatio < 0 || j.policy.JitterRatio > 1 {
		return ErrInvalidInput
	}
	return nil
}

func (j *DeliveryJob) observeAttempt(result domain.DeliveryResultClass, started time.Time) {
	if j.metrics != nil {
		j.metrics.ObserveDeliveryAttempt(string(result), time.Since(started))
	}
}

func (j *DeliveryJob) observeFailure(failure DeliveryFailure) {
	if j.metrics == nil {
		return
	}
	switch failure.Class {
	case domain.DeliveryResultRetryable:
		j.metrics.ObserveDeliveryRetry(failure.Code)
	case domain.DeliveryResultAmbiguous:
		j.metrics.ObserveDeliveryAmbiguous(failure.Code)
	case domain.DeliveryResultPermanent:
		j.metrics.ObserveDeliveryPermanentFailure(failure.Code)
	}
}

func classifyDeliveryFailure(err error, attempt int32, policy DeliveryRetryPolicy, now time.Time, random func() float64) DeliveryFailure {
	failure := DeliveryFailure{
		Class: domain.DeliveryResultAmbiguous, Code: "unclassified_transport_error",
		Summary: "Telegram send outcome is unknown", FailedAt: now,
	}
	var sendError *DeliverySendError
	if errors.As(err, &sendError) {
		failure.Class = sendError.Class
		failure.Code = strings.TrimSpace(sendError.Code)
		failure.Summary = strings.TrimSpace(sendError.Summary)
		failure.HTTPStatus = sendError.HTTPStatus
		failure.TelegramErrorCode = sendError.TelegramErrorCode
		failure.RetryAfter = sendError.RetryAfter
	}
	if failure.Code == "" {
		failure.Code = "delivery_send_failed"
	}
	if failure.Summary == "" {
		failure.Summary = "Telegram delivery failed"
	}
	if failure.Class == domain.DeliveryResultRetryable {
		if attempt >= policy.MaxAttempts {
			failure.Class = domain.DeliveryResultPermanent
			failure.Code = "attempts_exhausted"
			failure.Summary = "delivery attempts exhausted"
		} else {
			failure.NextAttemptAt = now.Add(DeliveryRetryDelay(policy, attempt, failure.RetryAfter, random))
		}
	}
	return failure
}

func DeliveryRetryDelay(policy DeliveryRetryPolicy, attempt int32, retryAfter time.Duration, random func() float64) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	exponent := attempt - 1
	if exponent > 30 {
		exponent = 30
	}
	delay := float64(policy.Base) * math.Pow(2, float64(exponent))
	if delay > float64(policy.Max) {
		delay = float64(policy.Max)
	}
	if random == nil {
		random = func() float64 { return 0.5 }
	}
	jitter := (random()*2 - 1) * policy.JitterRatio
	delay *= 1 + jitter
	if delay < float64(policy.Base)/2 {
		delay = float64(policy.Base) / 2
	}
	result := time.Duration(delay)
	if retryAfter > result {
		result = retryAfter
	}
	return result
}

func BuildDeliveryMessage(envelope DeliveryEnvelope, payloads [][]byte, support string, maxBytes int) (string, error) {
	if envelope.OrderID <= 0 || envelope.Quantity <= 0 || len(payloads) != int(envelope.Quantity) || maxBytes <= 0 {
		return "", ErrDeliveryInventoryMismatch
	}
	var builder strings.Builder
	fmt.Fprintf(&builder, "<b>Giao hàng · đơn #%d</b>\nSản phẩm: <b>%s</b>\nSố lượng: %d\n\n", envelope.OrderID, html.EscapeString(envelope.ProductName), envelope.Quantity)
	for index, payload := range payloads {
		if len(payload) == 0 || !utf8.Valid(payload) {
			return "", ErrInvalidInventoryPayload
		}
		fmt.Fprintf(&builder, "%d. <code>%s</code>\n", index+1, html.EscapeString(string(payload)))
	}
	builder.WriteString("\nHãy lưu thông tin ở nơi an toàn và không chia sẻ cho người khác.")
	if strings.TrimSpace(support) != "" {
		builder.WriteString("\nHỗ trợ: ")
		builder.WriteString(html.EscapeString(strings.TrimSpace(support)))
	}
	message := builder.String()
	if len([]byte(message)) > maxBytes {
		return "", ErrDeliveryMessageTooLarge
	}
	return message, nil
}

func clearDeliveryPlaintext(values [][]byte) {
	for _, value := range values {
		clearBytes(value)
	}
}
