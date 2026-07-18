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
	updates *app.UpdateService,
	messenger Messenger,
	supportContact string,
	logger *slog.Logger,
	metrics RouterMetrics,
) *Router {
	return &Router{
		users: users, catalog: catalog, admins: admins, updates: updates,
		messenger: messenger, supportContact: supportContact, logger: logger, metrics: metrics,
	}
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
	if strings.TrimSpace(message.Text) != "" {
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
	Step          string `json:"step"`
	CategoryID    int64  `json:"category_id,omitempty"`
	ProductID     int64  `json:"product_id,omitempty"`
	RecordVersion int64  `json:"record_version,omitempty"`
	Name          string `json:"name,omitempty"`
	Description   string `json:"description,omitempty"`
	SortOrder     int32  `json:"sort_order,omitempty"`
	PriceVND      int64  `json:"price_vnd,omitempty"`
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
	default:
		return responsePlan{}, app.ErrStaleVersion
	}
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
