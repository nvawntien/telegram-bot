package telegram

import (
	"fmt"
	"html"
	"strconv"
	"strings"
	"time"

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
		{{Text: "🧾 Đơn hàng", Data: "v1:o:l:0"}},
		{{Text: "💰 Số dư", Data: "v1:w:v"}},
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
	return text, Keyboard{
		{{Text: "Mua 1", Data: fmt.Sprintf("v1:o:q:%d:1", product.ID)}, {Text: "Mua 2", Data: fmt.Sprintf("v1:o:q:%d:2", product.ID)}, {Text: "Mua 5", Data: fmt.Sprintf("v1:o:q:%d:5", product.ID)}},
		{{Text: "⬅️ Sản phẩm", Data: fmt.Sprintf("v1:p:%d:%d", categoryID, page)}},
	}
}

func BankSelectionView(productID int64, quantity int32, banks []app.BankAccountOption) (string, Keyboard) {
	rows := make(Keyboard, 0, len(banks)+1)
	for _, bank := range banks {
		rows = append(rows, []Button{{
			Text: fmt.Sprintf("%s · ••••%s", bank.DisplayName, bank.Last4),
			Data: fmt.Sprintf("v1:o:b:%d:%d:%d", productID, quantity, bank.ID),
		}})
	}
	rows = append(rows, []Button{{Text: "⬅️ Sản phẩm", Data: "v1:c:0"}})
	return fmt.Sprintf("<b>Chọn tài khoản nhận</b>\nSố lượng: %d", quantity), rows
}

func OrderConfirmView(flowID, productID int64, quantity int32, bank app.BankAccountOption) (string, Keyboard) {
	text := fmt.Sprintf(
		"<b>Xác nhận tạo đơn</b>\nSản phẩm #%d · số lượng %d\nNgân hàng: %s · ••••%s\n\nGiá và trạng thái sẽ được kiểm tra lại trong giao dịch tạo đơn.",
		productID, quantity, Escape(bank.DisplayName), Escape(bank.Last4),
	)
	return text, Keyboard{{{Text: "Xác nhận", Data: fmt.Sprintf("v1:o:c:%d:%d:%d:%d", flowID, productID, quantity, bank.ID)}}}
}

func PaymentInstructionView(order app.OrderDetail, instruction app.PaymentInstruction) (string, Keyboard) {
	text := fmt.Sprintf(
		"<b>Hướng dẫn chuyển khoản · đơn #%d</b>\nTrạng thái: <b>đang chờ thanh toán</b>\nNgân hàng: %s\nSố tài khoản: <code>%s</code>\nTên tài khoản: <b>%s</b>\nSố tiền: <b>%s ₫</b>\nNội dung: <code>%s</code>\nHết hạn: %s\n\nChỉ chuyển đúng số tiền và nội dung. QR là hướng dẫn, không chứng minh giao dịch đã được thanh toán.",
		order.ID, Escape(instruction.BankDisplayName), Escape(instruction.AccountNumber),
		Escape(instruction.AccountName), formatVND(instruction.Amount.Int64()), Escape(instruction.TransferContent),
		instruction.ExpiresAt.Local().Format("02/01/2006 15:04:05"),
	)
	return text, Keyboard{
		{{Text: "Mở VietQR", URL: instruction.ImageURL}},
		{{Text: "Xem đơn", Data: fmt.Sprintf("v1:o:v:%d", order.ID)}},
		{{Text: "Danh sách đơn", Data: "v1:o:l:0"}},
	}
}

func OrdersView(page app.OrderPage) (string, Keyboard) {
	lines := []string{"<b>Đơn hàng của bạn</b>"}
	rows := make(Keyboard, 0, len(page.Items)+2)
	for _, order := range page.Items {
		lines = append(lines, fmt.Sprintf("#%d · %s · %dx %s · %s ₫ · %s", order.ID, Escape(deliveryStatusLabel(order.Status, order.DeliveryStatus)), order.Quantity, Escape(order.ProductName), formatVND(order.Total.Int64()), order.CreatedAt.Local().Format("02/01 15:04")))
		rows = append(rows, []Button{{Text: fmt.Sprintf("Đơn #%d", order.ID), Data: fmt.Sprintf("v1:o:v:%d", order.ID)}})
	}
	if len(page.Items) == 0 {
		lines = append(lines, "Chưa có đơn hàng.")
	}
	rows = append(rows, navigationRows("v1:o:l:%d", page.Page)...)
	rows = append(rows, []Button{{Text: "⬅️ Menu", Data: "v1:m"}})
	return strings.Join(lines, "\n"), rows
}

