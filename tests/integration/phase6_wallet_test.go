//go:build integration

package integration_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/nvawntien/telegram-bot/internal/app"
	"github.com/nvawntien/telegram-bot/internal/inventorycrypto"
	"github.com/nvawntien/telegram-bot/internal/postgres"
	"github.com/nvawntien/telegram-bot/internal/postgres/generated"
	telegramadapter "github.com/nvawntien/telegram-bot/internal/telegram"
	"github.com/nvawntien/telegram-bot/internal/vietqr"
)

func TestPhase6WalletTopupCreditsExactlyOnce(t *testing.T) {
	database := newTestDatabase(t, true)
	user := database.createUser(t)
	wallet, err := database.queries.EnsureWalletAccount(context.Background(), user.ID)
	if err != nil {
		t.Fatal(err)
	}
	bankID := createPhase6Bank(t, database)
	topup, err := database.queries.CreateWalletTopup(context.Background(), generated.CreateWalletTopupParams{
		UserID: user.ID, WalletAccountID: wallet.ID, AmountVnd: 50_000,
		PaymentReference: "TOPUP-EXACT-1", IdempotencyKey: "topup-idempotency-1",
		ExpiresAt: requiredTestTimestamp(time.Now().Add(time.Hour)), BankAccountID: bankID,
		BankBinSnapshot: "970415", BankNameSnapshot: "Test Bank", BankDisplayNameSnapshot: "Test",
		BankAccountNameSnapshot: "ACCOUNT", EncryptedAccountNumberSnapshot: make([]byte, 16),
		AccountNumberNonceSnapshot: make([]byte, 12), AccountKeyVersionSnapshot: 1, AccountLast4Snapshot: "1234",
	})
	if err != nil {
		t.Fatal(err)
	}
	orderLike := generated.Order{PaymentReference: topup.PaymentReference, TotalVnd: topup.AmountVnd}
	processPhase6Event(t, database, orderLike, "topup-transaction-1")

	store := postgres.NewAppStore(database.pool)
	duplicate := phase6Event(t, orderLike, "topup-transaction-1")
	duplicate.ExternalEventID = "event-topup-duplicate"
	if _, err := app.NewPaymentEventIngestionService(store, 5).Ingest(context.Background(), duplicate); err != nil {
		t.Fatal(err)
	}
	job := app.NewPaymentEventJob(store, app.NewPaymentAcceptanceService(store, time.Hour, nil), 10, time.Millisecond, time.Minute)
	if _, err := job.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}

	var balance, ledgerSum int64
	var ledgerCount, paymentCount int
	if err := database.pool.QueryRow(context.Background(), `SELECT balance_vnd FROM wallet_accounts WHERE id=$1`, wallet.ID).Scan(&balance); err != nil || balance != 50_000 {
		t.Fatalf("balance = %d, err=%v", balance, err)
	}
	if err := database.pool.QueryRow(context.Background(), `SELECT COALESCE(sum(amount_vnd),0), count(*) FROM wallet_ledger_entries WHERE account_id=$1`, wallet.ID).Scan(&ledgerSum, &ledgerCount); err != nil || ledgerSum != balance || ledgerCount != 1 {
		t.Fatalf("ledger sum/count = %d/%d, err=%v", ledgerSum, ledgerCount, err)
	}
	if err := database.pool.QueryRow(context.Background(), `SELECT count(*) FROM payments WHERE provider_transaction_id='topup-transaction-1'`).Scan(&paymentCount); err != nil || paymentCount != 1 {
		t.Fatalf("payment count = %d, err=%v", paymentCount, err)
	}
}

