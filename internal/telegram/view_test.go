package telegram

import (
	"strings"
	"testing"

	"github.com/nvawntien/telegram-bot/internal/app"
	"github.com/nvawntien/telegram-bot/internal/domain"
)

func TestEscapeProtectsTelegramHTML(t *testing.T) {
	value := Escape(`<script>& "unsafe"`)
	if strings.Contains(value, "<script>") || !strings.Contains(value, "&lt;script&gt;") || !strings.Contains(value, "&amp;") {
		t.Fatalf("Escape() = %q", value)
	}
}

func TestViewsKeepPaginationContextAndCallbackLimits(t *testing.T) {
	page := app.ProductPage{
		Items: []app.Product{{ID: 20, CategoryID: 10, Name: "<unsafe>", Price: domain.Money(120_000)}},
		Page:  app.PageInfo{Page: 1, PageSize: 8, TotalItems: 24, TotalPages: 3},
	}
	text, keyboard := ProductsView(10, page)
	if !strings.Contains(text, "Sản phẩm") || len(keyboard) < 3 {
		t.Fatalf("ProductsView() = %q, %#v", text, keyboard)
	}
	for _, row := range keyboard {
		for _, button := range row {
			if len(button.Data) > MaxCallbackDataBytes {
				t.Fatalf("callback exceeds Telegram limit: %q", button.Data)
			}
		}
	}
}
