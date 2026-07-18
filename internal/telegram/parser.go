package telegram

import (
	"errors"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/go-telegram/bot/models"
)

const MaxCallbackDataBytes = 64

var ErrInvalidCallback = errors.New("invalid callback data")

type Command struct {
	Name    string
	Payload string
}

func ParseCommand(text string) (Command, bool) {
	fields := strings.Fields(strings.TrimSpace(text))
	if len(fields) == 0 || !strings.HasPrefix(fields[0], "/") {
		return Command{}, false
	}
	name := strings.TrimPrefix(fields[0], "/")
	if index := strings.IndexByte(name, '@'); index >= 0 {
		name = name[:index]
	}
	name = strings.ToLower(name)
	if name == "" {
		return Command{}, false
	}
	command := Command{Name: name}
	if len(fields) > 1 {
		command.Payload = fields[1]
	}
	return command, true
}

type CallbackAction string

const (
	CallbackMenu                   CallbackAction = "menu"
	CallbackSupport                CallbackAction = "support"
	CallbackCategories             CallbackAction = "categories"
	CallbackProducts               CallbackAction = "products"
	CallbackProductDetail          CallbackAction = "product_detail"
	CallbackAdminCategories        CallbackAction = "admin_categories"
	CallbackAdminProducts          CallbackAction = "admin_products"
	CallbackAdminCategoryNew       CallbackAction = "admin_category_new"
	CallbackAdminCategoryEdit      CallbackAction = "admin_category_edit"
	CallbackAdminCategoryAskToggle CallbackAction = "admin_category_ask_toggle"
	CallbackAdminCategoryToggle    CallbackAction = "admin_category_toggle"
	CallbackAdminProductNew        CallbackAction = "admin_product_new"
	CallbackAdminProductEdit       CallbackAction = "admin_product_edit"
	CallbackAdminProductAskToggle  CallbackAction = "admin_product_ask_toggle"
	CallbackAdminProductToggle     CallbackAction = "admin_product_toggle"
	CallbackAdminCancel            CallbackAction = "admin_cancel"
)

type Callback struct {
	Action         CallbackAction
	Page           int
	CategoryID     int64
	ProductID      int64
	RecordVersion  int64
	SessionID      int64
	SessionVersion int64
	Active         bool
}

func ParseCallback(data string) (Callback, error) {
	if data == "" || len(data) > MaxCallbackDataBytes || !utf8.ValidString(data) {
		return Callback{}, ErrInvalidCallback
	}
	parts := strings.Split(data, ":")
	if len(parts) < 2 || parts[0] != "v1" {
		return Callback{}, ErrInvalidCallback
	}
	switch parts[1] {
	case "m":
		return exact(parts, 2, Callback{Action: CallbackMenu})
	case "s":
		return exact(parts, 2, Callback{Action: CallbackSupport})
	case "c":
		page, err := parseNonNegative(parts, 2, 3)
		return Callback{Action: CallbackCategories, Page: int(page)}, err
	case "p":
		if len(parts) != 4 {
			return Callback{}, ErrInvalidCallback
		}
		categoryID, err := positive(parts[2])
		if err != nil {
			return Callback{}, err
		}
		page, err := nonNegative(parts[3])
		return Callback{Action: CallbackProducts, CategoryID: categoryID, Page: int(page)}, err
	case "d":
		if len(parts) != 5 {
			return Callback{}, ErrInvalidCallback
		}
		productID, err := positive(parts[2])
		if err != nil {
			return Callback{}, err
		}
		categoryID, err := positive(parts[3])
		if err != nil {
			return Callback{}, err
		}
		page, err := nonNegative(parts[4])
		return Callback{Action: CallbackProductDetail, ProductID: productID, CategoryID: categoryID, Page: int(page)}, err
	case "a":
		return parseAdminCallback(parts)
	default:
		return Callback{}, ErrInvalidCallback
	}
}

