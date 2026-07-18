package telegram

import (
	"errors"
	"strings"
	"testing"

	"github.com/go-telegram/bot/models"
)

func TestParseCommand(t *testing.T) {
	command, ok := ParseCommand(" /start@shop_bot ignored-payload ")
	if !ok || command.Name != "start" || command.Payload != "ignored-payload" {
		t.Fatalf("ParseCommand() = %#v, %t", command, ok)
	}
	for _, value := range []string{"", "hello", "/"} {
		if _, ok := ParseCommand(value); ok {
			t.Errorf("ParseCommand(%q) unexpectedly succeeded", value)
		}
	}
}

func TestParseCallback(t *testing.T) {
	tests := []struct {
		data   string
		action CallbackAction
	}{
		{data: "v1:m", action: CallbackMenu},
		{data: "v1:s", action: CallbackSupport},
		{data: "v1:c:0", action: CallbackCategories},
		{data: "v1:p:12:3", action: CallbackProducts},
		{data: "v1:d:42:12:3", action: CallbackProductDetail},
		{data: "v1:a:ce:3:2", action: CallbackAdminCategoryEdit},
		{data: "v1:a:ca:3:2:0", action: CallbackAdminCategoryAskToggle},
		{data: "v1:a:pt:1:2:3:4:1", action: CallbackAdminProductToggle},
	}
	for _, test := range tests {
		callback, err := ParseCallback(test.data)
		if err != nil || callback.Action != test.action {
			t.Errorf("ParseCallback(%q) = %#v, %v", test.data, callback, err)
		}
	}
}

func TestParseCallbackRejectsMalformedData(t *testing.T) {
	values := []string{
		"", "v2:m", "v1:unknown", "v1:c:-1", "v1:p:0:1", "v1:p:not-id:0",
		"v1:m:extra", "v1:d:1:2", "v1:a:ct:1:2:3:4:2", "v1:a:ce:1",
		strings.Repeat("x", MaxCallbackDataBytes+1), string([]byte{0xff}),
	}
	for _, value := range values {
		if _, err := ParseCallback(value); !errors.Is(err, ErrInvalidCallback) {
			t.Errorf("ParseCallback(%q) error = %v", value, err)
		}
	}
}

func TestClassifyUpdate(t *testing.T) {
	tests := []struct {
		update *models.Update
		want   string
	}{
		{update: nil, want: "unknown"},
		{update: &models.Update{Message: &models.Message{}}, want: "message"},
		{update: &models.Update{CallbackQuery: &models.CallbackQuery{}}, want: "callback_query"},
		{update: &models.Update{}, want: "unknown"},
	}
	for _, test := range tests {
		if got := ClassifyUpdate(test.update); got != test.want {
			t.Errorf("ClassifyUpdate() = %s, want %s", got, test.want)
		}
	}
}
