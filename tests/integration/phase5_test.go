//go:build integration

package integration_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/nvawntien/telegram-bot/internal/app"
	"github.com/nvawntien/telegram-bot/internal/bankcrypto"
	"github.com/nvawntien/telegram-bot/internal/domain"
	"github.com/nvawntien/telegram-bot/internal/inventorycrypto"
	"github.com/nvawntien/telegram-bot/internal/postgres"
	"github.com/nvawntien/telegram-bot/internal/postgres/generated"
	telegramadapter "github.com/nvawntien/telegram-bot/internal/telegram"
	"github.com/nvawntien/telegram-bot/internal/vietqr"
)

type orderFixture struct {
	database *testDatabase
	store    *postgres.AppStore
	updates  *app.UpdateService
	admins   *app.AdminService
	banks    *app.BankAccountService
	orders   *app.OrderService
	cipher   *bankcrypto.Cipher
	bank     app.RedactedBankAccount
}

func newOrderFixture(t *testing.T) *orderFixture {
	t.Helper()
	database := newTestDatabase(t, true)
	store := postgres.NewAppStore(database.pool)
	updates := app.NewUpdateService(store, time.Minute)
	admins := app.NewAdminService(store, time.Hour)
	cipher, err := bankcrypto.New(bytes.Repeat([]byte{0x35}, 32), 1)
	if err != nil {
		t.Fatal(err)
	}
	banks := app.NewBankAccountService(store, cipher, admins, 8)
	generator, err := vietqr.New("https://img.example.test/image/", "compact2")
	if err != nil {
		t.Fatal(err)
	}
	references, err := app.NewPaymentReferenceGenerator("TS", 6)
	if err != nil {
		t.Fatal(err)
	}
	fixture := &orderFixture{
		database: database, store: store, updates: updates, admins: admins,
		banks: banks, cipher: cipher,
		orders: app.NewOrderService(store, cipher, generator, references, 15*time.Minute, 10, 8),
	}
	fixture.bank = insertTestBank(t, fixture, "970422", "1234567890")
	return fixture
}

func TestPhase5OrderCreationIdempotencySnapshotsOwnershipAndNoReservation(t *testing.T) {
	fixture := newOrderFixture(t)
	ctx := context.Background()
	user := fixture.database.createUser(t)
	other := fixture.database.createUser(t)
	categoryID := fixture.database.createCategory(t)
	productID := fixture.database.createProduct(t, categoryID)
	for range 2 {
		fixture.database.createInventory(t, productID, "available", pgtype.Int8{}, pgtype.Timestamptz{}, pgtype.Int8{})
	}

	first := createPendingOrder(t, fixture, user, productID, fixture.bank.ID, 2, "phase5-snapshot")
	if first.Order.Status != domain.OrderStatusPendingPayment || first.Order.Item.Quantity != 2 || first.Order.Item.Name == "" || first.Order.Total != 20_000 {
		t.Fatalf("created order = %#v", first.Order)
	}
	if first.Instruction.Amount != first.Order.Total || first.Instruction.TransferContent != first.Order.PaymentReference {
		t.Fatalf("instruction = %#v", first.Instruction)
	}
	assertOrderCounts(t, fixture.database, user.ID, "phase5-snapshot", 1, 1)
	assertNoReservation(t, fixture.database, productID, 2)

	second := createPendingOrder(t, fixture, user, productID, fixture.bank.ID, 1, "phase5-second-pending")
	if second.Order.ID == first.Order.ID {
		t.Fatal("second operation reused first order")
	}
	assertNoReservation(t, fixture.database, productID, 2)

	if _, err := fixture.database.pool.Exec(ctx, `UPDATE products SET name = 'Changed product', price_vnd = 99000 WHERE id = $1`, productID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.database.pool.Exec(ctx, `UPDATE bank_accounts SET display_name = 'Changed bank', account_name = 'CHANGED' WHERE id = $1`, fixture.bank.ID); err != nil {
		t.Fatal(err)
	}
	detail, instruction, err := fixture.orders.Get(ctx, user.TelegramUserID, first.Order.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Item.Name == "Changed product" || detail.Item.UnitPrice != 10_000 || detail.BankDisplayName == "Changed bank" || instruction.AccountName == "CHANGED" {
		t.Fatalf("mutable source changed order snapshot: %#v %#v", detail, instruction)
	}

	page, err := fixture.orders.List(ctx, other.TelegramUserID, 0)
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("foreign list = %#v, %v", page, err)
	}
	if _, _, err := fixture.orders.Get(ctx, other.TelegramUserID, first.Order.ID); !errors.Is(err, app.ErrOrderNotFound) {
		t.Fatalf("foreign get error = %v", err)
	}
	foreignUpdate := nextPhase5Update(fixture.database)
	claimUpdate(t, fixture.updates, foreignUpdate)
	_, err = fixture.orders.Cancel(ctx, app.CancelOrderCommand{
		TelegramUserID: other.TelegramUserID, OrderID: first.Order.ID,
		ExpectedVersion: first.Order.Version, Meta: app.RequestMeta{UpdateID: foreignUpdate},
	})
	if !errors.Is(err, app.ErrOrderNotFound) {
		t.Fatalf("foreign cancel error = %v", err)
	}
}

func TestPhase5TenConcurrentDuplicateCreatesOneOrder(t *testing.T) {
	fixture := newOrderFixture(t)
	ctx := context.Background()
	user := fixture.database.createUser(t)
	productID := fixture.database.createProduct(t, fixture.database.createCategory(t))
	fixture.database.createInventory(t, productID, "available", pgtype.Int8{}, pgtype.Timestamptz{}, pgtype.Int8{})

	const operations = 10
	updateIDs := make([]int64, operations)
	for index := range updateIDs {
		updateIDs[index] = nextPhase5Update(fixture.database)
		claimUpdate(t, fixture.updates, updateIDs[index])
	}
	start := make(chan struct{})
	results := make(chan app.CreateOrderResult, operations)
	errorsFound := make(chan error, operations)
	var waitGroup sync.WaitGroup
	for _, updateID := range updateIDs {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			<-start
			result, err := fixture.orders.Create(ctx, app.CreateOrderCommand{
				TelegramUserID: user.TelegramUserID, ProductID: productID,
				BankAccountID: fixture.bank.ID, Quantity: 1,
				IdempotencyKey: "phase5-concurrent-duplicate",
				Meta:           app.RequestMeta{RequestID: "concurrent", UpdateID: updateID},
			})
			results <- result
			errorsFound <- err
		}()
	}
	close(start)
	waitGroup.Wait()
	close(results)
	close(errorsFound)
	for err := range errorsFound {
		if err != nil {
			t.Fatalf("concurrent Create() error = %v", err)
		}
	}
	var orderID int64
	var reference string
	for result := range results {
		if orderID == 0 {
			orderID, reference = result.Order.ID, result.Order.PaymentReference
		}
		if result.Order.ID != orderID || result.Order.PaymentReference != reference {
			t.Fatalf("duplicate result changed order/reference: %#v", result)
		}
	}
	assertOrderCounts(t, fixture.database, user.ID, "phase5-concurrent-duplicate", 1, 1)
	var historyCount int
	if err := fixture.database.pool.QueryRow(ctx, `SELECT count(*) FROM order_status_history WHERE order_id = $1`, orderID).Scan(&historyCount); err != nil || historyCount != 1 {
		t.Fatalf("creation history count = %d, %v", historyCount, err)
	}
}

