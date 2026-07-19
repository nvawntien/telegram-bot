package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/go-telegram/bot/models"
	"github.com/nvawntien/telegram-bot/internal/app"
	"github.com/nvawntien/telegram-bot/internal/domain"
)

type RouterMetrics interface {
	ObserveUpdate(updateType, result string, duration time.Duration)
	ObserveDuplicate()
	ObserveCatalog(operation, result string)
	ObserveAdminMutation(operation, result string)
	ObserveAdminSession(operation, result string)
}

type Router struct {
	users          *app.UserService
	catalog        *app.CatalogService
	admins         *app.AdminService
	inventory      *app.InventoryAdminService
	banks          *app.BankAccountService
	orders         *app.OrderService
	updates        *app.UpdateService
	messenger      Messenger
	supportContact string
	logger         *slog.Logger
	metrics        RouterMetrics
}

func NewRouter(
	users *app.UserService,
	catalog *app.CatalogService,
	admins *app.AdminService,
	inventory *app.InventoryAdminService,
	updates *app.UpdateService,
	messenger Messenger,
	supportContact string,
	logger *slog.Logger,
	metrics RouterMetrics,
) *Router {
	return &Router{
		users: users, catalog: catalog, admins: admins, inventory: inventory, updates: updates,
		messenger: messenger, supportContact: supportContact, logger: logger, metrics: metrics,
	}
}

func NewRouterWithOrdering(
	users *app.UserService,
	catalog *app.CatalogService,
	admins *app.AdminService,
	inventory *app.InventoryAdminService,
	banks *app.BankAccountService,
	orders *app.OrderService,
	updates *app.UpdateService,
	messenger Messenger,
	supportContact string,
	logger *slog.Logger,
	metrics RouterMetrics,
) *Router {
	router := NewRouter(users, catalog, admins, inventory, updates, messenger, supportContact, logger, metrics)
	router.banks = banks
	router.orders = orders
	return router
}

type responsePlan struct {
	chatID         int64
	messageID      int
	text           string
	keyboard       Keyboard
	callbackID     string
	callbackText   string
	callbackAlert  bool
	edit           bool
	completedByApp bool
}

func (r *Router) Process(ctx context.Context, update *models.Update, requestID string) (err error) {
	started := time.Now()
	updateType := ClassifyUpdate(update)
	result := "success"
	defer func() {
		if err != nil {
			result = "failed"
		}
		if r.metrics != nil {
			r.metrics.ObserveUpdate(updateType, result, time.Since(started))
		}
	}()
	if update == nil || update.ID < 0 {
		return app.ErrInvalidInput
	}

	claim, err := r.updates.Claim(ctx, update.ID, updateType)
	if err != nil {
		r.answerDuplicateCallback(ctx, update, "Vui lòng thử lại.")
		return err
	}
	if claim != app.UpdateClaimed {
		result = "duplicate"
		if r.metrics != nil {
			r.metrics.ObserveDuplicate()
		}
		r.answerDuplicateCallback(ctx, update, "Đã xử lý.")
		return nil
	}

	plan, dispatchErr := r.dispatch(ctx, update, requestID)
	if dispatchErr != nil {
		_ = r.updates.Fail(ctx, update.ID, errorCode(dispatchErr))
		r.answerDuplicateCallback(ctx, update, userError(dispatchErr))
		return dispatchErr
	}
	if !plan.completedByApp {
		if err := r.updates.Complete(ctx, update.ID); err != nil {
			_ = r.updates.Fail(ctx, update.ID, "receipt_completion_failed")
			return err
		}
	}
	r.executePlan(ctx, update, plan)
	r.logger.InfoContext(ctx, "Telegram update processed",
		"request_id", requestID,
		"telegram_update_id", update.ID,
		"telegram_update_type", updateType,
		"result", result,
		"duration_ms", time.Since(started).Milliseconds(),
	)
	return nil
}

func (r *Router) dispatch(ctx context.Context, update *models.Update, requestID string) (responsePlan, error) {
	if update.Message != nil {
		return r.handleMessage(ctx, update, requestID)
	}
	if update.CallbackQuery != nil {
		return r.handleCallback(ctx, update, requestID)
	}
	return responsePlan{}, nil
}

func (r *Router) handleMessage(ctx context.Context, update *models.Update, requestID string) (responsePlan, error) {
	message := update.Message
	if message.From == nil || message.From.IsBot || message.Chat.ID == 0 {
		return responsePlan{}, nil
	}
	user, err := r.users.Sync(ctx, profileFromUser(*message.From))
	if errors.Is(err, app.ErrUserBlocked) {
		return responsePlan{chatID: message.Chat.ID, text: "Tài khoản không thể sử dụng chức năng này."}, nil
	}
	if err != nil {
		return responsePlan{}, err
	}
	command, commandFound := ParseCommand(message.Text)
	if commandFound {
		return r.handleCommand(ctx, update.ID, requestID, message, user, command)
	}
	if message.Text != "" {
		if session, sessionErr := r.admins.LoadSession(ctx, message.From.ID); sessionErr == nil {
			return r.handleSessionText(ctx, update.ID, requestID, message, session)
		}
	}
	return responsePlan{chatID: message.Chat.ID, text: "Lệnh chưa được hỗ trợ. Dùng /menu để mở menu."}, nil
}

