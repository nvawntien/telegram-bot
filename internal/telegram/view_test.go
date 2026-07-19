package telegram

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nvawntien/telegram-bot/internal/app"
	"github.com/nvawntien/telegram-bot/internal/domain"
)

func TestEscapeProtectsTelegramHTML(t *testing.T) {
	value := Escape(`<script>& "unsafe"`)
	if strings.Contains(value, "<script>") || !strings.Contains(value, "&lt;script&gt;") || !strings.Contains(value, "&amp;") {
		t.Fatalf("Escape() = %q", value)
	}
}

func TestInventoryViewsExposeOnlyRedactedMetadata(t *testing.T) {
	secretMarker := fmt.Sprintf("runtime-secret-%d", time.Now().UnixNano())
	page := app.RedactedInventoryPage{
		Items: []app.RedactedInventoryItem{{
			ID: 91, ProductID: 12, ProductName: "Product", Status: domain.InventoryStatusAvailable,
			KeyVersion: 2, Version: 4, CreatedAt: time.Now(),
		}},
		Page: app.PageInfo{PageSize: 8, TotalItems: 1, TotalPages: 1},
	}
	text, keyboard := AdminInventoryItemsView(12, page)
	joined := text
	for _, row := range keyboard {
		for _, button := range row {
			joined += button.Text + button.Data
			if len(button.Data) > MaxCallbackDataBytes {
				t.Fatalf("callback exceeds Telegram limit: %q", button.Data)
			}
		}
	}
	for _, prohibited := range []string{secretMarker, "ciphertext", "fingerprint", "nonce"} {
		if strings.Contains(joined, prohibited) {
			t.Fatalf("redacted view contains prohibited data %q", prohibited)
		}
	}
	if !strings.Contains(text, "#91") || !strings.Contains(text, "available") {
		t.Fatalf("redacted view omitted safe metadata: %q", text)
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
