package app

import (
	"context"
	"errors"
	"time"
)

type InventoryReservationRepository interface {
	ClaimInventory(context.Context, InventoryClaimRequest) (InventoryClaimResult, error)
	ReleaseInventory(context.Context, InventoryReleaseRequest) (InventoryReleaseResult, error)
	RecoverExpiredReservation(context.Context, int64, time.Time, string) (InventoryRecoveryResult, error)
}

type InventoryReservationService struct {
	repository InventoryReservationRepository
	maxTTL     time.Duration
	clock      func() time.Time
	metrics    InventoryOperationMetrics
}

func NewInventoryReservationService(
	repository InventoryReservationRepository,
	maxTTL time.Duration,
	metrics InventoryOperationMetrics,
) *InventoryReservationService {
	return &InventoryReservationService{repository: repository, maxTTL: maxTTL, clock: time.Now, metrics: metrics}
}

func (s *InventoryReservationService) Claim(ctx context.Context, request InventoryClaimRequest) (InventoryClaimResult, error) {
	now := s.clock()
	if request.OrderID <= 0 || request.OrderItemID <= 0 || request.ProductID <= 0 || request.Quantity <= 0 ||
		s.maxTTL <= 0 || !request.ReservedUntil.After(now) || request.ReservedUntil.After(now.Add(s.maxTTL)) {
		s.observeClaim(ErrInvalidInput, InventoryClaimResult{})
		return InventoryClaimResult{}, ErrInvalidInput
	}
	result, err := s.repository.ClaimInventory(ctx, request)
	s.observeClaim(err, result)
	if err != nil {
		return InventoryClaimResult{}, err
	}
	return result, nil
}

func (s *InventoryReservationService) Release(ctx context.Context, request InventoryReleaseRequest) (InventoryReleaseResult, error) {
	if request.OrderID <= 0 || !request.Reason.IsValid() {
		s.observeRelease(ErrInvalidInput, InventoryReleaseResult{})
		return InventoryReleaseResult{}, ErrInvalidInput
	}
	result, err := s.repository.ReleaseInventory(ctx, request)
	s.observeRelease(err, result)
	if err != nil {
		return InventoryReleaseResult{}, err
	}
	return result, nil
}

func (s *InventoryReservationService) RecoverExpired(
	ctx context.Context,
	orderID int64,
	now time.Time,
	requestID string,
) (InventoryRecoveryResult, error) {
	if orderID <= 0 || now.IsZero() {
		s.observeRecovery(ErrInvalidInput, InventoryRecoveryResult{})
		return InventoryRecoveryResult{}, ErrInvalidInput
	}
	result, err := s.repository.RecoverExpiredReservation(ctx, orderID, now, requestID)
	s.observeRecovery(err, result)
	if err != nil {
		return InventoryRecoveryResult{}, err
	}
	return result, nil
}

func (s *InventoryReservationService) observeClaim(err error, result InventoryClaimResult) {
	if s.metrics == nil {
		return
	}
	s.metrics.ObserveInventoryClaim(inventoryMetricResult(err), result.Count)
}

func (s *InventoryReservationService) observeRelease(err error, result InventoryReleaseResult) {
	if s.metrics == nil {
		return
	}
	s.metrics.ObserveInventoryRelease(inventoryMetricResult(err), result.Released)
}

func (s *InventoryReservationService) observeRecovery(err error, result InventoryRecoveryResult) {
	if s.metrics == nil {
		return
	}
	metric := inventoryMetricResult(err)
	if result.RecoveryRequired {
		metric = "required"
	}
	s.metrics.ObserveInventoryRecovery(metric)
}

func inventoryMetricResult(err error) string {
	switch {
	case err == nil:
		return "success"
	case errors.Is(err, ErrInsufficientInventory):
		return "insufficient"
	case errors.Is(err, ErrUnsafeReservationRelease):
		return "unsafe"
	case errors.Is(err, ErrNotFound), errors.Is(err, ErrInventoryNotFound):
		return "not_found"
	case errors.Is(err, ErrInvalidInput), errors.Is(err, ErrInvalidInventoryState):
		return "invalid"
	default:
		return "failed"
	}
}
