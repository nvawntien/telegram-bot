package app

import (
	"encoding/json"
	"time"

	"github.com/nvawntien/telegram-bot/internal/domain"
)

const (
	DefaultPageSize = 8
	MaxPageSize     = 20
)

type User struct {
	ID             int64
	TelegramUserID int64
	Username       string
	DisplayName    string
	Status         domain.UserStatus
}

type TelegramProfile struct {
	TelegramUserID int64
	Username       string
	FirstName      string
	LastName       string
}

type Admin struct {
	ID             int64
	UserID         int64
	TelegramUserID int64
	Role           string
	Active         bool
	UserStatus     domain.UserStatus
}

func (a Admin) CanOpenAdmin() bool {
	return a.Active && a.UserStatus == domain.UserStatusActive
}

func (a Admin) CanManageCatalog() bool {
	if !a.CanOpenAdmin() {
		return false
	}
	switch a.Role {
	case "owner", "admin", "operator":
		return true
	default:
		return false
	}
}

type Category struct {
	ID        int64
	Name      string
	Slug      string
	Emoji     string
	SortOrder int32
	Active    bool
	Version   int64
}

type Product struct {
	ID           int64
	CategoryID   int64
	Name         string
	Slug         string
	Description  string
	Price        domain.Money
	DeliveryType string
	ContactURL   string
	Active       bool
	Version      int64
}

type PageInfo struct {
	Page       int
	PageSize   int
	TotalItems int64
	TotalPages int
}

type CategoryPage struct {
	Items []Category
	Page  PageInfo
}

type ProductPage struct {
	Items []Product
	Page  PageInfo
}

type AdminSession struct {
	ID        int64
	AdminID   int64
	State     string
	Payload   json.RawMessage
	ExpiresAt time.Time
	Version   int64
}

type RequestMeta struct {
	RequestID string
	UpdateID  int64
}

type SessionCommand struct {
	AdminTelegramID int64
	SessionID       int64
	ExpectedVersion int64
	Meta            RequestMeta
}

type CategorySnapshot struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	SortOrder int32  `json:"sort_order"`
	Active    bool   `json:"active"`
	Version   int64  `json:"version"`
}

