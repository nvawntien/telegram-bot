//go:build integration

package integration_test

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nvawntien/telegram-bot/internal/app"
	"github.com/nvawntien/telegram-bot/internal/domain"
	"github.com/nvawntien/telegram-bot/internal/inventorycrypto"
	"github.com/nvawntien/telegram-bot/internal/postgres"
	"github.com/nvawntien/telegram-bot/internal/postgres/generated"
)

type phase7Sender struct {
	mu       sync.Mutex
	calls    int
	messages []string
	err      error
}

func (s *phase7Sender) SendDelivery(_ context.Context, request app.DeliverySendRequest) (app.DeliverySendResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	s.messages = append(s.messages, request.Text)
	if s.err != nil {
		return app.DeliverySendResult{}, s.err
	}
	return app.DeliverySendResult{ChatID: request.ChatID, MessageID: int64(7000 + s.calls), SentAt: time.Now(), Method: "sendMessage"}, nil
}

func (s *phase7Sender) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

type phase7Fixture struct {
	orderID     int64
	jobID       int64
	inventoryID int64
	secret      string
}

type phase7FinalizeFailureStore struct {
	*postgres.AppStore
}

func (s *phase7FinalizeFailureStore) FinalizeDeliverySuccess(context.Context, int64, string, app.DeliverySuccess) error {
	return errors.New("simulated commit failure after confirmed send")
}

func TestPhase7DeliverySuccessIsAtomicAndPlaintextDoesNotPersist(t *testing.T) {
	database := newTestDatabase(t, true)
	cipher := newPhase7Cipher(t)
	fixture := createPhase7Delivery(t, database, cipher, true)
	sender := &phase7Sender{}
	job := newPhase7Job(postgres.NewAppStore(database.pool), cipher, sender, "phase7-success-worker")

	claimed, err := job.RunOnce(context.Background())
	if err != nil || claimed != 1 {
		t.Fatalf("RunOnce() = %d, %v", claimed, err)
	}
	if sender.callCount() != 1 || len(sender.messages) != 1 || strings.Contains(sender.messages[0], fixture.secret) || !strings.Contains(sender.messages[0], "&lt;&amp;") {
		t.Fatalf("sender evidence calls=%d messages=%q", sender.callCount(), sender.messages)
	}
	var jobStatus, orderStatus, inventoryStatus string
	var messageID int64
	ctx := context.Background()
	if err := database.pool.QueryRow(ctx, `SELECT status, telegram_message_id FROM outbox_events WHERE id=$1`, fixture.jobID).Scan(&jobStatus, &messageID); err != nil {
		t.Fatal(err)
	}
	if err := database.pool.QueryRow(ctx, `SELECT status FROM orders WHERE id=$1`, fixture.orderID).Scan(&orderStatus); err != nil {
		t.Fatal(err)
	}
	if err := database.pool.QueryRow(ctx, `SELECT status FROM inventory_items WHERE id=$1`, fixture.inventoryID).Scan(&inventoryStatus); err != nil {
		t.Fatal(err)
	}
	if jobStatus != "completed" || orderStatus != "delivered" || inventoryStatus != "sold" || messageID <= 0 {
		t.Fatalf("states job=%s order=%s inventory=%s message=%d", jobStatus, orderStatus, inventoryStatus, messageID)
	}
	assertCount(t, database, `SELECT count(*) FROM delivery_attempts WHERE delivery_job_id=$1 AND status IN ('started','succeeded')`, 2, fixture.jobID)
	assertCount(t, database, `SELECT count(*) FROM order_status_history WHERE order_id=$1 AND to_status='delivered'`, 1, fixture.orderID)
	assertCount(t, database, `SELECT count(*) FROM audit_logs WHERE resource_type='delivery_job' AND resource_id=$1 AND action='delivery.completed'`, 1, fixture.jobID)
	assertPhase7PlaintextAbsent(t, database, fixture.secret)
}

