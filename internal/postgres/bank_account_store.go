package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/nvawntien/telegram-bot/internal/app"
	"github.com/nvawntien/telegram-bot/internal/postgres/generated"
)

func (s *AppStore) ListActiveBankAccounts(ctx context.Context, environment string) ([]app.BankAccountOption, error) {
	rows, err := s.queries.ListActiveBankAccountOptions(ctx, environment)
	if err != nil {
		return nil, err
	}
	items := make([]app.BankAccountOption, 0, len(rows))
	for _, row := range rows {
		items = append(items, app.BankAccountOption{
			ID: row.ID, BankBIN: row.BankBin, BankName: row.BankName,
			DisplayName: row.DisplayName, AccountName: row.AccountName,
			Last4: row.DisplayLast4, SortOrder: row.SortOrder, Version: row.Version, PaymentEnvironment: row.PaymentEnvironment,
		})
	}
	return items, nil
}

func (s *AppStore) ListAdminBankAccounts(ctx context.Context, offset, limit int32) ([]app.RedactedBankAccount, int64, error) {
	total, err := s.queries.CountAdminBankAccounts(ctx)
	if err != nil {
		return nil, 0, err
	}
	rows, err := s.queries.ListAdminBankAccountsPage(ctx, generated.ListAdminBankAccountsPageParams{PageOffset: offset, PageLimit: limit})
	if err != nil {
		return nil, 0, err
	}
	items := make([]app.RedactedBankAccount, 0, len(rows))
	for _, row := range rows {
		items = append(items, app.RedactedBankAccount{
			BankAccountOption: app.BankAccountOption{
				ID: row.ID, BankBIN: row.BankBin, BankName: row.BankName,
				DisplayName: row.DisplayName, AccountName: row.AccountName,
				Last4: row.DisplayLast4, SortOrder: row.SortOrder, Version: row.Version, PaymentEnvironment: row.PaymentEnvironment,
			},
			Active: row.IsActive, KeyVersion: row.EncryptionKeyVersion,
			Format: row.EncryptionFormat, CreatedAt: row.CreatedAt.Time,
		})
	}
	return items, total, nil
}