func TestPhase6WalletPaymentStockFailureRollsBackDebit(t *testing.T) {
	database := newTestDatabase(t, true)
	user := database.createUser(t)
	productID := database.createProduct(t, database.createCategory(t))
	order := createWalletOrder(t, database, user.ID, productID, 2, 20_000)
	database.createInventory(t, productID, "available", pgtype.Int8{}, pgtype.Timestamptz{}, pgtype.Int8{})
	wallet := creditTestWallet(t, database, user.ID, 20_000)
	startTestUpdate(t, database, 7001)

	_, err := postgres.NewAppStore(database.pool).PayOrderWithWallet(context.Background(), app.WalletOrderPaymentCommand{
		TelegramUserID: user.TelegramUserID, OrderID: order.ID, IdempotencyKey: "wallet-stock-failure", Meta: app.RequestMeta{UpdateID: 7001, RequestID: "wallet-stock-failure"},
	}, time.Now(), time.Hour)
	if !errors.Is(err, app.ErrInsufficientInventory) {
		t.Fatalf("PayOrderWithWallet() error = %v", err)
	}
	var balance int64
	var debits, mappings, payments int
	ctx := context.Background()
	_ = database.pool.QueryRow(ctx, `SELECT balance_vnd FROM wallet_accounts WHERE id=$1`, wallet.ID).Scan(&balance)
	_ = database.pool.QueryRow(ctx, `SELECT count(*) FROM wallet_ledger_entries WHERE account_id=$1 AND entry_type='debit'`, wallet.ID).Scan(&debits)
	_ = database.pool.QueryRow(ctx, `SELECT count(*) FROM order_inventory_items WHERE order_id=$1`, order.ID).Scan(&mappings)
	_ = database.pool.QueryRow(ctx, `SELECT count(*) FROM payments WHERE order_id=$1`, order.ID).Scan(&payments)
	if balance != 20_000 || debits != 0 || mappings != 0 || payments != 0 {
		t.Fatalf("rollback invariant balance=%d debits=%d mappings=%d payments=%d", balance, debits, mappings, payments)
	}
}

func TestPhase6HundredConcurrentWalletDebitsDoNotOverspend(t *testing.T) {
	database := newTestDatabase(t, true)
	user := database.createUser(t)
	productID := database.createProduct(t, database.createCategory(t))
	wallet := creditTestWallet(t, database, user.ID, 1_000)
	orders := make([]generated.Order, 100)
	for i := range orders {
		orders[i] = createWalletOrder(t, database, user.ID, productID, 1, 100)
		database.createInventory(t, productID, "available", pgtype.Int8{}, pgtype.Timestamptz{}, pgtype.Int8{})
		startTestUpdate(t, database, int64(8000+i))
	}
	store := postgres.NewAppStore(database.pool)
	var successes atomic.Int32
	var unexpected atomic.Value
	var wait sync.WaitGroup
	for i, order := range orders {
		wait.Add(1)
		go func(index int, order generated.Order) {
			defer wait.Done()
			_, err := store.PayOrderWithWallet(context.Background(), app.WalletOrderPaymentCommand{
				TelegramUserID: user.TelegramUserID, OrderID: order.ID,
				IdempotencyKey: fmt.Sprintf("concurrent-debit-%d", index),
				Meta:           app.RequestMeta{UpdateID: int64(8000 + index), RequestID: fmt.Sprintf("concurrent-%d", index)},
			}, time.Now(), time.Hour)
			if err == nil {
				successes.Add(1)
				return
			}
			if !errors.Is(err, app.ErrInsufficientWalletBalance) {
				unexpected.Store(err)
			}
		}(i, order)
	}
	wait.Wait()
	if value := unexpected.Load(); value != nil {
		t.Fatalf("unexpected debit error: %v", value)
	}
	var balance, ledgerSum int64
	var debitCount int
	ctx := context.Background()
	_ = database.pool.QueryRow(ctx, `SELECT balance_vnd FROM wallet_accounts WHERE id=$1`, wallet.ID).Scan(&balance)
	_ = database.pool.QueryRow(ctx, `SELECT COALESCE(sum(amount_vnd),0) FROM wallet_ledger_entries WHERE account_id=$1`, wallet.ID).Scan(&ledgerSum)
	_ = database.pool.QueryRow(ctx, `SELECT count(*) FROM wallet_ledger_entries WHERE account_id=$1 AND entry_type='debit'`, wallet.ID).Scan(&debitCount)
	if successes.Load() != 10 || balance != 0 || ledgerSum != 0 || debitCount != 10 {
		t.Fatalf("successes=%d balance=%d ledger_sum=%d debits=%d", successes.Load(), balance, ledgerSum, debitCount)
	}
}

