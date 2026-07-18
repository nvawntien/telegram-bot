//go:build integration

package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"
	"github.com/nvawntien/telegram-bot/internal/app"
	"github.com/nvawntien/telegram-bot/internal/domain"
	"github.com/nvawntien/telegram-bot/internal/postgres"
	telegramadapter "github.com/nvawntien/telegram-bot/internal/telegram"
)

func TestPhase3UserAndCustomerCatalog(t *testing.T) {
	database := newTestDatabase(t, true)
	ctx := context.Background()
	store := postgres.NewAppStore(database.pool)
	users := app.NewUserService(store)
	catalog := app.NewCatalogService(store, 2)

	profile := app.TelegramProfile{TelegramUserID: 8_100_001, Username: "phase3", FirstName: "Phase", LastName: "Three"}
	first, err := users.Sync(ctx, profile)
	if err != nil {
		t.Fatalf("first user sync: %v", err)
	}
	if _, err := users.Sync(ctx, app.TelegramProfile{TelegramUserID: profile.TelegramUserID}); err != nil {
		t.Fatalf("second user sync: %v", err)
	}
	persisted, err := database.queries.GetUserByTelegramID(ctx, profile.TelegramUserID)
	if err != nil {
		t.Fatalf("get synced user: %v", err)
	}
	if persisted.Username.String != "phase3" || persisted.DisplayName.String != "Phase Three" {
		t.Fatalf("empty profile overwrote persisted values: %#v", persisted)
	}
	if _, err := database.pool.Exec(ctx, `UPDATE users SET status = 'banned' WHERE id = $1`, first.ID); err != nil {
		t.Fatalf("ban user: %v", err)
	}
	if _, err := users.Sync(ctx, profile); !errors.Is(err, app.ErrUserBlocked) {
		t.Fatalf("banned user sync error = %v", err)
	}

	activeCategory := database.createCategory(t)
	inactiveCategory := database.createCategory(t)
	if _, err := database.pool.Exec(ctx, `UPDATE categories SET is_active = false WHERE id = $1`, inactiveCategory); err != nil {
		t.Fatalf("deactivate category: %v", err)
	}
	activeProduct := database.createProduct(t, activeCategory)
	inactiveProduct := database.createProduct(t, activeCategory)
	if _, err := database.pool.Exec(ctx, `UPDATE products SET is_active = false WHERE id = $1`, inactiveProduct); err != nil {
		t.Fatalf("deactivate product: %v", err)
	}
	database.createProduct(t, inactiveCategory)

	categories, err := catalog.ListCategories(ctx, 0)
	if err != nil {
		t.Fatalf("list active categories: %v", err)
	}
	for _, category := range categories.Items {
		if category.ID == inactiveCategory {
			t.Fatal("inactive category returned to customer")
		}
	}
	products, err := catalog.ListProducts(ctx, activeCategory, 0)
	if err != nil {
		t.Fatalf("list active products: %v", err)
	}
	if len(products.Items) != 1 || products.Items[0].ID != activeProduct {
		t.Fatalf("active products = %#v", products.Items)
	}
	if hidden, err := catalog.ListProducts(ctx, inactiveCategory, 0); err != nil || len(hidden.Items) != 0 {
		t.Fatalf("inactive category products = %#v, %v", hidden.Items, err)
	}

	for index := 0; index < 3; index++ {
		key := database.nextKey("stable")
		if _, err := database.pool.Exec(ctx, `INSERT INTO categories (name, slug, sort_order) VALUES ($1, $1, 50)`, key); err != nil {
			t.Fatalf("create stable category: %v", err)
		}
	}
	pageOne, _ := catalog.ListCategories(ctx, 1)
	pageOneAgain, _ := catalog.ListCategories(ctx, 1)
	if fmt.Sprint(pageOne.Items) != fmt.Sprint(pageOneAgain.Items) {
		t.Fatalf("pagination changed between reads: %#v / %#v", pageOne.Items, pageOneAgain.Items)
	}
}