func (r *Router) handleCommand(
	ctx context.Context,
	updateID int64,
	requestID string,
	message *models.Message,
	user app.User,
	command Command,
) (responsePlan, error) {
	switch command.Name {
	case "start", "menu":
		return responsePlan{chatID: message.Chat.ID, text: MainMenuText(user.DisplayName), keyboard: MainMenuKeyboard()}, nil
	case "products":
		page, err := r.catalog.ListCategories(ctx, 0)
		r.observeCatalog("list_categories", err)
		if err != nil {
			return responsePlan{}, err
		}
		text, keyboard := CategoriesView(page)
		return responsePlan{chatID: message.Chat.ID, text: text, keyboard: keyboard}, nil
	case "orders":
		page, err := r.orders.List(ctx, user.TelegramUserID, 0)
		r.observeOrder("history", err, 0)
		if err != nil {
			return responsePlan{}, err
		}
		text, keyboard := OrdersView(page)
		return responsePlan{chatID: message.Chat.ID, text: text, keyboard: keyboard}, nil
	case "order":
		orderID, err := strconv.ParseInt(command.Payload, 10, 64)
		if err != nil || orderID <= 0 {
			return responsePlan{}, app.ErrInvalidInput
		}
		order, instruction, err := r.orders.Get(ctx, user.TelegramUserID, orderID)
		r.observeOrder("history", err, 0)
		if err != nil {
			return responsePlan{}, err
		}
		text, keyboard := OrderDetailView(order, instruction)
		return responsePlan{chatID: message.Chat.ID, text: text, keyboard: keyboard}, nil
	case "support":
		return responsePlan{chatID: message.Chat.ID, text: "Hỗ trợ: <b>" + Escape(r.supportContact) + "</b>"}, nil
	case "myid":
		return responsePlan{chatID: message.Chat.ID, text: fmt.Sprintf("Telegram ID của bạn: <code>%d</code>", user.TelegramUserID)}, nil
	case "admin":
		if _, err := r.admins.Authorize(ctx, message.From.ID, false); err != nil {
			r.logger.WarnContext(ctx, "Unauthorized admin command",
				"request_id", requestID, "telegram_update_id", updateID,
				"telegram_user_id", message.From.ID, "result", "forbidden")
			return responsePlan{chatID: message.Chat.ID, text: "Bạn không có quyền sử dụng chức năng này."}, nil
		}
		text, keyboard := AdminMenu()
		return responsePlan{chatID: message.Chat.ID, text: text, keyboard: keyboard}, nil
	default:
		return responsePlan{chatID: message.Chat.ID, text: "Lệnh chưa được hỗ trợ. Dùng /menu để mở menu."}, nil
	}
}

func (r *Router) handleCallback(ctx context.Context, update *models.Update, requestID string) (responsePlan, error) {
	query := update.CallbackQuery
	chatID, messageID := callbackMessage(query)
	plan := responsePlan{chatID: chatID, messageID: messageID, callbackID: query.ID, edit: chatID != 0 && messageID != 0}
	if query.From.IsBot || query.From.ID <= 0 {
		plan.callbackText = "Không hợp lệ."
		return plan, nil
	}
	if _, err := r.users.Sync(ctx, profileFromUser(query.From)); errors.Is(err, app.ErrUserBlocked) {
		plan.callbackText, plan.callbackAlert = "Tài khoản không thể sử dụng chức năng này.", true
		return plan, nil
	} else if err != nil {
		return responsePlan{}, err
	}
	callback, err := ParseCallback(query.Data)
	if err != nil {
		plan.callbackText, plan.callbackAlert = "Thao tác không hợp lệ.", true
		return plan, nil
	}
	meta := app.RequestMeta{RequestID: requestID, UpdateID: update.ID}
	return r.routeCallback(ctx, query.From.ID, callback, meta, plan)
}

