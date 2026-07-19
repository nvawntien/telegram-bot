//go:build integration

package integration_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/nvawntien/telegram-bot/internal/app"
	"github.com/nvawntien/telegram-bot/internal/domain"
	"github.com/nvawntien/telegram-bot/internal/inventorycrypto"
	"github.com/nvawntien/telegram-bot/internal/postgres"
	"github.com/nvawntien/telegram-bot/internal/postgres/generated"
	telegramadapter "github.com/nvawntien/telegram-bot/internal/telegram"
)

type inventoryFixture struct {
	database  *testDatabase
	store     *postgres.AppStore
	admins    *app.AdminService
	updates   *app.UpdateService
	cipher    *inventorycrypto.Cipher
	inventory *app.InventoryAdminService
}

func newInventoryFixture(t *testing.T) *inventoryFixture {
	t.Helper()
	database := newTestDatabase(t, true)
	store := postgres.NewAppStore(database.pool)
	admins := app.NewAdminService(store, time.Hour)
	updates := app.NewUpdateService(store, time.Minute)
	master := make([]byte, 32)
	if _, err := rand.Read(master); err != nil {
		t.Fatalf("generate inventory master key: %v", err)
	}
	cipher, err := inventorycrypto.New(master, 1, nil)
	if err != nil {
		t.Fatalf("create inventory cipher: %v", err)
	}
	inventory := app.NewInventoryAdminService(
		store, admins, cipher,
		app.InventoryImportLimits{MaxItems: 100, MaxItemBytes: 4096, MaxTotalBytes: 256 * 1024},
		8, nil,
	)
	return &inventoryFixture{
		database: database, store: store, admins: admins, updates: updates,
		cipher: cipher, inventory: inventory,
	}
}

func TestPhase4EncryptedImportPersistenceAndRedaction(t *testing.T) {
	fixture := newInventoryFixture(t)
	ctx := context.Background()
	const adminTelegramID int64 = 9_100_001
	if err := fixture.admins.Bootstrap(ctx, []int64{adminTelegramID}); err != nil {
		t.Fatalf("bootstrap admin: %v", err)
	}
	productID := fixture.database.createProduct(t, fixture.database.createCategory(t))
	secret := runtimeOpaqueSecret(t)
	session := startInventorySession(t, fixture, adminTelegramID, app.SessionInventoryImport, map[string]any{"product_id": productID})
	updateID := nextPhase4Update(fixture.database)
	claimUpdate(t, fixture.updates, updateID)
	raw := append([]byte(nil), secret...)
	result, err := fixture.inventory.Import(ctx, adminTelegramID, session, productID, raw, app.RequestMeta{
		RequestID: "phase4-import", UpdateID: updateID,
	})
	if err != nil {
		t.Fatalf("Import() error = %v", err)
	}
	if result.Inserted != 1 || result.Duplicates != 0 || !allZero(raw) {
		t.Fatalf("Import() result = %#v, temporary input cleared = %t", result, allZero(raw))
	}

	var inventoryID int64
	var ciphertext, nonce, fingerprint []byte
	var keyVersion int32
	var format string
	if err := fixture.database.pool.QueryRow(ctx, `
		SELECT id, encrypted_payload, encryption_nonce, payload_fingerprint,
		       encryption_key_version, encryption_format
		FROM inventory_items WHERE product_id = $1
	`, productID).Scan(&inventoryID, &ciphertext, &nonce, &fingerprint, &keyVersion, &format); err != nil {
		t.Fatalf("query encrypted inventory: %v", err)
	}
	if bytes.Contains(ciphertext, secret) || len(nonce) != 12 || len(fingerprint) != 32 || keyVersion != 1 || format != inventorycrypto.FormatAES256GCMV1 {
		t.Fatalf("invalid persisted encryption envelope")
	}
	decrypted, err := fixture.cipher.Decrypt(ctx, productID, app.EncryptedInventoryPayload{
		Ciphertext: ciphertext, Nonce: nonce, Fingerprint: fingerprint,
		KeyVersion: keyVersion, Format: format,
	})
	if err != nil || !bytes.Equal(decrypted, secret) {
		t.Fatalf("decrypt persisted inventory = %x, %v", decrypted, err)
	}

	overview, err := fixture.inventory.ListOverview(ctx, adminTelegramID, 0)
	if err != nil || len(overview.Items) != 1 || overview.Items[0].AvailableCount != 1 {
		t.Fatalf("ListOverview() = %#v, %v", overview, err)
	}
	redacted, err := fixture.inventory.ListItems(ctx, adminTelegramID, productID, 0)
	if err != nil || len(redacted.Items) != 1 || redacted.Items[0].ID != inventoryID {
		t.Fatalf("ListItems() = %#v, %v", redacted, err)
	}
	redactedJSON, err := json.Marshal(redacted)
	if err != nil || bytes.Contains(redactedJSON, secret) || bytes.Contains(redactedJSON, ciphertext) || bytes.Contains(redactedJSON, fingerprint) {
		t.Fatal("redacted inventory output exposed protected data")
	}

	assertSecretAbsentFromPersistence(t, fixture, secret)
	var plaintextColumns int
	if err := fixture.database.pool.QueryRow(ctx, `
		SELECT count(*) FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = 'inventory_items'
		  AND column_name ILIKE '%plain%'
	`).Scan(&plaintextColumns); err != nil || plaintextColumns != 0 {
		t.Fatalf("plaintext inventory columns = %d, %v", plaintextColumns, err)
	}
}

