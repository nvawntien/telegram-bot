package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

type BankAccountCipher interface {
	Protect(context.Context, string, string) (ProtectedBankAccountNumber, error)
	Decrypt(context.Context, string, ProtectedBankAccountNumber) (string, error)
}

type BankAccountRepository interface {
	ListActiveBankAccounts(context.Context) ([]BankAccountOption, error)
	ListAdminBankAccounts(context.Context, int32, int32) ([]RedactedBankAccount, int64, error)
	CreateBankAccount(context.Context, Admin, AdminSession, CreateBankAccountInput, ProtectedBankAccountNumber) (RedactedBankAccount, error)
	UpdateBankAccount(context.Context, Admin, AdminSession, UpdateBankAccountInput, ProtectedBankAccountNumber) (RedactedBankAccount, error)
	SetBankAccountActive(context.Context, Admin, AdminSession, SetBankAccountActiveInput) (RedactedBankAccount, error)
}

type BankAccountService struct {
	repository BankAccountRepository
	cipher     BankAccountCipher
	admin      *AdminService
	pageSize   int
}

func NewBankAccountService(repository BankAccountRepository, cipher BankAccountCipher, admin *AdminService, pageSize int) *BankAccountService {
	if pageSize <= 0 || pageSize > MaxPageSize {
		pageSize = DefaultPageSize
	}
	return &BankAccountService{repository: repository, cipher: cipher, admin: admin, pageSize: pageSize}
}

func (s *BankAccountService) ListActive(ctx context.Context) ([]BankAccountOption, error) {
	items, err := s.repository.ListActiveBankAccounts(ctx)
	if err != nil {
		return nil, fmt.Errorf("list active bank accounts: %w", err)
	}
	return items, nil
}

func (s *BankAccountService) ListAdmin(ctx context.Context, telegramID int64, page int) (RedactedBankAccountPage, error) {
	if page < 0 {
		return RedactedBankAccountPage{}, ErrInvalidInput
	}
	if _, err := s.admin.Authorize(ctx, telegramID, false); err != nil {
		return RedactedBankAccountPage{}, err
	}
	items, total, err := s.repository.ListAdminBankAccounts(ctx, int32(page*s.pageSize), int32(s.pageSize))
	if err != nil {
		return RedactedBankAccountPage{}, fmt.Errorf("list admin bank accounts: %w", err)
	}
	return RedactedBankAccountPage{Items: items, Page: pageInfo(page, s.pageSize, total)}, nil
}

func (s *BankAccountService) Create(ctx context.Context, telegramID int64, session AdminSession, input CreateBankAccountInput) (RedactedBankAccount, error) {
	normalizeBankInput(&input.BankAccountInput)
	if err := validateBankInput(input.BankAccountInput); err != nil {
		return RedactedBankAccount{}, err
	}
	admin, err := s.admin.Authorize(ctx, telegramID, true)
	if err != nil {
		return RedactedBankAccount{}, err
	}
	protected, err := s.cipher.Protect(ctx, input.BankBIN, input.AccountNumber)
	if err != nil {
		return RedactedBankAccount{}, fmt.Errorf("protect bank account: %w", err)
	}
	return s.repository.CreateBankAccount(ctx, admin, session, input, protected)
}

func (s *BankAccountService) Update(ctx context.Context, telegramID int64, session AdminSession, input UpdateBankAccountInput) (RedactedBankAccount, error) {
	normalizeBankInput(&input.BankAccountInput)
	if input.BankAccountID <= 0 || input.ExpectedRecord <= 0 || validateBankInput(input.BankAccountInput) != nil {
		return RedactedBankAccount{}, ErrInvalidInput
	}
	admin, err := s.admin.Authorize(ctx, telegramID, true)
	if err != nil {
		return RedactedBankAccount{}, err
	}
	protected, err := s.cipher.Protect(ctx, input.BankBIN, input.AccountNumber)
	if err != nil {
		return RedactedBankAccount{}, fmt.Errorf("protect bank account: %w", err)
	}
	return s.repository.UpdateBankAccount(ctx, admin, session, input, protected)
}

func (s *BankAccountService) SetActive(ctx context.Context, telegramID int64, session AdminSession, input SetBankAccountActiveInput) (RedactedBankAccount, error) {
	if input.BankAccountID <= 0 || input.ExpectedRecord <= 0 {
		return RedactedBankAccount{}, ErrInvalidInput
	}
	admin, err := s.admin.Authorize(ctx, telegramID, true)
	if err != nil {
		return RedactedBankAccount{}, err
	}
	return s.repository.SetBankAccountActive(ctx, admin, session, input)
}

func normalizeBankInput(input *BankAccountInput) {
	input.BankBIN = strings.TrimSpace(input.BankBIN)
	input.BankName = strings.TrimSpace(input.BankName)
	input.DisplayName = strings.TrimSpace(input.DisplayName)
	input.AccountName = strings.TrimSpace(input.AccountName)
	input.AccountNumber = strings.TrimSpace(input.AccountNumber)
}

func validateBankInput(input BankAccountInput) error {
	if !isDigits(input.BankBIN, 6, 6) || !isDigits(input.AccountNumber, 4, 34) ||
		input.BankName == "" || len([]rune(input.BankName)) > 120 ||
		input.DisplayName == "" || len([]rune(input.DisplayName)) > 120 ||
		input.AccountName == "" || len([]rune(input.AccountName)) > 160 ||
		input.SortOrder < 0 {
		return ErrInvalidInput
	}
	return nil
}

func isDigits(value string, minimum, maximum int) bool {
	if len(value) < minimum || len(value) > maximum {
		return false
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}

func mapBankMutationError(err error) error {
	if errors.Is(err, ErrConflict) {
		return ErrConflict
	}
	return err
}
