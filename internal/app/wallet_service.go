package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/nvawntien/telegram-bot/internal/domain"
)

type WalletAccount struct {
	ID      int64
	UserID  int64
	Balance domain.Money
	Status  string
	Version int64
}

type WalletTopup struct {
	ID               int64
	UserID           int64
	WalletAccountID  int64
	Amount           domain.Money
	Currency         string
	PaymentReference string
	Status           string
	ExpiresAt        time.Time
	Version          int64
	BankBIN          string
	BankName         string
	BankDisplayName  string
	BankAccountName  string
	BankAccountLast4 string
	BankProtected    ProtectedBankAccountNumber
}

type CreateWalletTopupCommand struct {
	TelegramUserID     int64
	Amount             domain.Money
	BankAccountID      int64
	IdempotencyKey     string
	Meta               RequestMeta
	PaymentEnvironment string
}

type WalletOrderPaymentCommand struct {
	TelegramUserID int64
	OrderID        int64
	IdempotencyKey string
	Meta           RequestMeta
}

type WalletOrderPaymentResult struct {
	OrderID   int64
	Balance   domain.Money
	Duplicate bool
	Claimed   int
}

type WalletAdjustmentCommand struct {
	AdminTelegramID  int64
	TargetTelegramID int64
	Amount           domain.Money
	Debit            bool
	Reason           string
	IdempotencyKey   string
	Session          AdminSession
	Meta             RequestMeta
}

type WalletRepository interface {
	EnsureWallet(context.Context, int64) (WalletAccount, error)
	CreateWalletTopup(context.Context, CreateWalletTopupCommand, string, time.Time) (WalletTopup, bool, error)
	PayOrderWithWallet(context.Context, WalletOrderPaymentCommand, time.Time, time.Duration, ...int32) (WalletOrderPaymentResult, error)
	AdjustWallet(context.Context, WalletAdjustmentCommand, time.Time) (WalletAccount, error)
}

type WalletMetrics interface {
	ObserveWalletTopupCreated(result string)
	ObserveWalletPayment(result string)
	ObserveWalletLedger(entryType, result string)
	ObserveWalletAdjustment(entryType, result string)
}

type WalletService struct {
	repository          WalletRepository
	cipher              BankAccountCipher
	instructions        PaymentInstructionGenerator
	references          PaymentReferenceGenerator
	topupMin            domain.Money
	topupMax            domain.Money
	topupExpiry         time.Duration
	reservationTTL      time.Duration
	clock               func() time.Time
	metrics             WalletMetrics
	deliveryMaxAttempts int32
	paymentEnvironment  string
}

func NewWalletService(repository WalletRepository, cipher BankAccountCipher, instructions PaymentInstructionGenerator, references PaymentReferenceGenerator, topupMin, topupMax domain.Money, topupExpiry, reservationTTL time.Duration, metrics WalletMetrics) *WalletService {
	return &WalletService{repository: repository, cipher: cipher, instructions: instructions, references: references, topupMin: topupMin, topupMax: topupMax, topupExpiry: topupExpiry, reservationTTL: reservationTTL, clock: time.Now, metrics: metrics, deliveryMaxAttempts: DefaultDeliveryMaxAttempts, paymentEnvironment: "production"}
}

func (s *WalletService) WithPaymentEnvironment(environment string) *WalletService {
	if validPaymentEnvironment(environment) {
		s.paymentEnvironment = environment
	}
	return s
}

func (s *WalletService) WithDeliveryMaxAttempts(maxAttempts int32) *WalletService {
	if maxAttempts > 0 {
		s.deliveryMaxAttempts = maxAttempts
	}
	return s
}

func (s *WalletService) Balance(ctx context.Context, telegramUserID int64) (WalletAccount, error) {
	if telegramUserID <= 0 {
		return WalletAccount{}, ErrInvalidInput
	}
	account, err := s.repository.EnsureWallet(ctx, telegramUserID)
	if err != nil {
		return WalletAccount{}, fmt.Errorf("load wallet balance: %w", err)
	}
	return account, nil
}

