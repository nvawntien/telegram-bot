//go:build integration

package integration_test

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/nvawntien/telegram-bot/internal/postgres/generated"
	"github.com/pressly/goose/v3"
)

const integrationDatabaseEnvironment = "INTEGRATION_DATABASE_URL"

type testDatabase struct {
	adminPool   *pgxpool.Pool
	pool        *pgxpool.Pool
	sqlDB       *sql.DB
	queries     *generated.Queries
	schema      string
	migrations  string
	keySequence atomic.Int64
}

func newTestDatabase(t *testing.T, migrate bool) *testDatabase {
	t.Helper()
	baseURL := os.Getenv(integrationDatabaseEnvironment)
	if baseURL == "" {
		t.Fatalf("%s is required for integration tests", integrationDatabaseEnvironment)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)

	adminPool, err := pgxpool.New(ctx, baseURL)
	if err != nil {
		t.Fatalf("open admin pool: %v", err)
	}
	if err := adminPool.Ping(ctx); err != nil {
		adminPool.Close()
		t.Fatalf("ping integration database: %v", err)
	}

	schema := "integration_" + randomHex(t, 8)
	if _, err := adminPool.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize()); err != nil {
		adminPool.Close()
		t.Fatalf("create test schema: %v", err)
	}

	scopedURL := withSearchPath(t, baseURL, schema)
	sqlDB, err := sql.Open("pgx", scopedURL)
	if err != nil {
		dropSchema(context.Background(), adminPool, schema)
		adminPool.Close()
		t.Fatalf("open migration connection: %v", err)
	}
	pool, err := pgxpool.New(ctx, scopedURL)
	if err != nil {
		_ = sqlDB.Close()
		dropSchema(context.Background(), adminPool, schema)
		adminPool.Close()
		t.Fatalf("open test pool: %v", err)
	}

	database := &testDatabase{
		adminPool:  adminPool,
		pool:       pool,
		sqlDB:      sqlDB,
		queries:    generated.New(pool),
		schema:     schema,
		migrations: migrationsDirectory(t),
	}
	t.Cleanup(func() {
		pool.Close()
		if err := sqlDB.Close(); err != nil {
			t.Errorf("close migration connection: %v", err)
		}
		dropSchema(context.Background(), adminPool, schema)
		adminPool.Close()
	})

	if migrate {
		database.migrateUp(t)
	}
	return database
}

func (d *testDatabase) migrateUp(t *testing.T) {
	t.Helper()
	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatalf("set goose dialect: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := goose.UpContext(ctx, d.sqlDB, d.migrations); err != nil {
		t.Fatalf("migrate test schema up: %v", err)
	}
}

func (d *testDatabase) nextKey(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, d.keySequence.Add(1))
}

func (d *testDatabase) createUser(t *testing.T) generated.User {
	t.Helper()
	user, err := d.queries.UpsertTelegramUser(context.Background(), generated.UpsertTelegramUserParams{
		TelegramUserID: 1_000_000 + d.keySequence.Add(1),
		Username:       pgtype.Text{String: "integration_user", Valid: true},
		DisplayName:    pgtype.Text{String: "Integration User", Valid: true},
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	return user
}

func (d *testDatabase) createCategory(t *testing.T) int64 {
	t.Helper()
	var id int64
	key := d.nextKey("category")
	err := d.pool.QueryRow(context.Background(), `
		INSERT INTO categories (name, slug)
		VALUES ($1, $2)
		RETURNING id
	`, key, key).Scan(&id)
	if err != nil {
		t.Fatalf("create category: %v", err)
	}
	return id
}

func (d *testDatabase) createProduct(t *testing.T, categoryID int64) int64 {
	t.Helper()
	var id int64
	key := d.nextKey("product")
	err := d.pool.QueryRow(context.Background(), `
		INSERT INTO products (category_id, name, slug, price_vnd)
		VALUES ($1, $2, $3, 10000)
		RETURNING id
	`, categoryID, key, key).Scan(&id)
	if err != nil {
		t.Fatalf("create product: %v", err)
	}
	return id
}

func (d *testDatabase) createOrder(t *testing.T, userID int64) generated.Order {
	t.Helper()
	key := d.nextKey("order")
	order, err := d.queries.CreatePendingOrder(context.Background(), generated.CreatePendingOrderParams{
		UserID:           userID,
		SubtotalVnd:      10_000,
		TotalVnd:         10_000,
		PaymentReference: "PAY-" + key,
		IdempotencyKey:   "IDEMPOTENCY-" + key,
		ExpiresAt:        pgtype.Timestamptz{Time: time.Now().Add(15 * time.Minute), Valid: true},
	})
	if err != nil {
		t.Fatalf("create order: %v", err)
	}
	return order
}

func (d *testDatabase) createInventory(
	t *testing.T,
	productID int64,
	status string,
	reservedOrderID pgtype.Int8,
	reservedUntil pgtype.Timestamptz,
	soldOrderID pgtype.Int8,
) int64 {
	t.Helper()
	fingerprint := make([]byte, 32)
	fingerprint[0] = byte(d.keySequence.Add(1))
	nonce := make([]byte, 12)
	nonce[0] = fingerprint[0]
	ciphertext := make([]byte, 16)
	ciphertext[0] = fingerprint[0]
	var id int64
	err := d.pool.QueryRow(context.Background(), `
		INSERT INTO inventory_items (
			product_id, encrypted_payload, encryption_key_id, payload_fingerprint,
			encryption_nonce, encryption_format, encryption_key_version,
			status, reserved_order_id, reserved_until, sold_order_id
		) VALUES ($1, $2, 'test-key-v1', $3, $4, 'aes-256-gcm-v1', 1, $5, $6, $7, $8)
		RETURNING id
	`, productID, ciphertext, fingerprint, nonce, status, reservedOrderID, reservedUntil, soldOrderID).Scan(&id)
	if err != nil {
		t.Fatalf("create inventory: %v", err)
	}
	return id
}

func requirePostgresCode(t *testing.T, err error, code string) {
	t.Helper()
	if err == nil {
		t.Fatalf("error = nil, want PostgreSQL code %s", code)
	}
	var pgError *pgconn.PgError
	if !errors.As(err, &pgError) {
		t.Fatalf("error = %T %v, want *pgconn.PgError", err, err)
	}
	if pgError.Code != code {
		t.Fatalf("PostgreSQL code = %s (%s), want %s", pgError.Code, pgError.ConstraintName, code)
	}
}

func assertCount(t *testing.T, database *testDatabase, query string, want int, args ...any) {
	t.Helper()
	var got int
	if err := database.pool.QueryRow(context.Background(), query, args...).Scan(&got); err != nil {
		t.Fatalf("query count: %v", err)
	}
	if got != want {
		t.Fatalf("count = %d, want %d", got, want)
	}
}

func withSearchPath(t *testing.T, baseURL, schema string) string {
	t.Helper()
	parsed, err := url.Parse(baseURL)
	if err != nil {
		t.Fatalf("parse integration database URL: %v", err)
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func migrationsDirectory(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve integration test path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(filename), "..", "..", "migrations"))
}

func randomHex(t *testing.T, byteCount int) string {
	t.Helper()
	value := make([]byte, byteCount)
	if _, err := rand.Read(value); err != nil {
		t.Fatalf("generate schema suffix: %v", err)
	}
	return hex.EncodeToString(value)
}

func dropSchema(ctx context.Context, pool *pgxpool.Pool, schema string) {
	dropCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	_, _ = pool.Exec(dropCtx, "DROP SCHEMA IF EXISTS "+pgx.Identifier{schema}.Sanitize()+" CASCADE")
}