func OrderDetailView(order app.OrderDetail, instruction app.PaymentInstruction) (string, Keyboard) {
	text := fmt.Sprintf(
		"<b>Đơn #%d</b>\n%s · số lượng %d\nĐơn giá: %s ₫\nTổng: <b>%s ₫</b>\nTrạng thái: <b>%s</b>\nMã chuyển khoản: <code>%s</code>\nHết hạn: %s",
		order.ID, Escape(order.Item.Name), order.Item.Quantity, formatVND(order.Item.UnitPrice.Int64()),
		formatVND(order.Total.Int64()), Escape(deliveryStatusLabel(order.Status, order.DeliveryStatus)), Escape(order.PaymentReference),
		order.ExpiresAt.Local().Format("02/01/2006 15:04:05"),
	)
	rows := Keyboard{}
	if order.Status == domain.OrderStatusPendingPayment && instruction.ImageURL != "" {
		rows = append(rows, []Button{{Text: "Mở VietQR", URL: instruction.ImageURL}})
		rows = append(rows, []Button{{Text: "Thanh toán bằng ví", Data: fmt.Sprintf("v1:o:w:%d", order.ID)}})
		rows = append(rows, []Button{{Text: "Hủy đơn", Data: fmt.Sprintf("v1:o:x:%d:%d", order.ID, order.Version)}})
	}
	rows = append(rows, []Button{{Text: "⬅️ Danh sách", Data: "v1:o:l:0"}})
	return text, rows
}

func WalletBalanceView(account app.WalletAccount) (string, Keyboard) {
	return fmt.Sprintf("<b>Số dư ví</b>\n%s ₫", formatVND(account.Balance.Int64())), Keyboard{
		{{Text: "Nạp 50.000 ₫", Data: "v1:w:a:50000"}, {Text: "Nạp 100.000 ₫", Data: "v1:w:a:100000"}},
		{{Text: "Nạp 500.000 ₫", Data: "v1:w:a:500000"}},
		{{Text: "⬅️ Menu", Data: "v1:m"}},
	}
}

func WalletTopupBankView(flowID, amount int64, banks []app.BankAccountOption) (string, Keyboard) {
	rows := make(Keyboard, 0, len(banks)+1)
	for _, bank := range banks {
		rows = append(rows, []Button{{Text: bank.DisplayName + " · ••••" + bank.Last4, Data: fmt.Sprintf("v1:w:b:%d:%d:%d", flowID, amount, bank.ID)}})
	}
	rows = append(rows, []Button{{Text: "⬅️ Ví", Data: "v1:w:a:50000"}})
	return fmt.Sprintf("<b>Chọn tài khoản nhận</b>\nSố tiền nạp: %s ₫", formatVND(amount)), rows
}

func WalletTopupInstructionView(topup app.WalletTopup, instruction app.PaymentInstruction) (string, Keyboard) {
	text := fmt.Sprintf("<b>Hướng dẫn nạp ví</b>\nNgân hàng: %s\nSố tài khoản: <code>%s</code>\nTên tài khoản: <b>%s</b>\nSố tiền: <b>%s ₫</b>\nNội dung: <code>%s</code>\nHết hạn: %s\n\nQR chỉ là hướng dẫn. Ví chỉ được cộng sau khi payment được chấp nhận.", Escape(instruction.BankDisplayName), Escape(instruction.AccountNumber), Escape(instruction.AccountName), formatVND(topup.Amount.Int64()), Escape(topup.PaymentReference), topup.ExpiresAt.Local().Format("02/01/2006 15:04:05"))
	return text, Keyboard{{{Text: "Mở VietQR", URL: instruction.ImageURL}}, {{Text: "Xem số dư", Data: "v1:w:v"}}}
}