func (s *WalletService) CreateTopup(ctx context.Context, command CreateWalletTopupCommand) (WalletTopup, PaymentInstruction, bool, error) {
	if command.TelegramUserID <= 0 || command.BankAccountID <= 0 || command.IdempotencyKey == "" || command.Meta.UpdateID <= 0 || command.Amount < s.topupMin || command.Amount > s.topupMax || s.topupExpiry <= 0 {
		s.observeTopup("invalid")
		return WalletTopup{}, PaymentInstruction{}, false, ErrInvalidWalletAmount
	}
	reference, err := s.references.Generate()
	if err != nil {
		return WalletTopup{}, PaymentInstruction{}, false, err
	}
	command.PaymentEnvironment = s.paymentEnvironment
	topup, duplicate, err := s.repository.CreateWalletTopup(ctx, command, reference, s.clock().Add(s.topupExpiry))
	if err != nil {
		s.observeTopup("failed")
		return WalletTopup{}, PaymentInstruction{}, false, fmt.Errorf("create wallet top-up: %w", err)
	}
	accountNumber, err := s.cipher.Decrypt(ctx, topup.BankBIN, topup.BankProtected)
	if err != nil {
		return WalletTopup{}, PaymentInstruction{}, false, fmt.Errorf("decrypt top-up instruction: %w", err)
	}
	instruction, err := s.instructions.Generate(ctx, PaymentInstructionRequest{
		BankBIN: topup.BankBIN, BankName: topup.BankName, BankDisplayName: topup.BankDisplayName,
		AccountName: topup.BankAccountName, AccountNumber: accountNumber, Amount: topup.Amount,
		PaymentReference: topup.PaymentReference, ExpiresAt: topup.ExpiresAt,
		OrderID: topup.ID,
	})
	if err != nil {
		return WalletTopup{}, PaymentInstruction{}, false, fmt.Errorf("generate top-up instruction: %w", err)
	}
	result := "created"
	if duplicate {
		result = "duplicate"
	}
	s.observeTopup(result)
	return topup, instruction, duplicate, nil
}

func (s *WalletService) PayOrder(ctx context.Context, command WalletOrderPaymentCommand) (WalletOrderPaymentResult, error) {
	if command.TelegramUserID <= 0 || command.OrderID <= 0 || command.IdempotencyKey == "" || command.Meta.UpdateID <= 0 || s.reservationTTL <= 0 {
		return WalletOrderPaymentResult{}, ErrInvalidInput
	}
	result, err := s.repository.PayOrderWithWallet(ctx, command, s.clock(), s.reservationTTL, s.deliveryMaxAttempts)
	if err != nil {
		s.observePayment("failed")
		return WalletOrderPaymentResult{}, fmt.Errorf("pay order with wallet: %w", err)
	}
	metricResult := "accepted"
	if result.Duplicate {
		metricResult = "duplicate"
	}
	s.observePayment(metricResult)
	return result, nil
}

func (s *WalletService) Adjust(ctx context.Context, command WalletAdjustmentCommand) (WalletAccount, error) {
	command.Reason = strings.TrimSpace(command.Reason)
	if command.AdminTelegramID <= 0 || command.TargetTelegramID <= 0 || command.Amount <= 0 || command.Reason == "" || command.IdempotencyKey == "" || command.Meta.UpdateID <= 0 {
		return WalletAccount{}, ErrInvalidInput
	}
	account, err := s.repository.AdjustWallet(ctx, command, s.clock())
	entryType := "credit"
	if command.Debit {
		entryType = "debit"
	}
	if s.metrics != nil {
		result := "success"
		if err != nil {
			result = "failed"
		}
		s.metrics.ObserveWalletAdjustment(entryType, result)
	}
	if err != nil {
		return WalletAccount{}, fmt.Errorf("adjust wallet: %w", err)
	}
	return account, nil
}

func (s *WalletService) observeTopup(result string) {
	if s.metrics != nil {
		s.metrics.ObserveWalletTopupCreated(result)
	}
}
func (s *WalletService) observePayment(result string) {
	if s.metrics != nil {
		s.metrics.ObserveWalletPayment(result)
	}
}
