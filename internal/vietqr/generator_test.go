package vietqr

import (
	"context"
	"errors"
	"net/url"
	"testing"
	"time"

	"github.com/nvawntien/telegram-bot/internal/app"
	"github.com/nvawntien/telegram-bot/internal/domain"
)

func TestGeneratorProducesDeterministicEscapedInstruction(t *testing.T) {
	generator, err := New("https://img.example.test/image/", "compact2")
	if err != nil {
		t.Fatal(err)
	}
	request := app.PaymentInstructionRequest{
		BankBIN: "970422", BankName: "Test Bank", AccountNumber: "1234567890",
		AccountName: "NGUYỄN & TEST", BankDisplayName: "Tài khoản chính",
		Amount: domain.Money(125000), PaymentReference: "TS0A1B2C3D",
		OrderID: 42, ExpiresAt: time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC),
	}
	first, err := generator.Generate(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := generator.Generate(context.Background(), request)
	if err != nil || first != second {
		t.Fatalf("output is not deterministic: %#v %#v %v", first, second, err)
	}
	parsed, err := url.Parse(first.ImageURL)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Query().Get("amount") != "125000" || parsed.Query().Get("addInfo") != request.PaymentReference || parsed.Query().Get("accountName") != request.AccountName {
		t.Fatalf("unexpected query: %v", parsed.Query())
	}
	if first.TransferContent != request.PaymentReference || first.Amount != request.Amount {
		t.Fatalf("unexpected instruction: %#v", first)
	}
}

func TestGeneratorRejectsInvalidBankAndAmount(t *testing.T) {
	generator, err := New("https://img.example.test/image/", "compact2")
	if err != nil {
		t.Fatal(err)
	}
	base := app.PaymentInstructionRequest{
		BankBIN: "970422", BankName: "Test Bank", AccountNumber: "1234567890",
		AccountName: "TEST", BankDisplayName: "Primary", Amount: 1,
		PaymentReference: "TS1234", OrderID: 1, ExpiresAt: time.Now().Add(time.Hour),
	}
	invalid := []app.PaymentInstructionRequest{base, base}
	invalid[0].Amount = 0
	invalid[1].BankBIN = "invalid"
	for _, request := range invalid {
		if _, err := generator.Generate(context.Background(), request); !errors.Is(err, app.ErrInvalidPaymentInstruction) {
			t.Fatalf("Generate() error = %v", err)
		}
	}
}

func TestGeneratorRejectsUnsafeTemplate(t *testing.T) {
	if _, err := New("https://img.example.test/image/", "../escape"); !errors.Is(err, app.ErrInvalidPaymentInstruction) {
		t.Fatalf("New() error = %v", err)
	}
}