func WalletOrderConfirmationView(order app.OrderDetail, balance app.WalletAccount, flowID int64) (string, Keyboard) {
	return fmt.Sprintf("Xác nhận dùng ví thanh toán đơn #%d?\nTổng: %s ₫\nSố dư: %s ₫", order.ID, formatVND(order.Total.Int64()), formatVND(balance.Balance.Int64())), Keyboard{{{Text: "Xác nhận", Data: fmt.Sprintf("v1:o:y:%d:%d", flowID, order.ID)}}, {{Text: "Quay lại", Data: fmt.Sprintf("v1:o:v:%d", order.ID)}}}
}

func OrderCancelConfirmationView(orderID, version int64) (string, Keyboard) {
	return fmt.Sprintf("Xác nhận hủy đơn #%d?", orderID), Keyboard{{{Text: "Xác nhận hủy", Data: fmt.Sprintf("v1:o:k:%d:%d", orderID, version)}}, {{Text: "Quay lại", Data: fmt.Sprintf("v1:o:v:%d", orderID)}}}
}

func AdminMenu() (string, Keyboard) {
	return "<b>Quản trị cửa hàng</b>", Keyboard{
		{{Text: "Danh mục", Data: "v1:a:c:0"}},
		{{Text: "Sản phẩm", Data: "v1:a:p:0"}},
		{{Text: "Kho mã hóa", Data: "v1:a:i:0"}},
		{{Text: "Tài khoản ngân hàng", Data: "v1:a:b:0"}},
		{{Text: "Payment reviews", Data: "v1:a:pr:0"}},
		{{Text: "Payment providers", Data: "v1:a:qh"}},
		{{Text: "Manual confirm", Data: "v1:a:pm"}},
		{{Text: "Điều chỉnh ví", Data: "v1:a:wa"}},
		{{Text: "Delivery queue", Data: "v1:a:dl:0"}},
		{{Text: "Delivery reconciliation", Data: "v1:a:dx"}},
	}
}

func PaymentProviderHealthView(items []app.PaymentProviderHealth) (string, Keyboard) {
	lines := []string{"<b>Payment provider health</b>"}
	for _, item := range items {
		capabilities := make([]string, 0, 2)
		if item.Capabilities.Webhook {
			capabilities = append(capabilities, "webhook")
		}
		if item.Capabilities.Reconciliation {
			capabilities = append(capabilities, "reconciliation")
		}
		line := fmt.Sprintf("\n<b>%s</b> · %s · %s\nMappings: %d · pending: %d · reviews: %d", Escape(item.Name), Escape(item.Environment), Escape(strings.Join(capabilities, "+")), item.ActiveMappings, item.PendingEvents, item.OpenReviews)
		if !item.LastWebhookAt.IsZero() {
			line += "\nLast webhook: " + item.LastWebhookAt.UTC().Format(time.RFC3339)
		}
		if !item.LastReconciliationSuccess.IsZero() {
			line += "\nLast reconciliation: " + item.LastReconciliationSuccess.UTC().Format(time.RFC3339)
		}
		if item.LastErrorCode != "" {
			line += "\nLast error: <code>" + Escape(item.LastErrorCode) + "</code>"
		}
		lines = append(lines, line)
	}
	if len(items) == 0 {
		lines = append(lines, "Chưa có provider được bật.")
	}
	return strings.Join(lines, "\n"), Keyboard{
		{{Text: "Account mappings", Data: "v1:a:ql:0"}},
		{{Text: "⬅️ Admin", Data: "v1:m"}},
	}
}

