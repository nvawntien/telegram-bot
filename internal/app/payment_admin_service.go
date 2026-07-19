package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/nvawntien/telegram-bot/internal/domain"
)

type ManualPaymentCommand struct {
	AdminTelegramID       int64
	Session               AdminSession
	ProviderTransactionID string
	Reference             string
	Amount                domain.Money
	Currency              string
	OccurredAt            time.Time
	Note                  string
	Meta                  RequestMeta
}

type PaymentReviewCase struct {
	ID                  int64
	Provider            string
	MaskedTransactionID string
	Reference           string
	Amount              domain.Money
	Currency            string
	OccurredAt          time.Time
	OrderID             int64
	TopupID             int64
	Reason              string
	Status              string
}

type PaymentReviewPage struct {
	Items []PaymentReviewCase
	Page  PageInfo
}

type ResolvePaymentReviewCommand struct {
	AdminTelegramID int64
	ReviewID        int64
	Status          string
	Note            string
	Session         AdminSession
	Meta            RequestMeta
}

type PaymentAdminRepository interface {
	ManualAcceptPayment(context.Context, ManualPaymentCommand, time.Time, time.Duration) (PaymentAcceptanceResult, error)
	ListPaymentReviews(context.Context, int64, int32, int32) ([]PaymentReviewCase, int64, error)
	ResolvePaymentReview(context.Context, ResolvePaymentReviewCommand) (PaymentReviewCase, error)
}

type PaymentAdminService struct {
	repository     PaymentAdminRepository
	pageSize       int
	reservationTTL time.Duration
	clock          func() time.Time
}

func NewPaymentAdminService(repository PaymentAdminRepository, pageSize int, reservationTTL time.Duration) *PaymentAdminService {
	if pageSize <= 0 || pageSize > MaxPageSize {
		pageSize = DefaultPageSize
	}
	return &PaymentAdminService{repository: repository, pageSize: pageSize, reservationTTL: reservationTTL, clock: time.Now}
}

func (s *PaymentAdminService) ManualConfirm(ctx context.Context, command ManualPaymentCommand) (PaymentAcceptanceResult, error) {
	command.ProviderTransactionID = strings.TrimSpace(command.ProviderTransactionID)
	command.Reference = strings.TrimSpace(command.Reference)
	command.Currency = strings.TrimSpace(command.Currency)
	command.Note = strings.TrimSpace(command.Note)
	if command.AdminTelegramID <= 0 || command.Session.ID <= 0 || command.Session.Version <= 0 ||
		command.ProviderTransactionID == "" || command.Reference == "" || command.Amount <= 0 || command.Currency == "" ||
		command.OccurredAt.IsZero() || command.Meta.UpdateID <= 0 || s.reservationTTL <= 0 {
		return PaymentAcceptanceResult{}, ErrInvalidInput
	}
	result, err := s.repository.ManualAcceptPayment(ctx, command, s.clock(), s.reservationTTL)
	if err != nil {
		return PaymentAcceptanceResult{}, fmt.Errorf("manual payment confirmation: %w", err)
	}
	return result, nil
}

func (s *PaymentAdminService) ListReviews(ctx context.Context, adminTelegramID int64, page int) (PaymentReviewPage, error) {
	if adminTelegramID <= 0 || page < 0 {
		return PaymentReviewPage{}, ErrInvalidInput
	}
	items, total, err := s.repository.ListPaymentReviews(ctx, adminTelegramID, int32(page*s.pageSize), int32(s.pageSize))
	if err != nil {
		return PaymentReviewPage{}, fmt.Errorf("list payment reviews: %w", err)
	}
	return PaymentReviewPage{Items: items, Page: pageInfo(page, s.pageSize, total)}, nil
}

func (s *PaymentAdminService) ResolveReview(ctx context.Context, command ResolvePaymentReviewCommand) (PaymentReviewCase, error) {
	command.Note = strings.TrimSpace(command.Note)
	if command.AdminTelegramID <= 0 || command.ReviewID <= 0 || command.Session.ID <= 0 || command.Session.Version <= 0 || command.Note == "" || command.Meta.UpdateID <= 0 || (command.Status != "held" && command.Status != "resolved") {
		return PaymentReviewCase{}, ErrInvalidInput
	}
	result, err := s.repository.ResolvePaymentReview(ctx, command)
	if err != nil {
		return PaymentReviewCase{}, fmt.Errorf("resolve payment review: %w", err)
	}
	return result, nil
}
