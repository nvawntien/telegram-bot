package domain

import "testing"

func TestTypedStatuses(t *testing.T) {
	valid := []interface{ IsValid() bool }{
		UserStatusActive,
		InventoryStatusReserved,
		PaymentStatusConfirmed,
		OutboxStatusProcessing,
		WalletEntryTypeDebit,
	}
	for _, status := range valid {
		if !status.IsValid() {
			t.Errorf("status %#v is invalid", status)
		}
	}

	invalid := []interface{ IsValid() bool }{
		UserStatus("unknown"),
		InventoryStatus("unknown"),
		PaymentStatus("unknown"),
		OutboxStatus("unknown"),
		WalletEntryType("unknown"),
	}
	for _, status := range invalid {
		if status.IsValid() {
			t.Errorf("status %#v is valid", status)
		}
	}
}

func TestWalletEntryAmountSigns(t *testing.T) {
	tests := []struct {
		entryType WalletEntryType
		amount    int64
		want      bool
	}{
		{WalletEntryTypeCredit, 1, true},
		{WalletEntryTypeCredit, -1, false},
		{WalletEntryTypeDebit, -1, true},
		{WalletEntryTypeDebit, 1, false},
		{WalletEntryTypeRefund, 1, true},
		{WalletEntryTypeAdjustment, -1, true},
		{WalletEntryTypeAdjustment, 0, false},
	}
	for _, test := range tests {
		if got := test.entryType.AcceptsAmount(test.amount); got != test.want {
			t.Errorf("%s.AcceptsAmount(%d) = %t, want %t", test.entryType, test.amount, got, test.want)
		}
	}
}
