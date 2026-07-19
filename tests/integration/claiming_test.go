//go:build integration

package integration_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/nvawntien/telegram-bot/internal/postgres"
	"github.com/nvawntien/telegram-bot/internal/postgres/generated"
)

func TestClaimAvailableInventory(t *testing.T) {
	database := newTestDatabase(t, true)
	ctx := context.Background()
	user := database.createUser(t)
	categoryID := database.createCategory(t)
	orderOne := database.createOrder(t, user.ID)
	orderTwo := database.createOrder(t, user.ID)
	transactor := postgres.NewTransactor(database.pool)

	t.Run("returns exact quantity when available", func(t *testing.T) {
		productID := database.createProduct(t, categoryID)
		for range 3 {
			database.createInventory(t, productID, "available", pgtype.Int8{}, pgtype.Timestamptz{}, pgtype.Int8{})
		}

		err := transactor.WithinTransaction(ctx, func(ctx context.Context, queries *generated.Queries) error {
			items, err := queries.ClaimAvailableInventory(ctx, claimInventoryParams(productID, orderOne.ID, 2))
			if err != nil {
				return err
			}
			if len(items) != 2 {
				t.Fatalf("claimed items = %d, want 2", len(items))
			}
			return nil
		})
		if err != nil {
			t.Fatalf("claim transaction: %v", err)
		}
		remaining, err := database.queries.CountAvailableInventoryByProduct(ctx, productID)
		if err != nil {
			t.Fatalf("count available inventory: %v", err)
		}
		if remaining != 1 {
			t.Fatalf("available inventory = %d, want 1", remaining)
		}
	})

	t.Run("caller detects shortage and rolls back partial claim", func(t *testing.T) {
		productID := database.createProduct(t, categoryID)
		database.createInventory(t, productID, "available", pgtype.Int8{}, pgtype.Timestamptz{}, pgtype.Int8{})
		insufficient := errors.New("insufficient inventory")

		err := transactor.WithinTransaction(ctx, func(ctx context.Context, queries *generated.Queries) error {
			items, err := queries.ClaimAvailableInventory(ctx, claimInventoryParams(productID, orderOne.ID, 2))
			if err != nil {
				return err
			}
			if len(items) != 2 {
				return insufficient
			}
			return nil
		})
		if !errors.Is(err, insufficient) {
			t.Fatalf("claim transaction error = %v, want shortage", err)
		}
		remaining, err := database.queries.CountAvailableInventoryByProduct(ctx, productID)
		if err != nil {
			t.Fatalf("count inventory after rollback: %v", err)
		}
		if remaining != 1 {
			t.Fatalf("available inventory after rollback = %d, want 1", remaining)
		}
	})

	t.Run("disabled and sold inventory are excluded", func(t *testing.T) {
		productID := database.createProduct(t, categoryID)
		database.createInventory(t, productID, "disabled", pgtype.Int8{}, pgtype.Timestamptz{}, pgtype.Int8{})
		database.createInventory(t, productID, "sold", pgtype.Int8{}, pgtype.Timestamptz{}, nullableOrderID(orderOne.ID))

		items, err := database.queries.ClaimAvailableInventory(ctx, claimInventoryParams(productID, orderTwo.ID, 5))
		if err != nil {
			t.Fatalf("claim excluded inventory: %v", err)
		}
		if len(items) != 0 {
			t.Fatalf("claimed excluded items = %d, want 0", len(items))
		}
	})

	t.Run("two transactions cannot claim the same item", func(t *testing.T) {
		productID := database.createProduct(t, categoryID)
		inventoryID := database.createInventory(t, productID, "available", pgtype.Int8{}, pgtype.Timestamptz{}, pgtype.Int8{})

		firstTx, err := database.pool.BeginTx(ctx, pgx.TxOptions{})
		if err != nil {
			t.Fatalf("begin first transaction: %v", err)
		}
		defer func() { _ = firstTx.Rollback(context.Background()) }()
		firstItems, err := generated.New(firstTx).ClaimAvailableInventory(ctx, claimInventoryParams(productID, orderOne.ID, 1))
		if err != nil {
			t.Fatalf("first worker claim: %v", err)
		}
		if len(firstItems) != 1 || firstItems[0] != inventoryID {
			t.Fatalf("first worker claimed %#v, want inventory %d", firstItems, inventoryID)
		}

		secondTx, err := database.pool.BeginTx(ctx, pgx.TxOptions{})
		if err != nil {
			t.Fatalf("begin second transaction: %v", err)
		}
		defer func() { _ = secondTx.Rollback(context.Background()) }()
		secondItems, err := generated.New(secondTx).ClaimAvailableInventory(ctx, claimInventoryParams(productID, orderTwo.ID, 1))
		if err != nil {
			t.Fatalf("second worker claim: %v", err)
		}
		if len(secondItems) != 0 {
			t.Fatalf("second worker claimed locked inventory: %#v", secondItems)
		}
		if err := firstTx.Commit(ctx); err != nil {
			t.Fatalf("commit first transaction: %v", err)
		}
		if err := secondTx.Commit(ctx); err != nil {
			t.Fatalf("commit second transaction: %v", err)
		}
	})
}