func (r *Router) routeCallback(ctx context.Context, telegramID int64, callback Callback, meta app.RequestMeta, plan responsePlan) (responsePlan, error) {
	switch callback.Action {
	case CallbackMenu:
		plan.text, plan.keyboard = MainMenuText("bạn"), MainMenuKeyboard()
	case CallbackSupport:
		plan.text = "Hỗ trợ: <b>" + Escape(r.supportContact) + "</b>"
		plan.keyboard = Keyboard{{{Text: "⬅️ Menu", Data: "v1:m"}}}
	case CallbackCategories:
		page, err := r.catalog.ListCategories(ctx, callback.Page)
		r.observeCatalog("list_categories", err)
		if err != nil {
			return plan, err
		}
		plan.text, plan.keyboard = CategoriesView(page)
	case CallbackProducts:
		page, err := r.catalog.ListProducts(ctx, callback.CategoryID, callback.Page)
		r.observeCatalog("list_products", err)
		if err != nil {
			return plan, err
		}
		plan.text, plan.keyboard = ProductsView(callback.CategoryID, page)
	case CallbackProductDetail:
		product, err := r.catalog.GetProduct(ctx, callback.ProductID)
		r.observeCatalog("get_product", err)
		if err != nil {
			return plan, err
		}
		plan.text, plan.keyboard = ProductView(product, callback.CategoryID, callback.Page)
	case CallbackOrderQuantity:
		banks, err := r.banks.ListActive(ctx)
		if err != nil {
			return plan, err
		}
		if len(banks) == 0 {
			return plan, app.ErrBankAccountNotFound
		}
		plan.text, plan.keyboard = BankSelectionView(callback.ProductID, callback.Quantity, banks)
	case CallbackOrderBank:
		banks, err := r.banks.ListActive(ctx)
		if err != nil {
			return plan, err
		}
		bank, found := activeBankOption(banks, callback.BankAccountID)
		if !found {
			return plan, app.ErrBankAccountInactive
		}
		plan.text, plan.keyboard = OrderConfirmView(meta.UpdateID, callback.ProductID, callback.Quantity, bank)
	case CallbackOrderConfirm:
		started := time.Now()
		result, err := r.orders.Create(ctx, app.CreateOrderCommand{
			TelegramUserID: telegramID, ProductID: callback.ProductID,
			BankAccountID: callback.BankAccountID, Quantity: callback.Quantity,
			IdempotencyKey: fmt.Sprintf("telegram-order-flow:%d", callback.FlowID), Meta: meta,
		})
		r.observeOrder("create", err, time.Since(started))
		if err != nil {
			return plan, err
		}
		r.observeOrder("instruction", nil, 0)
		plan.text, plan.keyboard = PaymentInstructionView(result.Order, result.Instruction)
		plan.completedByApp = true
	case CallbackOrders:
		page, err := r.orders.List(ctx, telegramID, callback.Page)
		r.observeOrder("history", err, 0)
		if err != nil {
			return plan, err
		}
		plan.text, plan.keyboard = OrdersView(page)
	case CallbackOrderView:
		order, instruction, err := r.orders.Get(ctx, telegramID, callback.OrderID)
		r.observeOrder("history", err, 0)
		if err != nil {
			return plan, err
		}
		r.observeOrder("instruction", nil, 0)
		plan.text, plan.keyboard = OrderDetailView(order, instruction)
	case CallbackOrderAskCancel:
		order, _, err := r.orders.Get(ctx, telegramID, callback.OrderID)
		if err != nil {
			return plan, err
		}
		if order.Status != domain.OrderStatusPendingPayment || order.Version != callback.RecordVersion {
			return plan, app.ErrInvalidOrderState
		}
		plan.text, plan.keyboard = OrderCancelConfirmationView(order.ID, order.Version)
	case CallbackOrderCancel:
		started := time.Now()
		result, err := r.orders.Cancel(ctx, app.CancelOrderCommand{
			TelegramUserID: telegramID, OrderID: callback.OrderID,
			ExpectedVersion: callback.RecordVersion, Meta: meta,
		})
		r.observeOrder("cancel", err, time.Since(started))
		if err != nil {
			return plan, err
		}
		plan.text = fmt.Sprintf("Đã hủy đơn #%d.", result.Order.ID)
		plan.keyboard = Keyboard{{{Text: "Danh sách đơn", Data: "v1:o:l:0"}}}
		plan.completedByApp = true
	case CallbackAdminCategories, CallbackAdminProducts:
		if _, err := r.admins.Authorize(ctx, telegramID, false); err != nil {
			return plan, err
		}
		if callback.Action == CallbackAdminCategories {
			page, err := r.catalog.ListAdminCategories(ctx, callback.Page)
			if err != nil {
				return plan, err
			}
			plan.text, plan.keyboard = AdminCategoriesView(page)
		} else {
			page, err := r.catalog.ListAdminProducts(ctx, callback.Page)
			if err != nil {
				return plan, err
			}
			plan.text, plan.keyboard = AdminProductsView(page)
		}
	case CallbackAdminInventory:
		page, err := r.inventory.ListOverview(ctx, telegramID, callback.Page)
		if err != nil {
			return plan, err
		}
		plan.text, plan.keyboard = AdminInventoryOverviewView(page)
	case CallbackAdminBanks:
		page, err := r.banks.ListAdmin(ctx, telegramID, callback.Page)
		if err != nil {
			return plan, err
		}
		plan.text, plan.keyboard = AdminBankAccountsView(page)
	case CallbackAdminBankNew:
		session, err := r.admins.StartSession(ctx, telegramID, app.SessionBankCreate, workflowPayload{Step: "bank_bin"}, meta)
		r.observeSession("start", err)
		if err != nil {
			return plan, err
		}
		plan.text, plan.keyboard, plan.completedByApp = "Gửi mã BIN ngân hàng gồm 6 chữ số.", cancelKeyboard(session), true
	case CallbackAdminBankEdit:
		session, err := r.admins.StartSession(ctx, telegramID, app.SessionBankEdit, workflowPayload{
			Step: "bank_bin", BankAccountID: callback.BankAccountID, RecordVersion: callback.RecordVersion,
		}, meta)
		r.observeSession("start", err)
		if err != nil {
			return plan, err
		}
		plan.text, plan.keyboard, plan.completedByApp = "Gửi mã BIN ngân hàng mới gồm 6 chữ số.", cancelKeyboard(session), true
	case CallbackAdminBankAskToggle:
		session, err := r.admins.StartSession(ctx, telegramID, app.SessionBankToggle, workflowPayload{
			Step: "confirm", BankAccountID: callback.BankAccountID,
			RecordVersion: callback.RecordVersion, Active: callback.Active,
		}, meta)
		r.observeSession("start", err)
		if err != nil {
			return plan, err
		}
		plan.text = "Xác nhận đổi trạng thái tài khoản ngân hàng?"
		plan.keyboard = Keyboard{{{Text: "Xác nhận", Data: fmt.Sprintf("v1:a:bt:%d:%d:%d:%d:%d", session.ID, session.Version, callback.BankAccountID, callback.RecordVersion, boolBit(callback.Active))}}}
		plan.completedByApp = true
	case CallbackAdminBankToggle:
		admin, err := r.admins.Authorize(ctx, telegramID, true)
		if err != nil {
			return plan, err
		}
		session := app.AdminSession{ID: callback.SessionID, AdminID: admin.ID, Version: callback.SessionVersion}
		bank, err := r.banks.SetActive(ctx, telegramID, session, app.SetBankAccountActiveInput{
			BankAccountID: callback.BankAccountID, ExpectedRecord: callback.RecordVersion,
			Active: callback.Active, Meta: meta,
		})
		r.observeBankMutation("toggle", err)
		if err != nil {
			return plan, err
		}
		plan.text, plan.keyboard, plan.completedByApp = "Đã cập nhật tài khoản: "+Escape(bank.DisplayName), nil, true
	case CallbackAdminInventoryList:
		page, err := r.inventory.ListItems(ctx, telegramID, callback.ProductID, callback.Page)
		if err != nil {
			return plan, err
		}
		plan.text, plan.keyboard = AdminInventoryItemsView(callback.ProductID, page)
	case CallbackAdminInventoryImport:
		session, err := r.admins.StartSession(ctx, telegramID, app.SessionInventoryImport, workflowPayload{
			Step: "payload", ProductID: callback.ProductID,
		}, meta)
		r.observeSession("start", err)
		if err != nil {
			return plan, err
		}
		plan.text = "Gửi inventory, mỗi dòng là một item. Nội dung sẽ không được hiển thị lại."
		plan.keyboard, plan.completedByApp = cancelKeyboard(session), true
	case CallbackAdminInventoryAskToggle:
		session, err := r.admins.StartSession(ctx, telegramID, app.SessionInventoryToggle, workflowPayload{
			Step: "confirm", InventoryItemID: callback.InventoryID,
			RecordVersion: callback.RecordVersion, Active: callback.Active,
		}, meta)
		r.observeSession("start", err)
		if err != nil {
			return plan, err
		}
		action := "tắt"
		if callback.Active {
			action = "bật"
		}
		plan.text = fmt.Sprintf("Xác nhận %s inventory item #%d?", action, callback.InventoryID)
		plan.keyboard = Keyboard{{{Text: "Xác nhận", Data: fmt.Sprintf(
			"v1:a:it:%d:%d:%d:%d:%d", session.ID, session.Version,
			callback.InventoryID, callback.RecordVersion, boolBit(callback.Active),
		)}}}
		plan.completedByApp = true
	case CallbackAdminInventoryToggle:
		admin, err := r.admins.Authorize(ctx, telegramID, true)
		if err != nil {
			return plan, err
		}
		session := app.AdminSession{ID: callback.SessionID, AdminID: admin.ID, Version: callback.SessionVersion}
		item, err := r.inventory.SetItemEnabled(
			ctx, telegramID, session, callback.InventoryID, callback.RecordVersion, callback.Active, meta,
		)
		if err != nil {
			return plan, err
		}
		plan.text = fmt.Sprintf("Đã cập nhật inventory item #%d thành %s.", item.ID, Escape(string(item.Status)))
		plan.keyboard, plan.completedByApp = nil, true
	case CallbackAdminCategoryNew:
		session, err := r.admins.StartSession(ctx, telegramID, app.SessionCategoryCreate, workflowPayload{Step: "name"}, meta)
		r.observeSession("start", err)
		if err != nil {
			return plan, err
		}
		plan.text, plan.keyboard, plan.completedByApp = "Gửi tên danh mục mới.", cancelKeyboard(session), true
	case CallbackAdminCategoryEdit, CallbackAdminCategoryAskToggle:
		state, step := app.SessionCategoryEdit, "name"
		if callback.Action == CallbackAdminCategoryAskToggle {
			state, step = app.SessionCategoryToggle, "confirm"
		}
		session, err := r.admins.StartSession(ctx, telegramID, state, workflowPayload{
			Step: step, CategoryID: callback.CategoryID, RecordVersion: callback.RecordVersion,
		}, meta)
		r.observeSession("start", err)
		if err != nil {
			return plan, err
		}
		plan.completedByApp = true
		if state == app.SessionCategoryEdit {
			plan.text, plan.keyboard = "Gửi tên danh mục mới.", cancelKeyboard(session)
		} else {
			plan.text = "Xác nhận đổi trạng thái danh mục?"
			plan.keyboard = Keyboard{{{Text: "Xác nhận", Data: fmt.Sprintf("v1:a:ct:%d:%d:%d:%d:%d", session.ID, session.Version, callback.CategoryID, callback.RecordVersion, boolBit(callback.Active))}}}
		}
	case CallbackAdminCategoryToggle:
		admin, err := r.admins.Authorize(ctx, telegramID, true)
		if err != nil {
			return plan, err
		}
		session := app.AdminSession{ID: callback.SessionID, AdminID: admin.ID, Version: callback.SessionVersion}
		category, err := r.admins.SetCategoryActive(ctx, telegramID, session, app.SetCategoryActiveInput{
			CategoryID: callback.CategoryID, ExpectedRecord: callback.RecordVersion,
			Active: callback.Active, Meta: meta,
		})
		r.observeMutation("category.toggle", err)
		if err != nil {
			return plan, err
		}
		plan.text, plan.keyboard, plan.completedByApp = "Đã cập nhật danh mục: "+Escape(category.Name), nil, true
	case CallbackAdminProductNew:
		session, err := r.admins.StartSession(ctx, telegramID, app.SessionProductCreate, workflowPayload{Step: "category"}, meta)
		r.observeSession("start", err)
		if err != nil {
			return plan, err
		}
		plan.text, plan.keyboard, plan.completedByApp = "Gửi ID danh mục cho sản phẩm.", cancelKeyboard(session), true
	case CallbackAdminProductEdit, CallbackAdminProductAskToggle:
		state, step := app.SessionProductEdit, "name"
		if callback.Action == CallbackAdminProductAskToggle {
			state, step = app.SessionProductToggle, "confirm"
		}
		session, err := r.admins.StartSession(ctx, telegramID, state, workflowPayload{
			Step: step, ProductID: callback.ProductID, RecordVersion: callback.RecordVersion,
		}, meta)
		r.observeSession("start", err)
		if err != nil {
			return plan, err
		}
		plan.completedByApp = true
		if state == app.SessionProductEdit {
			plan.text, plan.keyboard = "Gửi tên sản phẩm mới.", cancelKeyboard(session)
		} else {
			plan.text = "Xác nhận đổi trạng thái sản phẩm?"
			plan.keyboard = Keyboard{{{Text: "Xác nhận", Data: fmt.Sprintf("v1:a:pt:%d:%d:%d:%d:%d", session.ID, session.Version, callback.ProductID, callback.RecordVersion, boolBit(callback.Active))}}}
		}
	case CallbackAdminProductToggle:
		admin, err := r.admins.Authorize(ctx, telegramID, true)
		if err != nil {
			return plan, err
		}
		session := app.AdminSession{ID: callback.SessionID, AdminID: admin.ID, Version: callback.SessionVersion}
		product, err := r.admins.SetProductActive(ctx, telegramID, session, app.SetProductActiveInput{
			ProductID: callback.ProductID, ExpectedRecord: callback.RecordVersion,
			Active: callback.Active, Meta: meta,
		})
		r.observeMutation("product.toggle", err)
		if err != nil {
			return plan, err
		}
		plan.text, plan.keyboard, plan.completedByApp = "Đã cập nhật sản phẩm: "+Escape(product.Name), nil, true
	case CallbackAdminCancel:
		admin, err := r.admins.Authorize(ctx, telegramID, true)
		if err != nil {
			return plan, err
		}
		session := app.AdminSession{ID: callback.SessionID, AdminID: admin.ID, Version: callback.SessionVersion}
		err = r.admins.CancelSession(ctx, telegramID, session, meta)
		r.observeSession("cancel", err)
		if err != nil {
			return plan, err
		}
		plan.text, plan.keyboard, plan.completedByApp = "Đã hủy thao tác.", nil, true
	default:
		plan.callbackText, plan.callbackAlert = "Thao tác không hợp lệ.", true
	}
	return plan, nil
}