func TestPhase3CustomerCommandsAndCallbackRouting(t *testing.T) {
	database := newTestDatabase(t, true)
	ctx := context.Background()
	store := postgres.NewAppStore(database.pool)
	users := app.NewUserService(store)
	catalog := app.NewCatalogService(store, 8)
	admins := app.NewAdminService(store, time.Hour)
	updates := app.NewUpdateService(store, time.Minute)
	messenger := &recordingMessenger{}
	router := telegramadapter.NewRouter(
		users, catalog, admins, updates, messenger, "@shop_support",
		slog.New(slog.NewTextHandler(io.Discard, nil)), nil,
	)

	categoryID := database.createCategory(t)
	database.createProduct(t, categoryID)
	telegramUser := models.User{ID: 8_150_001, FirstName: "Customer"}
	for index, command := range []string{"/start", "/menu", "/products", "/support", "/myid"} {
		if err := router.Process(ctx, messageUpdate(int64(8_150_100+index), telegramUser, command), "customer-command"); err != nil {
			t.Fatalf("process %s: %v", command, err)
		}
	}

	callback := &models.Update{
		ID: 8_150_200,
		CallbackQuery: &models.CallbackQuery{
			ID: "customer-categories", From: telegramUser, Data: "v1:c:0",
			Message: models.MaybeInaccessibleMessage{
				Type:    models.MaybeInaccessibleMessageTypeMessage,
				Message: &models.Message{ID: 1, Date: 1, Chat: models.Chat{ID: telegramUser.ID, Type: models.ChatTypePrivate}},
			},
		},
	}
	if err := router.Process(ctx, callback, "customer-callback"); err != nil {
		t.Fatalf("process catalog callback: %v", err)
	}

	var userCount, completedReceipts int
	if err := database.pool.QueryRow(ctx, `SELECT count(*) FROM users WHERE telegram_user_id = $1`, telegramUser.ID).Scan(&userCount); err != nil {
		t.Fatalf("count command user: %v", err)
	}
	if err := database.pool.QueryRow(ctx, `SELECT count(*) FROM telegram_update_receipts WHERE update_id BETWEEN 8150100 AND 8150200 AND status = 'completed'`).Scan(&completedReceipts); err != nil {
		t.Fatalf("count completed command receipts: %v", err)
	}
	messages, edits, answers := messenger.counts()
	if userCount != 1 || completedReceipts != 6 || messages != 5 || edits != 1 || answers != 1 {
		t.Fatalf("customer routing = users:%d receipts:%d messages:%d edits:%d answers:%d", userCount, completedReceipts, messages, edits, answers)
	}
}

