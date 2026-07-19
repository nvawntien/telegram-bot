package postgres

import (
	"context"

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

var _ app.FinancialReconciliationRepository = (*AppStore)(nil)
