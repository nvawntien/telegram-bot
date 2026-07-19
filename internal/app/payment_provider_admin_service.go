package app

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"time"
)

type PaymentProviderCapabilities struct {
	Webhook        bool
	Reconciliation bool
	TestMode       bool
}

type PaymentProviderDescriptor struct {
	Name         string
	Enabled      bool
	Environment  string
	Capabilities PaymentProviderCapabilities
}

type PaymentProviderAccount struct {
	ID                     int64
	Provider               string
	Environment            string
	MaskedExternalIdentity string
	LocalBankAccountID     int64
	LocalBankDisplayName   string
	LocalBankLast4         string
	Status                 string
	Version                int64
	CreatedAt              time.Time
	UpdatedAt              time.Time
}

type PaymentProviderAccountPage struct {
	Items []PaymentProviderAccount
	Page  PageInfo
}

type PaymentProviderHealth struct {
	PaymentProviderDescriptor
	ActiveMappings            int64
	LastWebhookAt             time.Time
	LastReconciliationAttempt time.Time
	LastReconciliationSuccess time.Time
	LastErrorCode             string
	LastTransactionAt         time.Time
	PendingEvents             int64
	OpenReviews               int64
}

type CreatePaymentProviderAccountCommand struct {
	AdminTelegramID         int64
	Provider                string
	Environment             string
	ExternalAccountIdentity string
	LocalBankAccountID      int64
	Confirmed               bool
	Session                 AdminSession
	Meta                    RequestMeta
}

type SetPaymentProviderAccountStatusCommand struct {
	AdminTelegramID int64
	MappingID       int64
	ExpectedVersion int64
	Active          bool
	Confirmed       bool
	Session         AdminSession
	Meta            RequestMeta
}

type PaymentProviderAdminRepository interface {
	ListPaymentProviderAccounts(context.Context, int32, int32) ([]PaymentProviderAccount, int64, error)
	CreatePaymentProviderAccount(context.Context, Admin, AdminSession, CreatePaymentProviderAccountCommand, []byte) (PaymentProviderAccount, error)
	SetPaymentProviderAccountStatus(context.Context, Admin, AdminSession, SetPaymentProviderAccountStatusCommand) (PaymentProviderAccount, error)
	GetPaymentProviderHealth(context.Context, string, string) (PaymentProviderHealth, error)
}

type PaymentProviderAdminMetrics interface {
	ObserveProviderAccountOperation(provider, operation, result string)
}

type PaymentProviderAdminService struct {
	repository   PaymentProviderAdminRepository
	admins       *AdminService
	providers    map[string]PaymentProviderDescriptor
	providerList []PaymentProviderDescriptor
	pageSize     int
	metrics      PaymentProviderAdminMetrics
}

func NewPaymentProviderAdminService(repository PaymentProviderAdminRepository, admins *AdminService, providers []PaymentProviderDescriptor, pageSize int, metrics PaymentProviderAdminMetrics) *PaymentProviderAdminService {
	if pageSize <= 0 || pageSize > MaxPageSize {
		pageSize = DefaultPageSize
	}
	byName := make(map[string]PaymentProviderDescriptor, len(providers))
	list := make([]PaymentProviderDescriptor, 0, len(providers))
	for _, provider := range providers {
		if provider.Name == "" {
			continue
		}
		byName[provider.Name] = provider
		list = append(list, provider)
	}
	return &PaymentProviderAdminService{repository: repository, admins: admins, providers: byName, providerList: list, pageSize: pageSize, metrics: metrics}
}

func (s *PaymentProviderAdminService) ListProviders(ctx context.Context, telegramID int64) ([]PaymentProviderDescriptor, error) {
	if _, err := s.admins.Authorize(ctx, telegramID, false); err != nil {
		return nil, err
	}
	result := make([]PaymentProviderDescriptor, 0, len(s.providerList))
	for _, provider := range s.providerList {
		if provider.Enabled {
			result = append(result, provider)
		}
	}
	return result, nil
}