type workflowPayload struct {
	Step            string `json:"step"`
	CategoryID      int64  `json:"category_id,omitempty"`
	ProductID       int64  `json:"product_id,omitempty"`
	RecordVersion   int64  `json:"record_version,omitempty"`
	InventoryItemID int64  `json:"inventory_item_id,omitempty"`
	Active          bool   `json:"active,omitempty"`
	Name            string `json:"name,omitempty"`
	Description     string `json:"description,omitempty"`
	SortOrder       int32  `json:"sort_order,omitempty"`
	PriceVND        int64  `json:"price_vnd,omitempty"`
	BankAccountID   int64  `json:"bank_account_id,omitempty"`
	BankBIN         string `json:"bank_bin,omitempty"`
	BankName        string `json:"bank_name,omitempty"`
	DisplayName     string `json:"display_name,omitempty"`
	AccountName     string `json:"account_name,omitempty"`
}

func (r *Router) handleSessionText(ctx context.Context, updateID int64, requestID string, message *models.Message, session app.AdminSession) (responsePlan, error) {
	var payload workflowPayload
	if err := json.Unmarshal(session.Payload, &payload); err != nil {
		return responsePlan{}, app.ErrInvalidInput
	}
	text := strings.TrimSpace(message.Text)
	meta := app.RequestMeta{RequestID: requestID, UpdateID: updateID}
	plan := responsePlan{chatID: message.Chat.ID, completedByApp: true}
	switch session.State {
	case app.SessionInventoryImport:
		if payload.Step != "payload" || payload.ProductID <= 0 {
			return responsePlan{}, app.ErrStaleVersion
		}
		result, err := r.inventory.Import(
			ctx, message.From.ID, session, payload.ProductID, []byte(message.Text), meta,
		)
		if err != nil {
			return responsePlan{}, err
		}
		plan.text = fmt.Sprintf(
			"Import hoàn tất: đã thêm %d, trùng %d, bỏ qua %d.",
			result.Inserted, result.Duplicates, result.Rejected,
		)
	case app.SessionCategoryCreate, app.SessionCategoryEdit:
		if payload.Step == "name" {
			if text == "" || len([]rune(text)) > 120 {
				return responsePlan{}, app.ErrInvalidInput
			}
			payload.Name, payload.Step = text, "sort"
			next, err := r.admins.AdvanceSession(ctx, message.From.ID, session, session.State, payload, meta)
			r.observeSession("advance", err)
			if err != nil {
				return responsePlan{}, err
			}
			plan.text, plan.keyboard = "Gửi thứ tự hiển thị (số nguyên không âm).", cancelKeyboard(next)
			return plan, nil
		}
		sortOrder, err := strconv.ParseInt(text, 10, 32)
		if err != nil || sortOrder < 0 {
			return responsePlan{}, app.ErrInvalidInput
		}
		if session.State == app.SessionCategoryCreate {
			category, err := r.admins.CreateCategory(ctx, message.From.ID, session, app.CreateCategoryInput{
				Name: payload.Name, Slug: fmt.Sprintf("category-%d", updateID), SortOrder: int32(sortOrder), Meta: meta,
			})
			r.observeMutation("category.create", err)
			if err != nil {
				return responsePlan{}, err
			}
			plan.text = "Đã tạo danh mục: " + Escape(category.Name)
		} else {
			category, err := r.admins.UpdateCategory(ctx, message.From.ID, session, app.UpdateCategoryInput{
				CategoryID: payload.CategoryID, ExpectedRecord: payload.RecordVersion,
				Name: payload.Name, SortOrder: int32(sortOrder), Meta: meta,
			})
			r.observeMutation("category.update", err)
			if err != nil {
				return responsePlan{}, err
			}
			plan.text = "Đã cập nhật danh mục: " + Escape(category.Name)
		}
	case app.SessionProductCreate, app.SessionProductEdit:
		return r.handleProductSessionText(ctx, message, session, payload, meta, plan)
	case app.SessionBankCreate, app.SessionBankEdit:
		return r.handleBankSessionText(ctx, message, session, payload, meta, plan)
	default:
		return responsePlan{}, app.ErrStaleVersion
	}
	return plan, nil
}

