//go:build integration

package integration_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/pressly/goose/v3"
)

func TestMigrationCycle(t *testing.T) {
	database := newTestDatabase(t, false)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	if err := goose.UpContext(ctx, database.sqlDB, database.migrations); err != nil {
		t.Fatalf("initial migrate up: %v", err)
	}
	version, err := goose.GetDBVersionContext(ctx, database.sqlDB)
	if err != nil {
		t.Fatalf("get migrated version: %v", err)
	}
	if version != 15 {
		t.Fatalf("migration version = %d, want 15", version)
	}
	if err := goose.DownToContext(ctx, database.sqlDB, database.migrations, 0); err != nil {
		t.Fatalf("migrate down to zero: %v", err)
	}
	if err := goose.UpContext(ctx, database.sqlDB, database.migrations); err != nil {
		t.Fatalf("second migrate up: %v", err)
	}
}

func TestCoreCheckAndUniqueConstraints(t *testing.T) {
	database := newTestDatabase(t, true)
	ctx := context.Background()
	user := database.createUser(t)
	categoryID := database.createCategory(t)
	productID := database.createProduct(t, categoryID)
	order := database.createOrder(t, user.ID)

	tests := []struct {
		name string
		code string
		run  func() error
	}{
		{
			name: "negative product price",
			code: "23514",
			run: func() error {
				_, err := database.pool.Exec(ctx, `
					INSERT INTO products (category_id, name, slug, price_vnd)
					VALUES ($1, 'invalid price', $2, -1)
				`, categoryID, database.nextKey("negative-price"))
				return err
			},
		},
		{
			name: "zero order item quantity",
			code: "23514",
			run: func() error {
				_, err := database.pool.Exec(ctx, `
					INSERT INTO order_items (order_id, product_id, product_name, unit_price_vnd, quantity, line_total_vnd)
					VALUES ($1, $2, 'snapshot', 10000, 0, 0)
				`, order.ID, productID)
				return err
			},
		},
		{
			name: "negative order total",
			code: "23514",
			run: func() error {
				key := database.nextKey("negative-order")
				_, err := database.pool.Exec(ctx, `
					INSERT INTO orders (user_id, subtotal_vnd, total_vnd, payment_reference, idempotency_key, expires_at)
					VALUES ($1, 0, -1, $2, $3, clock_timestamp() + interval '15 minutes')
				`, user.ID, "PAY-"+key, key)
				return err
			},
		},
		{
			name: "invalid order status",
			code: "23514",
			run: func() error {
				key := database.nextKey("invalid-status")
				_, err := database.pool.Exec(ctx, `
					INSERT INTO orders (user_id, status, subtotal_vnd, total_vnd, payment_reference, idempotency_key, expires_at)
					VALUES ($1, 'unknown', 0, 0, $2, $3, clock_timestamp() + interval '15 minutes')
				`, user.ID, "PAY-"+key, key)
				return err
			},
		},
		{
			name: "invalid inventory status",
			code: "23514",
			run: func() error {
				_, err := database.pool.Exec(ctx, `
					INSERT INTO inventory_items (
						product_id, encrypted_payload, encryption_key_id, payload_fingerprint, status
					) VALUES ($1, $2, 'key-v1', $3, 'unknown')
				`, productID, []byte{1}, fingerprint(1))
				return err
			},
		},
		{
			name: "reserved inventory without order",
			code: "23514",
			run: func() error {
				_, err := database.pool.Exec(ctx, `
					INSERT INTO inventory_items (
						product_id, encrypted_payload, encryption_key_id, payload_fingerprint, status, reserved_until
					) VALUES ($1, $2, 'key-v1', $3, 'reserved', clock_timestamp() + interval '15 minutes')
				`, productID, []byte{1}, fingerprint(2))
				return err
			},
		},
		{
			name: "sold inventory without order",
			code: "23514",
			run: func() error {
				_, err := database.pool.Exec(ctx, `
					INSERT INTO inventory_items (
						product_id, encrypted_payload, encryption_key_id, payload_fingerprint, status
					) VALUES ($1, $2, 'key-v1', $3, 'sold')
				`, productID, []byte{1}, fingerprint(3))
				return err
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			requirePostgresCode(t, test.run(), test.code)
		})
	}

	t.Run("duplicate Telegram user ID", func(t *testing.T) {
		_, err := database.pool.Exec(ctx, `INSERT INTO users (telegram_user_id) VALUES ($1)`, user.TelegramUserID)
		requirePostgresCode(t, err, "23505")
	})

	t.Run("duplicate payment event ID", func(t *testing.T) {
		insert := `
			INSERT INTO payment_events (
				provider, external_event_id, event_type, payload_hash, sanitized_payload, signature_verified
			) VALUES ('provider-a', 'event-duplicate', 'payment.succeeded', $1, '{}'::jsonb, true)
		`
		if _, err := database.pool.Exec(ctx, insert, fingerprint(10)); err != nil {
			t.Fatalf("insert first payment event: %v", err)
		}
		_, err := database.pool.Exec(ctx, insert, fingerprint(11))
		requirePostgresCode(t, err, "23505")
	})

	t.Run("duplicate provider transaction ID", func(t *testing.T) {
		for index := 0; index < 2; index++ {
			key := database.nextKey("payment")
			_, err := database.pool.Exec(ctx, `
				INSERT INTO payments (
					order_id, user_id, purpose, provider, provider_transaction_id,
					payment_reference, amount_vnd, status, confirmed_at
				) VALUES ($1, $2, 'order', 'provider-a', 'transaction-duplicate', $3, 10000, 'confirmed', clock_timestamp())
			`, order.ID, user.ID, key)
			if index == 0 && err != nil {
				t.Fatalf("insert first payment: %v", err)
			}
			if index == 1 {
				requirePostgresCode(t, err, "23505")
			}
		}
	})

	t.Run("duplicate wallet idempotency key", func(t *testing.T) {
		var accountID int64
		if err := database.pool.QueryRow(ctx, `
			INSERT INTO wallet_accounts (user_id) VALUES ($1) RETURNING id
		`, user.ID).Scan(&accountID); err != nil {
			t.Fatalf("create wallet account: %v", err)
		}
		insert := `
			INSERT INTO wallet_ledger_entries (
				account_id, entry_type, amount_vnd, balance_after_vnd,
				reference_type, reference_id, idempotency_key
			) VALUES ($1, 'credit', 10000, 10000, 'payment', 1, 'wallet-duplicate')
		`
		if _, err := database.pool.Exec(ctx, insert, accountID); err != nil {
			t.Fatalf("insert first ledger entry: %v", err)
		}
		_, err := database.pool.Exec(ctx, insert, accountID)
		requirePostgresCode(t, err, "23505")
	})
}

