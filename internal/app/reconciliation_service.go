package app

import (
	"context"
	"fmt"
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
