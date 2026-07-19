package telegram

import (
	"fmt"
	"html"
	"strconv"
	"strings"

	"github.com/nvawntien/telegram-bot/internal/app"
	"github.com/nvawntien/telegram-bot/internal/domain"
)

func Escape(value string) string {
	return html.EscapeString(value)
}

func MainMenuText(name string) string {
	if strings.TrimSpace(name) == "" {
		name = "bạn"
	}
	return fmt.Sprintf("Xin chào <b>%s</b>!\nChọn một mục bên dưới:", Escape(name))
}

func MainMenuKeyboard() Keyboard {
	return Keyboard{
		{{Text: "📦 Sản phẩm", Data: "v1:c:0"}},
		{{Text: "🆘 Hỗ trợ", Data: "v1:s"}},
	}
}

func CategoriesView(page app.CategoryPage) (string, Keyboard) {
	rows := make(Keyboard, 0, len(page.Items)+2)
	for _, category := range page.Items {
		rows = append(rows, []Button{{
			Text: category.Emoji + " " + category.Name,
			Data: fmt.Sprintf("v1:p:%d:0", category.ID),
		}})
	}
	rows = append(rows, navigationRows("v1:c:%d", page.Page)...)
	rows = append(rows, []Button{{Text: "⬅️ Menu", Data: "v1:m"}})
	return "<b>Danh mục sản phẩm</b>", rows
}

func ProductsView(categoryID int64, page app.ProductPage) (string, Keyboard) {
	rows := make(Keyboard, 0, len(page.Items)+2)
	for _, product := range page.Items {
		rows = append(rows, []Button{{
			Text: fmt.Sprintf("%s — %s ₫", product.Name, formatVND(product.Price.Int64())),
			Data: fmt.Sprintf("v1:d:%d:%d:%d", product.ID, categoryID, page.Page.Page),
		}})
	}
	rows = append(rows, navigationRows(fmt.Sprintf("v1:p:%d:%%d", categoryID), page.Page)...)
	rows = append(rows, []Button{{Text: "⬅️ Danh mục", Data: "v1:c:0"}})
	return "<b>Sản phẩm</b>", rows
}

func ProductView(product app.Product, categoryID int64, page int) (string, Keyboard) {
	description := Escape(product.Description)
	if description == "" {
		description = "Chưa có mô tả."
	}
	text := fmt.Sprintf("<b>%s</b>\n%s\n\nGiá: <b>%s ₫</b>", Escape(product.Name), description, formatVND(product.Price.Int64()))
	return text, Keyboard{{{Text: "⬅️ Sản phẩm", Data: fmt.Sprintf("v1:p:%d:%d", categoryID, page)}}}
}

func AdminMenu() (string, Keyboard) {
	return "<b>Quản trị cửa hàng</b>", Keyboard{
		{{Text: "Danh mục", Data: "v1:a:c:0"}},
		{{Text: "Sản phẩm", Data: "v1:a:p:0"}},
		{{Text: "Kho mã hóa", Data: "v1:a:i:0"}},
	}
}

func AdminInventoryOverviewView(page app.InventoryOverviewPage) (string, Keyboard) {
	rows := make(Keyboard, 0, len(page.Items)+1)
	lines := []string{"<b>Tổng quan kho mã hóa</b>"}
	for _, item := range page.Items {
		lines = append(lines, fmt.Sprintf(
			"\n<b>%s</b> (#%d)\nCó sẵn: %d · Đã giữ: %d · Đã bán: %d · Tắt: %d · Tổng: %d",
			Escape(item.ProductName), item.ProductID, item.AvailableCount,
			item.ReservedCount, item.SoldCount, item.DisabledCount, item.TotalCount,
		))
		rows = append(rows, []Button{{
			Text: fmt.Sprintf("%s (%d/%d)", item.ProductName, item.AvailableCount, item.TotalCount),
			Data: fmt.Sprintf("v1:a:il:%d:0", item.ProductID),
		}})
	}
	if len(page.Items) == 0 {
		lines = append(lines, "\nChưa có sản phẩm inventory.")
	}
	rows = append(rows, navigationRows("v1:a:i:%d", page.Page)...)
	return strings.Join(lines, "\n"), rows
}

