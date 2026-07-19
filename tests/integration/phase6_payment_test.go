//go:build integration

package integration_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/nvawntien/telegram-bot/internal/app"
	"github.com/nvawntien/telegram-bot/internal/domain"
	"github.com/nvawntien/telegram-bot/internal/postgres"
	"github.com/nvawntien/telegram-bot/internal/postgres/generated"
)

func TestPhase6ExactPaymentClaimsInventoryAtomically(t *testing.T) {
	database := newTestDatabase(t, true)
	order, productID := createPhase6PaymentOrder(t, database, 2)
	database.createInventory(t, productID, "available", pgtype.Int8{}, pgtype.Timestamptz{}, pgtype.Int8{})
	database.createInventory(t, productID, "available", pgtype.Int8{}, pgtype.Timestamptz{}, pgtype.Int8{})

	processPhase6Event(t, database, order, "transaction-success")

	var status, eventStatus string
	var claimed, paymentCount, allocationCount int
	if err := database.pool.QueryRow(context.Background(), `SELECT status FROM orders WHERE id=$1`, order.ID).Scan(&status); err != nil || status != "reserving" {
		t.Fatalf("order status = %q, err=%v", status, err)
	}
	if err := database.pool.QueryRow(context.Background(), `SELECT processing_status FROM payment_events WHERE external_event_id=$1`, "event-transaction-success").Scan(&eventStatus); err != nil || eventStatus != "completed" {
		t.Fatalf("event status = %q, err=%v", eventStatus, err)
	}
	if err := database.pool.QueryRow(context.Background(), `SELECT count(*) FROM order_inventory_items WHERE order_id=$1 AND status='active'`, order.ID).Scan(&claimed); err != nil || claimed != 2 {
		t.Fatalf("claimed = %d, err=%v", claimed, err)
	}
	if err := database.pool.QueryRow(context.Background(), `SELECT count(*) FROM payments WHERE order_id=$1 AND status='confirmed'`, order.ID).Scan(&paymentCount); err != nil || paymentCount != 1 {
		t.Fatalf("payments = %d, err=%v", paymentCount, err)
	}
	if err := database.pool.QueryRow(context.Background(), `SELECT count(*) FROM payment_allocations WHERE target_type='order' AND target_id=$1`, order.ID).Scan(&allocationCount); err != nil || allocationCount != 1 {
		t.Fatalf("allocations = %d, err=%v", allocationCount, err)
	}
}

func TestPhase6ExternalPaymentOutOfStockPreservesEvidenceWithoutPartialClaim(t *testing.T) {
	database := newTestDatabase(t, true)
	order, productID := createPhase6PaymentOrder(t, database, 2)
	database.createInventory(t, productID, "available", pgtype.Int8{}, pgtype.Timestamptz{}, pgtype.Int8{})

	processPhase6Event(t, database, order, "transaction-out-of-stock")

	var status, eventStatus, paymentStatus, reason string
	var claimed int
	ctx := context.Background()
	if err := database.pool.QueryRow(ctx, `SELECT status FROM orders WHERE id=$1`, order.ID).Scan(&status); err != nil || status != "out_of_stock" {
		t.Fatalf("order status = %q, err=%v", status, err)
	}
	if err := database.pool.QueryRow(ctx, `SELECT processing_status FROM payment_events WHERE external_event_id=$1`, "event-transaction-out-of-stock").Scan(&eventStatus); err != nil || eventStatus != "review" {
		t.Fatalf("event status = %q, err=%v", eventStatus, err)
	}
	if err := database.pool.QueryRow(ctx, `SELECT status FROM payments WHERE provider_transaction_id=$1`, "transaction-out-of-stock").Scan(&paymentStatus); err != nil || paymentStatus != "confirmed" {
		t.Fatalf("payment status = %q, err=%v", paymentStatus, err)
	}
	if err := database.pool.QueryRow(ctx, `SELECT reason FROM payment_review_cases WHERE order_id=$1`, order.ID).Scan(&reason); err != nil || reason != "out_of_stock" {
		t.Fatalf("review reason = %q, err=%v", reason, err)
	}
	if err := database.pool.QueryRow(ctx, `SELECT count(*) FROM order_inventory_items WHERE order_id=$1`, order.ID).Scan(&claimed); err != nil || claimed != 0 {
		t.Fatalf("partial claims = %d, err=%v", claimed, err)
	}
	var inventoryStatus string
	if err := database.pool.QueryRow(ctx, `SELECT status FROM inventory_items WHERE product_id=$1`, productID).Scan(&inventoryStatus); err != nil || inventoryStatus != "available" {
		t.Fatalf("inventory status = %q, err=%v", inventoryStatus, err)
	}
}