func TestPhase7FailureClassesDoNotRiskDuplicateDelivery(t *testing.T) {
	database := newTestDatabase(t, true)
	cipher := newPhase7Cipher(t)
	store := postgres.NewAppStore(database.pool)

	retryable := createPhase7Delivery(t, database, cipher, true)
	retrySender := &phase7Sender{err: &app.DeliverySendError{Class: domain.DeliveryResultRetryable, Code: "rate_limited", Summary: "Telegram rate limited", HTTPStatus: 429, TelegramErrorCode: 429, RetryAfter: time.Minute}}
	if _, err := newPhase7Job(store, cipher, retrySender, "phase7-retry-worker").RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertPhase7State(t, database, retryable, "retryable_failed", "delivering", "reserved")
	var delay time.Duration
	var next time.Time
	if err := database.pool.QueryRow(context.Background(), `SELECT next_attempt_at FROM outbox_events WHERE id=$1`, retryable.jobID).Scan(&next); err != nil {
		t.Fatal(err)
	}
	delay = time.Until(next)
	if delay < 55*time.Second {
		t.Fatalf("retry_after was not honored: %s", delay)
	}

	ambiguous := createPhase7Delivery(t, database, cipher, true)
	ambiguousSender := &phase7Sender{err: errors.New("response reset after request write")}
	ambiguousJob := newPhase7Job(store, cipher, ambiguousSender, "phase7-ambiguous-worker")
	if _, err := ambiguousJob.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertPhase7State(t, database, ambiguous, "ambiguous", "delivering", "reserved")
	if _, err := ambiguousJob.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if ambiguousSender.callCount() != 1 {
		t.Fatalf("ambiguous delivery was resent automatically: %d calls", ambiguousSender.callCount())
	}

	permanent := createPhase7Delivery(t, database, cipher, true)
	permanentSender := &phase7Sender{err: &app.DeliverySendError{Class: domain.DeliveryResultPermanent, Code: "chat_not_found", Summary: "Telegram recipient unavailable", HTTPStatus: 400, TelegramErrorCode: 400}}
	if _, err := newPhase7Job(store, cipher, permanentSender, "phase7-permanent-worker").RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertPhase7State(t, database, permanent, "permanent_failed", "delivery_failed", "reserved")
	for _, fixture := range []phase7Fixture{retryable, ambiguous, permanent} {
		assertPhase7PlaintextAbsent(t, database, fixture.secret)
	}
}

func TestPhase7ConfirmedSendFinalizeFailureBecomesAmbiguous(t *testing.T) {
	database := newTestDatabase(t, true)
	cipher := newPhase7Cipher(t)
	fixture := createPhase7Delivery(t, database, cipher, true)
	sender := &phase7Sender{}
	repository := &phase7FinalizeFailureStore{AppStore: postgres.NewAppStore(database.pool)}
	job := app.NewDeliveryJob(
		repository, cipher, sender, nil,
		app.DeliveryRetryPolicy{Base: time.Second, Max: time.Minute, MaxAttempts: 5},
		1, time.Minute, 5*time.Second, 3500, "support", "phase7-finalize-failure-worker",
	)
	if _, err := job.RunOnce(context.Background()); err == nil {
		t.Fatal("RunOnce() error = nil, want finalization failure")
	}
	if sender.callCount() != 1 {
		t.Fatalf("confirmed sends = %d", sender.callCount())
	}
	assertPhase7State(t, database, fixture, "ambiguous", "delivering", "reserved")
	var messageID int64
	if err := database.pool.QueryRow(context.Background(), `SELECT telegram_message_id FROM outbox_events WHERE id=$1`, fixture.jobID).Scan(&messageID); err != nil || messageID <= 0 {
		t.Fatalf("preserved Telegram evidence = %d, %v", messageID, err)
	}
}