func TestClaimPendingOutboxEvents(t *testing.T) {
	database := newTestDatabase(t, true)
	ctx := context.Background()
	now := time.Now()

	due, err := database.queries.InsertOutboxEvent(ctx, generated.InsertOutboxEventParams{
		EventType:        "test.outbox_due",
		AggregateType:    "test",
		AggregateID:      1,
		DeduplicationKey: "outbox-due",
		Payload:          []byte(`{"order_id":1}`),
		MaxAttempts:      5,
		NextAttemptAt:    pgtype.Timestamptz{Time: now.Add(-time.Minute), Valid: true},
	})
	if err != nil {
		t.Fatalf("insert due outbox event: %v", err)
	}
	if _, err := database.queries.InsertOutboxEvent(ctx, generated.InsertOutboxEventParams{
		EventType:        "test.outbox_future",
		AggregateType:    "test",
		AggregateID:      2,
		DeduplicationKey: "outbox-future",
		Payload:          []byte(`{"order_id":2}`),
		MaxAttempts:      5,
		NextAttemptAt:    pgtype.Timestamptz{Time: now.Add(time.Hour), Valid: true},
	}); err != nil {
		t.Fatalf("insert future outbox event: %v", err)
	}

	firstTx, err := database.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		t.Fatalf("begin first outbox transaction: %v", err)
	}
	defer func() { _ = firstTx.Rollback(context.Background()) }()
	firstEvents, err := generated.New(firstTx).ClaimPendingOutboxEvents(ctx, claimOutboxParams("worker-a", 1))
	if err != nil {
		t.Fatalf("first worker claim: %v", err)
	}
	if len(firstEvents) != 1 || firstEvents[0].ID != due.ID {
		t.Fatalf("first worker events = %#v, want event %d", firstEvents, due.ID)
	}

	secondTx, err := database.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		t.Fatalf("begin second outbox transaction: %v", err)
	}
	defer func() { _ = secondTx.Rollback(context.Background()) }()
	secondEvents, err := generated.New(secondTx).ClaimPendingOutboxEvents(ctx, claimOutboxParams("worker-b", 10))
	if err != nil {
		t.Fatalf("second worker claim: %v", err)
	}
	if len(secondEvents) != 0 {
		t.Fatalf("second worker claimed locked or future event: %#v", secondEvents)
	}
	if err := firstTx.Commit(ctx); err != nil {
		t.Fatalf("commit first outbox transaction: %v", err)
	}
	if err := secondTx.Commit(ctx); err != nil {
		t.Fatalf("commit second outbox transaction: %v", err)
	}

	completed, err := database.queries.MarkOutboxEventCompleted(ctx, generated.MarkOutboxEventCompletedParams{
		ID:       due.ID,
		WorkerID: pgtype.Text{String: "worker-a", Valid: true},
	})
	if err != nil {
		t.Fatalf("complete outbox event: %v", err)
	}
	if completed.Status != "completed" {
		t.Fatalf("completed event status = %s", completed.Status)
	}

	afterCompletion, err := database.queries.ClaimPendingOutboxEvents(ctx, claimOutboxParams("worker-c", 10))
	if err != nil {
		t.Fatalf("claim after completion: %v", err)
	}
	if len(afterCompletion) != 0 {
		t.Fatalf("claimed completed or future event: %#v", afterCompletion)
	}
}

func claimInventoryParams(productID, orderID int64, quantity int32) generated.ClaimAvailableInventoryParams {
	return generated.ClaimAvailableInventoryParams{
		ProductID:     productID,
		OrderID:       pgtype.Int8{Int64: orderID, Valid: true},
		Quantity:      quantity,
		ReservedUntil: pgtype.Timestamptz{Time: time.Now().Add(15 * time.Minute), Valid: true},
	}
}

func claimOutboxParams(workerID string, batchSize int32) generated.ClaimPendingOutboxEventsParams {
	return generated.ClaimPendingOutboxEventsParams{
		WorkerID:  pgtype.Text{String: workerID, Valid: true},
		BatchSize: batchSize,
	}
}