func (r *Router) handleBankSessionText(ctx context.Context, message *models.Message, session app.AdminSession, payload workflowPayload, meta app.RequestMeta, plan responsePlan) (responsePlan, error) {
	text := strings.TrimSpace(message.Text)
	nextPrompt := ""
	switch payload.Step {
	case "bank_bin":
		if len(text) != 6 {
			return responsePlan{}, app.ErrInvalidInput
		}
		payload.BankBIN, payload.Step, nextPrompt = text, "bank_name", "Gửi tên ngân hàng."
	case "bank_name":
		if text == "" || len([]rune(text)) > 120 {
			return responsePlan{}, app.ErrInvalidInput
		}
		payload.BankName, payload.Step, nextPrompt = text, "display_name", "Gửi tên hiển thị."
	case "display_name":
		if text == "" || len([]rune(text)) > 120 {
			return responsePlan{}, app.ErrInvalidInput
		}
		payload.DisplayName, payload.Step, nextPrompt = text, "account_name", "Gửi tên chủ tài khoản."
	case "account_name":
		if text == "" || len([]rune(text)) > 160 {
			return responsePlan{}, app.ErrInvalidInput
		}
		payload.AccountName, payload.Step, nextPrompt = text, "sort_order", "Gửi thứ tự hiển thị (số nguyên không âm)."
	case "sort_order":
		sortOrder, err := strconv.ParseInt(text, 10, 32)
		if err != nil || sortOrder < 0 {
			return responsePlan{}, app.ErrInvalidInput
		}
		payload.SortOrder, payload.Step, nextPrompt = int32(sortOrder), "account_number", "Gửi số tài khoản (chỉ chữ số). Nội dung này sẽ được mã hóa ngay và không lưu trong phiên."
	case "account_number":
		input := app.BankAccountInput{
			BankBIN: payload.BankBIN, BankName: payload.BankName, DisplayName: payload.DisplayName,
			AccountName: payload.AccountName, AccountNumber: text, SortOrder: payload.SortOrder,
		}
		if session.State == app.SessionBankCreate {
			bank, err := r.banks.Create(ctx, message.From.ID, session, app.CreateBankAccountInput{BankAccountInput: input, Meta: meta})
			r.observeBankMutation("create", err)
			if err != nil {
				return responsePlan{}, err
			}
			plan.text = "Đã tạo tài khoản: " + Escape(bank.DisplayName)
			return plan, nil
		}
		bank, err := r.banks.Update(ctx, message.From.ID, session, app.UpdateBankAccountInput{
			BankAccountID: payload.BankAccountID, ExpectedRecord: payload.RecordVersion,
			BankAccountInput: input, Meta: meta,
		})
		r.observeBankMutation("update", err)
		if err != nil {
			return responsePlan{}, err
		}
		plan.text = "Đã cập nhật tài khoản: " + Escape(bank.DisplayName)
		return plan, nil
	default:
		return responsePlan{}, app.ErrStaleVersion
	}
	next, err := r.admins.AdvanceSession(ctx, message.From.ID, session, session.State, payload, meta)
	r.observeSession("advance", err)
	if err != nil {
		return responsePlan{}, err
	}
	plan.text, plan.keyboard = nextPrompt, cancelKeyboard(next)
	return plan, nil
}

