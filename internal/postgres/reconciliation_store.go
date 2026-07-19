package postgres

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/nvawntien/telegram-bot/internal/app"
)

func (s *AppStore) ReconcileFinancialState(ctx context.Context) (app.FinancialReconciliation, error) {
	row, err := s.queries.GetFinancialReconciliation(ctx)
	if err != nil {
		return app.FinancialReconciliation{}, err
	}
	return app.FinancialReconciliation{
		CompletedEventsWithoutPayment:   row.CompletedEventsWithoutPayment,
		ConfirmedPaymentsWithoutOutcome: row.ConfirmedPaymentsWithoutOutcome,
		WalletBalanceMismatches:         row.WalletBalanceMismatches,
		ReservationCountMismatches:      row.ReservationCountMismatches,
		ReviewCasesWithoutReason:        row.ReviewCasesWithoutReason,
	}, nil
}

func (s *AppStore) ReconcileDeliveryState(ctx context.Context, staleBefore time.Time) (app.DeliveryReconciliation, error) {
	row, err := s.queries.CountDeliveryReconciliationAnomalies(ctx, pgtype.Timestamptz{Time: staleBefore, Valid: true})
	if err != nil {
		return app.DeliveryReconciliation{}, err
	}
	return app.DeliveryReconciliation{
		DeliveringWithoutJob:            row.DeliveringWithoutJob,
		ActiveJobWrongOrderState:        row.ActiveJobWrongOrderState,
		CompletedJobOrderNotDelivered:   row.CompletedJobOrderNotDelivered,
		DeliveredInventoryMismatch:      row.DeliveredInventoryMismatch,
		SoldWithoutCompletedJob:         row.SoldWithoutCompletedJob,
		DeliveredOrderReservedInventory: row.DeliveredOrderReservedInventory,
		MultipleActiveJobs:              row.MultipleActiveJobs,
		StaleProcessing:                 row.StaleProcessing,
		AmbiguousWithoutReview:          row.AmbiguousWithoutReview,
		SuccessEvidenceNotCompleted:     row.SuccessEvidenceNotCompleted,
	}, nil
}

var _ app.FinancialReconciliationRepository = (*AppStore)(nil)
var _ app.DeliveryReconciliationRepository = (*AppStore)(nil)
