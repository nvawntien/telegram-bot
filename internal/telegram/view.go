package telegram

import (
	"fmt"
	"html"
	"strconv"
	"strings"

	"github.com/nvawntien/telegram-bot/internal/app"
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
	return "<b>Quản trị catalog</b>", Keyboard{
		{{Text: "Danh mục", Data: "v1:a:c:0"}},
		{{Text: "Sản phẩm", Data: "v1:a:p:0"}},
	}
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