func (r *Router) handleProductSessionText(ctx context.Context, message *models.Message, session app.AdminSession, payload workflowPayload, meta app.RequestMeta, plan responsePlan) (responsePlan, error) {
	text := strings.TrimSpace(message.Text)
	if session.State == app.SessionProductEdit && payload.Step == "category" {
		categoryID, err := strconv.ParseInt(text, 10, 64)
		if err != nil || categoryID <= 0 {
			return responsePlan{}, app.ErrInvalidInput
		}
		product, err := r.admins.UpdateProduct(ctx, message.From.ID, session, app.UpdateProductInput{
			ProductID: payload.ProductID, ExpectedRecord: payload.RecordVersion,
			CategoryID: categoryID, Name: payload.Name, Description: payload.Description,
			Price: domain.Money(payload.PriceVND), Meta: meta,
		})
		r.observeMutation("product.update", err)
		if err != nil {
			return responsePlan{}, err
		}
		plan.text = "Đã cập nhật sản phẩm: " + Escape(product.Name)
		return plan, nil
	}
	nextPrompt := ""
	switch payload.Step {
	case "category":
		categoryID, err := strconv.ParseInt(text, 10, 64)
		if err != nil || categoryID <= 0 {
			return responsePlan{}, app.ErrInvalidInput
		}
		payload.CategoryID, payload.Step, nextPrompt = categoryID, "name", "Gửi tên sản phẩm."
	case "name":
		if text == "" || len([]rune(text)) > 160 {
			return responsePlan{}, app.ErrInvalidInput
		}
		payload.Name, payload.Step, nextPrompt = text, "description", "Gửi mô tả, hoặc dấu - để bỏ trống."
	case "description":
		if len([]rune(text)) > 2000 {
			return responsePlan{}, app.ErrInvalidInput
		}
		if text != "-" {
			payload.Description = text
		}
		payload.Step, nextPrompt = "price", "Gửi giá VND dạng số nguyên."
	case "price":
		price, err := app.ParseMoneyInput(text)
		if err != nil {
			return responsePlan{}, err
		}
		payload.PriceVND = price.Int64()
		if session.State == app.SessionProductEdit {
			payload.Step, nextPrompt = "category", "Gửi ID danh mục mới."
		} else {
			product, err := r.admins.CreateProduct(ctx, message.From.ID, session, app.CreateProductInput{
				CategoryID: payload.CategoryID, Name: payload.Name,
				Slug: fmt.Sprintf("product-%d", meta.UpdateID), Description: payload.Description,
				Price: price, Meta: meta,
			})
			r.observeMutation("product.create", err)
			if err != nil {
				return responsePlan{}, err
			}
			plan.text = "Đã tạo sản phẩm: " + Escape(product.Name)
			return plan, nil
		}
	default:
		return responsePlan{}, app.ErrStaleVersion
	}
	next, err := r.admins.AdvanceSession(ctx, message.From.ID, session, session.State, payload, meta)
	r.observeSession("advance", err)
	if err != nil {
		return responsePlan{}, err
	}
	plan.text, plan.keyboard = nextPrompt, cancelKeyboard(next)
	return plan, nil
}

