package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/nvawntien/telegram-bot/internal/app"
	"github.com/nvawntien/telegram-bot/internal/domain"
	"github.com/nvawntien/telegram-bot/internal/postgres/generated"
)

func (s *AppStore) EnsureWallet(ctx context.Context, telegramUserID int64) (app.WalletAccount, error) {
	var result app.WalletAccount
	err := s.transactor.WithinTransaction(ctx, func(ctx context.Context, queries *generated.Queries) error {
		user, err := queries.LockUserByTelegramID(ctx, telegramUserID)
		if errors.Is(err, pgx.ErrNoRows) {
			return app.ErrUnauthorized
		}
		if err != nil {
			return err
		}
		if domain.UserStatus(user.Status) != domain.UserStatusActive {
			return app.ErrUserBlocked
		}
		wallet, err := queries.EnsureWalletAccount(ctx, user.ID)
		if err != nil {
			return err
		}
		result = mapWallet(wallet)
		return nil
	})
	return result, err
}

func (s *AppStore) CreateWalletTopup(ctx context.Context, command app.CreateWalletTopupCommand, reference string, expiresAt time.Time) (app.WalletTopup, bool, error) {
	if command.PaymentEnvironment == "" {
		command.PaymentEnvironment = "production"
	}
	var result app.WalletTopup
	var duplicate bool
	err := s.transactor.WithinTransaction(ctx, func(ctx context.Context, queries *generated.Queries) error {
		user, err := queries.LockUserByTelegramID(ctx, command.TelegramUserID)
		if errors.Is(err, pgx.ErrNoRows) {
			return app.ErrUnauthorized
		}
		if err != nil {
			return err
		}
		if domain.UserStatus(user.Status) != domain.UserStatusActive {
			return app.ErrUserBlocked
		}
		existing, err := queries.FindWalletTopupByIdempotency(ctx, generated.FindWalletTopupByIdempotencyParams{UserID: user.ID, IdempotencyKey: command.IdempotencyKey})
		if err == nil {
			result = mapWalletTopup(existing)
			duplicate = true
			return completeReceipt(ctx, queries, command.Meta.UpdateID)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		wallet, err := queries.EnsureWalletAccount(ctx, user.ID)
		if err != nil {
			return err
		}
		bank, err := queries.GetActiveBankAccountForOrder(ctx, command.BankAccountID)
		if errors.Is(err, pgx.ErrNoRows) {
			return app.ErrBankAccountInactive
		}
		if err != nil {
			return err
		}
		topup, err := queries.CreateWalletTopup(ctx, generated.CreateWalletTopupParams{
			UserID: user.ID, WalletAccountID: wallet.ID, AmountVnd: command.Amount.Int64(),
			PaymentReference: reference, IdempotencyKey: command.IdempotencyKey, ExpiresAt: requiredTimestamp(expiresAt),
			BankAccountID: bank.ID, BankBinSnapshot: bank.BankBin, BankNameSnapshot: bank.BankName,
			BankDisplayNameSnapshot: bank.DisplayName, BankAccountNameSnapshot: bank.AccountName,
			EncryptedAccountNumberSnapshot: bank.EncryptedAccountNumber, AccountNumberNonceSnapshot: bank.EncryptionNonce,
			AccountKeyVersionSnapshot: bank.EncryptionKeyVersion, AccountLast4Snapshot: bank.DisplayLast4,
			PaymentEnvironment: command.PaymentEnvironment,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			existing, findErr := queries.FindWalletTopupByIdempotency(ctx, generated.FindWalletTopupByIdempotencyParams{UserID: user.ID, IdempotencyKey: command.IdempotencyKey})
			if findErr != nil {
				return findErr
			}
			result = mapWalletTopup(existing)
			duplicate = true
			return completeReceipt(ctx, queries, command.Meta.UpdateID)
		}
		if err != nil {
			return err
		}
		result = mapWalletTopup(topup)
		return completeReceipt(ctx, queries, command.Meta.UpdateID)
	})
	return result, duplicate, err
}

func (s *AppStore) PayOrderWithWallet(ctx context.Context, command app.WalletOrderPaymentCommand, paidAt time.Time, reservationTTL time.Duration, deliveryMaxAttempts ...int32) (app.WalletOrderPaymentResult, error) {
	var result app.WalletOrderPaymentResult
	err := s.transactor.WithinTransaction(ctx, func(ctx context.Context, queries *generated.Queries) error {
		user, err := queries.LockUserByTelegramID(ctx, command.TelegramUserID)
		if errors.Is(err, pgx.ErrNoRows) {
			return app.ErrUnauthorized
		}
		if err != nil {
			return err
		}
		if domain.UserStatus(user.Status) != domain.UserStatusActive {
			return app.ErrUserBlocked
		}
		order, err := queries.LockOrderDetailOwnedByTelegramUser(ctx, generated.LockOrderDetailOwnedByTelegramUserParams{OrderID: command.OrderID, TelegramUserID: command.TelegramUserID})
		if errors.Is(err, pgx.ErrNoRows) {
			return app.ErrOrderNotFound
		}
		if err != nil {
			return err
		}
		transactionID := fmt.Sprintf("wallet-order-%d", order.ID)
		existingPayment, paymentErr := queries.GetPaymentByProviderTransaction(ctx, generated.GetPaymentByProviderTransactionParams{Provider: "wallet", PaymentEnvironment: order.PaymentEnvironment, ProviderTransactionID: optionalText(transactionID)})
		if paymentErr == nil {
			allocation, allocationErr := queries.GetPaymentAllocation(ctx, existingPayment.ID)
			if allocationErr == nil && allocation.TargetType == "order" && allocation.TargetID == order.ID {
				wallet, err := queries.EnsureWalletAccount(ctx, user.ID)
				if err != nil {
					return err
				}
				result = app.WalletOrderPaymentResult{OrderID: order.ID, Balance: domain.Money(wallet.BalanceVnd), Duplicate: true}
				return completeReceipt(ctx, queries, command.Meta.UpdateID)
			}
			return app.ErrPaymentTransactionConflict
		}
		if !errors.Is(paymentErr, pgx.ErrNoRows) {
			return paymentErr
		}
		if domain.OrderStatus(order.Status) != domain.OrderStatusPendingPayment {
			return app.ErrOrderAlreadyPaid
		}
		if !order.ExpiresAt.Time.After(paidAt) {
			return app.ErrOrderExpired
		}
		wallet, err := queries.EnsureWalletAccount(ctx, user.ID)
		if err != nil {
			return err
		}
		wallet, err = queries.LockWalletAccount(ctx, wallet.ID)
		if err != nil {
			return err
		}
		if wallet.BalanceVnd < order.TotalVnd {
			return app.ErrInsufficientWalletBalance
		}
		inventoryIDs, err := queries.ClaimExactAvailableInventory(ctx, generated.ClaimExactAvailableInventoryParams{
			OrderID: requiredInt8(order.ID), ReservedUntil: requiredTimestamp(paidAt.Add(reservationTTL)), ProductID: order.ProductID, Quantity: order.Quantity,
		})
		if err != nil {
			return err
		}
		if len(inventoryIDs) != int(order.Quantity) {
			return app.ErrInsufficientInventory
		}
		payment, err := queries.InsertPayment(ctx, generated.InsertPaymentParams{
			OrderID: requiredInt8(order.ID), UserID: user.ID, Purpose: "order", Provider: "wallet",
			ProviderTransactionID: optionalText(transactionID), PaymentReference: order.PaymentReference,
			AmountVnd: order.TotalVnd, Status: "confirmed", ConfirmedAt: requiredTimestamp(paidAt), OccurredAt: requiredTimestamp(paidAt),
			PaymentEnvironment: order.PaymentEnvironment,
		})
		if err != nil {
			return err
		}
		updatedWallet, err := queries.UpdateWalletBalance(ctx, generated.UpdateWalletBalanceParams{AmountVnd: -order.TotalVnd, ID: wallet.ID})
		if errors.Is(err, pgx.ErrNoRows) {
			return app.ErrInsufficientWalletBalance
		}
		if err != nil {
			return err
		}
		if _, err := queries.InsertWalletLedgerEntry(ctx, generated.InsertWalletLedgerEntryParams{
			AccountID: wallet.ID, EntryType: "debit", AmountVnd: -order.TotalVnd, BalanceAfterVnd: updatedWallet.BalanceVnd,
			ReferenceType: "order", ReferenceID: order.ID, IdempotencyKey: command.IdempotencyKey,
		}); err != nil {
			return err
		}
		paidOrder, err := queries.MarkOrderPaidGuarded(ctx, generated.MarkOrderPaidGuardedParams{PaidAt: requiredTimestamp(paidAt), ID: order.ID, ExpectedVersion: order.Version})
		if err != nil {
			return err
		}
		actor := app.AcceptPaymentCommand{Actor: app.PaymentActor{Type: "user", ID: user.ID}, RequestID: command.Meta.RequestID, Environment: order.PaymentEnvironment, Source: "manual"}
		if err := insertPaymentOrderHistory(ctx, queries, actor, order.ID, domain.OrderStatusPendingPayment, domain.OrderStatusPaid, "wallet_payment_accepted"); err != nil {
			return err
		}
		if _, err := queries.UpdateOrderStatusGuarded(ctx, generated.UpdateOrderStatusGuardedParams{NewStatus: string(domain.OrderStatusReserving), ID: order.ID, ExpectedStatus: string(domain.OrderStatusPaid), ExpectedVersion: paidOrder.Version}); err != nil {
			return err
		}
		if err := insertPaymentOrderHistory(ctx, queries, actor, order.ID, domain.OrderStatusPaid, domain.OrderStatusReserving, "wallet_inventory_claimed"); err != nil {
			return err
		}
		for _, inventoryID := range inventoryIDs {
			if err := queries.InsertOrderInventoryMapping(ctx, generated.InsertOrderInventoryMappingParams{OrderID: order.ID, OrderItemID: order.OrderItemID, InventoryItemID: inventoryID}); err != nil {
				return err
			}
		}
		if _, err := queries.InsertPaymentAllocation(ctx, generated.InsertPaymentAllocationParams{PaymentID: payment.ID, TargetType: "order", TargetID: order.ID, AmountVnd: order.TotalVnd}); err != nil {
			return err
		}
		if _, err := createDeliveryHandoffWithinTransaction(
			ctx, queries, order.ID, paidAt, configuredDeliveryMaxAttempts(deliveryMaxAttempts),
			"user", user.ID, command.Meta.RequestID,
		); err != nil {
			return err
		}
		if err := completeReceipt(ctx, queries, command.Meta.UpdateID); err != nil {
			return err
		}
		result = app.WalletOrderPaymentResult{OrderID: order.ID, Balance: domain.Money(updatedWallet.BalanceVnd), Claimed: len(inventoryIDs)}
		return nil
	})
	return result, mapWalletError(err)
}

func (s *AppStore) AdjustWallet(ctx context.Context, command app.WalletAdjustmentCommand, adjustedAt time.Time) (app.WalletAccount, error) {
	var result app.WalletAccount
	err := s.transactor.WithinTransaction(ctx, func(ctx context.Context, queries *generated.Queries) error {
		adminRow, err := queries.GetAdminAuthorizationByTelegramID(ctx, command.AdminTelegramID)
		if errors.Is(err, pgx.ErrNoRows) {
			return app.ErrUnauthorized
		}
		if err != nil {
			return err
		}
		admin := app.Admin{ID: adminRow.AdminID, UserID: adminRow.UserID, TelegramUserID: adminRow.TelegramUserID, UserStatus: domain.UserStatus(adminRow.UserStatus), Role: adminRow.Role, Active: adminRow.IsActive}
		if err := requireCatalogAdmin(ctx, queries, admin); err != nil {
			return err
		}
		if _, err := lockAndVerifySession(ctx, queries, admin.ID, command.Session, app.SessionWalletAdjustment); err != nil {
			return err
		}
		user, err := queries.LockUserByTelegramID(ctx, command.TargetTelegramID)
		if errors.Is(err, pgx.ErrNoRows) {
			return app.ErrNotFound
		}
		if err != nil {
			return err
		}
		wallet, err := queries.EnsureWalletAccount(ctx, user.ID)
		if err != nil {
			return err
		}
		wallet, err = queries.LockWalletAccount(ctx, wallet.ID)
		if err != nil {
			return err
		}
		amount := command.Amount.Int64()
		entryType := "adjustment"
		if command.Debit {
			amount = -amount
		}
		updated, err := queries.UpdateWalletBalance(ctx, generated.UpdateWalletBalanceParams{AmountVnd: amount, ID: wallet.ID})
		if errors.Is(err, pgx.ErrNoRows) {
			return app.ErrInsufficientWalletBalance
		}
		if err != nil {
			return err
		}
		ledger, err := queries.InsertWalletLedgerEntry(ctx, generated.InsertWalletLedgerEntryParams{
			AccountID: wallet.ID, EntryType: entryType, AmountVnd: amount, BalanceAfterVnd: updated.BalanceVnd,
			ReferenceType: "admin_adjustment", ReferenceID: admin.ID, IdempotencyKey: command.IdempotencyKey,
		})
		if err != nil {
			return err
		}
		before, _ := json.Marshal(map[string]any{"balance_vnd": wallet.BalanceVnd})
		after, _ := json.Marshal(map[string]any{"balance_vnd": updated.BalanceVnd, "ledger_id": ledger.ID, "reason": command.Reason, "adjusted_at": adjustedAt})
		if _, err := queries.InsertAuditLog(ctx, generated.InsertAuditLogParams{ActorType: "admin", ActorID: requiredInt8(admin.ID), Action: "wallet.adjusted", ResourceType: "wallet_account", ResourceID: requiredInt8(wallet.ID), BeforeData: before, AfterData: after, RequestID: optionalText(command.Meta.RequestID), TelegramUpdateID: optionalInt8(command.Meta.UpdateID)}); err != nil {
			return err
		}
		if err := finishSession(ctx, queries, admin.ID, command.Session); err != nil {
			return err
		}
		if err := completeReceipt(ctx, queries, command.Meta.UpdateID); err != nil {
			return err
		}
		result = mapWallet(updated)
		return nil
	})
	return result, mapWalletError(err)
}

func mapWallet(row generated.WalletAccount) app.WalletAccount {
	return app.WalletAccount{ID: row.ID, UserID: row.UserID, Balance: domain.Money(row.BalanceVnd), Status: row.Status, Version: row.Version}
}

func mapWalletTopup(row generated.WalletTopupIntent) app.WalletTopup {
	return app.WalletTopup{
		ID: row.ID, UserID: row.UserID, WalletAccountID: row.WalletAccountID, Amount: domain.Money(row.AmountVnd), Currency: row.Currency,
		PaymentReference: row.PaymentReference, Status: row.Status, ExpiresAt: row.ExpiresAt.Time, Version: row.Version,
		BankBIN: row.BankBinSnapshot, BankName: row.BankNameSnapshot, BankDisplayName: row.BankDisplayNameSnapshot,
		BankAccountName: row.BankAccountNameSnapshot, BankAccountLast4: row.AccountLast4Snapshot,
		BankProtected: app.ProtectedBankAccountNumber{Ciphertext: append([]byte(nil), row.EncryptedAccountNumberSnapshot...), Nonce: append([]byte(nil), row.AccountNumberNonceSnapshot...), KeyVersion: row.AccountKeyVersionSnapshot, Format: row.AccountEncryptionFormatSnapshot},
	}
}

func mapWalletError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, app.ErrInsufficientInventory) || errors.Is(err, app.ErrInsufficientWalletBalance) {
		return err
	}
	return mapConstraintError(err)
}

var _ app.WalletRepository = (*AppStore)(nil)
