package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

type DeliveryReviewItem struct {
	ID                int64
	OrderID           int64
	Status            string
	Attempts          int32
	MaxAttempts       int32
	RecipientChatID   int64
	TelegramMessageID int64
	ErrorCode         string
	ErrorDetail       string
	CreatedAt         time.Time
	UpdatedAt         time.Time
	Version           int64
	ProductName       string
	Quantity          int32
}

type DeliveryAttemptView struct {
	Number            int32
	Status            string
	HTTPStatus        int32
	TelegramErrorCode int32
	RetryAfterSeconds int32
	TelegramChatID    int64
	TelegramMessageID int64
	ErrorClass        string
	ErrorCode         string
	ErrorDetail       string
	StartedAt         time.Time
	FinishedAt        time.Time
}

type DeliveryReviewPage struct {
	Items []DeliveryReviewItem
	Page  PageInfo
}

type DeliveryReviewDetail struct {
	DeliveryReviewItem
	AttemptsHistory []DeliveryAttemptView
}

type DeliveryResolutionCommand struct {
	AdminTelegramID   int64
	JobID             int64
	ExpectedVersion   int64
	TelegramMessageID int64
	Reason            string
	Session           AdminSession
	Meta              RequestMeta
}

type DeliveryAdminRepository interface {
	ListDeliveryReviews(context.Context, int64, int32, int32) ([]DeliveryReviewItem, int64, error)
	GetDeliveryReview(context.Context, int64, int64) (DeliveryReviewDetail, error)
	RetryDelivery(context.Context, DeliveryResolutionCommand, time.Time) (DeliveryReviewItem, error)
	CompleteDelivery(context.Context, DeliveryResolutionCommand, time.Time) (DeliveryReviewItem, error)
	ReconcileDeliveryForAdmin(context.Context, int64, time.Time) (DeliveryReconciliation, error)
}

func (s *DeliveryAdminService) Reconcile(ctx context.Context, adminTelegramID int64) (DeliveryReconciliation, error) {
	if adminTelegramID <= 0 || s.processingLease <= 0 {
		return DeliveryReconciliation{}, ErrInvalidInput
	}
	report, err := s.repository.ReconcileDeliveryForAdmin(ctx, adminTelegramID, s.clock().Add(-s.processingLease))
	if err != nil {
		return DeliveryReconciliation{}, fmt.Errorf("reconcile delivery state: %w", err)
	}
	return report, nil
}

type DeliveryAdminService struct {
	repository      DeliveryAdminRepository
	pageSize        int
	processingLease time.Duration
	clock           func() time.Time
}

func NewDeliveryAdminService(repository DeliveryAdminRepository, pageSize int) *DeliveryAdminService {
	if pageSize <= 0 || pageSize > MaxPageSize {
		pageSize = DefaultPageSize
	}
	return &DeliveryAdminService{repository: repository, pageSize: pageSize, processingLease: 2 * time.Minute, clock: time.Now}
}

func (s *DeliveryAdminService) WithProcessingLease(lease time.Duration) *DeliveryAdminService {
	if lease > 0 {
		s.processingLease = lease
	}
	return s
}

func (s *DeliveryAdminService) List(ctx context.Context, adminTelegramID int64, page int) (DeliveryReviewPage, error) {
	if adminTelegramID <= 0 || page < 0 {
		return DeliveryReviewPage{}, ErrInvalidInput
	}
	items, total, err := s.repository.ListDeliveryReviews(ctx, adminTelegramID, int32(page*s.pageSize), int32(s.pageSize))
	if err != nil {
		return DeliveryReviewPage{}, fmt.Errorf("list delivery reviews: %w", err)
	}
	return DeliveryReviewPage{Items: items, Page: pageInfo(page, s.pageSize, total)}, nil
}

func (s *DeliveryAdminService) Get(ctx context.Context, adminTelegramID, jobID int64) (DeliveryReviewDetail, error) {
	if adminTelegramID <= 0 || jobID <= 0 {
		return DeliveryReviewDetail{}, ErrInvalidInput
	}
	detail, err := s.repository.GetDeliveryReview(ctx, adminTelegramID, jobID)
	if errors.Is(err, ErrNotFound) {
		return DeliveryReviewDetail{}, ErrDeliveryNotFound
	}
	if err != nil {
		return DeliveryReviewDetail{}, fmt.Errorf("get delivery review: %w", err)
	}
	return detail, nil
}

func (s *DeliveryAdminService) Retry(ctx context.Context, command DeliveryResolutionCommand) (DeliveryReviewItem, error) {
	command.Reason = strings.TrimSpace(command.Reason)
	if command.AdminTelegramID <= 0 || command.JobID <= 0 || command.ExpectedVersion <= 0 ||
		command.Reason == "" || len([]rune(command.Reason)) > 1000 || command.Meta.UpdateID <= 0 {
		return DeliveryReviewItem{}, ErrInvalidInput
	}
	result, err := s.repository.RetryDelivery(ctx, command, s.clock())
	if err != nil {
		return DeliveryReviewItem{}, fmt.Errorf("retry delivery: %w", err)
	}
	return result, nil
}

func (s *DeliveryAdminService) Complete(ctx context.Context, command DeliveryResolutionCommand) (DeliveryReviewItem, error) {
	command.Reason = strings.TrimSpace(command.Reason)
	if command.AdminTelegramID <= 0 || command.JobID <= 0 || command.ExpectedVersion <= 0 ||
		command.TelegramMessageID <= 0 || command.Reason == "" || len([]rune(command.Reason)) > 1000 || command.Meta.UpdateID <= 0 {
		return DeliveryReviewItem{}, ErrInvalidInput
	}
	result, err := s.repository.CompleteDelivery(ctx, command, s.clock())
	if err != nil {
		return DeliveryReviewItem{}, fmt.Errorf("complete delivery: %w", err)
	}
	return result, nil
}