func (r *Router) executePlan(ctx context.Context, update *models.Update, plan responsePlan) {
	if plan.callbackID != "" {
		if err := r.messenger.AnswerCallback(ctx, AnswerCallbackRequest{
			CallbackID: plan.callbackID, Text: plan.callbackText, ShowAlert: plan.callbackAlert,
		}); err != nil {
			r.logSendFailure(ctx, update, "answerCallbackQuery", err)
		}
	}
	if plan.text == "" || plan.chatID == 0 {
		return
	}
	var err error
	method := "sendMessage"
	if plan.edit {
		method = "editMessageText"
		err = r.messenger.EditMessage(ctx, EditMessageRequest{
			ChatID: plan.chatID, MessageID: plan.messageID, Text: plan.text, Keyboard: plan.keyboard,
		})
	} else {
		err = r.messenger.SendMessage(ctx, SendMessageRequest{ChatID: plan.chatID, Text: plan.text, Keyboard: plan.keyboard})
	}
	if err != nil {
		r.logSendFailure(ctx, update, method, err)
	}
}

func (r *Router) answerDuplicateCallback(ctx context.Context, update *models.Update, text string) {
	if update == nil || update.CallbackQuery == nil || update.CallbackQuery.ID == "" {
		return
	}
	if err := r.messenger.AnswerCallback(ctx, AnswerCallbackRequest{CallbackID: update.CallbackQuery.ID, Text: text}); err != nil {
		r.logSendFailure(ctx, update, "answerCallbackQuery", err)
	}
}

