package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"unicode/utf8"
)

type InventoryCipher interface {
	Protect(context.Context, int64, []byte) (EncryptedInventoryPayload, error)
	Decrypt(context.Context, int64, EncryptedInventoryPayload) ([]byte, error)
}

type InventoryAdminRepository interface {
	ListInventoryOverview(context.Context, Admin, int32, int32) ([]InventoryOverview, int64, error)
	ListRedactedInventory(context.Context, Admin, int64, int32, int32) ([]RedactedInventoryItem, int64, error)
	ImportInventory(context.Context, Admin, AdminSession, int64, []EncryptedInventoryPayload, int, int, RequestMeta) (InventoryImportResult, error)
	SetInventoryItemEnabled(context.Context, Admin, AdminSession, int64, int64, bool, RequestMeta) (RedactedInventoryItem, error)
}

type InventoryOperationMetrics interface {
	ObserveInventoryImport(result string, inserted, duplicates int)
	ObserveInventoryClaim(result string, claimed int)
	ObserveInventoryRelease(result string, released int)
	ObserveInventoryRecovery(result string)
}

type InventoryAdminService struct {
	repository InventoryAdminRepository
	admins     *AdminService
	cipher     InventoryCipher
	limits     InventoryImportLimits
	pageSize   int
	metrics    InventoryOperationMetrics
}

func NewInventoryAdminService(
	repository InventoryAdminRepository,
	admins *AdminService,
	cipher InventoryCipher,
	limits InventoryImportLimits,
	pageSize int,
	metrics InventoryOperationMetrics,
) *InventoryAdminService {
	if pageSize <= 0 || pageSize > MaxPageSize {
		pageSize = DefaultPageSize
	}
	return &InventoryAdminService{
		repository: repository, admins: admins, cipher: cipher,
		limits: limits, pageSize: pageSize, metrics: metrics,
	}
}

func (s *InventoryAdminService) ListOverview(ctx context.Context, telegramID int64, page int) (InventoryOverviewPage, error) {
	offset, err := inventoryPageOffset(page, s.pageSize)
	if err != nil {
		return InventoryOverviewPage{}, err
	}
	admin, err := s.admins.Authorize(ctx, telegramID, false)
	if err != nil {
		return InventoryOverviewPage{}, err
	}
	items, total, err := s.repository.ListInventoryOverview(ctx, admin, int32(offset), int32(s.pageSize))
	if err != nil {
		return InventoryOverviewPage{}, fmt.Errorf("list inventory overview: %w", err)
	}
	return InventoryOverviewPage{Items: items, Page: pageInfo(page, s.pageSize, total)}, nil
}

func (s *InventoryAdminService) ListItems(ctx context.Context, telegramID, productID int64, page int) (RedactedInventoryPage, error) {
	if productID <= 0 {
		return RedactedInventoryPage{}, ErrInvalidInput
	}
	offset, err := inventoryPageOffset(page, s.pageSize)
	if err != nil {
		return RedactedInventoryPage{}, err
	}
	admin, err := s.admins.Authorize(ctx, telegramID, false)
	if err != nil {
		return RedactedInventoryPage{}, err
	}
	items, total, err := s.repository.ListRedactedInventory(ctx, admin, productID, int32(offset), int32(s.pageSize))
	if err != nil {
		return RedactedInventoryPage{}, fmt.Errorf("list redacted inventory: %w", err)
	}
	return RedactedInventoryPage{Items: items, Page: pageInfo(page, s.pageSize, total)}, nil
}