func TestPhase7ConcurrentFinalizeHasOneBusinessEffect(t *testing.T) {
	database := newTestDatabase(t, true)
	cipher := newPhase7Cipher(t)
	fixture := createPhase7Delivery(t, database, cipher, true)
	store := postgres.NewAppStore(database.pool)
	items, err := store.ClaimDeliveryJobs(context.Background(), time.Now(), "concurrent-finalize-worker", 1)
	if err != nil || len(items) != 1 {
		t.Fatalf("claim = %+v, %v", items, err)
	}
	if _, err := store.BeginDeliveryAttempt(context.Background(), fixture.jobID, "concurrent-finalize-worker", time.Now()); err != nil {
		t.Fatal(err)
	}
	success := app.DeliverySuccess{Result: app.DeliverySendResult{
		ChatID: items[0].RecipientChatID, MessageID: 88991, SentAt: time.Now(), Method: "sendMessage",
	}, CompletedAt: time.Now()}
	errorsFound := make(chan error, 2)
	var wait sync.WaitGroup
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			errorsFound <- store.FinalizeDeliverySuccess(context.Background(), fixture.jobID, "concurrent-finalize-worker", success)
		}()
	}
	wait.Wait()
	close(errorsFound)
	for err := range errorsFound {
		if err != nil {
			t.Fatalf("FinalizeDeliverySuccess() error = %v", err)
		}
	}
	assertPhase7State(t, database, fixture, "completed", "delivered", "sold")
	assertCount(t, database, `SELECT count(*) FROM delivery_attempts WHERE delivery_job_id=$1 AND status='succeeded'`, 1, fixture.jobID)
	assertCount(t, database, `SELECT count(*) FROM order_status_history WHERE order_id=$1 AND to_status='delivered'`, 1, fixture.orderID)
	assertCount(t, database, `SELECT count(*) FROM audit_logs WHERE resource_id=$1 AND action='delivery.completed'`, 1, fixture.jobID)
}

