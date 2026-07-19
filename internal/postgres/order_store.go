package postgres

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/nvawntien/telegram-bot/internal/app"
	"github.com/nvawntien/telegram-bot/internal/domain"
	"github.com/nvawntien/telegram-bot/internal/postgres/generated"
)

type orderTransaction struct {
	queries *generated.Queries
}

func (s *AppStore) WithinOrderTransaction(ctx context.Context, fn func(app.OrderTransaction) error) error {
	return s.transactor.WithinTransaction(ctx, func(ctx context.Context, queries *generated.Queries) error {
		return fn(&orderTransaction{queries: queries})
	})
}

func (tx *orderTransaction) LockUserByTelegramID(ctx context.Context, telegramID int64) (app.User, error) {
	row, err := tx.queries.LockUserForOrderCreation(ctx, telegramID)
	if errors.Is(err, pgx.ErrNoRows) {
		return app.User{}, app.ErrNotFound
	}
	if err != nil {
		return app.User{}, err
	}
	return mapUser(row), nil
}

func (tx *orderTransaction) FindOrderByIdempotency(ctx context.Context, userID int64, key string) (app.OrderDetail, error) {
	row, err := tx.queries.FindOrderByUserIdempotency(ctx, generated.FindOrderByUserIdempotencyParams{UserID: userID, IdempotencyKey: key})
	if errors.Is(err, pgx.ErrNoRows) {
		return app.OrderDetail{}, app.ErrOrderNotFound
	}
	if err != nil {
		return app.OrderDetail{}, err
	}
	return mapOrder(row), nil
}

func (tx *orderTransaction) GetProductForOrderCreation(ctx context.Context, productID int64) (app.OrderProductRecord, error) {
	row, err := tx.queries.GetProductForOrderCreation(ctx, productID)
	if errors.Is(err, pgx.ErrNoRows) {
		return app.OrderProductRecord{}, app.ErrNotFound
	}
	if err != nil {
		return app.OrderProductRecord{}, err
	}
	return app.OrderProductRecord{
		ID: row.ID, Name: row.Name, Price: domain.Money(row.PriceVnd),
		DeliveryType: row.DeliveryType, ProductActive: row.ProductActive,
		CategoryActive: row.CategoryActive,
	}, nil
}

func (tx *orderTransaction) CountClaimableInventory(ctx context.Context, productID int64) (int64, error) {
	return tx.queries.CountClaimableInventoryForOrder(ctx, productID)
}

func (tx *orderTransaction) GetActiveBankAccountForOrder(ctx context.Context, bankID int64) (app.BankAccountRecord, error) {
	row, err := tx.queries.GetActiveBankAccountForOrder(ctx, bankID)
	if errors.Is(err, pgx.ErrNoRows) {
		return app.BankAccountRecord{}, app.ErrBankAccountNotFound
	}
	if err != nil {
		return app.BankAccountRecord{}, err
	}
	return app.BankAccountRecord{
		BankAccountOption: app.BankAccountOption{
			ID: row.ID, BankBIN: row.BankBin, BankName: row.BankName,
			DisplayName: row.DisplayName, AccountName: row.AccountName,
			Last4: row.DisplayLast4, SortOrder: row.SortOrder, Version: row.Version,
		},
		Protected: app.ProtectedBankAccountNumber{
			Ciphertext:  append([]byte(nil), row.EncryptedAccountNumber...),
			Nonce:       append([]byte(nil), row.EncryptionNonce...),
			Fingerprint: append([]byte(nil), row.AccountNumberFingerprint...),
			KeyVersion:  row.EncryptionKeyVersion, KeyID: row.EncryptionKeyID,
			Format: row.EncryptionFormat,
		},
		Active: row.IsActive, CreatedAt: row.CreatedAt.Time,
	}, nil
}