func TestPhase6DuplicateEventAndTransactionAreIdempotent(t *testing.T) {
	database := newTestDatabase(t, true)
	order, productID := createPhase6PaymentOrder(t, database, 1)
	database.createInventory(t, productID, "available", pgtype.Int8{}, pgtype.Timestamptz{}, pgtype.Int8{})
	store := postgres.NewAppStore(database.pool)
	ingestion := app.NewPaymentEventIngestionService(store, 5)
	event := phase6Event(t, order, "transaction-duplicate")
	first, err := ingestion.Ingest(context.Background(), event)
	if err != nil || first.Duplicate {
		t.Fatalf("first ingest = %+v, err=%v", first, err)
	}
	second, err := ingestion.Ingest(context.Background(), event)
	if err != nil || !second.Duplicate || second.EventID != first.EventID {
		t.Fatalf("second ingest = %+v, err=%v", second, err)
	}
	job := app.NewPaymentEventJob(store, app.NewPaymentAcceptanceService(store, time.Hour, nil), 10, time.Millisecond, time.Minute)
	if count, err := job.RunOnce(context.Background()); err != nil || count != 1 {
		t.Fatalf("RunOnce() = %d, %v", count, err)
	}
	if count, err := job.RunOnce(context.Background()); err != nil || count != 0 {
		t.Fatalf("second RunOnce() = %d, %v", count, err)
	}
	var payments, mappings int
	if err := database.pool.QueryRow(context.Background(), `SELECT count(*) FROM payments WHERE provider_transaction_id=$1`, "transaction-duplicate").Scan(&payments); err != nil || payments != 1 {
		t.Fatalf("payments = %d, err=%v", payments, err)
	}
	if err := database.pool.QueryRow(context.Background(), `SELECT count(*) FROM order_inventory_items WHERE order_id=$1`, order.ID).Scan(&mappings); err != nil || mappings != 1 {
		t.Fatalf("mappings = %d, err=%v", mappings, err)
	}
}

