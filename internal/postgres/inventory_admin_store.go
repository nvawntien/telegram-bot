package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/nvawntien/telegram-bot/internal/app"
	"github.com/nvawntien/telegram-bot/internal/domain"
	"github.com/nvawntien/telegram-bot/internal/postgres/generated"
)

func (s *AppStore) ListInventoryOverview(
	ctx context.Context,
	admin app.Admin,
	offset, limit int32,
) ([]app.InventoryOverview, int64, error) {
	var result []app.InventoryOverview
	var total int64
	err := s.transactor.WithinTransaction(ctx, func(ctx context.Context, queries *generated.Queries) error {
		if err := requireCatalogAdmin(ctx, queries, admin); err != nil {
			return err
		}
		var err error
		total, err = queries.CountInventoryOverviewProducts(ctx)
		if err != nil {
			return err
		}
		rows, err := queries.ListInventoryOverviewPage(ctx, generated.ListInventoryOverviewPageParams{
			PageOffset: offset, PageLimit: limit,
		})
		if err != nil {
			return err
		}
		result = make([]app.InventoryOverview, 0, len(rows))
		for _, row := range rows {
			result = append(result, app.InventoryOverview{
				ProductID: row.ProductID, ProductName: row.ProductName,
				AvailableCount: row.AvailableCount, ReservedCount: row.ReservedCount,
				SoldCount: row.SoldCount, DisabledCount: row.DisabledCount, TotalCount: row.TotalCount,
			})
		}
		return nil
	})
	return result, total, err
}

func (s *AppStore) ListRedactedInventory(
	ctx context.Context,
	admin app.Admin,
	productID int64,
	offset, limit int32,
) ([]app.RedactedInventoryItem, int64, error) {
	var result []app.RedactedInventoryItem
	var total int64
	err := s.transactor.WithinTransaction(ctx, func(ctx context.Context, queries *generated.Queries) error {
		if err := requireCatalogAdmin(ctx, queries, admin); err != nil {
			return err
		}
		product, err := queries.GetProductByID(ctx, productID)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && product.DeliveryType != "inventory") {
			return app.ErrNotFound
		}
		if err != nil {
			return err
		}
		total, err = queries.CountRedactedInventoryByProduct(ctx, productID)
		if err != nil {
			return err
		}
		rows, err := queries.ListRedactedInventoryPage(ctx, generated.ListRedactedInventoryPageParams{
			ProductID: productID, PageOffset: offset, PageLimit: limit,
		})
		if err != nil {
			return err
		}
		result = make([]app.RedactedInventoryItem, 0, len(rows))
		for _, row := range rows {
			result = append(result, mapRedactedInventory(
				row.ID, row.ProductID, row.ProductName, row.Status,
				row.ReservedOrderID, row.ReservedUntil, row.EncryptionKeyVersion, row.Version, row.CreatedAt,
			))
		}
		return nil
	})
	return result, total, err
}

func (s *AppStore) ImportInventory(
	ctx context.Context,
	admin app.Admin,
	session app.AdminSession,
	productID int64,
	items []app.EncryptedInventoryPayload,
	requested, rejected int,
	meta app.RequestMeta,
) (app.InventoryImportResult, error) {
	result := app.InventoryImportResult{ProductID: productID, Requested: requested, Rejected: rejected}
	err := s.transactor.WithinTransaction(ctx, func(ctx context.Context, queries *generated.Queries) error {
		if err := requireCatalogAdmin(ctx, queries, admin); err != nil {
			return err
		}
		if _, err := lockAndVerifySession(ctx, queries, admin.ID, session, app.SessionInventoryImport); err != nil {
			return err
		}
		product, err := queries.GetProductByID(ctx, productID)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && product.DeliveryType != "inventory") {
			return app.ErrNotFound
		}
		if err != nil {
			return err
		}
		for _, item := range items {
			if item.Format != "aes-256-gcm-v1" || item.KeyVersion <= 0 || len(item.Nonce) != 12 || len(item.Ciphertext) < 16 || len(item.Fingerprint) != 32 {
				return app.ErrEncryptionFailed
			}
			_, err := queries.InsertEncryptedInventoryItem(ctx, generated.InsertEncryptedInventoryItemParams{
				ProductID: productID, EncryptedPayload: item.Ciphertext,
				EncryptionKeyID: fmt.Sprintf("inventory-v%d", item.KeyVersion),
				EncryptionNonce: item.Nonce, EncryptionKeyVersion: item.KeyVersion,
				PayloadFingerprint: item.Fingerprint,
				ImportedByAdminID:  pgtype.Int8{Int64: admin.ID, Valid: true},
			})
			if errors.Is(err, pgx.ErrNoRows) {
				result.Duplicates++
				continue
			}
			if err != nil {
				return err
			}
			result.Inserted++
			result.KeyVersion = item.KeyVersion
		}
		if result.KeyVersion == 0 && len(items) > 0 {
			result.KeyVersion = items[0].KeyVersion
		}
		audit := map[string]any{
			"product_id": productID, "requested_count": requested,
			"inserted_count": result.Inserted, "duplicate_count": result.Duplicates,
			"rejected_count": rejected, "key_version": result.KeyVersion,
		}
		if err := insertAudit(ctx, queries, admin.ID, "inventory.imported", "product", productID, nil, audit, meta); err != nil {
			return err
		}
		if err := finishSession(ctx, queries, admin.ID, session); err != nil {
			return err
		}
		return completeReceipt(ctx, queries, meta.UpdateID)
	})
	return result, mapConstraintError(err)
}