func TestPhase3AdminBootstrapSessionsAndReceipts(t *testing.T) {
	database := newTestDatabase(t, true)
	ctx := context.Background()
	store := postgres.NewAppStore(database.pool)
	admins := app.NewAdminService(store, time.Hour)
	updates := app.NewUpdateService(store, time.Minute)
	const adminTelegramID int64 = 8_200_001
	var waitGroup sync.WaitGroup

	if err := admins.Bootstrap(ctx, []int64{adminTelegramID, adminTelegramID}); err != nil {
		t.Fatalf("bootstrap admin: %v", err)
	}
	if err := admins.Bootstrap(ctx, []int64{adminTelegramID}); err != nil {
		t.Fatalf("repeat bootstrap admin: %v", err)
	}
	var adminCount int
	if err := database.pool.QueryRow(ctx, `SELECT count(*) FROM admins`).Scan(&adminCount); err != nil || adminCount != 1 {
		t.Fatalf("admin count = %d, %v", adminCount, err)
	}

	claimUpdate(t, updates, 8_200_100)
	session, err := admins.StartSession(ctx, adminTelegramID, app.SessionCategoryCreate, map[string]any{"step": "name"}, app.RequestMeta{UpdateID: 8_200_100})
	if err != nil {
		t.Fatalf("start session: %v", err)
	}
	restartedService := app.NewAdminService(postgres.NewAppStore(database.pool), time.Hour)
	loaded, err := restartedService.LoadSession(ctx, adminTelegramID)
	if err != nil || loaded.ID != session.ID || loaded.Version != session.Version {
		t.Fatalf("load session after service restart = %#v, %v", loaded, err)
	}

	claimUpdate(t, updates, 8_200_101)
	claimUpdate(t, updates, 8_200_102)
	type advanceResult struct {
		session app.AdminSession
		err     error
	}
	advanceResults := make(chan advanceResult, 2)
	for _, updateID := range []int64{8_200_101, 8_200_102} {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			advanced, advanceErr := admins.AdvanceSession(ctx, adminTelegramID, session, app.SessionCategoryEdit,
				map[string]any{"category_id": 1}, app.RequestMeta{UpdateID: updateID})
			advanceResults <- advanceResult{session: advanced, err: advanceErr}
		}()
	}
	waitGroup.Wait()
	close(advanceResults)
	var advanced app.AdminSession
	advanceSuccesses, staleAdvances := 0, 0
	for result := range advanceResults {
		switch {
		case result.err == nil:
			advanced = result.session
			advanceSuccesses++
		case errors.Is(result.err, app.ErrStaleVersion):
			staleAdvances++
		default:
			t.Fatalf("concurrent advance error = %v", result.err)
		}
	}
	if advanceSuccesses != 1 || staleAdvances != 1 {
		t.Fatalf("concurrent advances = success:%d stale:%d", advanceSuccesses, staleAdvances)
	}

	if _, err := database.pool.Exec(ctx, `UPDATE admin_sessions SET expires_at = clock_timestamp() - interval '1 minute' WHERE id = $1`, advanced.ID); err != nil {
		t.Fatalf("expire session: %v", err)
	}
	if _, err := admins.LoadSession(ctx, adminTelegramID); !errors.Is(err, app.ErrSessionExpired) {
		t.Fatalf("expired session error = %v", err)
	}

	const otherAdminID int64 = 8_200_002
	if err := admins.Bootstrap(ctx, []int64{otherAdminID}); err != nil {
		t.Fatalf("bootstrap second admin: %v", err)
	}
	claimUpdate(t, updates, 8_200_103)
	otherSession, err := admins.StartSession(ctx, otherAdminID, app.SessionProductCreate, nil, app.RequestMeta{UpdateID: 8_200_103})
	if err != nil {
		t.Fatalf("start other session: %v", err)
	}
	claimUpdate(t, updates, 8_200_104)
	if err := admins.CancelSession(ctx, adminTelegramID, otherSession, app.RequestMeta{UpdateID: 8_200_104}); !errors.Is(err, app.ErrForbidden) {
		t.Fatalf("cross-admin session error = %v", err)
	}

	if _, err := database.pool.Exec(ctx, `UPDATE admins SET is_active = false WHERE user_id = (SELECT id FROM users WHERE telegram_user_id = $1)`, adminTelegramID); err != nil {
		t.Fatalf("revoke admin: %v", err)
	}
	if _, err := admins.Authorize(ctx, adminTelegramID, false); !errors.Is(err, app.ErrForbidden) {
		t.Fatalf("revoked admin authorization error = %v", err)
	}
	if err := admins.Bootstrap(ctx, []int64{adminTelegramID}); err != nil {
		t.Fatalf("repeat bootstrap revoked admin: %v", err)
	}
	if _, err := admins.Authorize(ctx, adminTelegramID, false); !errors.Is(err, app.ErrForbidden) {
		t.Fatalf("bootstrap reactivated revoked admin: %v", err)
	}

	const concurrentUpdate int64 = 8_200_200
	claims := make(chan app.UpdateClaim, 2)
	errorsChannel := make(chan error, 2)
	for range 2 {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			claim, err := updates.Claim(ctx, concurrentUpdate, "message")
			claims <- claim
			errorsChannel <- err
		}()
	}
	waitGroup.Wait()
	close(claims)
	close(errorsChannel)
	claimed, processing := 0, 0
	for err := range errorsChannel {
		if err != nil {
			t.Fatalf("concurrent receipt claim: %v", err)
		}
	}
	for claim := range claims {
		switch claim {
		case app.UpdateClaimed:
			claimed++
		case app.UpdateDuplicateProcessing:
			processing++
		}
	}
	if claimed != 1 || processing != 1 {
		t.Fatalf("concurrent claims = claimed:%d processing:%d", claimed, processing)
	}
	if _, err := database.pool.Exec(ctx, `UPDATE telegram_update_receipts SET processing_started_at = clock_timestamp() - interval '10 minutes' WHERE update_id = $1`, concurrentUpdate); err != nil {
		t.Fatalf("age receipt: %v", err)
	}
	if claim, err := updates.Claim(ctx, concurrentUpdate, "message"); err != nil || claim != app.UpdateClaimed {
		t.Fatalf("stale receipt reclaim = %v, %v", claim, err)
	}
}