func TestPhase7ConcurrentClaimAndStaleRecovery(t *testing.T) {
	database := newTestDatabase(t, true)
	cipher := newPhase7Cipher(t)
	store := postgres.NewAppStore(database.pool)
	fixture := createPhase7Delivery(t, database, cipher, true)
	var claimed atomic.Int32
	var wait sync.WaitGroup
	for worker := 0; worker < 100; worker++ {
		wait.Add(1)
		go func(worker int) {
			defer wait.Done()
			items, err := store.ClaimDeliveryJobs(context.Background(), time.Now(), fmt.Sprintf("worker-%d", worker), 1)
			if err != nil {
				t.Errorf("ClaimDeliveryJobs(): %v", err)
				return
			}
			claimed.Add(int32(len(items)))
		}(worker)
	}
	wait.Wait()
	if claimed.Load() != 1 {
		t.Fatalf("concurrent claims = %d", claimed.Load())
	}
	if _, err := database.pool.Exec(context.Background(), `UPDATE outbox_events SET locked_at=clock_timestamp()-interval '10 minutes' WHERE id=$1`, fixture.jobID); err != nil {
		t.Fatal(err)
	}
	recovered, err := store.RecoverStaleDeliveryJobs(context.Background(), time.Now(), time.Now().Add(-time.Minute), 10)
	if err != nil || recovered.Retryable != 1 || recovered.Ambiguous != 0 {
		t.Fatalf("claimed recovery = %+v, %v", recovered, err)
	}
	if _, err := database.pool.Exec(context.Background(), `UPDATE outbox_events SET next_attempt_at=clock_timestamp()+interval '1 hour' WHERE id=$1`, fixture.jobID); err != nil {
		t.Fatal(err)
	}

	sending := createPhase7Delivery(t, database, cipher, true)
	items, err := store.ClaimDeliveryJobs(context.Background(), time.Now(), "stale-sending-worker", 1)
	if err != nil || len(items) != 1 || items[0].ID != sending.jobID {
		t.Fatalf("sending claim = %+v, %v", items, err)
	}
	if _, err := store.BeginDeliveryAttempt(context.Background(), sending.jobID, "stale-sending-worker", time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := database.pool.Exec(context.Background(), `UPDATE outbox_events SET locked_at=clock_timestamp()-interval '10 minutes' WHERE id=$1`, sending.jobID); err != nil {
		t.Fatal(err)
	}
	recovered, err = store.RecoverStaleDeliveryJobs(context.Background(), time.Now(), time.Now().Add(-time.Minute), 10)
	if err != nil || recovered.Ambiguous != 1 {
		t.Fatalf("sending recovery = %+v, %v", recovered, err)
	}
	assertPhase7State(t, database, sending, "ambiguous", "delivering", "reserved")
}

func TestPhase7BackfillIsIdempotent(t *testing.T) {
	database := newTestDatabase(t, true)
	cipher := newPhase7Cipher(t)
	fixture := createPhase7Delivery(t, database, cipher, false)
	store := postgres.NewAppStore(database.pool)
	now := time.Now()
	first, err := store.BackfillDeliveryJobs(context.Background(), now, 10, 5)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.BackfillDeliveryJobs(context.Background(), now, 10, 5)
	if err != nil || first != 1 || second != 0 {
		t.Fatalf("backfill = %d then %d, %v", first, second, err)
	}
	assertCount(t, database, `SELECT count(*) FROM outbox_events WHERE delivery_order_id=$1 AND event_type='order.delivery_requested'`, 1, fixture.orderID)
	var status string
	if err := database.pool.QueryRow(context.Background(), `SELECT status FROM orders WHERE id=$1`, fixture.orderID).Scan(&status); err != nil || status != "delivering" {
		t.Fatalf("order status = %s, %v", status, err)
	}
}

func TestPhase7ManualRecoveryRequiresSessionReasonAndEvidence(t *testing.T) {
	database := newTestDatabase(t, true)
	cipher := newPhase7Cipher(t)
	store := postgres.NewAppStore(database.pool)
	adminTelegramID := int64(9700001)
	if err := store.BootstrapAdmin(context.Background(), adminTelegramID); err != nil {
		t.Fatal(err)
	}
	admins := app.NewAdminService(store, 15*time.Minute)
	deliveries := app.NewDeliveryAdminService(store, 8)
	if _, err := deliveries.List(context.Background(), adminTelegramID+1, 0); !errors.Is(err, app.ErrUnauthorized) {
		t.Fatalf("unauthorized delivery list error = %v", err)
	}
	revokedTelegramID := adminTelegramID + 2
	if err := store.BootstrapAdmin(context.Background(), revokedTelegramID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.pool.Exec(context.Background(), `UPDATE admins SET is_active=false WHERE user_id=(SELECT id FROM users WHERE telegram_user_id=$1)`, revokedTelegramID); err != nil {
		t.Fatal(err)
	}
	if _, err := deliveries.List(context.Background(), revokedTelegramID, 0); !errors.Is(err, app.ErrForbidden) {
		t.Fatalf("revoked delivery list error = %v", err)
	}

	retryFixture := createPhase7Delivery(t, database, cipher, true)
	makePhase7Ambiguous(t, database, store, cipher, retryFixture)
	startTestUpdate(t, database, 97001)
	retrySession, err := admins.StartSession(context.Background(), adminTelegramID, app.SessionDeliveryRetry, map[string]any{"step": "confirm"}, app.RequestMeta{UpdateID: 97001, RequestID: "retry-start"})
	if err != nil {
		t.Fatal(err)
	}
	startTestUpdate(t, database, 97002)
	result, err := deliveries.Retry(context.Background(), app.DeliveryResolutionCommand{
		AdminTelegramID: adminTelegramID, JobID: retryFixture.jobID, ExpectedVersion: phase7JobVersion(t, database, retryFixture.jobID),
		Reason: "operator verified the message was not delivered", Session: retrySession,
		Meta: app.RequestMeta{UpdateID: 97002, RequestID: "retry-confirm"},
	})
	if err != nil || result.Status != "pending" {
		t.Fatalf("Retry() = %+v, %v", result, err)
	}
	assertPhase7State(t, database, retryFixture, "pending", "delivering", "reserved")
	startTestUpdate(t, database, 97006)
	_, duplicateErr := deliveries.Retry(context.Background(), app.DeliveryResolutionCommand{
		AdminTelegramID: adminTelegramID, JobID: retryFixture.jobID, ExpectedVersion: result.Version,
		Reason: "duplicate callback must not enqueue again", Session: retrySession,
		Meta: app.RequestMeta{UpdateID: 97006, RequestID: "retry-duplicate"},
	})
	if !errors.Is(duplicateErr, app.ErrStaleVersion) && !errors.Is(duplicateErr, app.ErrSessionExpired) {
		t.Fatalf("duplicate retry error = %v", duplicateErr)
	}
	assertPhase7State(t, database, retryFixture, "pending", "delivering", "reserved")
	if _, err := database.pool.Exec(context.Background(), `UPDATE outbox_events SET next_attempt_at=clock_timestamp()+interval '1 hour' WHERE id=$1`, retryFixture.jobID); err != nil {
		t.Fatal(err)
	}

	completeFixture := createPhase7Delivery(t, database, cipher, true)
	makePhase7Ambiguous(t, database, store, cipher, completeFixture)
	startTestUpdate(t, database, 97003)
	completeSession, err := admins.StartSession(context.Background(), adminTelegramID, app.SessionDeliveryComplete, map[string]any{"step": "confirm"}, app.RequestMeta{UpdateID: 97003, RequestID: "complete-start"})
	if err != nil {
		t.Fatal(err)
	}
	startTestUpdate(t, database, 97004)
	_, err = deliveries.Complete(context.Background(), app.DeliveryResolutionCommand{
		AdminTelegramID: adminTelegramID, JobID: completeFixture.jobID,
		ExpectedVersion:   phase7JobVersion(t, database, completeFixture.jobID) - 1,
		TelegramMessageID: 777001, Reason: "message verified in the customer chat", Session: completeSession,
		Meta: app.RequestMeta{UpdateID: 97004, RequestID: "complete-stale"},
	})
	if !errors.Is(err, app.ErrStaleVersion) {
		t.Fatalf("stale completion error = %v", err)
	}
	assertPhase7State(t, database, completeFixture, "ambiguous", "delivering", "reserved")
	startTestUpdate(t, database, 97005)
	result, err = deliveries.Complete(context.Background(), app.DeliveryResolutionCommand{
		AdminTelegramID: adminTelegramID, JobID: completeFixture.jobID, ExpectedVersion: phase7JobVersion(t, database, completeFixture.jobID),
		TelegramMessageID: 777001, Reason: "message verified in the customer chat", Session: completeSession,
		Meta: app.RequestMeta{UpdateID: 97005, RequestID: "complete-confirm"},
	})
	if err != nil || result.Status != "completed" {
		t.Fatalf("Complete() = %+v, %v", result, err)
	}
	assertPhase7State(t, database, completeFixture, "completed", "delivered", "sold")
	assertCount(t, database, `SELECT count(*) FROM audit_logs WHERE action IN ('delivery.manual_retry','delivery.manual_completed')`, 2)
	if _, err := deliveries.Retry(context.Background(), app.DeliveryResolutionCommand{AdminTelegramID: adminTelegramID, JobID: retryFixture.jobID}); !errors.Is(err, app.ErrInvalidInput) {
		t.Fatalf("missing reason error = %v", err)
	}
	report, err := deliveries.Reconcile(context.Background(), adminTelegramID)
	if err != nil || !report.Clean() {
		t.Fatalf("reconciliation = %+v, %v", report, err)
	}
}

func TestPhase7ManualResolutionRollsBackWhenAuditFails(t *testing.T) {
	database := newTestDatabase(t, true)
	cipher := newPhase7Cipher(t)
	store := postgres.NewAppStore(database.pool)
	adminTelegramID := int64(9701001)
	if err := store.BootstrapAdmin(context.Background(), adminTelegramID); err != nil {
		t.Fatal(err)
	}
	admins := app.NewAdminService(store, 15*time.Minute)
	deliveries := app.NewDeliveryAdminService(store, 8)
	fixture := createPhase7Delivery(t, database, cipher, true)
	makePhase7Ambiguous(t, database, store, cipher, fixture)
	startTestUpdate(t, database, 97101)
	session, err := admins.StartSession(context.Background(), adminTelegramID, app.SessionDeliveryRetry, map[string]any{"step": "confirm"}, app.RequestMeta{UpdateID: 97101, RequestID: "audit-start"})
	if err != nil {
		t.Fatal(err)
	}
	startTestUpdate(t, database, 97102)
	if _, err := database.pool.Exec(context.Background(), `
		CREATE FUNCTION reject_delivery_audit() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			IF NEW.action = 'delivery.manual_retry' THEN RAISE EXCEPTION 'forced delivery audit failure'; END IF;
			RETURN NEW;
		END $$;
		CREATE TRIGGER reject_delivery_audit BEFORE INSERT ON audit_logs
		FOR EACH ROW EXECUTE FUNCTION reject_delivery_audit();
	`); err != nil {
		t.Fatal(err)
	}
	_, err = deliveries.Retry(context.Background(), app.DeliveryResolutionCommand{
		AdminTelegramID: adminTelegramID, JobID: fixture.jobID,
		ExpectedVersion: phase7JobVersion(t, database, fixture.jobID),
		Reason:          "verified not delivered", Session: session,
		Meta: app.RequestMeta{UpdateID: 97102, RequestID: "audit-failure"},
	})
	if err == nil {
		t.Fatal("Retry() error = nil, want forced audit failure")
	}
	assertPhase7State(t, database, fixture, "ambiguous", "delivering", "reserved")
	var sessionState, receiptStatus string
	if err := database.pool.QueryRow(context.Background(), `SELECT state FROM admin_sessions WHERE id=$1`, session.ID).Scan(&sessionState); err != nil {
		t.Fatal(err)
	}
	if err := database.pool.QueryRow(context.Background(), `SELECT status FROM telegram_update_receipts WHERE update_id=97102`).Scan(&receiptStatus); err != nil {
		t.Fatal(err)
	}
	if sessionState != app.SessionDeliveryRetry || receiptStatus != "processing" {
		t.Fatalf("rollback state session=%s receipt=%s", sessionState, receiptStatus)
	}
}

func newPhase7Cipher(t *testing.T) *inventorycrypto.Cipher {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	cipher, err := inventorycrypto.New(key, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	return cipher
}

func createPhase7Delivery(t *testing.T, database *testDatabase, cipher *inventorycrypto.Cipher, createJob bool) phase7Fixture {
	t.Helper()
	ctx := context.Background()
	user := database.createUser(t)
	productID := database.createProduct(t, database.createCategory(t))
	order := database.createOrder(t, user.ID)
	item, err := database.queries.InsertOrderItem(ctx, generated.InsertOrderItemParams{
		OrderID: order.ID, ProductID: productID, ProductName: "Phase 7 product", UnitPriceVnd: 10_000, Quantity: 1, LineTotalVnd: 10_000,
	})
	if err != nil {
		t.Fatal(err)
	}
	secret := fmt.Sprintf("runtime-phase7-%d-<&", time.Now().UnixNano())
	protected, err := cipher.Protect(ctx, productID, []byte(secret))
	if err != nil {
		t.Fatal(err)
	}
	var inventoryID int64
	if err := database.pool.QueryRow(ctx, `
		INSERT INTO inventory_items (
			product_id, encrypted_payload, encryption_key_id, payload_fingerprint,
			encryption_nonce, encryption_format, encryption_key_version,
			status, reserved_order_id, reserved_until
		) VALUES ($1,$2,'phase7-runtime-key',$3,$4,$5,$6,'reserved',$7,clock_timestamp()+interval '1 hour')
		RETURNING id
	`, productID, protected.Ciphertext, protected.Fingerprint, protected.Nonce, protected.Format, protected.KeyVersion, order.ID).Scan(&inventoryID); err != nil {
		t.Fatal(err)
	}
	if err := database.queries.InsertOrderInventoryMapping(ctx, generated.InsertOrderInventoryMappingParams{OrderID: order.ID, OrderItemID: item.ID, InventoryItemID: inventoryID}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.pool.Exec(ctx, `UPDATE orders SET status='reserving', paid_at=clock_timestamp(), version=version+1 WHERE id=$1`, order.ID); err != nil {
		t.Fatal(err)
	}
	fixture := phase7Fixture{orderID: order.ID, inventoryID: inventoryID, secret: secret}
	if !createJob {
		return fixture
	}
	job, err := database.queries.InsertDeliveryJob(ctx, generated.InsertDeliveryJobParams{
		OrderID: order.ID, MaxAttempts: 5, NextAttemptAt: requiredTestTimestamp(time.Now()), RecipientChatID: user.TelegramUserID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.pool.Exec(ctx, `UPDATE orders SET status='delivering', delivery_started_at=clock_timestamp(), version=version+1 WHERE id=$1`, order.ID); err != nil {
		t.Fatal(err)
	}
	fixture.jobID = job.ID
	return fixture
}

func newPhase7Job(store *postgres.AppStore, cipher *inventorycrypto.Cipher, sender app.DeliverySender, workerID string) *app.DeliveryJob {
	return app.NewDeliveryJob(
		store, cipher, sender, nil,
		app.DeliveryRetryPolicy{Base: time.Second, Max: time.Minute, MaxAttempts: 5},
		1, time.Minute, 5*time.Second, 3500, "support", workerID,
	)
}

func makePhase7Ambiguous(t *testing.T, database *testDatabase, store *postgres.AppStore, cipher *inventorycrypto.Cipher, fixture phase7Fixture) {
	t.Helper()
	sender := &phase7Sender{err: errors.New("ambiguous transport result")}
	if _, err := newPhase7Job(store, cipher, sender, fmt.Sprintf("ambiguous-%d", fixture.jobID)).RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertPhase7State(t, database, fixture, "ambiguous", "delivering", "reserved")
}

func assertPhase7State(t *testing.T, database *testDatabase, fixture phase7Fixture, wantJob, wantOrder, wantInventory string) {
	t.Helper()
	var jobStatus, orderStatus, inventoryStatus string
	ctx := context.Background()
	if err := database.pool.QueryRow(ctx, `SELECT status FROM outbox_events WHERE id=$1`, fixture.jobID).Scan(&jobStatus); err != nil {
		t.Fatal(err)
	}
	if err := database.pool.QueryRow(ctx, `SELECT status FROM orders WHERE id=$1`, fixture.orderID).Scan(&orderStatus); err != nil {
		t.Fatal(err)
	}
	if err := database.pool.QueryRow(ctx, `SELECT status FROM inventory_items WHERE id=$1`, fixture.inventoryID).Scan(&inventoryStatus); err != nil {
		t.Fatal(err)
	}
	if jobStatus != wantJob || orderStatus != wantOrder || inventoryStatus != wantInventory {
		t.Fatalf("states job=%s/%s order=%s/%s inventory=%s/%s", jobStatus, wantJob, orderStatus, wantOrder, inventoryStatus, wantInventory)
	}
}

func phase7JobVersion(t *testing.T, database *testDatabase, jobID int64) int64 {
	t.Helper()
	var version int64
	if err := database.pool.QueryRow(context.Background(), `SELECT version FROM outbox_events WHERE id=$1`, jobID).Scan(&version); err != nil {
		t.Fatal(err)
	}
	return version
}

func assertPhase7PlaintextAbsent(t *testing.T, database *testDatabase, secret string) {
	t.Helper()
	var count int
	err := database.pool.QueryRow(context.Background(), `
		SELECT count(*) FROM (
			SELECT payload::text AS value FROM outbox_events
			UNION ALL SELECT COALESCE(error_detail,'') FROM delivery_attempts
			UNION ALL SELECT COALESCE(before_data::text,'') || COALESCE(after_data::text,'') FROM audit_logs
			UNION ALL SELECT COALESCE(reason_code,'') FROM order_status_history
			UNION ALL SELECT payload::text FROM admin_sessions
		) AS persisted WHERE strpos(value, $1) > 0
	`, secret).Scan(&count)
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("plaintext persisted in %d rows", count)
	}
}

var _ app.DeliverySender = (*phase7Sender)(nil)