func TestPhase6AdminManualPaymentAndWalletAdjustmentAreAudited(t *testing.T) {
	database := newTestDatabase(t, true)
	store := postgres.NewAppStore(database.pool)
	adminTelegramID := int64(990001)
	if err := store.BootstrapAdmin(context.Background(), adminTelegramID); err != nil {
		t.Fatal(err)
	}
	admins := app.NewAdminService(store, 15*time.Minute)
	admin, err := admins.Authorize(context.Background(), adminTelegramID, true)
	if err != nil {
		t.Fatal(err)
	}

	buyer := database.createUser(t)
	productID := database.createProduct(t, database.createCategory(t))
	order := createWalletOrder(t, database, buyer.ID, productID, 1, 10_000)
	database.createInventory(t, productID, "available", pgtype.Int8{}, pgtype.Timestamptz{}, pgtype.Int8{})
	startTestUpdate(t, database, 9101)
	manualSession, err := admins.StartSession(context.Background(), adminTelegramID, app.SessionPaymentManual, map[string]any{"step": "payment"}, app.RequestMeta{UpdateID: 9101, RequestID: "manual-start"})
	if err != nil {
		t.Fatal(err)
	}
	startTestUpdate(t, database, 9102)
	paymentAdmin := app.NewPaymentAdminService(store, 8, time.Hour)
	result, err := paymentAdmin.ManualConfirm(context.Background(), app.ManualPaymentCommand{
		AdminTelegramID: adminTelegramID, Session: manualSession, ProviderTransactionID: "manual-transaction-1",
		Reference: order.PaymentReference, Amount: 10_000, Currency: "VND", OccurredAt: time.Now(), Note: "bank receipt checked",
		Meta: app.RequestMeta{UpdateID: 9102, RequestID: "manual-confirm"},
	})
	if err != nil || result.Decision != "accepted" {
		t.Fatalf("ManualConfirm() = %+v, %v", result, err)
	}

	startTestUpdate(t, database, 9201)
	adjustSession, err := admins.StartSession(context.Background(), adminTelegramID, app.SessionWalletAdjustment, map[string]any{"step": "adjustment"}, app.RequestMeta{UpdateID: 9201, RequestID: "adjust-start"})
	if err != nil {
		t.Fatal(err)
	}
	startTestUpdate(t, database, 9202)
	walletService := app.NewWalletService(store, nil, nil, nil, 1, 1_000_000, time.Minute, time.Hour, nil)
	account, err := walletService.Adjust(context.Background(), app.WalletAdjustmentCommand{
		AdminTelegramID: adminTelegramID, TargetTelegramID: buyer.TelegramUserID, Amount: 25_000,
		Reason: "approved customer credit", IdempotencyKey: "admin-adjustment-1", Session: adjustSession,
		Meta: app.RequestMeta{UpdateID: 9202, RequestID: "adjust-commit"},
	})
	if err != nil || account.Balance != 25_000 {
		t.Fatalf("Adjust() = %+v, %v", account, err)
	}
	var manualAudits, adjustmentAudits, ledgerEntries int
	ctx := context.Background()
	_ = database.pool.QueryRow(ctx, `SELECT count(*) FROM audit_logs WHERE actor_id=$1 AND action='payment.manual_confirmed'`, admin.ID).Scan(&manualAudits)
	_ = database.pool.QueryRow(ctx, `SELECT count(*) FROM audit_logs WHERE actor_id=$1 AND action='wallet.adjusted'`, admin.ID).Scan(&adjustmentAudits)
	_ = database.pool.QueryRow(ctx, `SELECT count(*) FROM wallet_ledger_entries WHERE account_id=$1 AND reference_type='admin_adjustment'`, account.ID).Scan(&ledgerEntries)
	if manualAudits != 1 || adjustmentAudits != 1 || ledgerEntries != 1 {
		t.Fatalf("audits/ledger = %d/%d/%d", manualAudits, adjustmentAudits, ledgerEntries)
	}
}