func (s *PaymentProviderAdminService) ListAccounts(ctx context.Context, telegramID int64, page int) (PaymentProviderAccountPage, error) {
	if page < 0 {
		return PaymentProviderAccountPage{}, ErrInvalidInput
	}
	if _, err := s.admins.Authorize(ctx, telegramID, false); err != nil {
		return PaymentProviderAccountPage{}, err
	}
	items, total, err := s.repository.ListPaymentProviderAccounts(ctx, int32(page*s.pageSize), int32(s.pageSize))
	if err != nil {
		return PaymentProviderAccountPage{}, fmt.Errorf("list payment provider accounts: %w", err)
	}
	return PaymentProviderAccountPage{Items: items, Page: pageInfo(page, s.pageSize, total)}, nil
}

func (s *PaymentProviderAdminService) CreateAccount(ctx context.Context, command CreatePaymentProviderAccountCommand) (PaymentProviderAccount, error) {
	command.Provider = strings.ToLower(strings.TrimSpace(command.Provider))
	command.Environment = strings.ToLower(strings.TrimSpace(command.Environment))
	command.ExternalAccountIdentity = strings.TrimSpace(command.ExternalAccountIdentity)
	provider, exists := s.providers[command.Provider]
	if !exists || !provider.Enabled || provider.Environment != command.Environment || command.ExternalAccountIdentity == "" || len(command.ExternalAccountIdentity) > 256 || command.LocalBankAccountID <= 0 || !command.Confirmed {
		s.observe(command.Provider, "create", "invalid")
		return PaymentProviderAccount{}, ErrInvalidInput
	}
	admin, err := s.admins.Authorize(ctx, command.AdminTelegramID, true)
	if err != nil {
		s.observe(command.Provider, "create", "forbidden")
		return PaymentProviderAccount{}, err
	}
	fingerprint := sha256.Sum256([]byte(command.Provider + "\x00" + command.Environment + "\x00" + command.ExternalAccountIdentity))
	result, err := s.repository.CreatePaymentProviderAccount(ctx, admin, command.Session, command, fingerprint[:])
	metricResult := "success"
	if err != nil {
		metricResult = "failed"
	}
	s.observe(command.Provider, "create", metricResult)
	return result, err
}

func (s *PaymentProviderAdminService) SetAccountActive(ctx context.Context, command SetPaymentProviderAccountStatusCommand) (PaymentProviderAccount, error) {
	if command.MappingID <= 0 || command.ExpectedVersion <= 0 || !command.Confirmed {
		return PaymentProviderAccount{}, ErrInvalidInput
	}
	admin, err := s.admins.Authorize(ctx, command.AdminTelegramID, true)
	if err != nil {
		return PaymentProviderAccount{}, err
	}
	result, err := s.repository.SetPaymentProviderAccountStatus(ctx, admin, command.Session, command)
	metricResult := "success"
	if err != nil {
		metricResult = "failed"
	}
	s.observe(result.Provider, "set_status", metricResult)
	return result, err
}

func (s *PaymentProviderAdminService) Health(ctx context.Context, telegramID int64) ([]PaymentProviderHealth, error) {
	if _, err := s.admins.Authorize(ctx, telegramID, false); err != nil {
		return nil, err
	}
	result := make([]PaymentProviderHealth, 0, len(s.providerList))
	for _, provider := range s.providerList {
		health, err := s.repository.GetPaymentProviderHealth(ctx, provider.Name, provider.Environment)
		if err != nil {
			return nil, fmt.Errorf("load payment provider health: %w", err)
		}
		health.PaymentProviderDescriptor = provider
		result = append(result, health)
	}
	return result, nil
}

func (s *PaymentProviderAdminService) observe(provider, operation, result string) {
	if s.metrics != nil {
		s.metrics.ObserveProviderAccountOperation(provider, operation, result)
	}
}