func (s *AppStore) CreateBankAccount(
	ctx context.Context,
	admin app.Admin,
	session app.AdminSession,
	input app.CreateBankAccountInput,
	protected app.ProtectedBankAccountNumber,
) (app.RedactedBankAccount, error) {
	var result app.RedactedBankAccount
	err := s.transactor.WithinTransaction(ctx, func(ctx context.Context, queries *generated.Queries) error {
		if err := requireCatalogAdmin(ctx, queries, admin); err != nil {
			return err
		}
		if _, err := lockAndVerifySession(ctx, queries, admin.ID, session, app.SessionBankCreate); err != nil {
			return err
		}
		row, err := queries.CreateEncryptedBankAccount(ctx, generated.CreateEncryptedBankAccountParams{
			BankBin: input.BankBIN, BankName: input.BankName, DisplayName: input.DisplayName,
			AccountName: input.AccountName, EncryptedAccountNumber: protected.Ciphertext,
			AccountNumberFingerprint: protected.Fingerprint, EncryptionKeyID: protected.KeyID,
			EncryptionNonce: protected.Nonce, EncryptionKeyVersion: protected.KeyVersion,
			DisplayLast4: last4(input.AccountNumber), SortOrder: input.SortOrder,
			PaymentEnvironment: input.PaymentEnvironment,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return app.ErrConflict
		}
		if err != nil {
			return err
		}
		result = mapRedactedBank(row)
		if err := insertAudit(ctx, queries, admin.ID, "bank_account.created", "bank_account", row.ID, nil, bankAuditSnapshot(result), input.Meta); err != nil {
			return err
		}
		if err := finishSession(ctx, queries, admin.ID, session); err != nil {
			return err
		}
		return completeReceipt(ctx, queries, input.Meta.UpdateID)
	})
	return result, mapConstraintError(err)
}

func (s *AppStore) UpdateBankAccount(
	ctx context.Context,
	admin app.Admin,
	session app.AdminSession,
	input app.UpdateBankAccountInput,
	protected app.ProtectedBankAccountNumber,
) (app.RedactedBankAccount, error) {
	var result app.RedactedBankAccount
	err := s.transactor.WithinTransaction(ctx, func(ctx context.Context, queries *generated.Queries) error {
		if err := requireCatalogAdmin(ctx, queries, admin); err != nil {
			return err
		}
		if _, err := lockAndVerifySession(ctx, queries, admin.ID, session, app.SessionBankEdit); err != nil {
			return err
		}
		before, err := queries.LockBankAccountByID(ctx, input.BankAccountID)
		if errors.Is(err, pgx.ErrNoRows) {
			return app.ErrBankAccountNotFound
		}
		if err != nil {
			return err
		}
		if before.Version != input.ExpectedRecord {
			return app.ErrStaleVersion
		}
		after, err := queries.UpdateEncryptedBankAccountGuarded(ctx, generated.UpdateEncryptedBankAccountGuardedParams{
			BankBin: input.BankBIN, BankName: input.BankName, DisplayName: input.DisplayName,
			AccountName: input.AccountName, EncryptedAccountNumber: protected.Ciphertext,
			AccountNumberFingerprint: protected.Fingerprint, EncryptionKeyID: protected.KeyID,
			EncryptionNonce: protected.Nonce, EncryptionKeyVersion: protected.KeyVersion,
			DisplayLast4: last4(input.AccountNumber), SortOrder: input.SortOrder,
			ID: input.BankAccountID, ExpectedVersion: input.ExpectedRecord,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return app.ErrStaleVersion
		}
		if err != nil {
			return err
		}
		beforeView := mapRedactedBank(before)
		result = mapRedactedBank(after)
		if err := insertAudit(ctx, queries, admin.ID, "bank_account.updated", "bank_account", after.ID, bankAuditSnapshot(beforeView), bankAuditSnapshot(result), input.Meta); err != nil {
			return err
		}
		if err := finishSession(ctx, queries, admin.ID, session); err != nil {
			return err
		}
		return completeReceipt(ctx, queries, input.Meta.UpdateID)
	})
	return result, mapConstraintError(err)
}

func (s *AppStore) SetBankAccountActive(
	ctx context.Context,
	admin app.Admin,
	session app.AdminSession,
	input app.SetBankAccountActiveInput,
) (app.RedactedBankAccount, error) {
	var result app.RedactedBankAccount
	err := s.transactor.WithinTransaction(ctx, func(ctx context.Context, queries *generated.Queries) error {
		if err := requireCatalogAdmin(ctx, queries, admin); err != nil {
			return err
		}
		if _, err := lockAndVerifySession(ctx, queries, admin.ID, session, app.SessionBankToggle); err != nil {
			return err
		}
		before, err := queries.LockBankAccountByID(ctx, input.BankAccountID)
		if errors.Is(err, pgx.ErrNoRows) {
			return app.ErrBankAccountNotFound
		}
		if err != nil {
			return err
		}
		if before.Version != input.ExpectedRecord || before.IsActive == input.Active {
			return app.ErrStaleVersion
		}
		after, err := queries.SetBankAccountActiveGuarded(ctx, generated.SetBankAccountActiveGuardedParams{
			IsActive: input.Active, ID: input.BankAccountID, ExpectedVersion: input.ExpectedRecord,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return app.ErrStaleVersion
		}
		if err != nil {
			return err
		}
		beforeView := mapRedactedBank(before)
		result = mapRedactedBank(after)
		action := "bank_account.deactivated"
		if input.Active {
			action = "bank_account.activated"
		}
		if err := insertAudit(ctx, queries, admin.ID, action, "bank_account", after.ID, bankAuditSnapshot(beforeView), bankAuditSnapshot(result), input.Meta); err != nil {
			return err
		}
		if err := finishSession(ctx, queries, admin.ID, session); err != nil {
			return err
		}
		return completeReceipt(ctx, queries, input.Meta.UpdateID)
	})
	return result, err
}

func mapRedactedBank(row generated.BankAccount) app.RedactedBankAccount {
	return app.RedactedBankAccount{
		BankAccountOption: app.BankAccountOption{
			ID: row.ID, BankBIN: row.BankBin, BankName: row.BankName,
			DisplayName: row.DisplayName, AccountName: row.AccountName,
			Last4: row.DisplayLast4, SortOrder: row.SortOrder, Version: row.Version, PaymentEnvironment: row.PaymentEnvironment,
		},
		Active: row.IsActive, KeyVersion: row.EncryptionKeyVersion,
		Format: row.EncryptionFormat, CreatedAt: row.CreatedAt.Time,
	}
}

func bankAuditSnapshot(value app.RedactedBankAccount) map[string]any {
	return map[string]any{
		"id": value.ID, "bank_bin": value.BankBIN, "bank_name": value.BankName,
		"display_name": value.DisplayName, "account_name": value.AccountName,
		"account_last4": value.Last4, "sort_order": value.SortOrder,
		"active": value.Active, "version": value.Version,
		"payment_environment": value.PaymentEnvironment,
	}
}

func last4(value string) string {
	if len(value) <= 4 {
		return value
	}
	return value[len(value)-4:]
}
