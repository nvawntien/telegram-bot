// Package vietqr builds deterministic payment-instruction image URLs.
package vietqr

import (
	"context"
	"net/url"
	"path"
	"strconv"
	"strings"

	"github.com/nvawntien/telegram-bot/internal/app"
)

type Generator struct {
	baseURL  *url.URL
	template string
}

func New(baseURL, template string) (*Generator, error) {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || !safeTemplate(template) {
		return nil, app.ErrInvalidPaymentInstruction
	}
	return &Generator{baseURL: parsed, template: strings.TrimSpace(template)}, nil
}

func (g *Generator) Generate(ctx context.Context, request app.PaymentInstructionRequest) (app.PaymentInstruction, error) {
	if ctx.Err() != nil {
		return app.PaymentInstruction{}, ctx.Err()
	}
	if !sixDigits(request.BankBIN) || !accountDigits(request.AccountNumber) || request.Amount <= 0 || request.OrderID <= 0 || request.ExpiresAt.IsZero() || !validReference(request.PaymentReference) || strings.TrimSpace(request.AccountName) == "" || strings.TrimSpace(request.BankDisplayName) == "" || strings.TrimSpace(request.BankName) == "" {
		return app.PaymentInstruction{}, app.ErrInvalidPaymentInstruction
	}
	resultURL := *g.baseURL
	resultURL.Path = path.Join(resultURL.Path, request.BankBIN+"-"+request.AccountNumber+"-"+g.template+".png")
	query := resultURL.Query()
	query.Set("amount", strconv.FormatInt(request.Amount.Int64(), 10))
	query.Set("addInfo", request.PaymentReference)
	query.Set("accountName", strings.TrimSpace(request.AccountName))
	resultURL.RawQuery = query.Encode()
	return app.PaymentInstruction{
		ImageURL: resultURL.String(), BankDisplayName: strings.TrimSpace(request.BankDisplayName),
		BankName: strings.TrimSpace(request.BankName), AccountNumber: request.AccountNumber,
		AccountName: strings.TrimSpace(request.AccountName), Amount: request.Amount,
		TransferContent: request.PaymentReference, ExpiresAt: request.ExpiresAt,
	}, nil
}

func sixDigits(value string) bool { return len(value) == 6 && accountDigits(value) }

func accountDigits(value string) bool {
	if len(value) < 4 || len(value) > 34 {
		return false
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}

func validReference(value string) bool {
	if len(value) < 4 || len(value) > 64 {
		return false
	}
	for _, char := range value {
		if (char < 'A' || char > 'Z') && (char < '0' || char > '9') {
			return false
		}
	}
	return true
}

func safeTemplate(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) < 1 || len(value) > 32 {
		return false
	}
	for _, char := range value {
		if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') && (char < '0' || char > '9') && char != '-' && char != '_' {
			return false
		}
	}
	return true
}

var _ app.PaymentInstructionGenerator = (*Generator)(nil)