func (tx *orderTransaction) InsertPendingOrder(ctx context.Context, input app.PendingOrderInsert) (app.OrderDetail, bool, error) {
	row, err := tx.queries.CreatePendingOrderWithBank(ctx, generated.CreatePendingOrderWithBankParams{
		UserID: input.UserID, SubtotalVnd: input.LineTotal.Int64(), TotalVnd: input.LineTotal.Int64(),
		PaymentReference: input.PaymentReference, IdempotencyKey: input.IdempotencyKey,
		ExpiresAt:                      pgtype.Timestamptz{Time: input.ExpiresAt, Valid: true},
		BankAccountID:                  pgtype.Int8{Int64: input.Bank.ID, Valid: true},
		BankBinSnapshot:                pgtype.Text{String: input.Bank.BankBIN, Valid: true},
		BankNameSnapshot:               pgtype.Text{String: input.Bank.BankName, Valid: true},
		BankDisplayNameSnapshot:        pgtype.Text{String: input.Bank.DisplayName, Valid: true},
		BankAccountNameSnapshot:        pgtype.Text{String: input.Bank.AccountName, Valid: true},
		EncryptedAccountNumberSnapshot: append([]byte(nil), input.Bank.Protected.Ciphertext...),
		AccountNumberNonceSnapshot:     append([]byte(nil), input.Bank.Protected.Nonce...),
		AccountKeyVersionSnapshot:      pgtype.Int4{Int32: input.Bank.Protected.KeyVersion, Valid: true},
		AccountLast4Snapshot:           pgtype.Text{String: input.Bank.Last4, Valid: true},
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return app.OrderDetail{}, false, nil
	}
	if err != nil {
		return app.OrderDetail{}, false, err
	}
	return mapOrder(row), true, nil
}

func (tx *orderTransaction) InsertOrderItem(ctx context.Context, orderID int64, item app.OrderItemSnapshot) (app.OrderItemSnapshot, error) {
	row, err := tx.queries.InsertOrderItem(ctx, generated.InsertOrderItemParams{
		OrderID: orderID, ProductID: item.ProductID, ProductName: item.Name,
		UnitPriceVnd: item.UnitPrice.Int64(), Quantity: item.Quantity,
		LineTotalVnd: item.LineTotal.Int64(),
	})
	if err != nil {
		return app.OrderItemSnapshot{}, err
	}
	return mapOrderItem(row), nil
}

func (tx *orderTransaction) InsertOrderHistory(
	ctx context.Context,
	orderID int64,
	from *domain.OrderStatus,
	to domain.OrderStatus,
	reason, actorType string,
	actorID int64,
	requestID string,
) error {
	fromValue := pgtype.Text{}
	if from != nil {
		fromValue = pgtype.Text{String: string(*from), Valid: true}
	}
	_, err := tx.queries.InsertOrderStatusHistory(ctx, generated.InsertOrderStatusHistoryParams{
		OrderID: orderID, FromStatus: fromValue, ToStatus: string(to),
		ReasonCode: optionalText(reason), ActorType: actorType,
		ActorID:   pgtype.Int8{Int64: actorID, Valid: actorID > 0},
		RequestID: optionalText(requestID),
	})
	return err
}

func (tx *orderTransaction) LockOrderOwnedByTelegramUser(ctx context.Context, telegramID, orderID int64) (app.OrderDetail, error) {
	row, err := tx.queries.LockOrderDetailOwnedByTelegramUser(ctx, generated.LockOrderDetailOwnedByTelegramUserParams{OrderID: orderID, TelegramUserID: telegramID})
	if errors.Is(err, pgx.ErrNoRows) {
		return app.OrderDetail{}, app.ErrOrderNotFound
	}
	if err != nil {
		return app.OrderDetail{}, err
	}
	return mapLockedOrderDetail(row), nil
}

func (tx *orderTransaction) CancelPendingOrder(ctx context.Context, orderID, userID, version int64, now time.Time) (app.OrderDetail, error) {
	row, err := tx.queries.CancelPendingOrderOwnedGuarded(ctx, generated.CancelPendingOrderOwnedGuardedParams{
		ID: orderID, UserID: userID, Now: pgtype.Timestamptz{Time: now, Valid: true}, ExpectedVersion: version,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return app.OrderDetail{}, app.ErrInvalidOrderState
	}
	if err != nil {
		return app.OrderDetail{}, err
	}
	return mapOrder(row), nil
}

func (tx *orderTransaction) CompleteUpdate(ctx context.Context, updateID int64) error {
	return completeReceipt(ctx, tx.queries, updateID)
}

func (s *AppStore) ListOrdersOwnedByTelegramUser(ctx context.Context, telegramID int64, offset, limit int32) ([]app.OrderSummary, int64, error) {
	total, err := s.queries.CountOrdersOwnedByTelegramUser(ctx, telegramID)
	if err != nil {
		return nil, 0, err
	}
	rows, err := s.queries.ListOrdersOwnedByTelegramUser(ctx, generated.ListOrdersOwnedByTelegramUserParams{
		TelegramUserID: telegramID, PageOffset: offset, PageLimit: limit,
	})
	if err != nil {
		return nil, 0, err
	}
	items := make([]app.OrderSummary, 0, len(rows))
	for _, row := range rows {
		items = append(items, app.OrderSummary{
			ID: row.ID, Status: domain.OrderStatus(row.Status), Total: domain.Money(row.TotalVnd),
			PaymentReference: row.PaymentReference, ExpiresAt: row.ExpiresAt.Time,
			Version: row.Version, CreatedAt: row.CreatedAt.Time,
			ProductName: row.ProductName, Quantity: row.Quantity,
		})
	}
	return items, total, nil
}

func (s *AppStore) GetOrderOwnedByTelegramUser(ctx context.Context, telegramID, orderID int64) (app.OrderDetail, error) {
	row, err := s.queries.GetOrderDetailOwnedByTelegramUser(ctx, generated.GetOrderDetailOwnedByTelegramUserParams{OrderID: orderID, TelegramUserID: telegramID})
	if errors.Is(err, pgx.ErrNoRows) {
		return app.OrderDetail{}, app.ErrOrderNotFound
	}
	if err != nil {
		return app.OrderDetail{}, err
	}
	return mapOwnedOrderDetail(row), nil
}

func (s *AppStore) ExpireOverdueOrders(ctx context.Context, now time.Time, batchSize int32) ([]int64, error) {
	ids := make([]int64, 0, batchSize)
	err := s.transactor.WithinTransaction(ctx, func(ctx context.Context, queries *generated.Queries) error {
		rows, err := queries.ClaimOverduePendingOrders(ctx, generated.ClaimOverduePendingOrdersParams{
			Now: pgtype.Timestamptz{Time: now, Valid: true}, BatchSize: batchSize,
		})
		if err != nil {
			return err
		}
		pending := domain.OrderStatusPendingPayment
		for _, row := range rows {
			if _, err := queries.InsertOrderStatusHistory(ctx, generated.InsertOrderStatusHistoryParams{
				OrderID: row.ID, FromStatus: pgtype.Text{String: string(pending), Valid: true},
				ToStatus: string(domain.OrderStatusExpired), ReasonCode: optionalText("payment_window_elapsed"),
				ActorType: "system",
			}); err != nil {
				return err
			}
			ids = append(ids, row.ID)
		}
		return nil
	})
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids, err
}

func mapOrder(row generated.Order) app.OrderDetail {
	return app.OrderDetail{
		ID: row.ID, UserID: row.UserID, Status: domain.OrderStatus(row.Status), Currency: row.Currency,
		Subtotal: domain.Money(row.SubtotalVnd), Total: domain.Money(row.TotalVnd),
		PaymentReference: row.PaymentReference, ExpiresAt: row.ExpiresAt.Time,
		CancelledAt: row.CancelledAt.Time, Version: row.Version, CreatedAt: row.CreatedAt.Time,
		BankAccountID: row.BankAccountID.Int64, BankBIN: row.BankBinSnapshot.String,
		BankName: row.BankNameSnapshot.String, BankDisplayName: row.BankDisplayNameSnapshot.String,
		BankAccountName: row.BankAccountNameSnapshot.String, BankAccountLast4: row.AccountLast4Snapshot.String,
		BankProtected: app.ProtectedBankAccountNumber{
			Ciphertext: append([]byte(nil), row.EncryptedAccountNumberSnapshot...),
			Nonce:      append([]byte(nil), row.AccountNumberNonceSnapshot...),
			KeyVersion: row.AccountKeyVersionSnapshot.Int32,
			Format:     row.AccountEncryptionFormatSnapshot.String,
		},
	}
}

func mapOwnedOrderDetail(row generated.GetOrderDetailOwnedByTelegramUserRow) app.OrderDetail {
	order := mapOrder(generated.Order{
		ID: row.ID, UserID: row.UserID, Status: row.Status, Currency: row.Currency,
		SubtotalVnd: row.SubtotalVnd, TotalVnd: row.TotalVnd, PaymentReference: row.PaymentReference,
		IdempotencyKey: row.IdempotencyKey, ExpiresAt: row.ExpiresAt, PaidAt: row.PaidAt,
		DeliveryStartedAt: row.DeliveryStartedAt, DeliveredAt: row.DeliveredAt,
		CancelledAt: row.CancelledAt, Version: row.Version, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
		BankAccountID: row.BankAccountID, BankBinSnapshot: row.BankBinSnapshot,
		BankNameSnapshot: row.BankNameSnapshot, BankDisplayNameSnapshot: row.BankDisplayNameSnapshot,
		BankAccountNameSnapshot:         row.BankAccountNameSnapshot,
		EncryptedAccountNumberSnapshot:  row.EncryptedAccountNumberSnapshot,
		AccountNumberNonceSnapshot:      row.AccountNumberNonceSnapshot,
		AccountEncryptionFormatSnapshot: row.AccountEncryptionFormatSnapshot,
		AccountKeyVersionSnapshot:       row.AccountKeyVersionSnapshot, AccountLast4Snapshot: row.AccountLast4Snapshot,
	})
	order.Item = app.OrderItemSnapshot{ID: row.OrderItemID, ProductID: row.ProductID, Name: row.ProductName, UnitPrice: domain.Money(row.UnitPriceVnd), Quantity: row.Quantity, LineTotal: domain.Money(row.LineTotalVnd)}
	return order
}

func mapLockedOrderDetail(row generated.LockOrderDetailOwnedByTelegramUserRow) app.OrderDetail {
	order := mapOrder(generated.Order{
		ID: row.ID, UserID: row.UserID, Status: row.Status, Currency: row.Currency,
		SubtotalVnd: row.SubtotalVnd, TotalVnd: row.TotalVnd, PaymentReference: row.PaymentReference,
		IdempotencyKey: row.IdempotencyKey, ExpiresAt: row.ExpiresAt, PaidAt: row.PaidAt,
		DeliveryStartedAt: row.DeliveryStartedAt, DeliveredAt: row.DeliveredAt,
		CancelledAt: row.CancelledAt, Version: row.Version, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
		BankAccountID: row.BankAccountID, BankBinSnapshot: row.BankBinSnapshot,
		BankNameSnapshot: row.BankNameSnapshot, BankDisplayNameSnapshot: row.BankDisplayNameSnapshot,
		BankAccountNameSnapshot:         row.BankAccountNameSnapshot,
		EncryptedAccountNumberSnapshot:  row.EncryptedAccountNumberSnapshot,
		AccountNumberNonceSnapshot:      row.AccountNumberNonceSnapshot,
		AccountEncryptionFormatSnapshot: row.AccountEncryptionFormatSnapshot,
		AccountKeyVersionSnapshot:       row.AccountKeyVersionSnapshot, AccountLast4Snapshot: row.AccountLast4Snapshot,
	})
	order.Item = app.OrderItemSnapshot{ID: row.OrderItemID, ProductID: row.ProductID, Name: row.ProductName, UnitPrice: domain.Money(row.UnitPriceVnd), Quantity: row.Quantity, LineTotal: domain.Money(row.LineTotalVnd)}
	return order
}

func mapOrderItem(row generated.OrderItem) app.OrderItemSnapshot {
	return app.OrderItemSnapshot{
		ID: row.ID, ProductID: row.ProductID, Name: row.ProductName,
		UnitPrice: domain.Money(row.UnitPriceVnd), Quantity: row.Quantity,
		LineTotal: domain.Money(row.LineTotalVnd),
	}
}

var _ app.OrderRepository = (*AppStore)(nil)
var _ app.OrderExpiryRepository = (*AppStore)(nil)