func TestPhase3AuditedCatalogMutationsAreAtomic(t *testing.T) {
	database := newTestDatabase(t, true)
	ctx := context.Background()
	store := postgres.NewAppStore(database.pool)
	admins := app.NewAdminService(store, time.Hour)
	updates := app.NewUpdateService(store, time.Minute)
	const telegramID int64 = 8_300_001
	if err := admins.Bootstrap(ctx, []int64{telegramID}); err != nil {
		t.Fatalf("bootstrap admin: %v", err)
	}

	claimUpdate(t, updates, 8_300_100)
	categorySession, err := admins.StartSession(ctx, telegramID, app.SessionCategoryCreate, nil, app.RequestMeta{UpdateID: 8_300_100})
	if err != nil {
		t.Fatalf("start category session: %v", err)
	}
	claimUpdate(t, updates, 8_300_101)
	category, err := admins.CreateCategory(ctx, telegramID, categorySession, app.CreateCategoryInput{
		Name: "Audited Category", Slug: "audited-category", SortOrder: 10,
		Meta: app.RequestMeta{RequestID: "request-category", UpdateID: 8_300_101},
	})
	if err != nil {
		t.Fatalf("create audited category: %v", err)
	}
	assertAuditCount(t, database, 8_300_101, "category.created", 1)
	if claim, err := updates.Claim(ctx, 8_300_101, "message"); err != nil || claim != app.UpdateDuplicateCompleted {
		t.Fatalf("completed mutation duplicate = %v, %v", claim, err)
	}

	claimUpdate(t, updates, 8_300_102)
	productSession, err := admins.StartSession(ctx, telegramID, app.SessionProductCreate, nil, app.RequestMeta{UpdateID: 8_300_102})
	if err != nil {
		t.Fatalf("start product session: %v", err)
	}
	claimUpdate(t, updates, 8_300_103)
	product, err := admins.CreateProduct(ctx, telegramID, productSession, app.CreateProductInput{
		CategoryID: category.ID, Name: "Audited Product", Slug: "audited-product",
		Description: "safe", Price: domain.Money(25_000), Meta: app.RequestMeta{UpdateID: 8_300_103},
	})
	if err != nil {
		t.Fatalf("create audited product: %v", err)
	}
	assertAuditCount(t, database, 8_300_103, "product.created", 1)

	claimUpdate(t, updates, 8_300_104)
	failingSession, err := admins.StartSession(ctx, telegramID, app.SessionCategoryCreate, nil, app.RequestMeta{UpdateID: 8_300_104})
	if err != nil {
		t.Fatalf("start failing session: %v", err)
	}
	_, err = admins.CreateCategory(ctx, telegramID, failingSession, app.CreateCategoryInput{
		Name: "Must Roll Back", Slug: "must-roll-back", Meta: app.RequestMeta{UpdateID: 9_999_999},
	})
	if err == nil {
		t.Fatal("category without receipt unexpectedly committed")
	}
	var rolledBackCount int
	if err := database.pool.QueryRow(ctx, `SELECT count(*) FROM categories WHERE slug = 'must-roll-back'`).Scan(&rolledBackCount); err != nil || rolledBackCount != 0 {
		t.Fatalf("rolled-back category count = %d, %v", rolledBackCount, err)
	}

	user := database.createUser(t)
	order := database.createOrder(t, user.ID)
	if _, err := database.pool.Exec(ctx, `
		INSERT INTO order_items (order_id, product_id, product_name, unit_price_vnd, quantity, line_total_vnd)
		VALUES ($1, $2, 'historical product', 25000, 1, 25000)
	`, order.ID, product.ID); err != nil {
		t.Fatalf("create historical order item: %v", err)
	}
	claimUpdate(t, updates, 8_300_105)
	toggleSession, err := admins.StartSession(ctx, telegramID, app.SessionProductToggle, nil, app.RequestMeta{UpdateID: 8_300_105})
	if err != nil {
		t.Fatalf("start product toggle: %v", err)
	}
	claimUpdate(t, updates, 8_300_106)
	if _, err := admins.SetProductActive(ctx, telegramID, toggleSession, app.SetProductActiveInput{
		ProductID: product.ID, ExpectedRecord: product.Version, Active: false,
		Meta: app.RequestMeta{UpdateID: 8_300_106},
	}); err != nil {
		t.Fatalf("deactivate product: %v", err)
	}
	var historicalCount int
	if err := database.pool.QueryRow(ctx, `SELECT count(*) FROM order_items WHERE order_id = $1 AND product_id = $2`, order.ID, product.ID).Scan(&historicalCount); err != nil || historicalCount != 1 {
		t.Fatalf("historical order item count = %d, %v", historicalCount, err)
	}

	claimUpdate(t, updates, 8_300_107)
	categoryToggleSession, err := admins.StartSession(ctx, telegramID, app.SessionCategoryToggle, nil, app.RequestMeta{UpdateID: 8_300_107})
	if err != nil {
		t.Fatalf("start category toggle: %v", err)
	}
	claimUpdate(t, updates, 8_300_108)
	if _, err := admins.SetCategoryActive(ctx, telegramID, categoryToggleSession, app.SetCategoryActiveInput{
		CategoryID: category.ID, ExpectedRecord: category.Version, Active: false,
		Meta: app.RequestMeta{UpdateID: 8_300_108},
	}); err != nil {
		t.Fatalf("deactivate category: %v", err)
	}
	var categoryCount, productCount int
	if err := database.pool.QueryRow(ctx, `SELECT count(*) FROM categories WHERE id = $1`, category.ID).Scan(&categoryCount); err != nil {
		t.Fatalf("count deactivated category: %v", err)
	}
	if err := database.pool.QueryRow(ctx, `SELECT count(*) FROM products WHERE id = $1`, product.ID).Scan(&productCount); err != nil {
		t.Fatalf("count product under deactivated category: %v", err)
	}
	if err := database.pool.QueryRow(ctx, `SELECT count(*) FROM order_items WHERE order_id = $1 AND product_id = $2`, order.ID, product.ID).Scan(&historicalCount); err != nil {
		t.Fatalf("count order history after category deactivation: %v", err)
	}
	if categoryCount != 1 || productCount != 1 || historicalCount != 1 {
		t.Fatalf("records after category deactivation = category:%d product:%d order_item:%d", categoryCount, productCount, historicalCount)
	}
}

