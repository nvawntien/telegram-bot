package app

import (
	"bytes"
	"errors"
	"testing"
)

func TestParseInventoryImportPreservesOpaqueLineBytes(t *testing.T) {
	limits := InventoryImportLimits{MaxItems: 10, MaxItemBytes: 64, MaxTotalBytes: 256}
	input := []byte(" first item \r\n\r\n\t \r\nsecond  item\n")
	items, rejected, err := ParseInventoryImport(input, limits)
	if err != nil {
		t.Fatalf("ParseInventoryImport() error = %v", err)
	}
	want := [][]byte{[]byte(" first item "), []byte("second  item")}
	if rejected != 3 || len(items) != len(want) {
		t.Fatalf("items = %q, rejected = %d", items, rejected)
	}
	for index := range want {
		if !bytes.Equal(items[index], want[index]) {
			t.Errorf("item %d = %q, want %q", index, items[index], want[index])
		}
	}
}

func TestParseInventoryImportLimitsAndInvalidPayload(t *testing.T) {
	tests := []struct {
		name   string
		raw    []byte
		limits InventoryImportLimits
		want   error
	}{
		{name: "empty", raw: nil, limits: InventoryImportLimits{2, 4, 8}, want: ErrInvalidInventoryPayload},
		{name: "whitespace", raw: []byte(" \n\t"), limits: InventoryImportLimits{2, 4, 8}, want: ErrInvalidInventoryPayload},
		{name: "invalid UTF-8", raw: []byte{0xff}, limits: InventoryImportLimits{2, 4, 8}, want: ErrInvalidInventoryPayload},
		{name: "item bytes", raw: []byte("12345"), limits: InventoryImportLimits{2, 4, 8}, want: ErrImportLimitExceeded},
		{name: "item count", raw: []byte("1\n2\n3"), limits: InventoryImportLimits{2, 4, 8}, want: ErrImportLimitExceeded},
		{name: "total bytes", raw: []byte("1234\n5678"), limits: InventoryImportLimits{4, 4, 8}, want: ErrImportLimitExceeded},
		{name: "invalid limits", raw: []byte("1"), limits: InventoryImportLimits{1, 9, 8}, want: ErrInvalidInput},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, _, err := ParseInventoryImport(test.raw, test.limits); !errors.Is(err, test.want) {
				t.Fatalf("ParseInventoryImport() error = %v, want %v", err, test.want)
			}
		})
	}
}
