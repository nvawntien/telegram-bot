//go:build integration

package integration_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/nvawntien/telegram-bot/internal/app"
	"github.com/nvawntien/telegram-bot/internal/domain"
	"github.com/nvawntien/telegram-bot/internal/inventorycrypto"
	"github.com/nvawntien/telegram-bot/internal/payment"
	"github.com/nvawntien/telegram-bot/internal/postgres"
	"github.com/nvawntien/telegram-bot/internal/postgres/generated"
)

const phase8Provider = "reference_api"

type phase8APIProvider struct {
	environment payment.ProviderEnvironment
	pages       map[string]payment.TransactionPage
	mu          sync.Mutex
	requests    []payment.ListTransactionsRequest
}

func (*phase8APIProvider) Name() payment.ProviderName { return phase8Provider }
func (*phase8APIProvider) Enabled() bool              { return true }
func (p *phase8APIProvider) Environment() payment.ProviderEnvironment {
	return p.environment
}
func (*phase8APIProvider) Capabilities() payment.ProviderCapabilities {
	return payment.ProviderCapabilities{SupportsReconciliation: true, SupportsTestMode: true}
}
func (p *phase8APIProvider) ListTransactions(_ context.Context, request payment.ListTransactionsRequest) (payment.TransactionPage, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.requests = append(p.requests, request)
	return p.pages[request.Cursor], nil
}

func TestPhase8AAutomaticProviderPaymentDeliversWithoutAdmin(t *testing.T) {
	database := newTestDatabase(t, true)
	cipher := newPhase7Cipher(t)
	order, productID, mappingID, destination := createPhase8MappedOrder(t, database, "production", "TS001122334455")
	secret := addPhase8EncryptedInventory(t, database, cipher, productID)
	store := postgres.NewAppStore(database.pool)
	ingestion := app.NewPaymentEventIngestionService(store, 5)
	event := phase8Event(t, order, phase8Provider, "production", "webhook-event", "provider-transaction", destination, "webhook")

	if result, err := ingestion.Ingest(context.Background(), event); err != nil || result.Duplicate {
		t.Fatalf("Ingest() = %+v, %v", result, err)
	}
	runPhase8PaymentWorker(t, store)

	var status string
	var storedMappingID int64
	if err := database.pool.QueryRow(context.Background(), `SELECT status FROM orders WHERE id=$1`, order.ID).Scan(&status); err != nil || status != "delivering" {
		t.Fatalf("order status = %q, err=%v", status, err)
	}
	if err := database.pool.QueryRow(context.Background(), `SELECT provider_account_mapping_id FROM payment_events WHERE external_event_id=$1`, event.ExternalEventID).Scan(&storedMappingID); err != nil || storedMappingID != mappingID {
		t.Fatalf("mapping = %d, err=%v", storedMappingID, err)
	}
	assertCount(t, database, `SELECT count(*) FROM payments WHERE order_id=$1 AND status='confirmed'`, 1, order.ID)
	assertCount(t, database, `SELECT count(*) FROM order_inventory_items WHERE order_id=$1 AND status='active'`, 1, order.ID)
	assertCount(t, database, `SELECT count(*) FROM outbox_events WHERE delivery_order_id=$1 AND event_type='order.delivery_requested'`, 1, order.ID)

	sender := &phase7Sender{}
	deliveryJob := newPhase7Job(store, cipher, sender, "phase8-delivery")
	if count, err := deliveryJob.RunOnce(context.Background()); err != nil || count != 1 {
		t.Fatalf("delivery RunOnce() = %d, %v", count, err)
	}
	if sender.callCount() != 1 {
		t.Fatalf("delivery calls = %d", sender.callCount())
	}
	if err := database.pool.QueryRow(context.Background(), `SELECT status FROM orders WHERE id=$1`, order.ID).Scan(&status); err != nil || status != "delivered" {
		t.Fatalf("delivered status = %q, err=%v", status, err)
	}
	assertCount(t, database, `SELECT count(*) FROM inventory_items WHERE product_id=$1 AND status='sold'`, 1, productID)
	assertPhase7PlaintextAbsent(t, database, secret)

	duplicate, err := ingestion.Ingest(context.Background(), event)
	if err != nil || !duplicate.Duplicate {
		t.Fatalf("duplicate ingest = %+v, %v", duplicate, err)
	}
	runPhase8PaymentWorker(t, store)
	assertCount(t, database, `SELECT count(*) FROM payments WHERE order_id=$1`, 1, order.ID)
	assertCount(t, database, `SELECT count(*) FROM outbox_events WHERE delivery_order_id=$1`, 1, order.ID)
}