type ProductSnapshot struct {
	ID          int64  `json:"id"`
	CategoryID  int64  `json:"category_id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	PriceVND    int64  `json:"price_vnd"`
	Active      bool   `json:"active"`
	Version     int64  `json:"version"`
}

type EncryptedInventoryPayload struct {
	Ciphertext  []byte
	Nonce       []byte
	Fingerprint []byte
	KeyVersion  int32
	Format      string
}

type InventoryOverview struct {
	ProductID      int64
	ProductName    string
	AvailableCount int64
	ReservedCount  int64
	SoldCount      int64
	DisabledCount  int64
	TotalCount     int64
}

type InventoryOverviewPage struct {
	Items []InventoryOverview
	Page  PageInfo
}

type RedactedInventoryItem struct {
	ID              int64
	ProductID       int64
	ProductName     string
	Status          domain.InventoryStatus
	ReservedOrderID int64
	ReservedUntil   time.Time
	KeyVersion      int32
	Version         int64
	CreatedAt       time.Time
}

type RedactedInventoryPage struct {
	Items []RedactedInventoryItem
	Page  PageInfo
}

type InventoryImportLimits struct {
	MaxItems      int
	MaxItemBytes  int
	MaxTotalBytes int
}

type InventoryImportResult struct {
	ProductID  int64
	Requested  int
	Inserted   int
	Duplicates int
	Rejected   int
	KeyVersion int32
}

type InventoryClaimRequest struct {
	OrderID       int64
	OrderItemID   int64
	ProductID     int64
	Quantity      int32
	ReservedUntil time.Time
	RequestID     string
}

type InventoryClaimResult struct {
	OrderID          int64
	InventoryItemIDs []int64
	Count            int
}

type InventoryReleaseRequest struct {
	OrderID   int64
	Reason    domain.InventoryReleaseReason
	RequestID string
}

type InventoryReleaseResult struct {
	OrderID  int64
	Released int
	Reason   domain.InventoryReleaseReason
}

type InventoryRecoveryResult struct {
	OrderID          int64
	Released         int
	RecoveryRequired bool
	OrderStatus      domain.OrderStatus
}

type ProtectedBankAccountNumber struct {
	Ciphertext  []byte
	Nonce       []byte
	Fingerprint []byte
	KeyVersion  int32
	KeyID       string
	Format      string
}

type BankAccountOption struct {
	ID          int64
	BankBIN     string
	BankName    string
	DisplayName string
	AccountName string
	Last4       string
	SortOrder   int32
	Version     int64
}

type BankAccountRecord struct {
	BankAccountOption
	Protected ProtectedBankAccountNumber
	Active    bool
	CreatedAt time.Time
}

type RedactedBankAccount struct {
	BankAccountOption
	Active     bool
	KeyVersion int32
	Format     string
	CreatedAt  time.Time
}

type RedactedBankAccountPage struct {
	Items []RedactedBankAccount
	Page  PageInfo
}

type BankAccountInput struct {
	BankBIN       string
	BankName      string
	DisplayName   string
	AccountName   string
	AccountNumber string
	SortOrder     int32
}

type CreateBankAccountInput struct {
	BankAccountInput
	Meta RequestMeta
}

type UpdateBankAccountInput struct {
	BankAccountID  int64
	ExpectedRecord int64
	BankAccountInput
	Meta RequestMeta
}

type SetBankAccountActiveInput struct {
	BankAccountID  int64
	ExpectedRecord int64
	Active         bool
	Meta           RequestMeta
}

type OrderItemSnapshot struct {
	ID        int64
	ProductID int64
	Name      string
	UnitPrice domain.Money
	Quantity  int32
	LineTotal domain.Money
}

type OrderSummary struct {
	ID               int64
	Status           domain.OrderStatus
	Total            domain.Money
	PaymentReference string
	ExpiresAt        time.Time
	Version          int64
	CreatedAt        time.Time
	ProductName      string
	Quantity         int32
}

type OrderPage struct {
	Items []OrderSummary
	Page  PageInfo
}

type OrderDetail struct {
	ID               int64
	UserID           int64
	Status           domain.OrderStatus
	Currency         string
	Subtotal         domain.Money
	Total            domain.Money
	PaymentReference string
	ExpiresAt        time.Time
	CancelledAt      time.Time
	Version          int64
	CreatedAt        time.Time
	BankAccountID    int64
	BankBIN          string
	BankName         string
	BankDisplayName  string
	BankAccountName  string
	BankAccountLast4 string
	BankProtected    ProtectedBankAccountNumber
	Item             OrderItemSnapshot
}

type PaymentInstructionRequest struct {
	BankBIN          string
	BankName         string
	AccountNumber    string
	AccountName      string
	BankDisplayName  string
	Amount           domain.Money
	PaymentReference string
	OrderID          int64
	ExpiresAt        time.Time
}

type PaymentInstruction struct {
	ImageURL        string
	BankDisplayName string
	BankName        string
	AccountNumber   string
	AccountName     string
	Amount          domain.Money
	TransferContent string
	ExpiresAt       time.Time
}

type CreateOrderCommand struct {
	TelegramUserID int64
	ProductID      int64
	BankAccountID  int64
	Quantity       int32
	IdempotencyKey string
	Meta           RequestMeta
}

type CreateOrderResult struct {
	Order       OrderDetail
	Instruction PaymentInstruction
	Duplicate   bool
}

type CancelOrderCommand struct {
	TelegramUserID  int64
	OrderID         int64
	ExpectedVersion int64
	Meta            RequestMeta
}

type CancelOrderResult struct {
	Order            OrderDetail
	AlreadyCancelled bool
}

func pageInfo(page, size int, total int64) PageInfo {
	totalPages := 0
	if total > 0 {
		totalPages = int((total + int64(size) - 1) / int64(size))
	}
	return PageInfo{Page: page, PageSize: size, TotalItems: total, TotalPages: totalPages}
}