func TestPhase4DuplicateImportAuthorizationAndRollback(t *testing.T) {
	fixture := newInventoryFixture(t)
	ctx := context.Background()
	const firstAdmin int64 = 9_200_001
	const secondAdmin int64 = 9_200_002
	if err := fixture.admins.Bootstrap(ctx, []int64{firstAdmin, secondAdmin}); err != nil {
		t.Fatalf("bootstrap admins: %v", err)
	}
	productID := fixture.database.createProduct(t, fixture.database.createCategory(t))
	secret := runtimeOpaqueSecret(t)
	firstSession := startInventorySession(t, fixture, firstAdmin, app.SessionInventoryImport, map[string]any{"product_id": productID})
	secondSession := startInventorySession(t, fixture, secondAdmin, app.SessionInventoryImport, map[string]any{"product_id": productID})
	restartedAdmins := app.NewAdminService(postgres.NewAppStore(fixture.database.pool), time.Hour)
	loaded, err := restartedAdmins.LoadSession(ctx, firstAdmin)
	if err != nil || loaded.ID != firstSession.ID || loaded.State != app.SessionInventoryImport || bytes.Contains(loaded.Payload, secret) {
		t.Fatalf("durable inventory session = %#v, %v", loaded, err)
	}
	firstUpdate, secondUpdate := nextPhase4Update(fixture.database), nextPhase4Update(fixture.database)
	claimUpdate(t, fixture.updates, firstUpdate)
	claimUpdate(t, fixture.updates, secondUpdate)

	type importResult struct {
		result app.InventoryImportResult
		err    error
	}
	results := make(chan importResult, 2)
	var waitGroup sync.WaitGroup
	for _, operation := range []struct {
		telegramID int64
		session    app.AdminSession
		updateID   int64
	}{{firstAdmin, firstSession, firstUpdate}, {secondAdmin, secondSession, secondUpdate}} {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			result, err := fixture.inventory.Import(ctx, operation.telegramID, operation.session, productID,
				append([]byte(nil), secret...), app.RequestMeta{RequestID: "concurrent-import", UpdateID: operation.updateID})
			results <- importResult{result: result, err: err}
		}()
	}
	waitGroup.Wait()
	close(results)
	inserted, duplicates := 0, 0
	for operation := range results {
		if operation.err != nil {
			t.Fatalf("concurrent Import() error = %v", operation.err)
		}
		inserted += operation.result.Inserted
		duplicates += operation.result.Duplicates
	}
	if inserted != 1 || duplicates != 1 {
		t.Fatalf("concurrent imports = inserted:%d duplicate:%d", inserted, duplicates)
	}
	assertInventoryCount(t, fixture.database, productID, 1)

	claim, err := fixture.updates.Claim(ctx, firstUpdate, "message")
	if err != nil || claim != app.UpdateDuplicateCompleted {
		t.Fatalf("duplicate Telegram update claim = %v, %v", claim, err)
	}
	var firstAuditCount int
	if err := fixture.database.pool.QueryRow(ctx, `
		SELECT count(*) FROM audit_logs
		WHERE telegram_update_id = $1 AND action = 'inventory.imported'
	`, firstUpdate).Scan(&firstAuditCount); err != nil || firstAuditCount != 1 {
		t.Fatalf("duplicate update audit count = %d, %v", firstAuditCount, err)
	}

	nonAdminInput := append([]byte(nil), secret...)
	_, err = fixture.inventory.Import(ctx, 9_200_999, app.AdminSession{ID: 1, Version: 1}, productID,
		nonAdminInput, app.RequestMeta{UpdateID: nextPhase4Update(fixture.database)})
	if (!errors.Is(err, app.ErrUnauthorized) && !errors.Is(err, app.ErrForbidden)) || !allZero(nonAdminInput) {
		t.Fatalf("non-admin Import() error = %v, input cleared = %t", err, allZero(nonAdminInput))
	}

	const revokedAdmin int64 = 9_200_003
	if err := fixture.admins.Bootstrap(ctx, []int64{revokedAdmin}); err != nil {
		t.Fatal(err)
	}
	revokedProduct := fixture.database.createProduct(t, fixture.database.createCategory(t))
	revokedSession := startInventorySession(t, fixture, revokedAdmin, app.SessionInventoryImport, map[string]any{"product_id": revokedProduct})
	if _, err := fixture.database.pool.Exec(ctx, `
		UPDATE admins SET is_active = false
		WHERE user_id = (SELECT id FROM users WHERE telegram_user_id = $1)
	`, revokedAdmin); err != nil {
		t.Fatal(err)
	}
	revokedUpdate := nextPhase4Update(fixture.database)
	claimUpdate(t, fixture.updates, revokedUpdate)
	if _, err := fixture.inventory.Import(ctx, revokedAdmin, revokedSession, revokedProduct,
		append([]byte(nil), secret...), app.RequestMeta{UpdateID: revokedUpdate}); !errors.Is(err, app.ErrForbidden) {
		t.Fatalf("revoked admin Import() error = %v", err)
	}
	assertInventoryCount(t, fixture.database, revokedProduct, 0)

	rollbackProduct := fixture.database.createProduct(t, fixture.database.createCategory(t))
	rollbackSession := startInventorySession(t, fixture, firstAdmin, app.SessionInventoryImport, map[string]any{"product_id": rollbackProduct})
	rollbackUpdate := nextPhase4Update(fixture.database)
	claimUpdate(t, fixture.updates, rollbackUpdate)
	if _, err := fixture.database.pool.Exec(ctx, `
		CREATE FUNCTION reject_inventory_audit() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			IF NEW.action = 'inventory.imported' THEN RAISE EXCEPTION 'forced audit failure'; END IF;
			RETURN NEW;
		END $$
	`); err != nil {
		t.Fatalf("install failing audit function: %v", err)
	}
	if _, err := fixture.database.pool.Exec(ctx, `
		CREATE TRIGGER reject_inventory_audit BEFORE INSERT ON audit_logs
		FOR EACH ROW EXECUTE FUNCTION reject_inventory_audit()
	`); err != nil {
		t.Fatalf("install failing audit trigger: %v", err)
	}
	_, err = fixture.inventory.Import(ctx, firstAdmin, rollbackSession, rollbackProduct,
		append([]byte(nil), runtimeOpaqueSecret(t)...), app.RequestMeta{UpdateID: rollbackUpdate})
	if err == nil {
		t.Fatal("Import() error = nil, want forced audit failure")
	}
	assertInventoryCount(t, fixture.database, rollbackProduct, 0)
	var sessionState, receiptStatus string
	if err := fixture.database.pool.QueryRow(ctx, `SELECT state FROM admin_sessions WHERE id = $1`, rollbackSession.ID).Scan(&sessionState); err != nil {
		t.Fatal(err)
	}
	if err := fixture.database.pool.QueryRow(ctx, `SELECT status FROM telegram_update_receipts WHERE update_id = $1`, rollbackUpdate).Scan(&receiptStatus); err != nil {
		t.Fatal(err)
	}
	if sessionState != app.SessionInventoryImport || receiptStatus != "processing" {
		t.Fatalf("rollback boundary = session:%s receipt:%s", sessionState, receiptStatus)
	}
}

