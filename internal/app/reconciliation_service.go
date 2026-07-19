package app

import (
	"context"
	"fmt"
	"time"
)

type FinancialReconciliation struct {
	CompletedEventsWithoutPayment   int64
	ConfirmedPaymentsWithoutOutcome int64
	WalletBalanceMismatches         int64
	ReservationCountMismatches      int64
	ReviewCasesWithoutReason        int64
}

func (r FinancialReconciliation) Clean() bool {
	return r.CompletedEventsWithoutPayment == 0 && r.ConfirmedPaymentsWithoutOutcome == 0 &&
		r.WalletBalanceMismatches == 0 && r.ReservationCountMismatches == 0 && r.ReviewCasesWithoutReason == 0
}

type FinancialReconciliationRepository interface {
	ReconcileFinancialState(context.Context) (FinancialReconciliation, error)
}

type DeliveryReconciliation struct {
	DeliveringWithoutJob            int64
	ActiveJobWrongOrderState        int64
	CompletedJobOrderNotDelivered   int64
	DeliveredInventoryMismatch      int64
	SoldWithoutCompletedJob         int64
	DeliveredOrderReservedInventory int64
	MultipleActiveJobs              int64
	StaleProcessing                 int64
	AmbiguousWithoutReview          int64
	SuccessEvidenceNotCompleted     int64
}

func (r DeliveryReconciliation) Clean() bool {
	return r.DeliveringWithoutJob == 0 && r.ActiveJobWrongOrderState == 0 &&
		r.CompletedJobOrderNotDelivered == 0 && r.DeliveredInventoryMismatch == 0 &&
		r.SoldWithoutCompletedJob == 0 && r.DeliveredOrderReservedInventory == 0 &&
		r.MultipleActiveJobs == 0 && r.StaleProcessing == 0 &&
		r.AmbiguousWithoutReview == 0 && r.SuccessEvidenceNotCompleted == 0
}

type DeliveryReconciliationRepository interface {
	ReconcileDeliveryState(context.Context, time.Time) (DeliveryReconciliation, error)
}

type ReconciliationService struct {
	repository FinancialReconciliationRepository
}

func NewReconciliationService(repository FinancialReconciliationRepository) *ReconciliationService {
	return &ReconciliationService{repository: repository}
}

func (s *ReconciliationService) Run(ctx context.Context) (FinancialReconciliation, error) {
	report, err := s.repository.ReconcileFinancialState(ctx)
	if err != nil {
		return FinancialReconciliation{}, fmt.Errorf("reconcile financial state: %w", err)
	}
	return report, nil
}

func (s *ReconciliationService) RunDelivery(ctx context.Context, staleBefore time.Time) (DeliveryReconciliation, error) {
	repository, ok := s.repository.(DeliveryReconciliationRepository)
	if !ok || staleBefore.IsZero() {
		return DeliveryReconciliation{}, ErrInvalidInput
	}
	report, err := repository.ReconcileDeliveryState(ctx, staleBefore)
	if err != nil {
		return DeliveryReconciliation{}, fmt.Errorf("reconcile delivery state: %w", err)
	}
	return report, nil
}