func TestPhase6MismatchAndLatePaymentsRemainReviewOnly(t *testing.T) {
	database := newTestDatabase(t, true)
	order, productID := createPhase6PaymentOrder(t, database, 1)
	database.createInventory(t, productID, "available", pgtype.Int8{}, pgtype.Timestamptz{}, pgtype.Int8{})
	mismatch := phase6Event(t, order, "transaction-mismatch")
	mismatch.Amount++
	metadata, _ := json.Marshal(map[string]any{"reference": mismatch.Reference, "amount_vnd": mismatch.Amount.Int64(), "currency": "VND", "occurred_at": mismatch.OccurredAt})
	mismatch.SanitizedMetadata = metadata
	store := postgres.NewAppStore(database.pool)
	if _, err := app.NewPaymentEventIngestionService(store, 5).Ingest(context.Background(), mismatch); err != nil {
		t.Fatal(err)
	}
	job := app.NewPaymentEventJob(store, app.NewPaymentAcceptanceService(store, time.Hour, nil), 10, time.Millisecond, time.Minute)
	if _, err := job.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	var status, reason string
	var mappings int
	ctx := context.Background()
	_ = database.pool.QueryRow(ctx, `SELECT status FROM orders WHERE id=$1`, order.ID).Scan(&status)
	_ = database.pool.QueryRow(ctx, `SELECT reason FROM payment_review_cases WHERE order_id=$1`, order.ID).Scan(&reason)
	_ = database.pool.QueryRow(ctx, `SELECT count(*) FROM order_inventory_items WHERE order_id=$1`, order.ID).Scan(&mappings)
	if status != "pending_payment" || reason != "amount_mismatch" || mappings != 0 {
		t.Fatalf("mismatch status=%s reason=%s mappings=%d", status, reason, mappings)
	}

	lateOrder, lateProductID := createPhase6PaymentOrder(t, database, 1)
	database.createInventory(t, lateProductID, "available", pgtype.Int8{}, pgtype.Timestamptz{}, pgtype.Int8{})
	if _, err := database.pool.Exec(ctx, `UPDATE orders SET status='expired',expires_at=clock_timestamp()-interval '1 minute' WHERE id=$1`, lateOrder.ID); err != nil {
		t.Fatal(err)
	}
	processPhase6Event(t, database, lateOrder, "transaction-late")
	_ = database.pool.QueryRow(ctx, `SELECT status FROM orders WHERE id=$1`, lateOrder.ID).Scan(&status)
	_ = database.pool.QueryRow(ctx, `SELECT reason FROM payment_review_cases WHERE order_id=$1`, lateOrder.ID).Scan(&reason)
	_ = database.pool.QueryRow(ctx, `SELECT count(*) FROM order_inventory_items WHERE order_id=$1`, lateOrder.ID).Scan(&mappings)
	if status != "expired" && status != "payment_review" {
		t.Fatalf("late order status=%s", status)
	}
	if reason != "late_or_cancelled" || mappings != 0 {
		t.Fatalf("late reason=%s mappings=%d", reason, mappings)
	}
}

func TestPhase6CompetingPaymentsAndWorkersSettleOnlyOnce(t *testing.T) {
	database := newTestDatabase(t, true)
	order, productID := createPhase6PaymentOrder(t, database, 1)
	database.createInventory(t, productID, "available", pgtype.Int8{}, pgtype.Timestamptz{}, pgtype.Int8{})
	store := postgres.NewAppStore(database.pool)
	ingestion := app.NewPaymentEventIngestionService(store, 5)
	for _, transactionID := range []string{"competing-one", "competing-two"} {
		if _, err := ingestion.Ingest(context.Background(), phase6Event(t, order, transactionID)); err != nil {
			t.Fatal(err)
		}
	}
	acceptance := app.NewPaymentAcceptanceService(store, time.Hour, nil)
	jobs := []*app.PaymentEventJob{
		app.NewPaymentEventJob(store, acceptance, 1, time.Millisecond, time.Minute),
		app.NewPaymentEventJob(store, acceptance, 1, time.Millisecond, time.Minute),
	}
	var wait sync.WaitGroup
	errorsByWorker := make(chan error, len(jobs))
	for _, job := range jobs {
		wait.Add(1)
		go func(job *app.PaymentEventJob) {
			defer wait.Done()
			_, err := job.RunOnce(context.Background())
			errorsByWorker <- err
		}(job)
	}
	wait.Wait()
	close(errorsByWorker)
	for err := range errorsByWorker {
		if err != nil {
			t.Fatal(err)
		}
	}
	var allocations, mappings, payments, reviews int
	ctx := context.Background()
	_ = database.pool.QueryRow(ctx, `SELECT count(*) FROM payment_allocations WHERE target_type='order' AND target_id=$1`, order.ID).Scan(&allocations)
	_ = database.pool.QueryRow(ctx, `SELECT count(*) FROM order_inventory_items WHERE order_id=$1 AND status='active'`, order.ID).Scan(&mappings)
	_ = database.pool.QueryRow(ctx, `SELECT count(*) FROM payments WHERE order_id=$1`, order.ID).Scan(&payments)
	_ = database.pool.QueryRow(ctx, `SELECT count(*) FROM payment_review_cases WHERE order_id=$1 AND reason='competing_payment'`, order.ID).Scan(&reviews)
	if allocations != 1 || mappings != 1 || payments != 2 || reviews != 1 {
		t.Fatalf("allocations=%d mappings=%d payments=%d reviews=%d", allocations, mappings, payments, reviews)
	}
	reconciliation, err := app.NewReconciliationService(store).Run(ctx)
	if err != nil || !reconciliation.Clean() {
		t.Fatalf("reconciliation=%+v err=%v", reconciliation, err)
	}
}

