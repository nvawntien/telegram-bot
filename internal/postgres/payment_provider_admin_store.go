package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/nvawntien/telegram-bot/internal/app"
	"github.com/nvawntien/telegram-bot/internal/postgres/generated"
)

func (s *AppStore) ListPaymentProviderAccounts(ctx context.Context, offset, limit int32) ([]app.PaymentProviderAccount, int64, error) {
	total, err := s.queries.CountPaymentProviderAccounts(ctx)
	if err != nil {
		return nil, 0, err
	}
	rows, err := s.queries.ListPaymentProviderAccounts(ctx, generated.ListPaymentProviderAccountsParams{PageOffset: offset, PageLimit: limit})
	if err != nil {
		return nil, 0, err
	}
	items := make([]app.PaymentProviderAccount, 0, len(rows))
	for _, row := range rows {
		items = append(items, mapPaymentProviderAccountView(
			row.ID, row.Provider, row.Environment, row.ExternalAccountIdentity, row.LocalBankAccountID,
			row.BankDisplayName, row.BankLast4, row.Status, row.Version, row.CreatedAt.Time, row.UpdatedAt.Time,
		))
	}
	return items, total, nil
}

func (s *AppStore) CreatePaymentProviderAccount(ctx context.Context, admin app.Admin, session app.AdminSession, command app.CreatePaymentProviderAccountCommand, fingerprint []byte) (app.PaymentProviderAccount, error) {
	var result app.PaymentProviderAccount
	err := s.transactor.WithinTransaction(ctx, func(ctx context.Context, queries *generated.Queries) error {
		if err := requireCatalogAdmin(ctx, queries, admin); err != nil {
			return err
		}
		if _, err := lockAndVerifySession(ctx, queries, admin.ID, session, app.SessionProviderAccountCreate); err != nil {
			return err
		}
		bank, err := queries.LockBankAccountByID(ctx, command.LocalBankAccountID)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && !bank.IsActive) {
			return app.ErrBankAccountInactive
		}
		if err != nil {
			return err
		}
		if bank.PaymentEnvironment != command.Environment {
			return app.ErrInvalidInput
		}
		row, err := queries.CreatePaymentProviderAccount(ctx, generated.CreatePaymentProviderAccountParams{
			Provider: command.Provider, Environment: command.Environment,
			ExternalAccountIdentity:     command.ExternalAccountIdentity,
			ExternalIdentityFingerprint: fingerprint, LocalBankAccountID: bank.ID,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return app.ErrConflict
		}
		if err != nil {
			return err
		}
		result = mapPaymentProviderAccountView(
			row.ID, row.Provider, row.Environment, row.ExternalAccountIdentity, row.LocalBankAccountID,
			bank.DisplayName, bank.DisplayLast4, row.Status, row.Version, row.CreatedAt.Time, row.UpdatedAt.Time,
		)
		if err := insertAudit(ctx, queries, admin.ID, "payment_provider_account.created", "payment_provider_account", row.ID, nil, providerAccountAuditSnapshot(result), command.Meta); err != nil {
			return err
		}
		if err := finishSession(ctx, queries, admin.ID, session); err != nil {
			return err
		}
		return completeReceipt(ctx, queries, command.Meta.UpdateID)
	})
	return result, mapConstraintError(err)
}

