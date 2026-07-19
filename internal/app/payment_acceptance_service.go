package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/nvawntien/telegram-bot/internal/domain"
)

const DefaultPostPaymentReservationTTL = 24 * time.Hour

type PaymentActor struct {
	Type string
	ID   int64
}

type AcceptPaymentCommand struct {
	PaymentEventID           int64
	Provider                 string
	ExternalEventID          string
	ProviderTransactionID    string
	Reference                string
	Amount                   domain.Money
	Currency                 string
	OccurredAt               time.Time
	Actor                    PaymentActor
	RequestID                string
	Environment              string
	Source                   string
	ProviderAccountMappingID int64
	LocalBankAccountID       int64
	DestinationAccountID     string
}

type PaymentAcceptanceResult struct {
	PaymentID int64
	OrderID   int64
	TopupID   int64
	Target    string
	Decision  string
	Reason    string
	Claimed   int
}

type PaymentAcceptanceRepository interface {
	AcceptPayment(context.Context, AcceptPaymentCommand, time.Time, time.Duration, ...int32) (PaymentAcceptanceResult, error)
}

type PaymentAcceptanceMetrics interface {
	ObservePaymentAcceptance(target, result string)
	ObservePaymentReview(reason string)
	ObservePostPaymentClaim(result string)
}

type PaymentAcceptanceService struct {
	repository          PaymentAcceptanceRepository
	reservationTTL      time.Duration
	clock               func() time.Time
	metrics             PaymentAcceptanceMetrics
	deliveryMaxAttempts int32
}

func NewPaymentAcceptanceService(repository PaymentAcceptanceRepository, reservationTTL time.Duration, metrics PaymentAcceptanceMetrics) *PaymentAcceptanceService {
	return &PaymentAcceptanceService{repository: repository, reservationTTL: reservationTTL, clock: time.Now, metrics: metrics, deliveryMaxAttempts: DefaultDeliveryMaxAttempts}
}

func (s *PaymentAcceptanceService) WithDeliveryMaxAttempts(maxAttempts int32) *PaymentAcceptanceService {
	if maxAttempts > 0 {
		s.deliveryMaxAttempts = maxAttempts
	}
	return s
}

func (s *PaymentAcceptanceService) Accept(ctx context.Context, command AcceptPaymentCommand) (PaymentAcceptanceResult, error) {
	command.Provider = strings.TrimSpace(command.Provider)
	command.ProviderTransactionID = strings.TrimSpace(command.ProviderTransactionID)
	command.Reference = strings.TrimSpace(command.Reference)
	command.Currency = strings.TrimSpace(command.Currency)
	command.Environment = strings.TrimSpace(strings.ToLower(command.Environment))
	if command.Environment == "" {
		command.Environment = "production"
	}
	command.Source = strings.TrimSpace(strings.ToLower(command.Source))
	command.DestinationAccountID = strings.TrimSpace(command.DestinationAccountID)
	if command.Source == "" {
		if command.Actor.Type == "provider" {
			command.Source = "webhook"
		} else {
			command.Source = "manual"
		}
	}
	if command.Provider == "" || command.ProviderTransactionID == "" || command.Reference == "" ||
		command.Amount <= 0 || command.Currency == "" || command.OccurredAt.IsZero() ||
		(command.Actor.Type != "provider" && command.Actor.Type != "admin" && command.Actor.Type != "user") ||
		!validPaymentEnvironment(command.Environment) ||
		(command.Source != "webhook" && command.Source != "reconciliation" && command.Source != "manual" && command.Source != "legacy") ||
		s.reservationTTL <= 0 {
		return PaymentAcceptanceResult{}, ErrInvalidInput
	}
	result, err := s.repository.AcceptPayment(ctx, command, s.clock(), s.reservationTTL, s.deliveryMaxAttempts)
	if err != nil {
		return PaymentAcceptanceResult{}, fmt.Errorf("accept payment: %w", err)
	}
	if s.metrics != nil {
		s.metrics.ObservePaymentAcceptance(result.Target, result.Decision)
		if result.Decision == "review" {
			s.metrics.ObservePaymentReview(result.Reason)
		}
		if result.Target == "order" && (result.Decision == "accepted" || result.Reason == "out_of_stock") {
			claimResult := "success"
			if result.Reason == "out_of_stock" {
				claimResult = "insufficient"
			}
			s.metrics.ObservePostPaymentClaim(claimResult)
		}
	}
	return result, nil
}