func TestPhase4TelegramSendFailureDoesNotRollbackImport(t *testing.T) {
	fixture := newInventoryFixture(t)
	ctx := context.Background()
	const adminTelegramID int64 = 9_350_001
	if err := fixture.admins.Bootstrap(ctx, []int64{adminTelegramID}); err != nil {
		t.Fatal(err)
	}
	productID := fixture.database.createProduct(t, fixture.database.createCategory(t))
	users := app.NewUserService(fixture.store)
	catalog := app.NewCatalogService(fixture.store, 8)
	messenger := &capturingFailingMessenger{err: errors.New("Telegram unavailable")}
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	router := telegramadapter.NewRouter(
		users, catalog, fixture.admins, fixture.inventory, fixture.updates,
		messenger, "Support", logger, nil,
	)
	telegramUser := models.User{ID: adminTelegramID, FirstName: "Admin"}
	startUpdate := &models.Update{
		ID: nextPhase4Update(fixture.database),
		CallbackQuery: &models.CallbackQuery{
			ID: "inventory-import", From: telegramUser, Data: fmt.Sprintf("v1:a:ii:%d", productID),
			Message: models.MaybeInaccessibleMessage{
				Type: models.MaybeInaccessibleMessageTypeMessage,
				Message: &models.Message{
					ID: 1, Date: 1, Chat: models.Chat{ID: adminTelegramID, Type: models.ChatTypePrivate},
				},
			},
		},
	}
	if err := router.Process(ctx, startUpdate, "inventory-start"); err != nil {
		t.Fatalf("start inventory import: %v", err)
	}
	secret := runtimeOpaqueSecret(t)
	message := messageUpdate(nextPhase4Update(fixture.database), telegramUser, string(secret))
	if err := router.Process(ctx, message, "inventory-message"); err != nil {
		t.Fatalf("process inventory import: %v", err)
	}
	if err := router.Process(ctx, message, "inventory-message-duplicate"); err != nil {
		t.Fatalf("process duplicate inventory import: %v", err)
	}
	assertInventoryCount(t, fixture.database, productID, 1)
	var auditCount int
	if err := fixture.database.pool.QueryRow(ctx, `
		SELECT count(*) FROM audit_logs
		WHERE telegram_update_id = $1 AND action = 'inventory.imported'
	`, message.ID).Scan(&auditCount); err != nil || auditCount != 1 {
		t.Fatalf("inventory import audit count = %d, %v", auditCount, err)
	}
	if messenger.failures == 0 {
		t.Fatal("Telegram send failure was not exercised")
	}
	if bytes.Contains(logs.Bytes(), secret) || bytes.Contains([]byte(messenger.output), secret) {
		t.Fatal("Telegram failure boundary exposed plaintext inventory")
	}
	assertSecretAbsentFromPersistence(t, fixture, secret)
}

