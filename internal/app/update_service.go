package app

import (
	"context"
	"fmt"
	"strings"
	"time"
)

type UpdateClaim int

const (
	UpdateClaimed UpdateClaim = iota
	UpdateDuplicateCompleted
	UpdateDuplicateProcessing
)

type UpdateReceiptRepository interface {
	ClaimUpdate(context.Context, int64, string, time.Time) (UpdateClaim, error)
	CompleteUpdate(context.Context, int64) error
	FailUpdate(context.Context, int64, string) error
}

type UpdateService struct {
	repository UpdateReceiptRepository
	staleAfter time.Duration
	clock      func() time.Time
}

func NewUpdateService(repository UpdateReceiptRepository, staleAfter time.Duration) *UpdateService {
	return &UpdateService{repository: repository, staleAfter: staleAfter, clock: time.Now}
}

func (s *UpdateService) Claim(ctx context.Context, updateID int64, updateType string) (UpdateClaim, error) {
	if updateID < 0 || strings.TrimSpace(updateType) == "" {
		return UpdateClaimed, ErrInvalidInput
	}
	claim, err := s.repository.ClaimUpdate(ctx, updateID, updateType, s.clock().Add(-s.staleAfter))
	if err != nil {
		return UpdateClaimed, fmt.Errorf("claim Telegram update: %w", err)
	}
	return claim, nil
}

func (s *UpdateService) Complete(ctx context.Context, updateID int64) error {
	if err := s.repository.CompleteUpdate(ctx, updateID); err != nil {
		return fmt.Errorf("complete Telegram update: %w", err)
	}
	return nil
}

func (s *UpdateService) Fail(ctx context.Context, updateID int64, code string) error {
	code = strings.TrimSpace(code)
	if code == "" || len(code) > 200 {
		code = "internal_error"
	}
	if err := s.repository.FailUpdate(ctx, updateID, code); err != nil {
		return fmt.Errorf("fail Telegram update: %w", err)
	}
	return nil
}