func TestPhase5CreationValidationRollbackAndCancellation(t *testing.T) {
	fixture := newOrderFixture(t)
	ctx := context.Background()
	user := fixture.database.createUser(t)
	categoryID := fixture.database.createCategory(t)
	productID := fixture.database.createProduct(t, categoryID)
	fixture.database.createInventory(t, productID, "available", pgtype.Int8{}, pgtype.Timestamptz{}, pgtype.Int8{})

	invalid := []struct {
		name     string
		quantity int32
		prepare  func()
		want     error
	}{
		{name: "zero quantity", quantity: 0, want: app.ErrInvalidQuantity},
		{name: "over limit", quantity: 11, want: app.ErrQuantityLimitExceeded},
		{name: "inactive product", quantity: 1, prepare: func() {
			_, _ = fixture.database.pool.Exec(ctx, `UPDATE products SET is_active = false WHERE id = $1`, productID)
		}, want: app.ErrProductInactive},
	}
	for _, test := range invalid {
		t.Run(test.name, func(t *testing.T) {
			if test.prepare != nil {
				test.prepare()
			}
			updateID := nextPhase5Update(fixture.database)
			claimUpdate(t, fixture.updates, updateID)
			_, err := fixture.orders.Create(ctx, app.CreateOrderCommand{
				TelegramUserID: user.TelegramUserID, ProductID: productID, BankAccountID: fixture.bank.ID,
				Quantity: test.quantity, IdempotencyKey: "invalid-" + test.name,
				Meta: app.RequestMeta{UpdateID: updateID},
			})
			if !errors.Is(err, test.want) {
				t.Fatalf("Create() error = %v, want %v", err, test.want)
			}
		})
	}
	if _, err := fixture.database.pool.Exec(ctx, `UPDATE products SET is_active = true WHERE id = $1`, productID); err != nil {
		t.Fatal(err)
	}

	zeroStockProduct := fixture.database.createProduct(t, categoryID)
	assertCreateError(t, fixture, user, zeroStockProduct, fixture.bank.ID, 1, "zero-stock", app.ErrInsufficientInventory)
	inactiveCategory := fixture.database.createCategory(t)
	inactiveCategoryProduct := fixture.database.createProduct(t, inactiveCategory)
	fixture.database.createInventory(t, inactiveCategoryProduct, "available", pgtype.Int8{}, pgtype.Timestamptz{}, pgtype.Int8{})
	if _, err := fixture.database.pool.Exec(ctx, `UPDATE categories SET is_active = false WHERE id = $1`, inactiveCategory); err != nil {
		t.Fatal(err)
	}
	assertCreateError(t, fixture, user, inactiveCategoryProduct, fixture.bank.ID, 1, "inactive-category", app.ErrCategoryInactive)
	if _, err := fixture.database.pool.Exec(ctx, `UPDATE bank_accounts SET is_active = false WHERE id = $1`, fixture.bank.ID); err != nil {
		t.Fatal(err)
	}
	assertCreateError(t, fixture, user, productID, fixture.bank.ID, 1, "inactive-bank", app.ErrBankAccountInactive)
	if _, err := fixture.database.pool.Exec(ctx, `UPDATE bank_accounts SET is_active = true WHERE id = $1`, fixture.bank.ID); err != nil {
		t.Fatal(err)
	}
	overflowProduct := fixture.database.createProduct(t, categoryID)
	fixture.database.createInventory(t, overflowProduct, "available", pgtype.Int8{}, pgtype.Timestamptz{}, pgtype.Int8{})
	fixture.database.createInventory(t, overflowProduct, "available", pgtype.Int8{}, pgtype.Timestamptz{}, pgtype.Int8{})
	if _, err := fixture.database.pool.Exec(ctx, `UPDATE products SET price_vnd = $2 WHERE id = $1`, overflowProduct, int64(^uint64(0)>>1)); err != nil {
		t.Fatal(err)
	}
	assertCreateError(t, fixture, user, overflowProduct, fixture.bank.ID, 2, "money-overflow", app.ErrMoneyOverflow)

	order := createPendingOrder(t, fixture, user, productID, fixture.bank.ID, 1, "phase5-cancel")
	cancelUpdate := nextPhase5Update(fixture.database)
	claimUpdate(t, fixture.updates, cancelUpdate)
	cancelled, err := fixture.orders.Cancel(ctx, app.CancelOrderCommand{
		TelegramUserID: user.TelegramUserID, OrderID: order.Order.ID,
		ExpectedVersion: order.Order.Version, Meta: app.RequestMeta{RequestID: "cancel", UpdateID: cancelUpdate},
	})
	if err != nil || cancelled.Order.Status != domain.OrderStatusCancelled {
		t.Fatalf("Cancel() = %#v, %v", cancelled, err)
	}
	repeatUpdate := nextPhase5Update(fixture.database)
	claimUpdate(t, fixture.updates, repeatUpdate)
	repeated, err := fixture.orders.Cancel(ctx, app.CancelOrderCommand{
		TelegramUserID: user.TelegramUserID, OrderID: order.Order.ID,
		ExpectedVersion: order.Order.Version, Meta: app.RequestMeta{RequestID: "cancel-repeat", UpdateID: repeatUpdate},
	})
	if err != nil || !repeated.AlreadyCancelled {
		t.Fatalf("repeated Cancel() = %#v, %v", repeated, err)
	}
	var histories int
	if err := fixture.database.pool.QueryRow(ctx, `SELECT count(*) FROM order_status_history WHERE order_id = $1 AND to_status = 'cancelled'`, order.Order.ID).Scan(&histories); err != nil || histories != 1 {
		t.Fatalf("cancellation histories = %d, %v", histories, err)
	}

	rollbackOrder := createPendingOrder(t, fixture, user, productID, fixture.bank.ID, 1, "phase5-cancel-rollback")
	if _, err := fixture.database.pool.Exec(ctx, `
		CREATE FUNCTION reject_cancel_history() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN IF NEW.to_status = 'cancelled' THEN RAISE EXCEPTION 'forced history failure'; END IF; RETURN NEW; END $$
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.database.pool.Exec(ctx, `CREATE TRIGGER reject_cancel_history BEFORE INSERT ON order_status_history FOR EACH ROW EXECUTE FUNCTION reject_cancel_history()`); err != nil {
		t.Fatal(err)
	}
	rollbackUpdate := nextPhase5Update(fixture.database)
	claimUpdate(t, fixture.updates, rollbackUpdate)
	_, err = fixture.orders.Cancel(ctx, app.CancelOrderCommand{
		TelegramUserID: user.TelegramUserID, OrderID: rollbackOrder.Order.ID,
		ExpectedVersion: rollbackOrder.Order.Version, Meta: app.RequestMeta{UpdateID: rollbackUpdate},
	})
	if err == nil {
		t.Fatal("Cancel() error = nil, want history failure")
	}
	var status, receipt string
	if err := fixture.database.pool.QueryRow(ctx, `SELECT status FROM orders WHERE id = $1`, rollbackOrder.Order.ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if err := fixture.database.pool.QueryRow(ctx, `SELECT status FROM telegram_update_receipts WHERE update_id = $1`, rollbackUpdate).Scan(&receipt); err != nil {
		t.Fatal(err)
	}
	if status != "pending_payment" || receipt != "processing" {
		t.Fatalf("rollback status = order:%s receipt:%s", status, receipt)
	}
}

func TestPhase5CreationHistoryFailureRollsBackOrderItemAndReceipt(t *testing.T) {
	fixture := newOrderFixture(t)
	ctx := context.Background()
	user := fixture.database.createUser(t)
	productID := fixture.database.createProduct(t, fixture.database.createCategory(t))
	fixture.database.createInventory(t, productID, "available", pgtype.Int8{}, pgtype.Timestamptz{}, pgtype.Int8{})
	if _, err := fixture.database.pool.Exec(ctx, `
		CREATE FUNCTION reject_creation_history() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN IF NEW.reason_code = 'order_created' THEN RAISE EXCEPTION 'forced history failure'; END IF; RETURN NEW; END $$
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.database.pool.Exec(ctx, `CREATE TRIGGER reject_creation_history BEFORE INSERT ON order_status_history FOR EACH ROW EXECUTE FUNCTION reject_creation_history()`); err != nil {
		t.Fatal(err)
	}
	updateID := nextPhase5Update(fixture.database)
	claimUpdate(t, fixture.updates, updateID)
	_, err := fixture.orders.Create(ctx, app.CreateOrderCommand{
		TelegramUserID: user.TelegramUserID, ProductID: productID, BankAccountID: fixture.bank.ID,
		Quantity: 1, IdempotencyKey: "creation-rollback", Meta: app.RequestMeta{UpdateID: updateID},
	})
	if err == nil {
		t.Fatal("Create() error = nil, want forced history failure")
	}
	assertOrderCounts(t, fixture.database, user.ID, "creation-rollback", 0, 0)
	var receipt string
	if err := fixture.database.pool.QueryRow(ctx, `SELECT status FROM telegram_update_receipts WHERE update_id = $1`, updateID).Scan(&receipt); err != nil {
		t.Fatal(err)
	}
	if receipt != "processing" {
		t.Fatalf("receipt after rollback = %s", receipt)
	}
}

func TestPhase5ExpiryBatchAndMultiWorkerSafety(t *testing.T) {
	fixture := newOrderFixture(t)
	ctx := context.Background()
	user := fixture.database.createUser(t)
	productID := fixture.database.createProduct(t, fixture.database.createCategory(t))
	for range 5 {
		fixture.database.createInventory(t, productID, "available", pgtype.Int8{}, pgtype.Timestamptz{}, pgtype.Int8{})
	}
	orders := make([]app.CreateOrderResult, 5)
	for index := range orders {
		orders[index] = createPendingOrder(t, fixture, user, productID, fixture.bank.ID, 1, fmt.Sprintf("phase5-expiry-%d", index))
	}
	for index := 0; index < 3; index++ {
		if _, err := fixture.database.pool.Exec(ctx, `UPDATE orders SET expires_at = clock_timestamp() - ($2::integer * interval '1 minute') WHERE id = $1`, orders[index].Order.ID, 3-index); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := fixture.database.pool.Exec(ctx, `UPDATE orders SET status = 'paid', paid_at = clock_timestamp(), expires_at = clock_timestamp() - interval '1 minute' WHERE id = $1`, orders[2].Order.ID); err != nil {
		t.Fatal(err)
	}
	cancelUpdate := nextPhase5Update(fixture.database)
	claimUpdate(t, fixture.updates, cancelUpdate)
	if _, err := fixture.orders.Cancel(ctx, app.CancelOrderCommand{
		TelegramUserID: user.TelegramUserID, OrderID: orders[4].Order.ID,
		ExpectedVersion: orders[4].Order.Version, Meta: app.RequestMeta{UpdateID: cancelUpdate},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.database.pool.Exec(ctx, `UPDATE orders SET expires_at = clock_timestamp() - interval '1 minute' WHERE id = $1`, orders[4].Order.ID); err != nil {
		t.Fatal(err)
	}

	firstWorker := app.NewOrderExpiryService(postgres.NewAppStore(fixture.database.pool), 1)
	secondWorker := app.NewOrderExpiryService(postgres.NewAppStore(fixture.database.pool), 1)
	start := make(chan struct{})
	counts := make(chan int, 2)
	var waitGroup sync.WaitGroup
	for _, workerService := range []*app.OrderExpiryService{firstWorker, secondWorker} {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			<-start
			count, err := workerService.RunOnce(ctx)
			if err != nil {
				t.Errorf("RunOnce() error = %v", err)
			}
			counts <- count
		}()
	}
	close(start)
	waitGroup.Wait()
	close(counts)
	total := 0
	for count := range counts {
		total += count
	}
	if total != 2 {
		t.Fatalf("two workers expired = %d, want 2", total)
	}
	var expired, histories int
	if err := fixture.database.pool.QueryRow(ctx, `SELECT count(*) FROM orders WHERE id = ANY($1) AND status = 'expired'`, []int64{orders[0].Order.ID, orders[1].Order.ID}).Scan(&expired); err != nil || expired != 2 {
		t.Fatalf("expired orders = %d, %v", expired, err)
	}
	if err := fixture.database.pool.QueryRow(ctx, `SELECT count(*) FROM order_status_history WHERE order_id = ANY($1) AND to_status = 'expired'`, []int64{orders[0].Order.ID, orders[1].Order.ID}).Scan(&histories); err != nil || histories != 2 {
		t.Fatalf("expiry histories = %d, %v", histories, err)
	}
	var paidStatus, futureStatus, cancelledStatus string
	if err := fixture.database.pool.QueryRow(ctx, `SELECT status FROM orders WHERE id = $1`, orders[2].Order.ID).Scan(&paidStatus); err != nil {
		t.Fatal(err)
	}
	if err := fixture.database.pool.QueryRow(ctx, `SELECT status FROM orders WHERE id = $1`, orders[3].Order.ID).Scan(&futureStatus); err != nil {
		t.Fatal(err)
	}
	if err := fixture.database.pool.QueryRow(ctx, `SELECT status FROM orders WHERE id = $1`, orders[4].Order.ID).Scan(&cancelledStatus); err != nil {
		t.Fatal(err)
	}
	if paidStatus != "paid" || futureStatus != "pending_payment" || cancelledStatus != "cancelled" {
		t.Fatalf("excluded expiry statuses = paid:%s future:%s cancelled:%s", paidStatus, futureStatus, cancelledStatus)
	}
}

func TestPhase5BankAdministrationAuditAndReferencedDeleteGuard(t *testing.T) {
	fixture := newOrderFixture(t)
	ctx := context.Background()
	const adminTelegramID int64 = 9_500_001
	if err := fixture.admins.Bootstrap(ctx, []int64{adminTelegramID}); err != nil {
		t.Fatal(err)
	}
	session := startPhase5Session(t, fixture, adminTelegramID, app.SessionBankCreate)
	createUpdate := nextPhase5Update(fixture.database)
	claimUpdate(t, fixture.updates, createUpdate)
	created, err := fixture.banks.Create(ctx, adminTelegramID, session, app.CreateBankAccountInput{
		BankAccountInput: app.BankAccountInput{
			BankBIN: "970415", BankName: "Test Bank", DisplayName: "Secondary",
			AccountName: "TEST OWNER", AccountNumber: "9876543210", SortOrder: 2,
		},
		Meta: app.RequestMeta{RequestID: "bank-create", UpdateID: createUpdate},
	})
	if err != nil {
		t.Fatal(err)
	}
	updateSession := startPhase5Session(t, fixture, adminTelegramID, app.SessionBankEdit)
	updateID := nextPhase5Update(fixture.database)
	claimUpdate(t, fixture.updates, updateID)
	updated, err := fixture.banks.Update(ctx, adminTelegramID, updateSession, app.UpdateBankAccountInput{
		BankAccountID: created.ID, ExpectedRecord: created.Version,
		BankAccountInput: app.BankAccountInput{
			BankBIN: "970415", BankName: "Test Bank", DisplayName: "Secondary Updated",
			AccountName: "TEST OWNER", AccountNumber: "9876543210", SortOrder: 3,
		},
		Meta: app.RequestMeta{RequestID: "bank-update", UpdateID: updateID},
	})
	if err != nil || updated.Version != created.Version+1 {
		t.Fatalf("Update() = %#v, %v", updated, err)
	}
	toggleSession := startPhase5Session(t, fixture, adminTelegramID, app.SessionBankToggle)
	toggleID := nextPhase5Update(fixture.database)
	claimUpdate(t, fixture.updates, toggleID)
	deactivated, err := fixture.banks.SetActive(ctx, adminTelegramID, toggleSession, app.SetBankAccountActiveInput{
		BankAccountID: updated.ID, ExpectedRecord: updated.Version, Active: false,
		Meta: app.RequestMeta{RequestID: "bank-toggle", UpdateID: toggleID},
	})
	if err != nil || deactivated.Active {
		t.Fatalf("SetActive() = %#v, %v", deactivated, err)
	}
	var audits, exposedSecrets int
	if err := fixture.database.pool.QueryRow(ctx, `SELECT count(*) FROM audit_logs WHERE resource_type = 'bank_account' AND resource_id = $1`, created.ID).Scan(&audits); err != nil || audits != 3 {
		t.Fatalf("bank audits = %d, %v", audits, err)
	}
	if err := fixture.database.pool.QueryRow(ctx, `SELECT count(*) FROM audit_logs WHERE resource_id = $1 AND (before_data::text LIKE '%9876543210%' OR after_data::text LIKE '%9876543210%')`, created.ID).Scan(&exposedSecrets); err != nil || exposedSecrets != 0 {
		t.Fatalf("audit account-number exposures = %d, %v", exposedSecrets, err)
	}

	user := fixture.database.createUser(t)
	productID := fixture.database.createProduct(t, fixture.database.createCategory(t))
	fixture.database.createInventory(t, productID, "available", pgtype.Int8{}, pgtype.Timestamptz{}, pgtype.Int8{})
	order := createPendingOrder(t, fixture, user, productID, fixture.bank.ID, 1, "phase5-bank-fk")
	if _, err := fixture.database.pool.Exec(ctx, `DELETE FROM bank_accounts WHERE id = $1`, order.Order.BankAccountID); err == nil {
		t.Fatal("referenced bank account hard delete unexpectedly succeeded")
	}
}

func TestPhase5TelegramFailureHappensAfterOrderCommit(t *testing.T) {
	fixture := newOrderFixture(t)
	ctx := context.Background()
	user := fixture.database.createUser(t)
	productID := fixture.database.createProduct(t, fixture.database.createCategory(t))
	fixture.database.createInventory(t, productID, "available", pgtype.Int8{}, pgtype.Timestamptz{}, pgtype.Int8{})

	users := app.NewUserService(fixture.store)
	catalog := app.NewCatalogService(fixture.store, 8)
	inventoryCipher, err := inventorycrypto.New(bytes.Repeat([]byte{0x61}, 32), 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	inventory := app.NewInventoryAdminService(fixture.store, fixture.admins, inventoryCipher, app.InventoryImportLimits{MaxItems: 10, MaxItemBytes: 100, MaxTotalBytes: 1000}, 8, nil)
	messenger := &capturingFailingMessenger{err: errors.New("transport unavailable")}
	var logs bytes.Buffer
	router := telegramadapter.NewRouterWithOrdering(
		users, catalog, fixture.admins, inventory, fixture.banks, fixture.orders, nil, nil, fixture.updates,
		messenger, "Support", slog.New(slog.NewTextHandler(&logs, nil)), nil,
	)
	updateID := nextPhase5Update(fixture.database)
	update := callbackUpdateForPhase5(updateID, models.User{ID: user.TelegramUserID, FirstName: "Buyer"}, fmt.Sprintf("v1:o:c:%d:%d:1:%d", 777001, productID, fixture.bank.ID))
	if err := router.Process(ctx, update, "phase5-send-failure"); err != nil {
		t.Fatalf("Process() error = %v", err)
	}
	if err := router.Process(ctx, update, "phase5-send-failure-duplicate"); err != nil {
		t.Fatalf("duplicate Process() error = %v", err)
	}
	var orders, items int
	if err := fixture.database.pool.QueryRow(ctx, `SELECT count(*) FROM orders WHERE user_id = $1`, user.ID).Scan(&orders); err != nil {
		t.Fatal(err)
	}
	if err := fixture.database.pool.QueryRow(ctx, `SELECT count(*) FROM order_items WHERE order_id IN (SELECT id FROM orders WHERE user_id = $1)`, user.ID).Scan(&items); err != nil {
		t.Fatal(err)
	}
	if orders != 1 || items != 1 {
		t.Fatalf("post-send-failure persistence = orders:%d items:%d", orders, items)
	}
	if bytes.Contains(logs.Bytes(), []byte("1234567890")) || bytes.Contains(logs.Bytes(), []byte("BOT_TOKEN")) {
		t.Fatalf("logs exposed protected data: %s", logs.String())
	}
}

func TestPhase5TelegramProductToBankOrderHistoryAndCancellationFlow(t *testing.T) {
	fixture := newOrderFixture(t)
	ctx := context.Background()
	user := fixture.database.createUser(t)
	categoryID := fixture.database.createCategory(t)
	productID := fixture.database.createProduct(t, categoryID)
	fixture.database.createInventory(t, productID, "available", pgtype.Int8{}, pgtype.Timestamptz{}, pgtype.Int8{})

	users := app.NewUserService(fixture.store)
	catalog := app.NewCatalogService(fixture.store, 8)
	inventoryCipher, err := inventorycrypto.New(bytes.Repeat([]byte{0x62}, 32), 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	inventory := app.NewInventoryAdminService(fixture.store, fixture.admins, inventoryCipher, app.InventoryImportLimits{MaxItems: 10, MaxItemBytes: 100, MaxTotalBytes: 1000}, 8, nil)
	messenger := &phase5RecordingMessenger{}
	router := telegramadapter.NewRouterWithOrdering(
		users, catalog, fixture.admins, inventory, fixture.banks, fixture.orders, nil, nil, fixture.updates,
		messenger, "Support", slog.New(slog.NewTextHandler(bytes.NewBuffer(nil), nil)), nil,
	)
	telegramUser := models.User{ID: user.TelegramUserID, FirstName: "Buyer"}
	if err := router.Process(ctx, messageUpdate(nextPhase5Update(fixture.database), telegramUser, "/products"), "phase5-products"); err != nil {
		t.Fatal(err)
	}
	callbacks := []string{
		fmt.Sprintf("v1:p:%d:0", categoryID),
		fmt.Sprintf("v1:d:%d:%d:0", productID, categoryID),
		fmt.Sprintf("v1:o:q:%d:1", productID),
		fmt.Sprintf("v1:o:b:%d:1:%d", productID, fixture.bank.ID),
	}
	for _, data := range callbacks {
		if err := router.Process(ctx, callbackUpdateForPhase5(nextPhase5Update(fixture.database), telegramUser, data), "phase5-customer-flow"); err != nil {
			t.Fatalf("Process(%s) error = %v", data, err)
		}
	}
	confirmData := messenger.callbackWithPrefix(t, "v1:o:c:")
	if err := router.Process(ctx, callbackUpdateForPhase5(nextPhase5Update(fixture.database), telegramUser, confirmData), "phase5-confirm"); err != nil {
		t.Fatal(err)
	}
	if err := router.Process(ctx, messageUpdate(nextPhase5Update(fixture.database), telegramUser, "/orders"), "phase5-orders"); err != nil {
		t.Fatal(err)
	}
	var orderID, version int64
	if err := fixture.database.pool.QueryRow(ctx, `SELECT id, version FROM orders WHERE user_id = $1`, user.ID).Scan(&orderID, &version); err != nil {
		t.Fatal(err)
	}
	for _, data := range []string{
		fmt.Sprintf("v1:o:v:%d", orderID),
		fmt.Sprintf("v1:o:x:%d:%d", orderID, version),
		fmt.Sprintf("v1:o:k:%d:%d", orderID, version),
	} {
		if err := router.Process(ctx, callbackUpdateForPhase5(nextPhase5Update(fixture.database), telegramUser, data), "phase5-order-action"); err != nil {
			t.Fatalf("Process(%s) error = %v", data, err)
		}
	}
	var status string
	if err := fixture.database.pool.QueryRow(ctx, `SELECT status FROM orders WHERE id = $1`, orderID).Scan(&status); err != nil || status != "cancelled" {
		t.Fatalf("order status = %s, %v", status, err)
	}
	messenger.mu.Lock()
	answers := messenger.answers
	output := messenger.output
	messenger.mu.Unlock()
	if answers != len(callbacks)+4 {
		t.Fatalf("callback answers = %d, want %d", answers, len(callbacks)+4)
	}
	for _, expected := range []string{"Chọn tài khoản nhận", "Hướng dẫn chuyển khoản", "Đơn hàng của bạn", "Đã hủy đơn"} {
		if !bytes.Contains([]byte(output), []byte(expected)) {
			t.Fatalf("Telegram output missing %q: %s", expected, output)
		}
	}
}

func insertTestBank(t *testing.T, fixture *orderFixture, bankBIN, accountNumber string) app.RedactedBankAccount {
	t.Helper()
	protected, err := fixture.cipher.Protect(context.Background(), bankBIN, accountNumber)
	if err != nil {
		t.Fatal(err)
	}
	row, err := fixture.database.queries.CreateEncryptedBankAccount(context.Background(), generated.CreateEncryptedBankAccountParams{
		BankBin: bankBIN, BankName: "Test Bank", DisplayName: "Primary Test Bank",
		AccountName: "TEST OWNER", EncryptedAccountNumber: protected.Ciphertext,
		AccountNumberFingerprint: protected.Fingerprint, EncryptionKeyID: protected.KeyID,
		EncryptionNonce: protected.Nonce, EncryptionKeyVersion: protected.KeyVersion,
		DisplayLast4: accountNumber[len(accountNumber)-4:], SortOrder: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	return app.RedactedBankAccount{BankAccountOption: app.BankAccountOption{
		ID: row.ID, BankBIN: row.BankBin, BankName: row.BankName, DisplayName: row.DisplayName,
		AccountName: row.AccountName, Last4: row.DisplayLast4, SortOrder: row.SortOrder, Version: row.Version,
	}, Active: row.IsActive, KeyVersion: row.EncryptionKeyVersion, Format: row.EncryptionFormat, CreatedAt: row.CreatedAt.Time}
}

func createPendingOrder(t *testing.T, fixture *orderFixture, user generated.User, productID, bankID int64, quantity int32, key string) app.CreateOrderResult {
	t.Helper()
	updateID := nextPhase5Update(fixture.database)
	claimUpdate(t, fixture.updates, updateID)
	result, err := fixture.orders.Create(context.Background(), app.CreateOrderCommand{
		TelegramUserID: user.TelegramUserID, ProductID: productID, BankAccountID: bankID,
		Quantity: quantity, IdempotencyKey: key,
		Meta: app.RequestMeta{RequestID: key, UpdateID: updateID},
	})
	if err != nil {
		t.Fatalf("Create(%s) error = %v", key, err)
	}
	return result
}

func assertCreateError(t *testing.T, fixture *orderFixture, user generated.User, productID, bankID int64, quantity int32, key string, want error) {
	t.Helper()
	updateID := nextPhase5Update(fixture.database)
	claimUpdate(t, fixture.updates, updateID)
	_, err := fixture.orders.Create(context.Background(), app.CreateOrderCommand{
		TelegramUserID: user.TelegramUserID, ProductID: productID, BankAccountID: bankID,
		Quantity: quantity, IdempotencyKey: key, Meta: app.RequestMeta{UpdateID: updateID},
	})
	if !errors.Is(err, want) {
		t.Fatalf("Create(%s) error = %v, want %v", key, err, want)
	}
}

func startPhase5Session(t *testing.T, fixture *orderFixture, telegramID int64, state string) app.AdminSession {
	t.Helper()
	updateID := nextPhase5Update(fixture.database)
	claimUpdate(t, fixture.updates, updateID)
	session, err := fixture.admins.StartSession(context.Background(), telegramID, state, map[string]any{"step": "test"}, app.RequestMeta{RequestID: "phase5-session", UpdateID: updateID})
	if err != nil {
		t.Fatal(err)
	}
	return session
}

func assertOrderCounts(t *testing.T, database *testDatabase, userID int64, key string, orders, items int) {
	t.Helper()
	var orderCount, itemCount int
	if err := database.pool.QueryRow(context.Background(), `SELECT count(*) FROM orders WHERE user_id = $1 AND idempotency_key = $2`, userID, key).Scan(&orderCount); err != nil {
		t.Fatal(err)
	}
	if err := database.pool.QueryRow(context.Background(), `SELECT count(*) FROM order_items WHERE order_id IN (SELECT id FROM orders WHERE user_id = $1 AND idempotency_key = $2)`, userID, key).Scan(&itemCount); err != nil {
		t.Fatal(err)
	}
	if orderCount != orders || itemCount != items {
		t.Fatalf("counts = orders:%d items:%d, want %d/%d", orderCount, itemCount, orders, items)
	}
}

func assertNoReservation(t *testing.T, database *testDatabase, productID int64, available int) {
	t.Helper()
	var availableCount, mappings int
	if err := database.pool.QueryRow(context.Background(), `SELECT count(*) FROM inventory_items WHERE product_id = $1 AND status = 'available'`, productID).Scan(&availableCount); err != nil {
		t.Fatal(err)
	}
	if err := database.pool.QueryRow(context.Background(), `SELECT count(*) FROM order_inventory_items WHERE status = 'active' AND order_id IN (SELECT id FROM orders WHERE status = 'pending_payment')`).Scan(&mappings); err != nil {
		t.Fatal(err)
	}
	if availableCount != available || mappings != 0 {
		t.Fatalf("reservation boundary = available:%d active mappings:%d", availableCount, mappings)
	}
}

func callbackUpdateForPhase5(updateID int64, user models.User, data string) *models.Update {
	return &models.Update{
		ID: updateID,
		CallbackQuery: &models.CallbackQuery{
			ID: fmt.Sprintf("callback-%d", updateID), From: user, Data: data,
			Message: models.MaybeInaccessibleMessage{
				Type:    models.MaybeInaccessibleMessageTypeMessage,
				Message: &models.Message{ID: int(updateID), Date: 1, Chat: models.Chat{ID: user.ID, Type: models.ChatTypePrivate}},
			},
		},
	}
}

type phase5RecordingMessenger struct {
	mu        sync.Mutex
	output    string
	keyboards []telegramadapter.Keyboard
	answers   int
}

func (m *phase5RecordingMessenger) SendMessage(_ context.Context, request telegramadapter.SendMessageRequest) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.output += request.Text
	m.keyboards = append(m.keyboards, request.Keyboard)
	return nil
}

func (m *phase5RecordingMessenger) EditMessage(_ context.Context, request telegramadapter.EditMessageRequest) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.output += request.Text
	m.keyboards = append(m.keyboards, request.Keyboard)
	return nil
}

func (m *phase5RecordingMessenger) AnswerCallback(context.Context, telegramadapter.AnswerCallbackRequest) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.answers++
	return nil
}

func (m *phase5RecordingMessenger) callbackWithPrefix(t *testing.T, prefix string) string {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	for index := len(m.keyboards) - 1; index >= 0; index-- {
		for _, row := range m.keyboards[index] {
			for _, button := range row {
				if len(button.Data) >= len(prefix) && button.Data[:len(prefix)] == prefix {
					return button.Data
				}
			}
		}
	}
	t.Fatalf("callback prefix %q not found", prefix)
	return ""
}

func nextPhase5Update(database *testDatabase) int64 {
	return 9_500_000 + database.keySequence.Add(1)
}