func PaymentProviderAccountsView(page app.PaymentProviderAccountPage) (string, Keyboard) {
	lines := []string{"<b>Provider account mappings</b>"}
	rows := Keyboard{{{Text: "➕ Link account", Data: "v1:a:qn"}}}
	for _, item := range page.Items {
		status, target := "🟢", 0
		if item.Status != "active" {
			status, target = "⚪", 1
		}
		lines = append(lines, fmt.Sprintf("%s #%d · %s/%s · %s → %s ••••%s · v%d", status, item.ID, Escape(item.Provider), Escape(item.Environment), Escape(item.MaskedExternalIdentity), Escape(item.LocalBankDisplayName), Escape(item.LocalBankLast4), item.Version))
		rows = append(rows, []Button{{Text: "Bật/tắt #" + strconv.FormatInt(item.ID, 10), Data: fmt.Sprintf("v1:a:qa:%d:%d:%d", item.ID, item.Version, target)}})
	}
	if len(page.Items) == 0 {
		lines = append(lines, "Chưa có mapping.")
	}
	rows = append(rows, navigationRows("v1:a:ql:%d", page.Page)...)
	rows = append(rows, []Button{{Text: "⬅️ Provider health", Data: "v1:a:qh"}})
	return strings.Join(lines, "\n"), rows
}

func DeliveryReviewsView(page app.DeliveryReviewPage) (string, Keyboard) {
	lines := []string{"<b>Delivery queue</b>"}
	rows := Keyboard{}
	for _, item := range page.Items {
		lines = append(lines, fmt.Sprintf("Job #%d · đơn #%d · %s · %d/%d attempts · %dx %s", item.ID, item.OrderID, Escape(item.Status), item.Attempts, item.MaxAttempts, item.Quantity, Escape(item.ProductName)))
		rows = append(rows, []Button{{Text: fmt.Sprintf("Delivery #%d", item.ID), Data: fmt.Sprintf("v1:a:dd:%d", item.ID)}})
	}
	if len(page.Items) == 0 {
		lines = append(lines, "Không có delivery job cần hiển thị.")
	}
	rows = append(rows, navigationRows("v1:a:dl:%d", page.Page)...)
	rows = append(rows, []Button{{Text: "⬅️ Admin", Data: "v1:m"}})
	return strings.Join(lines, "\n"), rows
}

func DeliveryDetailView(detail app.DeliveryReviewDetail) (string, Keyboard) {
	item := detail.DeliveryReviewItem
	lines := []string{
		fmt.Sprintf("<b>Delivery #%d</b>", item.ID),
		fmt.Sprintf("Đơn #%d · chat <code>%d</code>", item.OrderID, item.RecipientChatID),
		fmt.Sprintf("%dx %s", item.Quantity, Escape(item.ProductName)),
		fmt.Sprintf("Trạng thái: <b>%s</b> · attempt %d/%d · version %d", Escape(item.Status), item.Attempts, item.MaxAttempts, item.Version),
	}
	if item.TelegramMessageID > 0 {
		lines = append(lines, fmt.Sprintf("Telegram message ID: <code>%d</code>", item.TelegramMessageID))
	}
	if item.ErrorCode != "" {
		lines = append(lines, "Lỗi: <code>"+Escape(item.ErrorCode)+"</code> · "+Escape(item.ErrorDetail))
	}
	lines = append(lines, "", "<b>Attempt history</b>")
	for _, attempt := range detail.AttemptsHistory {
		line := fmt.Sprintf("#%d · %s", attempt.Number, Escape(attempt.Status))
		if attempt.ErrorCode != "" {
			line += " · " + Escape(attempt.ErrorCode)
		}
		if attempt.TelegramMessageID > 0 {
			line += fmt.Sprintf(" · message %d", attempt.TelegramMessageID)
		}
		lines = append(lines, line)
	}
	if len(detail.AttemptsHistory) == 0 {
		lines = append(lines, "Chưa có attempt.")
	}
	rows := Keyboard{}
	switch item.Status {
	case "retryable_failed", "permanent_failed", "ambiguous", "manual_review":
		rows = append(rows, []Button{{Text: "Verified not delivered → retry", Data: fmt.Sprintf("v1:a:dr:%d:%d", item.ID, item.Version)}})
	}
	if item.Status == "ambiguous" || item.Status == "manual_review" {
		rows = append(rows, []Button{{Text: "Verified delivered", Data: fmt.Sprintf("v1:a:dm:%d:%d", item.ID, item.Version)}})
	}
	rows = append(rows, []Button{{Text: "⬅️ Delivery queue", Data: "v1:a:dl:0"}})
	return strings.Join(lines, "\n"), rows
}