func TestPhase6TelegramWalletBalanceTopupAndOrderPaymentFlow(t *testing.T) {
	fixture := newOrderFixture(t)
	user := fixture.database.createUser(t)
	qr, err := vietqr.New("https://img.example.test/image/", "compact2")
	if err != nil {
		t.Fatal(err)
	}
	references, err := app.NewPaymentReferenceGenerator("TS", 6)
	if err != nil {
		t.Fatal(err)
	}
	wallet := app.NewWalletService(fixture.store, fixture.cipher, qr, references, 10_000, 1_000_000, time.Hour, time.Hour, nil)
	paymentAdmin := app.NewPaymentAdminService(fixture.store, 8, time.Hour)
	inventoryCipher, err := inventorycrypto.New(bytes.Repeat([]byte{0x71}, 32), 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	inventory := app.NewInventoryAdminService(fixture.store, fixture.admins, inventoryCipher, app.InventoryImportLimits{MaxItems: 10, MaxItemBytes: 100, MaxTotalBytes: 1000}, 8, nil)
	messenger := &phase5RecordingMessenger{}
	router := telegramadapter.NewRouterWithOrdering(
		app.NewUserService(fixture.store), app.NewCatalogService(fixture.store, 8), fixture.admins,
		inventory, fixture.banks, fixture.orders, wallet, paymentAdmin, fixture.updates,
		messenger, "Support", slog.New(slog.NewTextHandler(bytes.NewBuffer(nil), nil)), nil,
	)
	telegramUser := models.User{ID: user.TelegramUserID, FirstName: "Wallet Buyer"}
	ctx := context.Background()
	for _, command := range []string{"/balance", "/nap"} {
		if err := router.Process(ctx, messageUpdate(nextPhase5Update(fixture.database), telegramUser, command), "phase6-wallet-command"); err != nil {
			t.Fatalf("Process(%s): %v", command, err)
		}
	}
	if err := router.Process(ctx, callbackUpdateForPhase5(nextPhase5Update(fixture.database), telegramUser, "v1:w:a:50000"), "phase6-topup-amount"); err != nil {
		t.Fatal(err)
	}
	topupBankCallback := messenger.callbackWithPrefix(t, "v1:w:b:")
	if err := router.Process(ctx, callbackUpdateForPhase5(nextPhase5Update(fixture.database), telegramUser, topupBankCallback), "phase6-topup-bank"); err != nil {
		t.Fatal(err)
	}
	var topups int
	if err := fixture.database.pool.QueryRow(ctx, `SELECT count(*) FROM wallet_topup_intents WHERE user_id=$1 AND status='pending_payment'`, user.ID).Scan(&topups); err != nil || topups != 1 {
		t.Fatalf("topups=%d err=%v", topups, err)
	}

	productID := fixture.database.createProduct(t, fixture.database.createCategory(t))
	fixture.database.createInventory(t, productID, "available", pgtype.Int8{}, pgtype.Timestamptz{}, pgtype.Int8{})
	order := createPendingOrder(t, fixture, user, productID, fixture.bank.ID, 1, "phase6-wallet-order")
	creditTestWallet(t, fixture.database, user.ID, 10_000)
	if err := router.Process(ctx, callbackUpdateForPhase5(nextPhase5Update(fixture.database), telegramUser, fmt.Sprintf("v1:o:v:%d", order.Order.ID)), "phase6-wallet-order-view"); err != nil {
		t.Fatal(err)
	}
	walletAsk := messenger.callbackWithPrefix(t, "v1:o:w:")
	if err := router.Process(ctx, callbackUpdateForPhase5(nextPhase5Update(fixture.database), telegramUser, walletAsk), "phase6-wallet-order-ask"); err != nil {
		t.Fatal(err)
	}
	walletPay := messenger.callbackWithPrefix(t, "v1:o:y:")
	if err := router.Process(ctx, callbackUpdateForPhase5(nextPhase5Update(fixture.database), telegramUser, walletPay), "phase6-wallet-order-pay"); err != nil {
		t.Fatal(err)
	}
	var orderStatus string
	if err := fixture.database.pool.QueryRow(ctx, `SELECT status FROM orders WHERE id=$1`, order.Order.ID).Scan(&orderStatus); err != nil || orderStatus != "delivering" {
		t.Fatalf("order status=%s err=%v", orderStatus, err)
	}
	assertCount(t, fixture.database, `SELECT count(*) FROM outbox_events WHERE delivery_order_id=$1 AND event_type='order.delivery_requested'`, 1, order.Order.ID)
	messenger.mu.Lock()
	output, answers := messenger.output, messenger.answers
	messenger.mu.Unlock()
	for _, expected := range []string{"Số dư ví", "Hướng dẫn nạp ví", "Đã thanh toán đơn"} {
		if !bytes.Contains([]byte(output), []byte(expected)) {
			t.Fatalf("Telegram output missing %q: %s", expected, output)
		}
	}
	if answers < 5 {
		t.Fatalf("callback answers=%d", answers)
	}
}

func createPhase6Bank(t *testing.T, database *testDatabase) int64 {
	t.Helper()
	var id int64
	fingerprint := make([]byte, 32)
	fingerprint[0] = 1
	err := database.pool.QueryRow(context.Background(), `
		INSERT INTO bank_accounts (bank_bin,bank_name,account_name,encrypted_account_number,account_number_fingerprint,encryption_key_id,display_last4,display_name,encryption_nonce,encryption_format,encryption_key_version)
		VALUES ('970415','Test Bank','ACCOUNT',$1,$2,'bank-key-v1','1234','Test',$3,'aes-256-gcm-v1',1) RETURNING id
	`, make([]byte, 16), fingerprint, make([]byte, 12)).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func createWalletOrder(t *testing.T, database *testDatabase, userID, productID int64, quantity int32, total int64) generated.Order {
	t.Helper()
	order := database.createOrder(t, userID)
	_, err := database.queries.InsertOrderItem(context.Background(), generated.InsertOrderItemParams{OrderID: order.ID, ProductID: productID, ProductName: "Wallet product", UnitPriceVnd: total / int64(quantity), Quantity: quantity, LineTotalVnd: total})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.pool.Exec(context.Background(), `UPDATE orders SET subtotal_vnd=$2,total_vnd=$2 WHERE id=$1`, order.ID, total); err != nil {
		t.Fatal(err)
	}
	order.TotalVnd = total
	return order
}

func creditTestWallet(t *testing.T, database *testDatabase, userID, amount int64) generated.WalletAccount {
	t.Helper()
	wallet, err := database.queries.EnsureWalletAccount(context.Background(), userID)
	if err != nil {
		t.Fatal(err)
	}
	wallet, err = database.queries.UpdateWalletBalance(context.Background(), generated.UpdateWalletBalanceParams{AmountVnd: amount, ID: wallet.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.queries.InsertWalletLedgerEntry(context.Background(), generated.InsertWalletLedgerEntryParams{AccountID: wallet.ID, EntryType: "adjustment", AmountVnd: amount, BalanceAfterVnd: wallet.BalanceVnd, ReferenceType: "test_seed", ReferenceID: userID, IdempotencyKey: fmt.Sprintf("seed-%d", wallet.ID)}); err != nil {
		t.Fatal(err)
	}
	return wallet
}

func startTestUpdate(t *testing.T, database *testDatabase, updateID int64) {
	t.Helper()
	ctx := context.Background()
	if _, err := database.queries.InsertTelegramUpdateReceipt(ctx, generated.InsertTelegramUpdateReceiptParams{UpdateID: updateID, UpdateType: "callback_query"}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.queries.StartTelegramUpdateProcessing(ctx, generated.StartTelegramUpdateProcessingParams{UpdateID: updateID, StaleBefore: requiredTestTimestamp(time.Now().Add(-time.Minute))}); err != nil {
		t.Fatal(err)
	}
}

func requiredTestTimestamp(value time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: value, Valid: true}
}
