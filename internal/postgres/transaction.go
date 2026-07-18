package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nvawntien/telegram-bot/internal/postgres/generated"
)

const rollbackTimeout = 5 * time.Second

// TransactionFunc contains database work that must commit or roll back as one
// unit. The supplied query set is bound to the transaction.
type TransactionFunc func(context.Context, *generated.Queries) error

// Transactor owns PostgreSQL transaction lifecycle without hiding business
// decisions inside the infrastructure layer.
type Transactor struct {
	pool *pgxpool.Pool
}

func NewTransactor(pool *pgxpool.Pool) *Transactor {
	return &Transactor{pool: pool}
}

// WithinTransaction executes fn using PostgreSQL default transaction options.
func (t *Transactor) WithinTransaction(ctx context.Context, fn TransactionFunc) error {
	return t.WithinTransactionOptions(ctx, pgx.TxOptions{}, fn)
}

// WithinTransactionOptions executes fn with explicit transaction options. A
// callback error is returned unchanged when rollback succeeds. Panics trigger a
// best-effort rollback and are re-panicked with their original value.
func (t *Transactor) WithinTransactionOptions(
	ctx context.Context,
	options pgx.TxOptions,
	fn TransactionFunc,
) (err error) {
	tx, err := t.pool.BeginTx(ctx, options)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}

	committed := false
	defer func() {
		if recovered := recover(); recovered != nil {
			_ = rollbackTransaction(ctx, tx)
			panic(recovered)
		}
		if committed {
			return
		}
		rollbackErr := rollbackTransaction(ctx, tx)
		if rollbackErr == nil || errors.Is(rollbackErr, pgx.ErrTxClosed) {
			return
		}
		wrappedRollbackErr := fmt.Errorf("rollback transaction: %w", rollbackErr)
		if err == nil {
			err = wrappedRollbackErr
			return
		}
		err = errors.Join(err, wrappedRollbackErr)
	}()

	if err = fn(ctx, generated.New(tx)); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	committed = true
	return nil
}

func rollbackTransaction(ctx context.Context, tx pgx.Tx) error {
	rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), rollbackTimeout)
	defer cancel()
	return tx.Rollback(rollbackCtx)
}