func TestPhase6StalePaymentEventIsReclaimed(t *testing.T) {
	database := newTestDatabase(t, true)
	order, productID := createPhase6PaymentOrder(t, database, 1)
	database.createInventory(t, productID, "available", pgtype.Int8{}, pgtype.Timestamptz{}, pgtype.Int8{})
	store := postgres.NewAppStore(database.pool)
	result, err := app.NewPaymentEventIngestionService(store, 5).Ingest(context.Background(), phase6Event(t, order, "stale-event"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.pool.Exec(context.Background(), `UPDATE payment_events SET processing_status='processing',attempts=1,processing_started_at=clock_timestamp()-interval '10 minutes' WHERE id=$1`, result.EventID); err != nil {
		t.Fatal(err)
	}
	job := app.NewPaymentEventJob(store, app.NewPaymentAcceptanceService(store, time.Hour, nil), 10, time.Millisecond, time.Minute)
	if count, err := job.RunOnce(context.Background()); err != nil || count != 1 {
		t.Fatalf("RunOnce()=%d,%v", count, err)
	}
	var status string
	if err := database.pool.QueryRow(context.Background(), `SELECT processing_status FROM payment_events WHERE id=$1`, result.EventID).Scan(&status); err != nil || status != "completed" {
		t.Fatalf("event status=%s err=%v", status, err)
	}
}

func createPhase6PaymentOrder(t *testing.T, database *testDatabase, quantity int32) (generated.Order, int64) {
	t.Helper()
	user := database.createUser(t)
	productID := database.createProduct(t, database.createCategory(t))
	order := database.createOrder(t, user.ID)
	_, err := database.queries.InsertOrderItem(context.Background(), generated.InsertOrderItemParams{
		OrderID: order.ID, ProductID: productID, ProductName: "Payment product",
		UnitPriceVnd: 10_000, Quantity: quantity, LineTotalVnd: int64(quantity) * 10_000,
	})
	if err != nil {
		t.Fatalf("insert order item: %v", err)
	}
	_, err = database.pool.Exec(context.Background(), `UPDATE orders SET subtotal_vnd=$2,total_vnd=$2 WHERE id=$1`, order.ID, int64(quantity)*10_000)
	if err != nil {
		t.Fatalf("update order total: %v", err)
	}
	order.TotalVnd = int64(quantity) * 10_000
	return order, productID
}

func processPhase6Event(t *testing.T, database *testDatabase, order generated.Order, transactionID string) {
	t.Helper()
	store := postgres.NewAppStore(database.pool)
	if _, err := app.NewPaymentEventIngestionService(store, 5).Ingest(context.Background(), phase6Event(t, order, transactionID)); err != nil {
		t.Fatalf("ingest event: %v", err)
	}
	job := app.NewPaymentEventJob(store, app.NewPaymentAcceptanceService(store, time.Hour, nil), 10, time.Millisecond, time.Minute)
	if count, err := job.RunOnce(context.Background()); err != nil || count != 1 {
		t.Fatalf("process event = %d, %v", count, err)
	}
}

func phase6Event(t *testing.T, order generated.Order, transactionID string) app.NormalizedPaymentEvent {
	t.Helper()
	occurredAt := time.Now().UTC().Truncate(time.Second)
	metadata, err := json.Marshal(map[string]any{
		"reference": order.PaymentReference, "amount_vnd": order.TotalVnd,
		"currency": "VND", "occurred_at": occurredAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256([]byte(fmt.Sprintf("%s:%s", order.PaymentReference, transactionID)))
	return app.NormalizedPaymentEvent{
		Provider: "signed_json", ExternalEventID: "event-" + transactionID,
		ProviderTransactionID: transactionID, Reference: order.PaymentReference,
		Amount: domain.Money(order.TotalVnd), Currency: "VND", OccurredAt: occurredAt,
		EventType: "payment.received", PayloadHash: hash[:], SanitizedMetadata: metadata,
	}
}