func DeliveryReconciliationView(report app.DeliveryReconciliation) (string, Keyboard) {
	text := fmt.Sprintf("<b>Delivery reconciliation</b>\nClean: <b>%t</b>\nDelivering without job: %d\nActive job / wrong order: %d\nCompleted job / order not delivered: %d\nDelivered inventory mismatch: %d\nSold without completed job: %d\nDelivered with reserved inventory: %d\nMultiple active jobs: %d\nStale processing: %d\nAmbiguous without review: %d\nSuccess evidence not completed: %d", report.Clean(), report.DeliveringWithoutJob, report.ActiveJobWrongOrderState, report.CompletedJobOrderNotDelivered, report.DeliveredInventoryMismatch, report.SoldWithoutCompletedJob, report.DeliveredOrderReservedInventory, report.MultipleActiveJobs, report.StaleProcessing, report.AmbiguousWithoutReview, report.SuccessEvidenceNotCompleted)
	return text, Keyboard{{{Text: "⬅️ Admin", Data: "v1:m"}}}
}

func deliveryStatusLabel(status domain.OrderStatus, deliveryStatus string) string {
	switch deliveryStatus {
	case "ambiguous", "manual_review":
		return "giao hàng đang được kiểm tra"
	case "permanent_failed":
		return "giao hàng thất bại"
	case "pending", "processing", "retryable_failed":
		return "đang giao hàng"
	case "completed":
		return "đã giao hàng"
	}
	switch status {
	case domain.OrderStatusReserving:
		return "đang chuẩn bị giao hàng"
	case domain.OrderStatusDelivering:
		return "đang giao hàng"
	case domain.OrderStatusDelivered:
		return "đã giao hàng"
	case domain.OrderStatusDeliveryFailed:
		return "giao hàng thất bại"
	case domain.OrderStatusPendingPayment:
		return "đang chờ thanh toán"
	default:
		return string(status)
	}
}

func PaymentReviewsView(page app.PaymentReviewPage) (string, Keyboard) {
	lines := []string{"<b>Payment review queue</b>"}
	rows := Keyboard{}
	for _, item := range page.Items {
		lines = append(lines, fmt.Sprintf("#%d · %s · %s ₫ · %s · ref <code>%s</code> · tx %s", item.ID, Escape(item.Reason), formatVND(item.Amount.Int64()), Escape(item.Currency), Escape(item.Reference), Escape(item.MaskedTransactionID)))
		rows = append(rows, []Button{{Text: fmt.Sprintf("Resolve #%d", item.ID), Data: fmt.Sprintf("v1:a:rr:%d", item.ID)}})
	}
	if len(page.Items) == 0 {
		lines = append(lines, "Không có case đang mở.")
	}
	rows = append(rows, navigationRows("v1:a:pr:%d", page.Page)...)
	return strings.Join(lines, "\n"), rows
}

func AdminBankAccountsView(page app.RedactedBankAccountPage) (string, Keyboard) {
	lines := []string{"<b>Tài khoản ngân hàng</b>"}
	rows := Keyboard{{{Text: "➕ Tạo tài khoản", Data: "v1:a:bn"}}}
	for _, bank := range page.Items {
		status, target := "🟢", 0
		if !bank.Active {
			status, target = "⚪", 1
		}
		lines = append(lines, fmt.Sprintf("%s #%d · %s · ••••%s · v%d", status, bank.ID, Escape(bank.DisplayName), Escape(bank.Last4), bank.Version))
		rows = append(rows, []Button{
			{Text: "Sửa " + bank.DisplayName, Data: fmt.Sprintf("v1:a:be:%d:%d", bank.ID, bank.Version)},
			{Text: "Bật/tắt", Data: fmt.Sprintf("v1:a:ba:%d:%d:%d", bank.ID, bank.Version, target)},
		})
	}
	rows = append(rows, navigationRows("v1:a:b:%d", page.Page)...)
	return strings.Join(lines, "\n"), rows
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