func TestPhase3TelegramSendFailureDoesNotRollbackAdminMutation(t *testing.T) {
	database := newTestDatabase(t, true)
	ctx := context.Background()
	store := postgres.NewAppStore(database.pool)
	users := app.NewUserService(store)
	catalog := app.NewCatalogService(store, 8)
	admins := app.NewAdminService(store, time.Hour)
	updates := app.NewUpdateService(store, time.Minute)
	const telegramID int64 = 8_400_001
	if err := admins.Bootstrap(ctx, []int64{telegramID}); err != nil {
		t.Fatalf("bootstrap admin: %v", err)
	}
	messenger := &failingMessenger{err: errors.New("Telegram unavailable")}
	router := telegramadapter.NewRouter(
		users, catalog, admins, updates, messenger, "Support",
		slog.New(slog.NewTextHandler(io.Discard, nil)), nil,
	)
	telegramUser := models.User{ID: telegramID, FirstName: "Admin"}
	callbackUpdate := &models.Update{
		ID: 8_400_100,
		CallbackQuery: &models.CallbackQuery{
			ID: "create-category", From: telegramUser, Data: "v1:a:cn",
			Message: models.MaybeInaccessibleMessage{
				Type:    models.MaybeInaccessibleMessageTypeMessage,
				Message: &models.Message{ID: 1, Date: 1, Chat: models.Chat{ID: telegramID, Type: models.ChatTypePrivate}},
			},
		},
	}
	if err := router.Process(ctx, callbackUpdate, "request-start"); err != nil {
		t.Fatalf("start category workflow: %v", err)
	}
	nameUpdate := messageUpdate(8_400_101, telegramUser, "Persistent Category")
	if err := router.Process(ctx, nameUpdate, "request-name"); err != nil {
		t.Fatalf("category name step: %v", err)
	}
	sortUpdate := messageUpdate(8_400_102, telegramUser, "7")
	processErrors := make(chan error, 2)
	var processWaitGroup sync.WaitGroup
	for range 2 {
		processWaitGroup.Add(1)
		go func() {
			defer processWaitGroup.Done()
			processErrors <- router.Process(ctx, sortUpdate, "request-sort")
		}()
	}
	processWaitGroup.Wait()
	close(processErrors)
	for err := range processErrors {
		if err != nil {
			t.Fatalf("concurrent category create step: %v", err)
		}
	}
	if err := router.Process(ctx, sortUpdate, "request-sort-duplicate"); err != nil {
		t.Fatalf("duplicate category update: %v", err)
	}
	var categoryCount, auditCount int
	if err := database.pool.QueryRow(ctx, `SELECT count(*) FROM categories WHERE slug = 'category-8400102'`).Scan(&categoryCount); err != nil {
		t.Fatalf("count committed category: %v", err)
	}
	if err := database.pool.QueryRow(ctx, `SELECT count(*) FROM audit_logs WHERE telegram_update_id = 8400102 AND action = 'category.created'`).Scan(&auditCount); err != nil {
		t.Fatalf("count committed audit: %v", err)
	}
	if categoryCount != 1 || auditCount != 1 {
		t.Fatalf("committed category/audit counts = %d/%d", categoryCount, auditCount)
	}
	if messenger.failures == 0 {
		t.Fatal("fake Telegram failure was not exercised")
	}
}