func TestForeignKeyConstraints(t *testing.T) {
	database := newTestDatabase(t, true)
	ctx := context.Background()
	categoryID := database.createCategory(t)
	productID := database.createProduct(t, categoryID)

	tests := []struct {
		name string
		run  func() error
	}{
		{
			name: "order requires user",
			run: func() error {
				key := database.nextKey("missing-user")
				_, err := database.pool.Exec(ctx, `
					INSERT INTO orders (user_id, subtotal_vnd, total_vnd, payment_reference, idempotency_key, expires_at)
					VALUES (9223372036854775807, 0, 0, $1, $2, clock_timestamp() + interval '15 minutes')
				`, "PAY-"+key, key)
				return err
			},
		},
		{
			name: "order item requires order",
			run: func() error {
				_, err := database.pool.Exec(ctx, `
					INSERT INTO order_items (order_id, product_id, product_name, unit_price_vnd, quantity, line_total_vnd)
					VALUES (9223372036854775807, $1, 'snapshot', 10000, 1, 10000)
				`, productID)
				return err
			},
		},
		{
			name: "inventory requires product",
			run: func() error {
				_, err := database.pool.Exec(ctx, `
					INSERT INTO inventory_items (
						product_id, encrypted_payload, encryption_key_id, payload_fingerprint
					) VALUES (9223372036854775807, $1, 'key-v1', $2)
				`, []byte{1}, fingerprint(20))
				return err
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			requirePostgresCode(t, test.run(), "23503")
		})
	}
}

func TestUpdatedAtTriggerAndAppendOnlyAudit(t *testing.T) {
	database := newTestDatabase(t, true)
	ctx := context.Background()
	user := database.createUser(t)
	before := user.UpdatedAt.Time

	if _, err := database.pool.Exec(ctx, `SELECT pg_sleep(0.01)`); err != nil {
		t.Fatalf("wait for timestamp boundary: %v", err)
	}
	var after time.Time
	if err := database.pool.QueryRow(ctx, `
		UPDATE users SET display_name = 'Updated User' WHERE id = $1 RETURNING updated_at
	`, user.ID).Scan(&after); err != nil {
		t.Fatalf("update user: %v", err)
	}
	if !after.After(before) {
		t.Fatalf("updated_at = %s, want after %s", after, before)
	}

	var auditID int64
	if err := database.pool.QueryRow(ctx, `
		INSERT INTO audit_logs (actor_type, action, resource_type, resource_id)
		VALUES ('system', 'test.created', 'user', $1)
		RETURNING id
	`, user.ID).Scan(&auditID); err != nil {
		t.Fatalf("insert audit log: %v", err)
	}
	_, err := database.pool.Exec(ctx, `UPDATE audit_logs SET action = 'test.changed' WHERE id = $1`, auditID)
	requirePostgresCode(t, err, "55000")
}

func fingerprint(value byte) []byte {
	result := make([]byte, 32)
	result[0] = value
	return result
}

func nullableOrderID(id int64) pgtype.Int8 {
	return pgtype.Int8{Int64: id, Valid: true}
}