func parseAdminCallback(parts []string) (Callback, error) {
	if len(parts) < 3 {
		return Callback{}, ErrInvalidCallback
	}
	switch parts[2] {
	case "c", "p":
		page, err := parseNonNegative(parts, 3, 4)
		action := CallbackAdminCategories
		if parts[2] == "p" {
			action = CallbackAdminProducts
		}
		return Callback{Action: action, Page: int(page)}, err
	case "cn":
		return exact(parts, 3, Callback{Action: CallbackAdminCategoryNew})
	case "pn":
		return exact(parts, 3, Callback{Action: CallbackAdminProductNew})
	case "ce", "pe":
		if len(parts) != 5 {
			return Callback{}, ErrInvalidCallback
		}
		id, err := positive(parts[3])
		if err != nil {
			return Callback{}, err
		}
		version, err := positive(parts[4])
		if err != nil {
			return Callback{}, err
		}
		callback := Callback{RecordVersion: version}
		switch parts[2] {
		case "ce":
			callback.Action, callback.CategoryID = CallbackAdminCategoryEdit, id
		case "pe":
			callback.Action, callback.ProductID = CallbackAdminProductEdit, id
		}
		return callback, nil
	case "ca", "pa":
		if len(parts) != 6 {
			return Callback{}, ErrInvalidCallback
		}
		id, err := positive(parts[3])
		if err != nil {
			return Callback{}, err
		}
		version, err := positive(parts[4])
		if err != nil {
			return Callback{}, err
		}
		active, err := parseBoolBit(parts[5])
		callback := Callback{RecordVersion: version, Active: active}
		if parts[2] == "ca" {
			callback.Action, callback.CategoryID = CallbackAdminCategoryAskToggle, id
		} else {
			callback.Action, callback.ProductID = CallbackAdminProductAskToggle, id
		}
		return callback, err
	case "ct", "pt":
		if len(parts) != 8 {
			return Callback{}, ErrInvalidCallback
		}
		sessionID, err := positive(parts[3])
		if err != nil {
			return Callback{}, err
		}
		sessionVersion, err := positive(parts[4])
		if err != nil {
			return Callback{}, err
		}
		resourceID, err := positive(parts[5])
		if err != nil {
			return Callback{}, err
		}
		recordVersion, err := positive(parts[6])
		if err != nil {
			return Callback{}, err
		}
		active, err := parseBoolBit(parts[7])
		callback := Callback{SessionID: sessionID, SessionVersion: sessionVersion, RecordVersion: recordVersion, Active: active}
		if parts[2] == "ct" {
			callback.Action, callback.CategoryID = CallbackAdminCategoryToggle, resourceID
		} else {
			callback.Action, callback.ProductID = CallbackAdminProductToggle, resourceID
		}
		return callback, err
	case "x":
		if len(parts) != 5 {
			return Callback{}, ErrInvalidCallback
		}
		sessionID, err := positive(parts[3])
		if err != nil {
			return Callback{}, err
		}
		version, err := positive(parts[4])
		return Callback{Action: CallbackAdminCancel, SessionID: sessionID, SessionVersion: version}, err
	default:
		return Callback{}, ErrInvalidCallback
	}
}

func ClassifyUpdate(update *models.Update) string {
	switch {
	case update == nil:
		return "unknown"
	case update.Message != nil:
		return "message"
	case update.CallbackQuery != nil:
		return "callback_query"
	case update.EditedMessage != nil:
		return "edited_message"
	default:
		return "unknown"
	}
}

func exact(parts []string, count int, callback Callback) (Callback, error) {
	if len(parts) != count {
		return Callback{}, ErrInvalidCallback
	}
	return callback, nil
}

func parseNonNegative(parts []string, index, count int) (int64, error) {
	if len(parts) != count {
		return 0, ErrInvalidCallback
	}
	return nonNegative(parts[index])
}

func positive(raw string) (int64, error) {
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value <= 0 {
		return 0, ErrInvalidCallback
	}
	return value, nil
}

func nonNegative(raw string) (int64, error) {
	value, err := strconv.ParseInt(raw, 10, 32)
	if err != nil || value < 0 {
		return 0, ErrInvalidCallback
	}
	return value, nil
}

func parseBoolBit(raw string) (bool, error) {
	switch raw {
	case "0":
		return false, nil
	case "1":
		return true, nil
	default:
		return false, ErrInvalidCallback
	}
}