func TestPhase4InventoryDisableAndEnableGuards(t *testing.T) {
	fixture := newInventoryFixture(t)
	ctx := context.Background()
	const adminTelegramID int64 = 9_300_001
	if err := fixture.admins.Bootstrap(ctx, []int64{adminTelegramID}); err != nil {
		t.Fatal(err)
	}
	productID := fixture.database.createProduct(t, fixture.database.createCategory(t))
	inventoryID := insertProtectedInventory(t, fixture, productID)

	disabled := setInventoryEnabled(t, fixture, adminTelegramID, inventoryID, 1, false)
	if disabled.Status != domain.InventoryStatusDisabled || disabled.Version != 2 {
		t.Fatalf("disabled item = %#v", disabled)
	}
	staleSession := startInventorySession(t, fixture, adminTelegramID, app.SessionInventoryToggle, map[string]any{"inventory_item_id": inventoryID})
	staleUpdate := nextPhase4Update(fixture.database)
	claimUpdate(t, fixture.updates, staleUpdate)
	if _, err := fixture.inventory.SetItemEnabled(ctx, adminTelegramID, staleSession, inventoryID, 1, false,
		app.RequestMeta{UpdateID: staleUpdate}); !errors.Is(err, app.ErrStaleVersion) {
		t.Fatalf("stale disable error = %v", err)
	}
	enabled := setInventoryEnabled(t, fixture, adminTelegramID, inventoryID, 2, true)
	if enabled.Status != domain.InventoryStatusAvailable || enabled.Version != 3 {
		t.Fatalf("enabled item = %#v", enabled)
	}

	orderID := createReservingOrderWithItem(t, fixture.database, productID, 1).order.ID
	reservedID := insertProtectedInventory(t, fixture, productID)
	if _, err := fixture.database.pool.Exec(ctx, `
		UPDATE inventory_items SET status = 'reserved', reserved_order_id = $1,
		reserved_until = clock_timestamp() + interval '10 minutes' WHERE id = $2
	`, orderID, reservedID); err != nil {
		t.Fatal(err)
	}
	assertToggleRejected(t, fixture, adminTelegramID, reservedID, 1, false)

	soldID := insertProtectedInventory(t, fixture, productID)
	if _, err := fixture.database.pool.Exec(ctx, `
		UPDATE inventory_items SET status = 'sold', sold_order_id = $1 WHERE id = $2
	`, orderID, soldID); err != nil {
		t.Fatal(err)
	}
	assertToggleRejected(t, fixture, adminTelegramID, soldID, 1, false)
}