func TestPhase8AMissedWebhookRecoveredByAPIAndCheckpoint(t *testing.T) {
	database := newTestDatabase(t, true)
	order, productID, _, destination := createPhase8MappedOrder(t, database, "production", "TS112233445566")
	database.createInventory(t, productID, "available", pgtype.Int8{}, pgtype.Timestamptz{}, pgtype.Int8{})
	event := phase8Event(t, order, phase8Provider, "production", "api-event", "api-transaction", destination, "reconciliation")
	provider := &phase8APIProvider{environment: payment.EnvironmentProduction, pages: map[string]payment.TransactionPage{
		"":         {Transactions: []app.NormalizedPaymentEvent{event}, NextCursor: "opaque-1"},
		"opaque-1": {Transactions: []app.NormalizedPaymentEvent{event}, NextCursor: "opaque-1"},
	}}
	registry, err := payment.NewProviderRegistry(provider)
	if err != nil {
		t.Fatal(err)
	}
	store := postgres.NewAppStore(database.pool)
	reconciliation, err := payment.NewReconciliationJob(
		registry, store, app.NewPaymentEventIngestionService(store, 5), "phase8-worker",
		3, 50, time.Second, time.Minute, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	first, err := reconciliation.RunOnce(context.Background())
	if err != nil || first.Ingested != 1 || first.Duplicates != 0 {
		t.Fatalf("first reconciliation = %+v, %v", first, err)
	}
	runPhase8PaymentWorker(t, store)
	assertCount(t, database, `SELECT count(*) FROM payments WHERE order_id=$1 AND status='confirmed'`, 1, order.ID)
	assertCount(t, database, `SELECT count(*) FROM outbox_events WHERE delivery_order_id=$1`, 1, order.ID)

	second, err := reconciliation.RunOnce(context.Background())
	if err != nil || second.Duplicates != 1 {
		t.Fatalf("second reconciliation = %+v, %v", second, err)
	}
	var cursor string
	var successful bool
	if err := database.pool.QueryRow(context.Background(), `SELECT cursor_value, last_successful_at IS NOT NULL FROM payment_provider_checkpoints`).Scan(&cursor, &successful); err != nil || cursor != "opaque-1" || !successful {
		t.Fatalf("checkpoint cursor=%q successful=%t err=%v", cursor, successful, err)
	}
	assertCount(t, database, `SELECT count(*) FROM payment_events WHERE provider_transaction_id=$1`, 1, event.ProviderTransactionID)
	assertCount(t, database, `SELECT count(*) FROM payments WHERE order_id=$1`, 1, order.ID)
}

func TestPhase8AWebhookAndAPITransactionConvergeOnOneEvent(t *testing.T) {
	database := newTestDatabase(t, true)
	order, productID, _, destination := createPhase8MappedOrder(t, database, "production", "TSAABBCCDDEEFF")
	database.createInventory(t, productID, "available", pgtype.Int8{}, pgtype.Timestamptz{}, pgtype.Int8{})
	store := postgres.NewAppStore(database.pool)
	ingestion := app.NewPaymentEventIngestionService(store, 5)
	webhookEvent := phase8Event(t, order, phase8Provider, "production", "webhook-copy", "shared-transaction", destination, "webhook")
	if _, err := ingestion.Ingest(context.Background(), webhookEvent); err != nil {
		t.Fatal(err)
	}
	apiEvent := webhookEvent
	apiEvent.ExternalEventID = "api-copy"
	apiEvent.Source = "reconciliation"
	provider := &phase8APIProvider{environment: payment.EnvironmentProduction, pages: map[string]payment.TransactionPage{"": {Transactions: []app.NormalizedPaymentEvent{apiEvent}}}}
	registry, _ := payment.NewProviderRegistry(provider)
	reconciliation, _ := payment.NewReconciliationJob(registry, store, ingestion, "phase8-both", 1, 10, time.Second, time.Minute, nil)
	summary, err := reconciliation.RunOnce(context.Background())
	if err != nil || summary.Duplicates != 1 {
		t.Fatalf("reconciliation = %+v, %v", summary, err)
	}
	runPhase8PaymentWorker(t, store)
	assertCount(t, database, `SELECT count(*) FROM payment_events WHERE provider_transaction_id='shared-transaction'`, 1)
	assertCount(t, database, `SELECT count(*) FROM payments WHERE order_id=$1`, 1, order.ID)
	assertCount(t, database, `SELECT count(*) FROM order_inventory_items WHERE order_id=$1`, 1, order.ID)
	assertCount(t, database, `SELECT count(*) FROM outbox_events WHERE delivery_order_id=$1`, 1, order.ID)
}

func TestPhase8AProviderSafetyBoundaries(t *testing.T) {
	t.Run("outbound ignored", func(t *testing.T) {
		database := newTestDatabase(t, true)
		order, productID, _, destination := createPhase8MappedOrder(t, database, "production", "TSABCDEF123456")
		database.createInventory(t, productID, "available", pgtype.Int8{}, pgtype.Timestamptz{}, pgtype.Int8{})
		store := postgres.NewAppStore(database.pool)
		event := phase8Event(t, order, phase8Provider, "production", "outbound-event", "outbound-transaction", destination, "webhook")
		event.Direction = "outbound"
		if _, err := app.NewPaymentEventIngestionService(store, 5).Ingest(context.Background(), event); err != nil {
			t.Fatal(err)
		}
		runPhase8PaymentWorker(t, store)
		assertCount(t, database, `SELECT count(*) FROM payments WHERE order_id=$1`, 0, order.ID)
		assertCount(t, database, `SELECT count(*) FROM order_inventory_items WHERE order_id=$1`, 0, order.ID)
		var status, code string
		if err := database.pool.QueryRow(context.Background(), `SELECT processing_status,last_error_code FROM payment_events WHERE external_event_id='outbound-event'`).Scan(&status, &code); err != nil || status != "completed" || code != "provider_unsupported_transaction" {
			t.Fatalf("outbound status=%s code=%s err=%v", status, code, err)
		}
	})

	t.Run("disabled mapping reviews", func(t *testing.T) {
		database := newTestDatabase(t, true)
		order, productID, mappingID, destination := createPhase8MappedOrder(t, database, "production", "TS123456ABCDEF")
		database.createInventory(t, productID, "available", pgtype.Int8{}, pgtype.Timestamptz{}, pgtype.Int8{})
		if _, err := database.pool.Exec(context.Background(), `UPDATE payment_provider_accounts SET status='inactive' WHERE id=$1`, mappingID); err != nil {
			t.Fatal(err)
		}
		store := postgres.NewAppStore(database.pool)
		if _, err := app.NewPaymentEventIngestionService(store, 5).Ingest(context.Background(), phase8Event(t, order, phase8Provider, "production", "unmapped-event", "unmapped-transaction", destination, "webhook")); err != nil {
			t.Fatal(err)
		}
		runPhase8PaymentWorker(t, store)
		assertCount(t, database, `SELECT count(*) FROM payments WHERE order_id=$1`, 0, order.ID)
		assertCount(t, database, `SELECT count(*) FROM payment_review_cases WHERE reason='provider_account_unmapped'`, 1)
	})

	t.Run("test event cannot settle production order", func(t *testing.T) {
		database := newTestDatabase(t, true)
		order, productID := createPhase6PaymentOrder(t, database, 1)
		reference := "TSFEDCBA654321"
		bankID := createPhase8Bank(t, database, "test")
		destination := "test-provider-account"
		createPhase8Mapping(t, database, phase8Provider, "test", destination, bankID)
		setPhase8OrderBank(t, database, order.ID, reference, "production", bankID)
		order.PaymentReference = reference
		database.createInventory(t, productID, "available", pgtype.Int8{}, pgtype.Timestamptz{}, pgtype.Int8{})
		store := postgres.NewAppStore(database.pool)
		if _, err := app.NewPaymentEventIngestionService(store, 5).Ingest(context.Background(), phase8Event(t, order, phase8Provider, "test", "test-event", "test-transaction", destination, "webhook")); err != nil {
			t.Fatal(err)
		}
		runPhase8PaymentWorker(t, store)
		assertCount(t, database, `SELECT count(*) FROM payments WHERE order_id=$1 AND status='confirmed'`, 0, order.ID)
		assertCount(t, database, `SELECT count(*) FROM payment_review_cases WHERE order_id=$1 AND reason='provider_destination_account_mismatch'`, 1, order.ID)
	})
}

func TestPhase8AWalletTopupCreditsExactlyOnceThroughMappedProvider(t *testing.T) {
	database := newTestDatabase(t, true)
	ctx := context.Background()
	user := database.createUser(t)
	wallet, err := database.queries.EnsureWalletAccount(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	bankID := createPhase8Bank(t, database, "production")
	destination := "wallet-provider-account"
	createPhase8Mapping(t, database, phase8Provider, "production", destination, bankID)
	reference := "TS998877665544"
	topup, err := database.queries.CreateWalletTopup(ctx, generated.CreateWalletTopupParams{
		UserID: user.ID, WalletAccountID: wallet.ID, AmountVnd: 75_000,
		PaymentReference: reference, IdempotencyKey: "phase8-wallet-topup",
		ExpiresAt: requiredTestTimestamp(time.Now().Add(time.Hour)), BankAccountID: bankID,
		BankBinSnapshot: "970415", BankNameSnapshot: "Test Bank", BankDisplayNameSnapshot: "Phase 8",
		BankAccountNameSnapshot: "ACCOUNT", EncryptedAccountNumberSnapshot: make([]byte, 16),
		AccountNumberNonceSnapshot: make([]byte, 12), AccountKeyVersionSnapshot: 1,
		AccountLast4Snapshot: "1234", PaymentEnvironment: "production",
	})
	if err != nil {
		t.Fatal(err)
	}
	orderLike := generated.Order{PaymentReference: topup.PaymentReference, TotalVnd: topup.AmountVnd, PaymentEnvironment: "production"}
	event := phase8Event(t, orderLike, phase8Provider, "production", "wallet-event", "wallet-transaction", destination, "webhook")
	store := postgres.NewAppStore(database.pool)
	ingestion := app.NewPaymentEventIngestionService(store, 5)
	if _, err := ingestion.Ingest(ctx, event); err != nil {
		t.Fatal(err)
	}
	runPhase8PaymentWorker(t, store)
	if _, err := ingestion.Ingest(ctx, event); err != nil {
		t.Fatal(err)
	}
	runPhase8PaymentWorker(t, store)
	var balance int64
	if err := database.pool.QueryRow(ctx, `SELECT balance_vnd FROM wallet_accounts WHERE id=$1`, wallet.ID).Scan(&balance); err != nil || balance != 75_000 {
		t.Fatalf("wallet balance=%d err=%v", balance, err)
	}
	assertCount(t, database, `SELECT count(*) FROM wallet_ledger_entries WHERE account_id=$1 AND reference_type='wallet_topup'`, 1, wallet.ID)
	assertCount(t, database, `SELECT count(*) FROM payments WHERE provider_transaction_id='wallet-transaction'`, 1)
}

func TestPhase8AProviderAccountAdministrationIsAuditedAndVersionGuarded(t *testing.T) {
	database := newTestDatabase(t, true)
	ctx := context.Background()
	store := postgres.NewAppStore(database.pool)
	const adminTelegramID int64 = 9_800_001
	if err := store.BootstrapAdmin(ctx, adminTelegramID); err != nil {
		t.Fatal(err)
	}
	admins := app.NewAdminService(store, time.Hour)
	service := app.NewPaymentProviderAdminService(store, admins, []app.PaymentProviderDescriptor{{
		Name: phase8Provider, Enabled: true, Environment: "production",
		Capabilities: app.PaymentProviderCapabilities{Reconciliation: true, TestMode: true},
	}}, 8, nil)
	bankID := createPhase8Bank(t, database, "production")
	destination := "admin-linked-provider-account-987654"

	startTestUpdate(t, database, 98001)
	session, err := admins.StartSession(ctx, adminTelegramID, app.SessionProviderAccountCreate, map[string]any{"step": "input"}, app.RequestMeta{UpdateID: 98001, RequestID: "provider-create-start"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateAccount(ctx, app.CreatePaymentProviderAccountCommand{
		AdminTelegramID: adminTelegramID, Provider: phase8Provider, Environment: "production",
		ExternalAccountIdentity: destination, LocalBankAccountID: bankID, Session: session,
	}); err != app.ErrInvalidInput {
		t.Fatalf("unconfirmed create error=%v", err)
	}
	startTestUpdate(t, database, 98002)
	mapping, err := service.CreateAccount(ctx, app.CreatePaymentProviderAccountCommand{
		AdminTelegramID: adminTelegramID, Provider: phase8Provider, Environment: "production",
		ExternalAccountIdentity: destination, LocalBankAccountID: bankID, Confirmed: true,
		Session: session, Meta: app.RequestMeta{UpdateID: 98002, RequestID: "provider-create-confirm"},
	})
	if err != nil || mapping.Status != "active" || mapping.MaskedExternalIdentity == destination {
		t.Fatalf("CreateAccount()=%+v err=%v", mapping, err)
	}
	assertCount(t, database, `SELECT count(*) FROM audit_logs WHERE action='payment_provider_account.created' AND resource_id=$1`, 1, mapping.ID)
	var auditText string
	if err := database.pool.QueryRow(ctx, `SELECT after_data::text FROM audit_logs WHERE action='payment_provider_account.created' AND resource_id=$1`, mapping.ID).Scan(&auditText); err != nil || strings.Contains(auditText, destination) {
		t.Fatalf("audit leaked identity: %q err=%v", auditText, err)
	}

	startTestUpdate(t, database, 98003)
	toggleSession, err := admins.StartSession(ctx, adminTelegramID, app.SessionProviderAccountToggle, map[string]any{"step": "confirm"}, app.RequestMeta{UpdateID: 98003, RequestID: "provider-toggle-start"})
	if err != nil {
		t.Fatal(err)
	}
	startTestUpdate(t, database, 98004)
	updated, err := service.SetAccountActive(ctx, app.SetPaymentProviderAccountStatusCommand{
		AdminTelegramID: adminTelegramID, MappingID: mapping.ID, ExpectedVersion: mapping.Version,
		Active: false, Confirmed: true, Session: toggleSession,
		Meta: app.RequestMeta{UpdateID: 98004, RequestID: "provider-toggle-confirm"},
	})
	if err != nil || updated.Status != "inactive" || updated.Version != mapping.Version+1 {
		t.Fatalf("SetAccountActive()=%+v err=%v", updated, err)
	}
	if _, err := service.SetAccountActive(ctx, app.SetPaymentProviderAccountStatusCommand{
		AdminTelegramID: adminTelegramID, MappingID: mapping.ID, ExpectedVersion: mapping.Version,
		Active: false, Confirmed: true, Session: toggleSession,
	}); err == nil {
		t.Fatal("stale mapping update succeeded")
	}
	if _, err := service.ListAccounts(ctx, 123456789, 0); err == nil {
		t.Fatal("unauthorized provider account list succeeded")
	}
	health, err := service.Health(ctx, adminTelegramID)
	if err != nil || len(health) != 1 || health[0].ActiveMappings != 0 {
		t.Fatalf("Health()=%+v err=%v", health, err)
	}
}

func TestPhase8AConcurrentDuplicateAndConflictEvidence(t *testing.T) {
	database := newTestDatabase(t, true)
	order, _, _, destination := createPhase8MappedOrder(t, database, "production", "TS010203040506")
	store := postgres.NewAppStore(database.pool)
	ingestion := app.NewPaymentEventIngestionService(store, 5)
	event := phase8Event(t, order, phase8Provider, "production", "concurrent-event", "concurrent-transaction", destination, "webhook")
	var wait sync.WaitGroup
	errorsByRequest := make(chan error, 100)
	for index := 0; index < 100; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, err := ingestion.Ingest(context.Background(), event)
			errorsByRequest <- err
		}()
	}
	wait.Wait()
	close(errorsByRequest)
	for err := range errorsByRequest {
		if err != nil {
			t.Fatal(err)
		}
	}
	assertCount(t, database, `SELECT count(*) FROM payment_events WHERE external_event_id='concurrent-event'`, 1)

	conflict := event
	conflict.Amount++
	conflict.PayloadHash = sha256Bytes("conflicting-payload")
	conflict.SanitizedMetadata, _ = json.Marshal(map[string]any{
		"reference": conflict.Reference, "amount_vnd": conflict.Amount.Int64(),
		"currency": conflict.Currency, "occurred_at": conflict.OccurredAt,
	})
	if _, err := ingestion.Ingest(context.Background(), conflict); err != app.ErrPaymentEventConflict {
		t.Fatalf("conflicting ingest error = %v", err)
	}
	assertCount(t, database, `SELECT count(*) FROM payment_review_cases WHERE reason='provider_event_payload_conflict'`, 1)
	assertCount(t, database, `SELECT count(*) FROM payments WHERE provider_transaction_id='concurrent-transaction'`, 0)
}

func createPhase8MappedOrder(t *testing.T, database *testDatabase, environment, reference string) (generated.Order, int64, int64, string) {
	t.Helper()
	order, productID := createPhase6PaymentOrder(t, database, 1)
	bankID := createPhase8Bank(t, database, environment)
	destination := fmt.Sprintf("provider-account-%d", order.ID)
	mappingID := createPhase8Mapping(t, database, phase8Provider, environment, destination, bankID)
	setPhase8OrderBank(t, database, order.ID, reference, environment, bankID)
	order.PaymentReference = reference
	order.PaymentEnvironment = environment
	return order, productID, mappingID, destination
}

func setPhase8OrderBank(t *testing.T, database *testDatabase, orderID int64, reference, environment string, bankID int64) {
	t.Helper()
	if _, err := database.pool.Exec(context.Background(), `
		UPDATE orders AS target
		SET payment_reference=$2,payment_environment=$3,bank_account_id=bank.id,
			bank_bin_snapshot=bank.bank_bin,bank_name_snapshot=bank.bank_name,
			bank_display_name_snapshot=bank.display_name,bank_account_name_snapshot=bank.account_name,
			encrypted_account_number_snapshot=bank.encrypted_account_number,
			account_number_nonce_snapshot=bank.encryption_nonce,
			account_encryption_format_snapshot=bank.encryption_format,
			account_key_version_snapshot=bank.encryption_key_version,
			account_last4_snapshot=bank.display_last4
		FROM bank_accounts AS bank
		WHERE target.id=$1 AND bank.id=$4
	`, orderID, reference, environment, bankID); err != nil {
		t.Fatal(err)
	}
}

func createPhase8Bank(t *testing.T, database *testDatabase, environment string) int64 {
	t.Helper()
	seed := database.keySequence.Add(1)
	fingerprint := sha256Bytes(fmt.Sprintf("phase8-bank-%d", seed))
	var id int64
	if err := database.pool.QueryRow(context.Background(), `
		INSERT INTO bank_accounts (
			bank_bin,bank_name,account_name,encrypted_account_number,account_number_fingerprint,
			encryption_key_id,display_last4,display_name,encryption_nonce,encryption_format,
			encryption_key_version,payment_environment
		) VALUES ('970415','Test Bank','ACCOUNT',$1,$2,'bank-key-v1','1234','Phase 8',$3,'aes-256-gcm-v1',1,$4)
		RETURNING id
	`, make([]byte, 16), fingerprint, make([]byte, 12), environment).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func createPhase8Mapping(t *testing.T, database *testDatabase, provider, environment, destination string, bankID int64) int64 {
	t.Helper()
	var id int64
	if err := database.pool.QueryRow(context.Background(), `
		INSERT INTO payment_provider_accounts (
			provider,environment,external_account_identity,external_identity_fingerprint,local_bank_account_id
		) VALUES ($1,$2,$3,$4,$5) RETURNING id
	`, provider, environment, destination, sha256Bytes(provider+"\x00"+environment+"\x00"+destination), bankID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func phase8Event(t *testing.T, order generated.Order, provider, environment, eventID, transactionID, destination, source string) app.NormalizedPaymentEvent {
	t.Helper()
	occurredAt := time.Now().UTC().Truncate(time.Second)
	metadata, err := json.Marshal(map[string]any{
		"reference": order.PaymentReference, "amount_vnd": order.TotalVnd,
		"currency": "VND", "occurred_at": occurredAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	return app.NormalizedPaymentEvent{
		Provider: provider, Environment: environment, ExternalEventID: eventID,
		ProviderTransactionID: transactionID, Reference: order.PaymentReference,
		Direction: "inbound", TransferContent: "payment " + order.PaymentReference + " completed",
		DestinationAccountID: destination, Source: source,
		Amount: domain.Money(order.TotalVnd), Currency: "VND", OccurredAt: occurredAt,
		EventType: "payment.received", PayloadHash: sha256Bytes(eventID + ":" + transactionID),
		SanitizedMetadata: metadata,
	}
}

func runPhase8PaymentWorker(t *testing.T, store *postgres.AppStore) {
	t.Helper()
	extractor, err := app.NewPaymentReferenceExtractor("TS", 6, app.DefaultPaymentTransferContentLimit)
	if err != nil {
		t.Fatal(err)
	}
	job := app.NewPaymentEventJob(
		store, app.NewPaymentAcceptanceService(store, time.Hour, nil),
		100, time.Millisecond, time.Minute,
	).WithReferenceExtractor(extractor)
	if _, err := job.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func addPhase8EncryptedInventory(t *testing.T, database *testDatabase, cipher *inventorycrypto.Cipher, productID int64) string {
	t.Helper()
	secret := fmt.Sprintf("phase8-secret-%d-<&", time.Now().UnixNano())
	protected, err := cipher.Protect(context.Background(), productID, []byte(secret))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.pool.Exec(context.Background(), `
		INSERT INTO inventory_items (
			product_id,encrypted_payload,encryption_key_id,payload_fingerprint,encryption_nonce,
			encryption_format,encryption_key_version,status
		) VALUES ($1,$2,'phase8-runtime-key',$3,$4,$5,$6,'available')
	`, productID, protected.Ciphertext, protected.Fingerprint, protected.Nonce, protected.Format, protected.KeyVersion); err != nil {
		t.Fatal(err)
	}
	return secret
}

func sha256Bytes(value string) []byte {
	hash := sha256.Sum256([]byte(value))
	return hash[:]
}
