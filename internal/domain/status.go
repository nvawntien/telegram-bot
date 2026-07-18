package domain

// UserStatus is the lifecycle state of a Telegram shop user.
type UserStatus string

const (
	UserStatusActive   UserStatus = "active"
	UserStatusBanned   UserStatus = "banned"
	UserStatusDisabled UserStatus = "disabled"
)

func (s UserStatus) IsValid() bool {
	switch s {
	case UserStatusActive, UserStatusBanned, UserStatusDisabled:
		return true
	default:
		return false
	}
}

// InventoryStatus is the lifecycle state of one encrypted digital good.
type InventoryStatus string

const (
	InventoryStatusAvailable InventoryStatus = "available"
	InventoryStatusReserved  InventoryStatus = "reserved"
	InventoryStatusSold      InventoryStatus = "sold"
	InventoryStatusDisabled  InventoryStatus = "disabled"
)

func (s InventoryStatus) IsValid() bool {
	switch s {
	case InventoryStatusAvailable, InventoryStatusReserved, InventoryStatusSold, InventoryStatusDisabled:
		return true
	default:
		return false
	}
}

// PaymentStatus is the normalized state of a payment record.
type PaymentStatus string

const (
	PaymentStatusCreated   PaymentStatus = "created"
	PaymentStatusConfirmed PaymentStatus = "confirmed"
	PaymentStatusFailed    PaymentStatus = "failed"
	PaymentStatusReview    PaymentStatus = "review"
	PaymentStatusRefunded  PaymentStatus = "refunded"
)

func (s PaymentStatus) IsValid() bool {
	switch s {
	case PaymentStatusCreated, PaymentStatusConfirmed, PaymentStatusFailed, PaymentStatusReview, PaymentStatusRefunded:
		return true
	default:
		return false
	}
}

// OutboxStatus is the durable processing state of an external side effect.
type OutboxStatus string

const (
	OutboxStatusPending    OutboxStatus = "pending"
	OutboxStatusProcessing OutboxStatus = "processing"
	OutboxStatusCompleted  OutboxStatus = "completed"
	OutboxStatusFailed     OutboxStatus = "failed"
)

func (s OutboxStatus) IsValid() bool {
	switch s {
	case OutboxStatusPending, OutboxStatusProcessing, OutboxStatusCompleted, OutboxStatusFailed:
		return true
	default:
		return false
	}
}

// WalletEntryType determines the required sign of a ledger amount.
type WalletEntryType string

const (
	WalletEntryTypeCredit     WalletEntryType = "credit"
	WalletEntryTypeDebit      WalletEntryType = "debit"
	WalletEntryTypeRefund     WalletEntryType = "refund"
	WalletEntryTypeAdjustment WalletEntryType = "adjustment"
)

func (t WalletEntryType) IsValid() bool {
	switch t {
	case WalletEntryTypeCredit, WalletEntryTypeDebit, WalletEntryTypeRefund, WalletEntryTypeAdjustment:
		return true
	default:
		return false
	}
}

// AcceptsAmount mirrors the signed ledger constraint: credit/refund are
// positive, debit is negative, and an adjustment may use either sign but not 0.
func (t WalletEntryType) AcceptsAmount(amount int64) bool {
	switch t {
	case WalletEntryTypeCredit, WalletEntryTypeRefund:
		return amount > 0
	case WalletEntryTypeDebit:
		return amount < 0
	case WalletEntryTypeAdjustment:
		return amount != 0
	default:
		return false
	}
}
