package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nvawntien/telegram-bot/internal/app"
	"github.com/nvawntien/telegram-bot/internal/domain"
	"github.com/nvawntien/telegram-bot/internal/postgres/generated"
)

// AppStore adapts use-case-oriented sqlc queries to application-owned models.
type AppStore struct {
	queries    *generated.Queries
	transactor *Transactor
}

func NewAppStore(pool *pgxpool.Pool) *AppStore {
	return &AppStore{queries: generated.New(pool), transactor: NewTransactor(pool)}
}

func (s *AppStore) UpsertTelegramUser(ctx context.Context, profile app.TelegramProfile) (app.User, error) {
	displayName := strings.TrimSpace(strings.Join([]string{profile.FirstName, profile.LastName}, " "))
	row, err := s.queries.UpsertTelegramUser(ctx, generated.UpsertTelegramUserParams{
		TelegramUserID: profile.TelegramUserID,
		Username:       optionalText(profile.Username),
		DisplayName:    optionalText(displayName),
	})
	if err != nil {
		return app.User{}, fmt.Errorf("upsert user: %w", err)
	}
	return mapUser(row), nil
}

func (s *AppStore) ListActiveCategories(ctx context.Context, offset, limit int32) ([]app.Category, int64, error) {
	total, err := s.queries.CountActiveCategories(ctx)
	if err != nil {
		return nil, 0, err
	}
	rows, err := s.queries.ListActiveCategoriesPage(ctx, generated.ListActiveCategoriesPageParams{PageOffset: offset, PageLimit: limit})
	if err != nil {
		return nil, 0, err
	}
	return mapCategories(rows), total, nil
}

func (s *AppStore) ListActiveProducts(ctx context.Context, categoryID int64, offset, limit int32) ([]app.Product, int64, error) {
	total, err := s.queries.CountActiveProductsByCategory(ctx, categoryID)
	if err != nil {
		return nil, 0, err
	}
	rows, err := s.queries.ListActiveProductsPage(ctx, generated.ListActiveProductsPageParams{
		CategoryID: categoryID,
		PageOffset: offset,
		PageLimit:  limit,
	})
	if err != nil {
		return nil, 0, err
	}
	return mapProducts(rows), total, nil
}

func (s *AppStore) GetActiveProduct(ctx context.Context, productID int64) (app.Product, error) {
	row, err := s.queries.GetActiveProductDetail(ctx, productID)
	if errors.Is(err, pgx.ErrNoRows) {
		return app.Product{}, app.ErrNotFound
	}
	if err != nil {
		return app.Product{}, err
	}
	return mapProduct(row), nil
}

func (s *AppStore) ListAdminCategories(ctx context.Context, offset, limit int32) ([]app.Category, int64, error) {
	total, err := s.queries.CountAdminCategories(ctx)
	if err != nil {
		return nil, 0, err
	}
	rows, err := s.queries.ListAdminCategoriesPage(ctx, generated.ListAdminCategoriesPageParams{PageOffset: offset, PageLimit: limit})
	if err != nil {
		return nil, 0, err
	}
	return mapCategories(rows), total, nil
}

func (s *AppStore) ListAdminProducts(ctx context.Context, offset, limit int32) ([]app.Product, int64, error) {
	total, err := s.queries.CountAdminProducts(ctx)
	if err != nil {
		return nil, 0, err
	}
	rows, err := s.queries.ListAdminProductsPage(ctx, generated.ListAdminProductsPageParams{PageOffset: offset, PageLimit: limit})
	if err != nil {
		return nil, 0, err
	}
	return mapProducts(rows), total, nil
}

func (s *AppStore) ClaimUpdate(ctx context.Context, updateID int64, updateType string, staleBefore time.Time) (app.UpdateClaim, error) {
	claim := app.UpdateClaimed
	err := s.transactor.WithinTransaction(ctx, func(ctx context.Context, queries *generated.Queries) error {
		if _, err := queries.InsertTelegramUpdateReceipt(ctx, generated.InsertTelegramUpdateReceiptParams{
			UpdateID: updateID, UpdateType: updateType,
		}); err != nil {
			return err
		}
		receipt, err := queries.LockTelegramUpdateReceipt(ctx, updateID)
		if err != nil {
			return err
		}
		switch receipt.Status {
		case "completed":
			claim = app.UpdateDuplicateCompleted
			return nil
		case "processing":
			if receipt.ProcessingStartedAt.Valid && receipt.ProcessingStartedAt.Time.After(staleBefore) {
				claim = app.UpdateDuplicateProcessing
				return nil
			}
		}
		_, err = queries.StartTelegramUpdateProcessing(ctx, generated.StartTelegramUpdateProcessingParams{
			UpdateID:    updateID,
			StaleBefore: pgtype.Timestamptz{Time: staleBefore, Valid: true},
		})
		if errors.Is(err, pgx.ErrNoRows) {
			claim = app.UpdateDuplicateProcessing
			return nil
		}
		return err
	})
	return claim, err
}

func (s *AppStore) CompleteUpdate(ctx context.Context, updateID int64) error {
	_, err := s.queries.CompleteTelegramUpdateReceipt(ctx, updateID)
	if errors.Is(err, pgx.ErrNoRows) {
		return app.ErrDuplicateUpdate
	}
	return err
}

func (s *AppStore) FailUpdate(ctx context.Context, updateID int64, code string) error {
	_, err := s.queries.FailTelegramUpdateReceipt(ctx, generated.FailTelegramUpdateReceiptParams{
		LastError: optionalText(code), UpdateID: updateID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return app.ErrDuplicateUpdate
	}
	return err
}

func mapUser(row generated.User) app.User {
	return app.User{
		ID: row.ID, TelegramUserID: row.TelegramUserID,
		Username: row.Username.String, DisplayName: row.DisplayName.String,
		Status: domain.UserStatus(row.Status),
	}
}

func mapCategory(row generated.Category) app.Category {
	return app.Category{
		ID: row.ID, Name: row.Name, Slug: row.Slug, Emoji: row.Emoji,
		SortOrder: row.SortOrder, Active: row.IsActive, Version: row.Version,
	}
}

func mapCategories(rows []generated.Category) []app.Category {
	result := make([]app.Category, 0, len(rows))
	for _, row := range rows {
		result = append(result, mapCategory(row))
	}
	return result
}

func mapProduct(row generated.Product) app.Product {
	return app.Product{
		ID: row.ID, CategoryID: row.CategoryID, Name: row.Name, Slug: row.Slug,
		Description: row.Description.String, Price: domain.Money(row.PriceVnd),
		DeliveryType: row.DeliveryType, ContactURL: row.ContactUrl.String,
		Active: row.IsActive, Version: row.Version,
	}
}

func mapProducts(rows []generated.Product) []app.Product {
	result := make([]app.Product, 0, len(rows))
	for _, row := range rows {
		result = append(result, mapProduct(row))
	}
	return result
}

func optionalText(value string) pgtype.Text {
	value = strings.TrimSpace(value)
	return pgtype.Text{String: value, Valid: value != ""}
}

func mapConstraintError(err error) error {
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) && postgresError.Code == "23505" {
		return fmt.Errorf("unique constraint: %w", app.ErrConflict)
	}
	return err
}