func claimUpdate(t *testing.T, service *app.UpdateService, updateID int64) {
	t.Helper()
	claim, err := service.Claim(context.Background(), updateID, "message")
	if err != nil || claim != app.UpdateClaimed {
		t.Fatalf("claim update %d = %v, %v", updateID, claim, err)
	}
}

func assertAuditCount(t *testing.T, database *testDatabase, updateID int64, action string, want int) {
	t.Helper()
	var count int
	err := database.pool.QueryRow(context.Background(), `
		SELECT count(*) FROM audit_logs WHERE telegram_update_id = $1 AND action = $2
	`, updateID, action).Scan(&count)
	if err != nil || count != want {
		t.Fatalf("audit count for %s = %d, %v", action, count, err)
	}
}

func decodeSessionPayload(t *testing.T, session app.AdminSession) map[string]any {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(session.Payload, &payload); err != nil {
		t.Fatalf("decode session payload: %v", err)
	}
	return payload
}

type failingMessenger struct {
	mu       sync.Mutex
	err      error
	failures int
}

type recordingMessenger struct {
	mu       sync.Mutex
	messages int
	edits    int
	answers  int
}

func (m *recordingMessenger) SendMessage(context.Context, telegramadapter.SendMessageRequest) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.messages++
	return nil
}

func (m *recordingMessenger) EditMessage(context.Context, telegramadapter.EditMessageRequest) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.edits++
	return nil
}

func (m *recordingMessenger) AnswerCallback(context.Context, telegramadapter.AnswerCallbackRequest) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.answers++
	return nil
}

func (m *recordingMessenger) counts() (int, int, int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.messages, m.edits, m.answers
}

func (m *failingMessenger) SendMessage(context.Context, telegramadapter.SendMessageRequest) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.failures++
	return m.err
}

func (m *failingMessenger) EditMessage(context.Context, telegramadapter.EditMessageRequest) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.failures++
	return m.err
}

func (m *failingMessenger) AnswerCallback(context.Context, telegramadapter.AnswerCallbackRequest) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.failures++
	return m.err
}

func messageUpdate(updateID int64, user models.User, text string) *models.Update {
	return &models.Update{
		ID: updateID,
		Message: &models.Message{
			ID: int(updateID), Date: 1, From: &user,
			Chat: models.Chat{ID: user.ID, Type: models.ChatTypePrivate}, Text: text,
		},
	}
}