func (s *AppStore) SetInventoryItemEnabled(
	ctx context.Context,
	admin app.Admin,
	session app.AdminSession,
	itemID, expectedVersion int64,
	enabled bool,
	meta app.RequestMeta,
) (app.RedactedInventoryItem, error) {
	var result app.RedactedInventoryItem
	err := s.transactor.WithinTransaction(ctx, func(ctx context.Context, queries *generated.Queries) error {
		if err := requireCatalogAdmin(ctx, queries, admin); err != nil {
			return err
		}
		if _, err := lockAndVerifySession(ctx, queries, admin.ID, session, app.SessionInventoryToggle); err != nil {
			return err
		}
		before, err := queries.LockRedactedInventoryItem(ctx, itemID)
		if errors.Is(err, pgx.ErrNoRows) {
			return app.ErrInventoryNotFound
		}
		if err != nil {
			return err
		}
		if before.Version != expectedVersion {
			return app.ErrStaleVersion
		}
		expectedStatus := domain.InventoryStatusAvailable
		if enabled {
			expectedStatus = domain.InventoryStatusDisabled
		}
		if domain.InventoryStatus(before.Status) != expectedStatus {
			return app.ErrInvalidInventoryState
		}
		if enabled {
			after, updateErr := queries.EnableDisabledInventoryItem(ctx, generated.EnableDisabledInventoryItemParams{
				ID: itemID, ExpectedVersion: expectedVersion,
			})
			err = updateErr
			if err == nil {
				result = mapRedactedInventory(after.ID, after.ProductID, "", after.Status,
					after.ReservedOrderID, after.ReservedUntil, after.EncryptionKeyVersion, after.Version, after.CreatedAt)
			}
		} else {
			after, updateErr := queries.DisableAvailableInventoryItem(ctx, generated.DisableAvailableInventoryItemParams{
				DisabledReason: optionalText("admin_disabled"), ID: itemID, ExpectedVersion: expectedVersion,
			})
			err = updateErr
			if err == nil {
				result = mapRedactedInventory(after.ID, after.ProductID, "", after.Status,
					after.ReservedOrderID, after.ReservedUntil, after.EncryptionKeyVersion, after.Version, after.CreatedAt)
			}
		}
		if errors.Is(err, pgx.ErrNoRows) {
			return app.ErrStaleVersion
		}
		if err != nil {
			return err
		}
		action := "inventory.disabled"
		if enabled {
			action = "inventory.enabled"
		}
		beforeAudit := inventoryAuditSnapshot(before.ID, before.ProductID, before.Status, before.Version)
		afterAudit := inventoryAuditSnapshot(result.ID, result.ProductID, string(result.Status), result.Version)
		if err := insertAudit(ctx, queries, admin.ID, action, "inventory_item", itemID, beforeAudit, afterAudit, meta); err != nil {
			return err
		}
		if err := finishSession(ctx, queries, admin.ID, session); err != nil {
			return err
		}
		return completeReceipt(ctx, queries, meta.UpdateID)
	})
	return result, err
}

func mapRedactedInventory(
	id, productID int64,
	productName, status string,
	reservedOrderID pgtype.Int8,
	reservedUntil pgtype.Timestamptz,
	keyVersion int32,
	version int64,
	createdAt pgtype.Timestamptz,
) app.RedactedInventoryItem {
	item := app.RedactedInventoryItem{
		ID: id, ProductID: productID, ProductName: productName,
		Status: domain.InventoryStatus(status), KeyVersion: keyVersion, Version: version,
	}
	if reservedOrderID.Valid {
		item.ReservedOrderID = reservedOrderID.Int64
	}
	if reservedUntil.Valid {
		item.ReservedUntil = reservedUntil.Time
	}
	if createdAt.Valid {
		item.CreatedAt = createdAt.Time
	}
	return item
}

func inventoryAuditSnapshot(id, productID int64, status string, version int64) map[string]any {
	return map[string]any{"id": id, "product_id": productID, "status": status, "version": version}
}
