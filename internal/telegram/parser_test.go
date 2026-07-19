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
		{data: "v1:o:q:42:2", action: CallbackOrderQuantity},
		{data: "v1:o:b:42:2:7", action: CallbackOrderBank},
		{data: "v1:o:c:91:42:2:7", action: CallbackOrderConfirm},
		{data: "v1:o:l:0", action: CallbackOrders},
		{data: "v1:o:v:11", action: CallbackOrderView},
		{data: "v1:o:x:11:2", action: CallbackOrderAskCancel},
		{data: "v1:o:k:11:2", action: CallbackOrderCancel},
		{data: "v1:o:w:11", action: CallbackOrderWalletAsk},
		{data: "v1:o:y:91:11", action: CallbackOrderWalletPay},
		{data: "v1:w:v", action: CallbackWalletBalance},
		{data: "v1:w:a:50000", action: CallbackWalletTopupAmount},
		{data: "v1:w:b:91:50000:7", action: CallbackWalletTopupBank},
		{data: "v1:a:ce:3:2", action: CallbackAdminCategoryEdit},
		{data: "v1:a:ca:3:2:0", action: CallbackAdminCategoryAskToggle},
		{data: "v1:a:pt:1:2:3:4:1", action: CallbackAdminProductToggle},
		{data: "v1:a:i:0", action: CallbackAdminInventory},
		{data: "v1:a:il:12:3", action: CallbackAdminInventoryList},
		{data: "v1:a:ii:12", action: CallbackAdminInventoryImport},
		{data: "v1:a:is:42:3:0", action: CallbackAdminInventoryAskToggle},
		{data: "v1:a:it:1:2:42:3:1", action: CallbackAdminInventoryToggle},
		{data: "v1:a:b:0", action: CallbackAdminBanks},
		{data: "v1:a:bn", action: CallbackAdminBankNew},
		{data: "v1:a:be:7:2", action: CallbackAdminBankEdit},
		{data: "v1:a:ba:7:2:0", action: CallbackAdminBankAskToggle},
		{data: "v1:a:bt:1:2:7:3:1", action: CallbackAdminBankToggle},
		{data: "v1:a:pr:0", action: CallbackAdminPaymentReviews},
		{data: "v1:a:pm", action: CallbackAdminPaymentManual},
		{data: "v1:a:rr:9", action: CallbackAdminPaymentResolve},
		{data: "v1:a:wa", action: CallbackAdminWalletAdjustment},
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
		"v1:a:il:0:1", "v1:a:is:1:0:1", "v1:a:it:1:2:3:4:2",
		"v1:o:q:1:-1", "v1:o:c:1:2:0:3", "v1:o:v:0", "v1:o:k:1:0",
		"v1:a:be:1:0", "v1:a:bt:1:2:3:4:2",
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