func (s *InventoryAdminService) Import(
	ctx context.Context,
	telegramID int64,
	session AdminSession,
	productID int64,
	raw []byte,
	meta RequestMeta,
) (result InventoryImportResult, err error) {
	defer clearBytes(raw)
	if productID <= 0 || session.ID <= 0 || session.Version <= 0 {
		return InventoryImportResult{}, ErrInvalidInput
	}
	admin, err := s.admins.Authorize(ctx, telegramID, true)
	if err != nil {
		return InventoryImportResult{}, err
	}
	items, rejected, err := ParseInventoryImport(raw, s.limits)
	if err != nil {
		s.observeImport(err, InventoryImportResult{})
		return InventoryImportResult{}, err
	}
	encrypted := make([]EncryptedInventoryPayload, 0, len(items))
	for _, item := range items {
		protected, protectErr := s.cipher.Protect(ctx, productID, item)
		if protectErr != nil {
			clearImportItems(items)
			s.observeImport(ErrEncryptionFailed, InventoryImportResult{})
			return InventoryImportResult{}, fmt.Errorf("protect inventory item: %w", ErrEncryptionFailed)
		}
		encrypted = append(encrypted, protected)
	}
	clearImportItems(items)
	result, err = s.repository.ImportInventory(ctx, admin, session, productID, encrypted, len(encrypted), rejected, meta)
	s.observeImport(err, result)
	if err != nil {
		return InventoryImportResult{}, err
	}
	return result, nil
}

func (s *InventoryAdminService) SetItemEnabled(
	ctx context.Context,
	telegramID int64,
	session AdminSession,
	itemID, expectedVersion int64,
	enabled bool,
	meta RequestMeta,
) (RedactedInventoryItem, error) {
	if itemID <= 0 || expectedVersion <= 0 || session.ID <= 0 || session.Version <= 0 {
		return RedactedInventoryItem{}, ErrInvalidInput
	}
	admin, err := s.admins.Authorize(ctx, telegramID, true)
	if err != nil {
		return RedactedInventoryItem{}, err
	}
	item, err := s.repository.SetInventoryItemEnabled(ctx, admin, session, itemID, expectedVersion, enabled, meta)
	if err != nil {
		return RedactedInventoryItem{}, err
	}
	return item, nil
}

func ParseInventoryImport(raw []byte, limits InventoryImportLimits) ([][]byte, int, error) {
	if limits.MaxItems <= 0 || limits.MaxItemBytes <= 0 || limits.MaxTotalBytes <= 0 || limits.MaxItemBytes > limits.MaxTotalBytes {
		return nil, 0, ErrInvalidInput
	}
	if len(raw) == 0 || !utf8.Valid(raw) {
		return nil, 0, ErrInvalidInventoryPayload
	}
	if len(raw) > limits.MaxTotalBytes {
		return nil, 0, ErrImportLimitExceeded
	}
	lines := bytes.Split(raw, []byte{'\n'})
	items := make([][]byte, 0, len(lines))
	rejected := 0
	for _, line := range lines {
		if len(line) > 0 && line[len(line)-1] == '\r' {
			line = line[:len(line)-1]
		}
		if len(line) == 0 || len(bytes.TrimSpace(line)) == 0 {
			rejected++
			continue
		}
		if len(line) > limits.MaxItemBytes || len(items) == limits.MaxItems {
			clearImportItems(items)
			return nil, rejected, ErrImportLimitExceeded
		}
		items = append(items, line)
	}
	if len(items) == 0 {
		return nil, rejected, ErrInvalidInventoryPayload
	}
	return items, rejected, nil
}

func inventoryPageOffset(page, size int) (int, error) {
	if page < 0 || size <= 0 || page > int(^uint(0)>>1)/size {
		return 0, ErrInvalidInput
	}
	return page * size, nil
}

func clearImportItems(items [][]byte) {
	for _, item := range items {
		clearBytes(item)
	}
}

func clearBytes(value []byte) {
	for index := range value {
		value[index] = 0
	}
}

func (s *InventoryAdminService) observeImport(err error, result InventoryImportResult) {
	if s.metrics == nil {
		return
	}
	metric := "success"
	if err != nil {
		metric = "failed"
		if errors.Is(err, ErrImportLimitExceeded) {
			metric = "limit_exceeded"
		} else if errors.Is(err, ErrInvalidInventoryPayload) {
			metric = "invalid"
		}
	}
	s.metrics.ObserveInventoryImport(metric, result.Inserted, result.Duplicates)
}