func (r *Router) logSendFailure(ctx context.Context, update *models.Update, method string, err error) {
	r.logger.ErrorContext(ctx, "Telegram response failed after update commit",
		"telegram_update_id", update.ID, "telegram_update_type", ClassifyUpdate(update),
		"telegram_api_method", method, "result", "failed", "error", err)
}

func (r *Router) observeCatalog(operation string, err error) {
	if r.metrics != nil {
		r.metrics.ObserveCatalog(operation, metricResult(err))
	}
}

func (r *Router) observeMutation(operation string, err error) {
	if r.metrics != nil {
		r.metrics.ObserveAdminMutation(operation, metricResult(err))
	}
}

func (r *Router) observeSession(operation string, err error) {
	if r.metrics != nil {
		r.metrics.ObserveAdminSession(operation, metricResult(err))
	}
}

func (r *Router) observeOrder(operation string, err error, duration time.Duration) {
	if metrics, ok := r.metrics.(interface {
		ObserveOrder(string, string, time.Duration)
	}); ok {
		metrics.ObserveOrder(operation, metricResult(err), duration)
	}
}

func (r *Router) observeBankMutation(operation string, err error) {
	if metrics, ok := r.metrics.(interface {
		ObserveBankMutation(string, string)
	}); ok {
		metrics.ObserveBankMutation(operation, metricResult(err))
	}
}

func activeBankOption(items []app.BankAccountOption, id int64) (app.BankAccountOption, bool) {
	for _, item := range items {
		if item.ID == id {
			return item, true
		}
	}
	return app.BankAccountOption{}, false
}

func profileFromUser(user models.User) app.TelegramProfile {
	return app.TelegramProfile{
		TelegramUserID: user.ID, Username: user.Username,
		FirstName: user.FirstName, LastName: user.LastName,
	}
}

func callbackMessage(query *models.CallbackQuery) (int64, int) {
	if query == nil {
		return 0, 0
	}
	if query.Message.Message != nil {
		return query.Message.Message.Chat.ID, query.Message.Message.ID
	}
	if query.Message.InaccessibleMessage != nil {
		return query.Message.InaccessibleMessage.Chat.ID, query.Message.InaccessibleMessage.MessageID
	}
	return 0, 0
}

func cancelKeyboard(session app.AdminSession) Keyboard {
	return Keyboard{{{Text: "Hủy", Data: fmt.Sprintf("v1:a:x:%d:%d", session.ID, session.Version)}}}
}

func metricResult(err error) string {
	if err == nil {
		return "success"
	}
	return errorCode(err)
}

func errorCode(err error) string {
	switch {
	case errors.Is(err, app.ErrInvalidInput):
		return "invalid_input"
	case errors.Is(err, app.ErrInvalidInventoryPayload):
		return "invalid_input"
	case errors.Is(err, app.ErrImportLimitExceeded):
		return "limit_exceeded"
	case errors.Is(err, app.ErrInventoryNotFound):
		return "not_found"
	case errors.Is(err, app.ErrInvalidInventoryState), errors.Is(err, app.ErrInventoryUnavailable):
		return "stale"
	case errors.Is(err, app.ErrForbidden), errors.Is(err, app.ErrUnauthorized):
		return "forbidden"
	case errors.Is(err, app.ErrNotFound):
		return "not_found"
	case errors.Is(err, app.ErrSessionExpired):
		return "session_expired"
	case errors.Is(err, app.ErrStaleVersion):
		return "stale"
	case errors.Is(err, app.ErrConflict):
		return "conflict"
	case errors.Is(err, app.ErrInvalidQuantity), errors.Is(err, app.ErrQuantityLimitExceeded):
		return "invalid_quantity"
	case errors.Is(err, app.ErrInsufficientInventory):
		return "insufficient_inventory"
	case errors.Is(err, app.ErrOrderNotFound), errors.Is(err, app.ErrOrderNotOwned), errors.Is(err, app.ErrBankAccountNotFound):
		return "not_found"
	case errors.Is(err, app.ErrOrderExpired), errors.Is(err, app.ErrInvalidOrderState), errors.Is(err, app.ErrBankAccountInactive):
		return "stale"
	default:
		return "internal_error"
	}
}

func userError(err error) string {
	switch errorCode(err) {
	case "invalid_input":
		return "Dữ liệu không hợp lệ."
	case "forbidden":
		return "Bạn không có quyền thực hiện thao tác này."
	case "not_found":
		return "Không tìm thấy dữ liệu."
	case "session_expired":
		return "Phiên thao tác đã hết hạn."
	case "stale":
		return "Thao tác đã cũ, vui lòng mở lại menu."
	case "conflict":
		return "Dữ liệu đã tồn tại hoặc vừa thay đổi."
	case "limit_exceeded":
		return "Dữ liệu import vượt giới hạn cho phép."
	case "invalid_quantity":
		return "Số lượng không hợp lệ."
	case "insufficient_inventory":
		return "Sản phẩm hiện không đủ số lượng khả dụng."
	default:
		return "Có lỗi xảy ra, vui lòng thử lại."
	}
}

func boolBit(value bool) int {
	if value {
		return 1
	}
	return 0
}
