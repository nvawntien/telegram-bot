//go:build integration

package integration_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/nvawntien/telegram-bot/internal/postgres"
	"github.com/nvawntien/telegram-bot/internal/postgres/generated"
)

func TestTransactorCommitAndRollback(t *testing.T) {
	database := newTestDatabase(t, true)
	ctx := context.Background()
	transactor := postgres.NewTransactor(database.pool)

	t.Run("commit success", func(t *testing.T) {
		const telegramID int64 = 7_000_001
		err := transactor.WithinTransactionOptions(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(
			ctx context.Context,
			queries *generated.Queries,
		) error {
			_, err := queries.UpsertTelegramUser(ctx, generated.UpsertTelegramUserParams{
				TelegramUserID: telegramID,
				Username:       pgtype.Text{String: "committed", Valid: true},
			})
			return err
		})
		if err != nil {
			t.Fatalf("WithinTransactionOptions() error = %v", err)
		}
		if _, err := database.queries.GetUserByTelegramID(ctx, telegramID); err != nil {
			t.Fatalf("committed user not found: %v", err)
		}
	})

	t.Run("callback error rolls back and remains wrapped", func(t *testing.T) {
		const telegramID int64 = 7_000_002
		sentinel := errors.New("intentional rollback")
		err := transactor.WithinTransaction(ctx, func(ctx context.Context, queries *generated.Queries) error {
			if _, err := queries.UpsertTelegramUser(ctx, generated.UpsertTelegramUserParams{
				TelegramUserID: telegramID,
			}); err != nil {
				return err
			}
			return fmt.Errorf("business operation failed: %w", sentinel)
		})
		if !errors.Is(err, sentinel) {
			t.Fatalf("WithinTransaction() error = %v, want wrapped sentinel", err)
		}
		_, err = database.queries.GetUserByTelegramID(ctx, telegramID)
		if !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("rolled-back user lookup error = %v, want pgx.ErrNoRows", err)
		}
	})
}

func TestTransactorRollsBackAndRepanics(t *testing.T) {
	database := newTestDatabase(t, true)
	ctx := context.Background()
	transactor := postgres.NewTransactor(database.pool)
	const telegramID int64 = 7_000_003
	panicValue := "intentional panic"

	var recovered any
	func() {
		defer func() { recovered = recover() }()
		_ = transactor.WithinTransaction(ctx, func(ctx context.Context, queries *generated.Queries) error {
			if _, err := queries.UpsertTelegramUser(ctx, generated.UpsertTelegramUserParams{
				TelegramUserID: telegramID,
			}); err != nil {
				return err
			}
			panic(panicValue)
		})
	}()

	if recovered != panicValue {
		t.Fatalf("recovered panic = %#v, want %#v", recovered, panicValue)
	}
	_, err := database.queries.GetUserByTelegramID(ctx, telegramID)
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("panic transaction user lookup error = %v, want pgx.ErrNoRows", err)
	}
}