func (s *AppStore) SetPaymentProviderAccountStatus(ctx context.Context, admin app.Admin, session app.AdminSession, command app.SetPaymentProviderAccountStatusCommand) (app.PaymentProviderAccount, error) {
	var result app.PaymentProviderAccount
	err := s.transactor.WithinTransaction(ctx, func(ctx context.Context, queries *generated.Queries) error {
		if err := requireCatalogAdmin(ctx, queries, admin); err != nil {
			return err
		}
		if _, err := lockAndVerifySession(ctx, queries, admin.ID, session, app.SessionProviderAccountToggle); err != nil {
			return err
		}
		before, err := queries.LockPaymentProviderAccount(ctx, command.MappingID)
		if errors.Is(err, pgx.ErrNoRows) {
			return app.ErrNotFound
		}
		if err != nil {
			return err
		}
		if before.Version != command.ExpectedVersion || (before.Status == "active") == command.Active {
			return app.ErrStaleVersion
		}
		status := "inactive"
		if command.Active {
			status = "active"
		}
		after, err := queries.SetPaymentProviderAccountStatusGuarded(ctx, generated.SetPaymentProviderAccountStatusGuardedParams{
			Status: status, ID: before.ID, ExpectedVersion: command.ExpectedVersion,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return app.ErrStaleVersion
		}
		if err != nil {
			return err
		}
		bank, err := queries.LockBankAccountByID(ctx, after.LocalBankAccountID)
		if err != nil {
			return err
		}
		beforeView := mapPaymentProviderAccountView(
			before.ID, before.Provider, before.Environment, before.ExternalAccountIdentity, before.LocalBankAccountID,
			bank.DisplayName, bank.DisplayLast4, before.Status, before.Version, before.CreatedAt.Time, before.UpdatedAt.Time,
		)
		result = mapPaymentProviderAccountView(
			after.ID, after.Provider, after.Environment, after.ExternalAccountIdentity, after.LocalBankAccountID,
			bank.DisplayName, bank.DisplayLast4, after.Status, after.Version, after.CreatedAt.Time, after.UpdatedAt.Time,
		)
		action := "payment_provider_account.deactivated"
		if command.Active {
			action = "payment_provider_account.activated"
		}
		if err := insertAudit(ctx, queries, admin.ID, action, "payment_provider_account", after.ID, providerAccountAuditSnapshot(beforeView), providerAccountAuditSnapshot(result), command.Meta); err != nil {
			return err
		}
		if err := finishSession(ctx, queries, admin.ID, session); err != nil {
			return err
		}
		return completeReceipt(ctx, queries, command.Meta.UpdateID)
	})
	return result, mapConstraintError(err)
}

func (s *AppStore) GetPaymentProviderHealth(ctx context.Context, provider, environment string) (app.PaymentProviderHealth, error) {
	row, err := s.queries.GetPaymentProviderHealth(ctx, generated.GetPaymentProviderHealthParams{Provider: provider, Environment: environment})
	if err != nil {
		return app.PaymentProviderHealth{}, err
	}
	return app.PaymentProviderHealth{
		ActiveMappings: row.ActiveMappings, LastWebhookAt: row.LastWebhookAt.Time,
		LastReconciliationAttempt: row.LastReconciliationAttempt.Time,
		LastReconciliationSuccess: row.LastReconciliationSuccess.Time,
		LastErrorCode:             row.LastErrorCode.String, LastTransactionAt: row.LastTransactionAt.Time,
		PendingEvents: row.PendingEvents, OpenReviews: row.OpenReviews,
	}, nil
}

func mapPaymentProviderAccountView(id int64, provider, environment, externalIdentity string, localBankID int64, bankName, bankLast4, status string, version int64, createdAt, updatedAt time.Time) app.PaymentProviderAccount {
	return app.PaymentProviderAccount{
		ID: id, Provider: provider, Environment: environment,
		MaskedExternalIdentity: maskProviderAccountIdentity(externalIdentity),
		LocalBankAccountID:     localBankID, LocalBankDisplayName: bankName, LocalBankLast4: bankLast4,
		Status: status, Version: version, CreatedAt: createdAt, UpdatedAt: updatedAt,
	}
}

func maskProviderAccountIdentity(value string) string {
	if len(value) <= 4 {
		return "***"
	}
	return "***" + value[len(value)-4:]
}

func providerAccountAuditSnapshot(value app.PaymentProviderAccount) map[string]any {
	return map[string]any{
		"id": value.ID, "provider": value.Provider, "environment": value.Environment,
		"external_identity": value.MaskedExternalIdentity, "local_bank_account_id": value.LocalBankAccountID,
		"status": value.Status, "version": value.Version,
	}
}

var _ app.PaymentProviderAdminRepository = (*AppStore)(nil)
