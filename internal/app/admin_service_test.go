package app

import (
	"encoding/json"
	"errors"
	"math"
	"testing"

	"github.com/nvawntien/telegram-bot/internal/domain"
)

func TestAdminAuthorizationDecision(t *testing.T) {
	tests := []struct {
		name   string
		admin  Admin
		open   bool
		manage bool
	}{
		{name: "owner", admin: Admin{Role: "owner", Active: true, UserStatus: domain.UserStatusActive}, open: true, manage: true},
		{name: "support read only", admin: Admin{Role: "support", Active: true, UserStatus: domain.UserStatusActive}, open: true},
		{name: "revoked", admin: Admin{Role: "owner", Active: false, UserStatus: domain.UserStatusActive}},
		{name: "banned", admin: Admin{Role: "owner", Active: true, UserStatus: domain.UserStatusBanned}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.admin.CanOpenAdmin() != test.open || test.admin.CanManageCatalog() != test.manage {
				t.Fatalf("authorization = open:%t manage:%t", test.admin.CanOpenAdmin(), test.admin.CanManageCatalog())
			}
		})
	}
}

func TestParseMoneyInput(t *testing.T) {
	for _, raw := range []string{"-1", "1.5", "1,000", "abc", "", "9223372036854775808"} {
		if _, err := ParseMoneyInput(raw); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("ParseMoneyInput(%q) error = %v", raw, err)
		}
	}
	value, err := ParseMoneyInput(" 120000 ")
	if err != nil || value != 120000 {
		t.Fatalf("ParseMoneyInput() = %d, %v", value, err)
	}
	if _, err := ParseMoneyInput(string(rune(math.MaxInt32))); err == nil {
		t.Fatal("invalid numeric rune accepted")
	}
}

func TestSessionStatesAndAuditSnapshots(t *testing.T) {
	if !validSessionState(SessionCategoryCreate) || validSessionState("payment.create") {
		t.Fatal("session state validation mismatch")
	}
	payload, err := encodePayload(map[string]int64{"category_id": 12})
	if err != nil {
		t.Fatalf("encodePayload() error = %v", err)
	}
	var decoded map[string]int64
	if err := json.Unmarshal(payload, &decoded); err != nil || decoded["category_id"] != 12 {
		t.Fatalf("payload = %s, error = %v", payload, err)
	}
	snapshot := CategorySnapshot{ID: 1, Name: "Category", SortOrder: 2, Active: true, Version: 3}
	encoded, err := json.Marshal(snapshot)
	if err != nil || string(encoded) != `{"id":1,"name":"Category","sort_order":2,"active":true,"version":3}` {
		t.Fatalf("audit snapshot = %s, %v", encoded, err)
	}
}