func TestPhase4AtomicClaimAndRollback(t *testing.T) {
	fixture := newInventoryFixture(t)
	ctx := context.Background()
	productID := fixture.database.createProduct(t, fixture.database.createCategory(t))
	service := app.NewInventoryReservationService(fixture.store, time.Hour, nil)
	for range 3 {
		insertProtectedInventory(t, fixture, productID)
	}
	order := createReservingOrderWithItem(t, fixture.database, productID, 2)
	claimed, err := service.Claim(ctx, app.InventoryClaimRequest{
		OrderID: order.order.ID, OrderItemID: order.item.ID, ProductID: productID,
		Quantity: 2, ReservedUntil: time.Now().Add(15 * time.Minute), RequestID: "claim-exact",
	})
	if err != nil || claimed.Count != 2 || len(claimed.InventoryItemIDs) != 2 {
		t.Fatalf("Claim() = %#v, %v", claimed, err)
	}
	var mappingCount int
	if err := fixture.database.pool.QueryRow(ctx, `
		SELECT count(*) FROM order_inventory_items
		WHERE order_id = $1 AND order_item_id = $2 AND status = 'active'
	`, order.order.ID, order.item.ID).Scan(&mappingCount); err != nil || mappingCount != 2 {
		t.Fatalf("claim mappings = %d, %v", mappingCount, err)
	}

	shortageProduct := fixture.database.createProduct(t, fixture.database.createCategory(t))
	insertProtectedInventory(t, fixture, shortageProduct)
	shortageOrder := createReservingOrderWithItem(t, fixture.database, shortageProduct, 2)
	_, err = service.Claim(ctx, app.InventoryClaimRequest{
		OrderID: shortageOrder.order.ID, OrderItemID: shortageOrder.item.ID, ProductID: shortageProduct,
		Quantity: 2, ReservedUntil: time.Now().Add(15 * time.Minute), RequestID: "claim-shortage",
	})
	if !errors.Is(err, app.ErrInsufficientInventory) {
		t.Fatalf("shortage Claim() error = %v", err)
	}
	assertStatusCount(t, fixture.database, shortageProduct, "available", 1)
	assertStatusCount(t, fixture.database, shortageProduct, "reserved", 0)

	excludedProduct := fixture.database.createProduct(t, fixture.database.createCategory(t))
	reservedOrder := createReservingOrderWithItem(t, fixture.database, excludedProduct, 1)
	disabled := insertProtectedInventory(t, fixture, excludedProduct)
	reserved := insertProtectedInventory(t, fixture, excludedProduct)
	sold := insertProtectedInventory(t, fixture, excludedProduct)
	if _, err := fixture.database.pool.Exec(ctx, `UPDATE inventory_items SET status = 'disabled', disabled_reason = 'test' WHERE id = $1`, disabled); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.database.pool.Exec(ctx, `
		UPDATE inventory_items SET status = 'reserved', reserved_order_id = $1,
		reserved_until = clock_timestamp() + interval '10 minutes' WHERE id = $2
	`, reservedOrder.order.ID, reserved); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.database.pool.Exec(ctx, `UPDATE inventory_items SET status = 'sold', sold_order_id = $1 WHERE id = $2`, reservedOrder.order.ID, sold); err != nil {
		t.Fatal(err)
	}
	_, err = service.Claim(ctx, app.InventoryClaimRequest{
		OrderID: reservedOrder.order.ID, OrderItemID: reservedOrder.item.ID, ProductID: excludedProduct,
		Quantity: 1, ReservedUntil: time.Now().Add(15 * time.Minute), RequestID: "claim-excluded",
	})
	if !errors.Is(err, app.ErrInsufficientInventory) {
		t.Fatalf("excluded Claim() error = %v", err)
	}

	rollbackProduct := fixture.database.createProduct(t, fixture.database.createCategory(t))
	insertProtectedInventory(t, fixture, rollbackProduct)
	rollbackOrder := createReservingOrderWithItem(t, fixture.database, rollbackProduct, 1)
	if _, err := fixture.database.pool.Exec(ctx, `
		CREATE FUNCTION reject_reservation_audit() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			IF NEW.action = 'inventory.reserved' THEN RAISE EXCEPTION 'forced reservation audit failure'; END IF;
			RETURN NEW;
		END $$
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.database.pool.Exec(ctx, `
		CREATE TRIGGER reject_reservation_audit BEFORE INSERT ON audit_logs
		FOR EACH ROW EXECUTE FUNCTION reject_reservation_audit()
	`); err != nil {
		t.Fatal(err)
	}
	_, err = service.Claim(ctx, app.InventoryClaimRequest{
		OrderID: rollbackOrder.order.ID, OrderItemID: rollbackOrder.item.ID, ProductID: rollbackProduct,
		Quantity: 1, ReservedUntil: time.Now().Add(15 * time.Minute), RequestID: "claim-rollback",
	})
	if err == nil {
		t.Fatal("Claim() error = nil, want forced audit failure")
	}
	assertStatusCount(t, fixture.database, rollbackProduct, "available", 1)
	if err := fixture.database.pool.QueryRow(ctx, `SELECT count(*) FROM order_inventory_items WHERE order_id = $1`, rollbackOrder.order.ID).Scan(&mappingCount); err != nil || mappingCount != 0 {
		t.Fatalf("rollback mappings = %d, %v", mappingCount, err)
	}
}

func TestPhase4OneHundredConcurrentClaimsAndMultiItemSafety(t *testing.T) {
	fixture := newInventoryFixture(t)
	ctx := context.Background()
	service := app.NewInventoryReservationService(fixture.store, time.Hour, nil)
	productID := fixture.database.createProduct(t, fixture.database.createCategory(t))
	inventoryID := insertProtectedInventory(t, fixture, productID)
	orders := make([]reservationOrder, 100)
	for index := range orders {
		orders[index] = createReservingOrderWithItem(t, fixture.database, productID, 1)
	}

	start := make(chan struct{})
	results := make(chan error, len(orders))
	var waitGroup sync.WaitGroup
	for _, order := range orders {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			<-start
			_, err := service.Claim(ctx, app.InventoryClaimRequest{
				OrderID: order.order.ID, OrderItemID: order.item.ID, ProductID: productID,
				Quantity: 1, ReservedUntil: time.Now().Add(15 * time.Minute), RequestID: "claim-100",
			})
			results <- err
		}()
	}
	close(start)
	waitGroup.Wait()
	close(results)
	successes, shortages := 0, 0
	for err := range results {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, app.ErrInsufficientInventory):
			shortages++
		default:
			t.Fatalf("concurrent Claim() error = %v", err)
		}
	}
	if successes != 1 || shortages != 99 {
		t.Fatalf("100 concurrent claims = success:%d shortage:%d", successes, shortages)
	}
	var activeMappings, distinctItems int
	if err := fixture.database.pool.QueryRow(ctx, `
		SELECT count(*), count(DISTINCT inventory_item_id)
		FROM order_inventory_items WHERE inventory_item_id = $1 AND status = 'active'
	`, inventoryID).Scan(&activeMappings, &distinctItems); err != nil || activeMappings != 1 || distinctItems != 1 {
		t.Fatalf("concurrent mapping uniqueness = mappings:%d distinct:%d error:%v", activeMappings, distinctItems, err)
	}

	multiProduct := fixture.database.createProduct(t, fixture.database.createCategory(t))
	const itemCount = 16
	for range itemCount {
		insertProtectedInventory(t, fixture, multiProduct)
	}
	multiOrders := make([]reservationOrder, itemCount)
	for index := range multiOrders {
		multiOrders[index] = createReservingOrderWithItem(t, fixture.database, multiProduct, 1)
	}
	multiResults := make(chan app.InventoryClaimResult, itemCount)
	for _, order := range multiOrders {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			result, err := service.Claim(ctx, app.InventoryClaimRequest{
				OrderID: order.order.ID, OrderItemID: order.item.ID, ProductID: multiProduct,
				Quantity: 1, ReservedUntil: time.Now().Add(15 * time.Minute), RequestID: "claim-multi",
			})
			if err != nil {
				t.Errorf("multi-item Claim() error = %v", err)
				return
			}
			multiResults <- result
		}()
	}
	waitGroup.Wait()
	close(multiResults)
	unique := make(map[int64]struct{}, itemCount)
	for result := range multiResults {
		unique[result.InventoryItemIDs[0]] = struct{}{}
	}
	if len(unique) != itemCount {
		t.Fatalf("multi-item concurrent unique claims = %d, want %d", len(unique), itemCount)
	}
}

func TestPhase4ReleaseAndReservationRecoveryPolicy(t *testing.T) {
	fixture := newInventoryFixture(t)
	ctx := context.Background()
	service := app.NewInventoryReservationService(fixture.store, time.Hour, nil)
	productID := fixture.database.createProduct(t, fixture.database.createCategory(t))

	claimOne := func(requestID string) (reservationOrder, int64) {
		order := createReservingOrderWithItem(t, fixture.database, productID, 1)
		insertProtectedInventory(t, fixture, productID)
		result, err := service.Claim(ctx, app.InventoryClaimRequest{
			OrderID: order.order.ID, OrderItemID: order.item.ID, ProductID: productID,
			Quantity: 1, ReservedUntil: time.Now().Add(15 * time.Minute), RequestID: requestID,
		})
		if err != nil || result.Count != 1 {
			t.Fatalf("claimOne() = %#v, %v", result, err)
		}
		return order, result.InventoryItemIDs[0]
	}

	cancelledOrder, cancelledItem := claimOne("release-cancelled")
	setOrderStatus(t, fixture.database, cancelledOrder.order.ID, domain.OrderStatusCancelled)
	released, err := service.Release(ctx, app.InventoryReleaseRequest{
		OrderID: cancelledOrder.order.ID, Reason: domain.InventoryReleaseOrderCancelled, RequestID: "release-cancelled",
	})
	if err != nil || released.Released != 1 {
		t.Fatalf("Release() = %#v, %v", released, err)
	}
	repeated, err := service.Release(ctx, app.InventoryReleaseRequest{
		OrderID: cancelledOrder.order.ID, Reason: domain.InventoryReleaseOrderCancelled, RequestID: "release-repeat",
	})
	if err != nil || repeated.Released != 0 {
		t.Fatalf("repeated Release() = %#v, %v", repeated, err)
	}
	var itemStatus, mappingStatus, releaseReason string
	if err := fixture.database.pool.QueryRow(ctx, `SELECT status FROM inventory_items WHERE id = $1`, cancelledItem).Scan(&itemStatus); err != nil {
		t.Fatal(err)
	}
	if err := fixture.database.pool.QueryRow(ctx, `
		SELECT status, release_reason FROM order_inventory_items WHERE inventory_item_id = $1
	`, cancelledItem).Scan(&mappingStatus, &releaseReason); err != nil {
		t.Fatal(err)
	}
	if itemStatus != "available" || mappingStatus != "released" || releaseReason != string(domain.InventoryReleaseOrderCancelled) {
		t.Fatalf("release history = item:%s mapping:%s reason:%s", itemStatus, mappingStatus, releaseReason)
	}

	concurrentOrder, concurrentItem := claimOne("release-concurrent")
	setOrderStatus(t, fixture.database, concurrentOrder.order.ID, domain.OrderStatusCancelled)
	releaseResults := make(chan app.InventoryReleaseResult, 2)
	releaseErrors := make(chan error, 2)
	var releaseWaitGroup sync.WaitGroup
	for range 2 {
		releaseWaitGroup.Add(1)
		go func() {
			defer releaseWaitGroup.Done()
			result, err := service.Release(ctx, app.InventoryReleaseRequest{
				OrderID: concurrentOrder.order.ID, Reason: domain.InventoryReleaseOrderCancelled,
				RequestID: "release-concurrent",
			})
			releaseResults <- result
			releaseErrors <- err
		}()
	}
	releaseWaitGroup.Wait()
	close(releaseResults)
	close(releaseErrors)
	concurrentReleased := 0
	for result := range releaseResults {
		concurrentReleased += result.Released
	}
	for err := range releaseErrors {
		if err != nil {
			t.Fatalf("concurrent Release() error = %v", err)
		}
	}
	if concurrentReleased != 1 {
		t.Fatalf("concurrent released count = %d, want 1", concurrentReleased)
	}
	assertInventoryStatus(t, fixture.database, concurrentItem, "available")

	paidOrder, paidItem := claimOne("release-paid")
	setOrderStatus(t, fixture.database, paidOrder.order.ID, domain.OrderStatusPaid)
	if _, err := service.Release(ctx, app.InventoryReleaseRequest{
		OrderID: paidOrder.order.ID, Reason: domain.InventoryReleaseOrderCancelled,
	}); !errors.Is(err, app.ErrUnsafeReservationRelease) {
		t.Fatalf("paid Release() error = %v", err)
	}
	assertInventoryStatus(t, fixture.database, paidItem, "reserved")

	deliveredOrder, deliveredItem := claimOne("release-delivered")
	setOrderStatus(t, fixture.database, deliveredOrder.order.ID, domain.OrderStatusDelivered)
	if _, err := service.Release(ctx, app.InventoryReleaseRequest{
		OrderID: deliveredOrder.order.ID, Reason: domain.InventoryReleaseOrderCancelled,
	}); !errors.Is(err, app.ErrUnsafeReservationRelease) {
		t.Fatalf("delivered Release() error = %v", err)
	}
	assertInventoryStatus(t, fixture.database, deliveredItem, "reserved")

	otherOrder := createReservingOrderWithItem(t, fixture.database, productID, 1)
	setOrderStatus(t, fixture.database, otherOrder.order.ID, domain.OrderStatusCancelled)
	otherRelease, err := service.Release(ctx, app.InventoryReleaseRequest{
		OrderID: otherOrder.order.ID, Reason: domain.InventoryReleaseOrderCancelled,
	})
	if err != nil || otherRelease.Released != 0 {
		t.Fatalf("cross-order Release() = %#v, %v", otherRelease, err)
	}
	assertInventoryStatus(t, fixture.database, paidItem, "reserved")

	setReservationExpired(t, fixture.database, paidItem)
	recovery, err := service.RecoverExpired(ctx, paidOrder.order.ID, time.Now(), "recover-paid")
	if err != nil || !recovery.RecoveryRequired || recovery.Released != 0 {
		t.Fatalf("paid RecoverExpired() = %#v, %v", recovery, err)
	}
	assertInventoryStatus(t, fixture.database, paidItem, "reserved")
	recovery, err = service.RecoverExpired(ctx, paidOrder.order.ID, time.Now(), "recover-paid-repeat")
	if err != nil || !recovery.RecoveryRequired {
		t.Fatalf("repeated paid recovery = %#v, %v", recovery, err)
	}
	var recoveryAudits int
	if err := fixture.database.pool.QueryRow(ctx, `
		SELECT count(*) FROM audit_logs
		WHERE action = 'inventory.reservation_recovery_required' AND resource_id = $1
	`, paidOrder.order.ID).Scan(&recoveryAudits); err != nil || recoveryAudits != 1 {
		t.Fatalf("recovery audit count = %d, %v", recoveryAudits, err)
	}

	expiredOrder, expiredItem := claimOne("recover-expired")
	setOrderStatus(t, fixture.database, expiredOrder.order.ID, domain.OrderStatusExpired)
	setReservationExpired(t, fixture.database, expiredItem)
	recovery, err = service.RecoverExpired(ctx, expiredOrder.order.ID, time.Now(), "recover-expired")
	if err != nil || recovery.Released != 1 || recovery.RecoveryRequired {
		t.Fatalf("expired RecoverExpired() = %#v, %v", recovery, err)
	}
	assertInventoryStatus(t, fixture.database, expiredItem, "available")

	soldOrder, soldItem := claimOne("release-sold")
	if _, err := fixture.database.pool.Exec(ctx, `
		UPDATE inventory_items SET status = 'sold', reserved_order_id = NULL, reserved_until = NULL,
		sold_order_id = $1 WHERE id = $2
	`, soldOrder.order.ID, soldItem); err != nil {
		t.Fatal(err)
	}
	setOrderStatus(t, fixture.database, soldOrder.order.ID, domain.OrderStatusCancelled)
	soldRelease, err := service.Release(ctx, app.InventoryReleaseRequest{
		OrderID: soldOrder.order.ID, Reason: domain.InventoryReleaseOrderCancelled,
	})
	if err != nil || soldRelease.Released != 0 {
		t.Fatalf("sold Release() = %#v, %v", soldRelease, err)
	}
	assertInventoryStatus(t, fixture.database, soldItem, "sold")
}

type reservationOrder struct {
	order generated.Order
	item  generated.OrderItem
}

type capturingFailingMessenger struct {
	mu       sync.Mutex
	err      error
	output   string
	failures int
}

func (m *capturingFailingMessenger) SendMessage(_ context.Context, request telegramadapter.SendMessageRequest) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.failures++
	m.output += request.Text
	return m.err
}

func (m *capturingFailingMessenger) EditMessage(_ context.Context, request telegramadapter.EditMessageRequest) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.failures++
	m.output += request.Text
	return m.err
}

func (m *capturingFailingMessenger) AnswerCallback(_ context.Context, request telegramadapter.AnswerCallbackRequest) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.failures++
	m.output += request.Text
	return m.err
}

func createReservingOrderWithItem(t *testing.T, database *testDatabase, productID int64, quantity int32) reservationOrder {
	t.Helper()
	order := database.createOrder(t, database.createUser(t).ID)
	setOrderStatus(t, database, order.ID, domain.OrderStatusReserving)
	item, err := database.queries.InsertOrderItem(context.Background(), generated.InsertOrderItemParams{
		OrderID: order.ID, ProductID: productID, ProductName: "Inventory product",
		UnitPriceVnd: 10_000, Quantity: quantity, LineTotalVnd: 10_000 * int64(quantity),
	})
	if err != nil {
		t.Fatalf("insert order item: %v", err)
	}
	return reservationOrder{order: order, item: item}
}

func insertProtectedInventory(t *testing.T, fixture *inventoryFixture, productID int64) int64 {
	t.Helper()
	payload, err := fixture.cipher.Protect(context.Background(), productID, runtimeOpaqueSecret(t))
	if err != nil {
		t.Fatalf("protect inventory: %v", err)
	}
	id, err := fixture.database.queries.InsertEncryptedInventoryItem(context.Background(), generated.InsertEncryptedInventoryItemParams{
		ProductID: productID, EncryptedPayload: payload.Ciphertext,
		EncryptionKeyID: fmt.Sprintf("inventory-v%d", payload.KeyVersion),
		EncryptionNonce: payload.Nonce, EncryptionKeyVersion: payload.KeyVersion,
		PayloadFingerprint: payload.Fingerprint, ImportedByAdminID: pgtype.Int8{},
	})
	if err != nil {
		t.Fatalf("insert protected inventory: %v", err)
	}
	return id
}

func startInventorySession(
	t *testing.T,
	fixture *inventoryFixture,
	telegramID int64,
	state string,
	payload any,
) app.AdminSession {
	t.Helper()
	updateID := nextPhase4Update(fixture.database)
	claimUpdate(t, fixture.updates, updateID)
	session, err := fixture.admins.StartSession(context.Background(), telegramID, state, payload, app.RequestMeta{
		RequestID: "start-inventory-session", UpdateID: updateID,
	})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	return session
}

func setInventoryEnabled(
	t *testing.T,
	fixture *inventoryFixture,
	telegramID, inventoryID, version int64,
	enabled bool,
) app.RedactedInventoryItem {
	t.Helper()
	session := startInventorySession(t, fixture, telegramID, app.SessionInventoryToggle, map[string]any{"inventory_item_id": inventoryID})
	updateID := nextPhase4Update(fixture.database)
	claimUpdate(t, fixture.updates, updateID)
	item, err := fixture.inventory.SetItemEnabled(context.Background(), telegramID, session, inventoryID, version, enabled,
		app.RequestMeta{RequestID: "toggle-inventory", UpdateID: updateID})
	if err != nil {
		t.Fatalf("SetItemEnabled() error = %v", err)
	}
	return item
}

func assertToggleRejected(t *testing.T, fixture *inventoryFixture, telegramID, inventoryID, version int64, enabled bool) {
	t.Helper()
	session := startInventorySession(t, fixture, telegramID, app.SessionInventoryToggle, map[string]any{"inventory_item_id": inventoryID})
	updateID := nextPhase4Update(fixture.database)
	claimUpdate(t, fixture.updates, updateID)
	_, err := fixture.inventory.SetItemEnabled(context.Background(), telegramID, session, inventoryID, version, enabled,
		app.RequestMeta{RequestID: "reject-toggle", UpdateID: updateID})
	if !errors.Is(err, app.ErrInvalidInventoryState) {
		t.Fatalf("SetItemEnabled() error = %v, want invalid state", err)
	}
}

func setOrderStatus(t *testing.T, database *testDatabase, orderID int64, status domain.OrderStatus) {
	t.Helper()
	query := `UPDATE orders SET status = $1 WHERE id = $2`
	if status == domain.OrderStatusCancelled {
		query = `UPDATE orders SET status = $1, cancelled_at = clock_timestamp() WHERE id = $2`
	} else if status == domain.OrderStatusDelivered {
		query = `UPDATE orders SET status = $1, delivered_at = clock_timestamp() WHERE id = $2`
	}
	if _, err := database.pool.Exec(context.Background(), query, status, orderID); err != nil {
		t.Fatalf("set order %d status %s: %v", orderID, status, err)
	}
}

func setReservationExpired(t *testing.T, database *testDatabase, inventoryID int64) {
	t.Helper()
	if _, err := database.pool.Exec(context.Background(), `
		UPDATE inventory_items SET reserved_until = clock_timestamp() - interval '1 minute' WHERE id = $1
	`, inventoryID); err != nil {
		t.Fatalf("expire reservation: %v", err)
	}
}

func assertInventoryCount(t *testing.T, database *testDatabase, productID int64, want int) {
	t.Helper()
	var count int
	if err := database.pool.QueryRow(context.Background(), `SELECT count(*) FROM inventory_items WHERE product_id = $1`, productID).Scan(&count); err != nil || count != want {
		t.Fatalf("inventory count = %d, %v, want %d", count, err, want)
	}
}

func assertStatusCount(t *testing.T, database *testDatabase, productID int64, status string, want int) {
	t.Helper()
	var count int
	if err := database.pool.QueryRow(context.Background(), `SELECT count(*) FROM inventory_items WHERE product_id = $1 AND status = $2`, productID, status).Scan(&count); err != nil || count != want {
		t.Fatalf("inventory %s count = %d, %v, want %d", status, count, err, want)
	}
}

func assertInventoryStatus(t *testing.T, database *testDatabase, inventoryID int64, want string) {
	t.Helper()
	var status string
	if err := database.pool.QueryRow(context.Background(), `SELECT status FROM inventory_items WHERE id = $1`, inventoryID).Scan(&status); err != nil || status != want {
		t.Fatalf("inventory %d status = %s, %v, want %s", inventoryID, status, err, want)
	}
}

func assertSecretAbsentFromPersistence(t *testing.T, fixture *inventoryFixture, secret []byte) {
	t.Helper()
	ctx := context.Background()
	for name, query := range map[string]string{
		"audit":   `SELECT coalesce(string_agg(coalesce(before_data::text, '') || coalesce(after_data::text, ''), ''), '') FROM audit_logs`,
		"session": `SELECT coalesce(string_agg(payload::text, ''), '') FROM admin_sessions`,
		"receipt": `SELECT coalesce(string_agg(update_type || coalesce(last_error, ''), ''), '') FROM telegram_update_receipts`,
	} {
		var boundary string
		if err := fixture.database.pool.QueryRow(ctx, query).Scan(&boundary); err != nil {
			t.Fatalf("query %s boundary: %v", name, err)
		}
		if bytes.Contains([]byte(boundary), secret) {
			t.Fatalf("%s persistence contains plaintext inventory", name)
		}
	}
}

func runtimeOpaqueSecret(t *testing.T) []byte {
	t.Helper()
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		t.Fatalf("generate runtime inventory payload: %v", err)
	}
	encoded := make([]byte, hex.EncodedLen(len(raw)))
	hex.Encode(encoded, raw)
	return encoded
}

func nextPhase4Update(database *testDatabase) int64 {
	return 9_900_000 + database.keySequence.Add(1)
}

func allZero(value []byte) bool {
	for _, item := range value {
		if item != 0 {
			return false
		}
	}
	return true
}