func AdminInventoryItemsView(productID int64, page app.RedactedInventoryPage) (string, Keyboard) {
	rows := Keyboard{{{Text: "➕ Import stock", Data: fmt.Sprintf("v1:a:ii:%d", productID)}}}
	lines := []string{fmt.Sprintf("<b>Inventory đã redacted</b> · sản phẩm #%d", productID)}
	for _, item := range page.Items {
		line := fmt.Sprintf("#%d · %s · key v%d · version %d", item.ID, Escape(string(item.Status)), item.KeyVersion, item.Version)
		if !item.CreatedAt.IsZero() {
			line += " · " + item.CreatedAt.UTC().Format("2006-01-02")
		}
		if item.ReservedOrderID > 0 {
			line += fmt.Sprintf(" · order #%d", item.ReservedOrderID)
		}
		if !item.ReservedUntil.IsZero() {
			line += " đến " + item.ReservedUntil.UTC().Format("2006-01-02 15:04Z")
		}
		lines = append(lines, line)
		switch item.Status {
		case domain.InventoryStatusAvailable:
			rows = append(rows, []Button{{Text: fmt.Sprintf("Tắt #%d", item.ID), Data: fmt.Sprintf("v1:a:is:%d:%d:0", item.ID, item.Version)}})
		case domain.InventoryStatusDisabled:
			rows = append(rows, []Button{{Text: fmt.Sprintf("Bật #%d", item.ID), Data: fmt.Sprintf("v1:a:is:%d:%d:1", item.ID, item.Version)}})
		}
	}
	if len(page.Items) == 0 {
		lines = append(lines, "Chưa có item.")
	}
	rows = append(rows, navigationRows(fmt.Sprintf("v1:a:il:%d:%%d", productID), page.Page)...)
	rows = append(rows, []Button{{Text: "⬅️ Tổng quan", Data: "v1:a:i:0"}})
	return strings.Join(lines, "\n"), rows
}

func AdminCategoriesView(page app.CategoryPage) (string, Keyboard) {
	rows := Keyboard{{{Text: "➕ Tạo danh mục", Data: "v1:a:cn"}}}
	for _, category := range page.Items {
		status := "🟢"
		target := 0
		if !category.Active {
			status = "⚪"
			target = 1
		}
		rows = append(rows, []Button{
			{Text: status + " " + category.Name, Data: fmt.Sprintf("v1:a:ce:%d:%d", category.ID, category.Version)},
			{Text: "Bật/tắt", Data: fmt.Sprintf("v1:a:ca:%d:%d:%d", category.ID, category.Version, target)},
		})
	}
	rows = append(rows, navigationRows("v1:a:c:%d", page.Page)...)
	return "<b>Danh mục quản trị</b>", rows
}

func AdminProductsView(page app.ProductPage) (string, Keyboard) {
	rows := Keyboard{{{Text: "➕ Tạo sản phẩm", Data: "v1:a:pn"}}}
	for _, product := range page.Items {
		status := "🟢"
		target := 0
		if !product.Active {
			status = "⚪"
			target = 1
		}
		rows = append(rows, []Button{
			{Text: status + " " + product.Name, Data: fmt.Sprintf("v1:a:pe:%d:%d", product.ID, product.Version)},
			{Text: "Bật/tắt", Data: fmt.Sprintf("v1:a:pa:%d:%d:%d", product.ID, product.Version, target)},
		})
	}
	rows = append(rows, navigationRows("v1:a:p:%d", page.Page)...)
	return "<b>Sản phẩm quản trị</b>", rows
}

func navigationRows(format string, page app.PageInfo) Keyboard {
	row := make([]Button, 0, 2)
	if page.Page > 0 {
		row = append(row, Button{Text: "⬅️", Data: fmt.Sprintf(format, page.Page-1)})
	}
	if page.TotalPages > 0 && page.Page+1 < page.TotalPages {
		row = append(row, Button{Text: "➡️", Data: fmt.Sprintf(format, page.Page+1)})
	}
	if len(row) == 0 {
		return nil
	}
	return Keyboard{row}
}

func formatVND(value int64) string {
	raw := strconv.FormatInt(value, 10)
	for index := len(raw) - 3; index > 0; index -= 3 {
		raw = raw[:index] + "." + raw[index:]
	}
	return raw
}
