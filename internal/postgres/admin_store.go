package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/nvawntien/telegram-bot/internal/app"
	"github.com/nvawntien/telegram-bot/internal/domain"
	"github.com/nvawntien/telegram-bot/internal/postgres/generated"
)

func (s *AppStore) BootstrapAdmin(ctx context.Context, telegramID int64) error {
	return s.transactor.WithinTransaction(ctx, func(ctx context.Context, queries *generated.Queries) error {
		user, err := queries.EnsureBootstrapUser(ctx, telegramID)
		if err != nil {
			return err
		}
		_, err = queries.EnsureBootstrapAdmin(ctx, user.ID)
		return err
	})
}

func (s *AppStore) AuthorizeAdmin(ctx context.Context, telegramID int64) (app.Admin, error) {
	return authorizeAdmin(ctx, s.queries, telegramID)
}

func authorizeAdmin(ctx context.Context, queries *generated.Queries, telegramID int64) (app.Admin, error) {
	row, err := queries.GetAdminAuthorizationByTelegramID(ctx, telegramID)
	if errors.Is(err, pgx.ErrNoRows) {
		return app.Admin{}, app.ErrNotFound
	}
	if err != nil {
		return app.Admin{}, err
	}
	return app.Admin{
		ID: row.AdminID, UserID: row.UserID, TelegramUserID: row.TelegramUserID,
		UserStatus: domain.UserStatus(row.UserStatus), Role: row.Role, Active: row.IsActive,
	}, nil
}

func (s *AppStore) StartSession(
	ctx context.Context,
	admin app.Admin,
	state string,
	payload json.RawMessage,
	expiresAt time.Time,
	meta app.RequestMeta,
) (app.AdminSession, error) {
	var result app.AdminSession
	err := s.transactor.WithinTransaction(ctx, func(ctx context.Context, queries *generated.Queries) error {
		if err := requireCatalogAdmin(ctx, queries, admin); err != nil {
			return err
		}
		row, err := queries.StartAdminSession(ctx, generated.StartAdminSessionParams{
			AdminID: admin.ID, State: state, Payload: payload,
			ExpiresAt: pgtype.Timestamptz{Time: expiresAt, Valid: true},
		})
		if err != nil {
			return err
		}
		if err := insertAudit(ctx, queries, admin.ID, "admin.session.started", "admin_session", row.ID, nil,
			map[string]any{"state": row.State, "version": row.Version}, meta); err != nil {
			return err
		}
		if err := completeReceipt(ctx, queries, meta.UpdateID); err != nil {
			return err
		}
		result = mapSession(row)
		return nil
	})
	return result, mapConstraintError(err)
}

func (s *AppStore) GetActiveSession(ctx context.Context, adminID int64) (app.AdminSession, error) {
	row, err := s.queries.GetActiveAdminSession(ctx, adminID)
	if errors.Is(err, pgx.ErrNoRows) {
		return app.AdminSession{}, app.ErrNotFound
	}
	if err != nil {
		return app.AdminSession{}, err
	}
	return mapSession(row), nil
}

func (s *AppStore) AdvanceSession(
	ctx context.Context,
	admin app.Admin,
	session app.AdminSession,
	state string,
	payload json.RawMessage,
	expiresAt time.Time,
	meta app.RequestMeta,
) (app.AdminSession, error) {
	var result app.AdminSession
	err := s.transactor.WithinTransaction(ctx, func(ctx context.Context, queries *generated.Queries) error {
		if err := requireCatalogAdmin(ctx, queries, admin); err != nil {
			return err
		}
		if _, err := lockAndVerifySession(ctx, queries, admin.ID, session, ""); err != nil {
			return err
		}
		row, err := queries.AdvanceAdminSessionGuarded(ctx, generated.AdvanceAdminSessionGuardedParams{
			State: state, Payload: payload,
			ExpiresAt: pgtype.Timestamptz{Time: expiresAt, Valid: true},
			ID:        session.ID, AdminID: admin.ID, ExpectedVersion: session.Version,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return app.ErrStaleVersion
		}
		if err != nil {
			return err
		}
		if err := completeReceipt(ctx, queries, meta.UpdateID); err != nil {
			return err
		}
		result = mapSession(row)
		return nil
	})
	return result, err
}

func (s *AppStore) CancelSession(ctx context.Context, admin app.Admin, session app.AdminSession, meta app.RequestMeta) error {
	return s.transactor.WithinTransaction(ctx, func(ctx context.Context, queries *generated.Queries) error {
		if err := requireCatalogAdmin(ctx, queries, admin); err != nil {
			return err
		}
		if _, err := lockAndVerifySession(ctx, queries, admin.ID, session, ""); err != nil {
			return err
		}
		row, err := queries.FinishAdminSessionGuarded(ctx, generated.FinishAdminSessionGuardedParams{
			State: "cancelled", ID: session.ID, AdminID: admin.ID, ExpectedVersion: session.Version,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return app.ErrStaleVersion
		}
		if err != nil {
			return err
		}
		if err := insertAudit(ctx, queries, admin.ID, "admin.session.cancelled", "admin_session", row.ID,
			map[string]any{"state": session.State, "version": session.Version},
			map[string]any{"state": row.State, "version": row.Version}, meta); err != nil {
			return err
		}
		return completeReceipt(ctx, queries, meta.UpdateID)
	})
}

func (s *AppStore) CreateCategory(ctx context.Context, admin app.Admin, session app.AdminSession, input app.CreateCategoryInput) (app.Category, error) {
	var result app.Category
	err := s.transactor.WithinTransaction(ctx, func(ctx context.Context, queries *generated.Queries) error {
		if err := requireCatalogAdmin(ctx, queries, admin); err != nil {
			return err
		}
		if _, err := lockAndVerifySession(ctx, queries, admin.ID, session, app.SessionCategoryCreate); err != nil {
			return err
		}
		row, err := queries.CreateCategory(ctx, generated.CreateCategoryParams{Name: input.Name, Slug: input.Slug, SortOrder: input.SortOrder})
		if err != nil {
			return err
		}
		after := categorySnapshot(row)
		if err := insertAudit(ctx, queries, admin.ID, "category.created", "category", row.ID, nil, after, input.Meta); err != nil {
			return err
		}
		if err := finishSession(ctx, queries, admin.ID, session); err != nil {
			return err
		}
		if err := completeReceipt(ctx, queries, input.Meta.UpdateID); err != nil {
			return err
		}
		result = mapCategory(row)
		return nil
	})
	return result, mapConstraintError(err)
}

func (s *AppStore) UpdateCategory(ctx context.Context, admin app.Admin, session app.AdminSession, input app.UpdateCategoryInput) (app.Category, error) {
	var result app.Category
	err := s.transactor.WithinTransaction(ctx, func(ctx context.Context, queries *generated.Queries) error {
		if err := requireCatalogAdmin(ctx, queries, admin); err != nil {
			return err
		}
		if _, err := lockAndVerifySession(ctx, queries, admin.ID, session, app.SessionCategoryEdit); err != nil {
			return err
		}
		before, err := queries.LockCategoryByID(ctx, input.CategoryID)
		if errors.Is(err, pgx.ErrNoRows) {
			return app.ErrNotFound
		}
		if err != nil {
			return err
		}
		if before.Version != input.ExpectedRecord {
			return app.ErrStaleVersion
		}
		after, err := queries.UpdateCategoryDetailsGuarded(ctx, generated.UpdateCategoryDetailsGuardedParams{
			Name: input.Name, SortOrder: input.SortOrder, ID: input.CategoryID, ExpectedVersion: input.ExpectedRecord,
		})
		if err != nil {
			return err
		}
		if err := insertAudit(ctx, queries, admin.ID, "category.updated", "category", after.ID,
			categorySnapshot(before), categorySnapshot(after), input.Meta); err != nil {
			return err
		}
		if err := finishSession(ctx, queries, admin.ID, session); err != nil {
			return err
		}
		if err := completeReceipt(ctx, queries, input.Meta.UpdateID); err != nil {
			return err
		}
		result = mapCategory(after)
		return nil
	})
	return result, mapConstraintError(err)
}

func (s *AppStore) SetCategoryActive(ctx context.Context, admin app.Admin, session app.AdminSession, input app.SetCategoryActiveInput) (app.Category, error) {
	var result app.Category
	err := s.transactor.WithinTransaction(ctx, func(ctx context.Context, queries *generated.Queries) error {
		if err := requireCatalogAdmin(ctx, queries, admin); err != nil {
			return err
		}
		if _, err := lockAndVerifySession(ctx, queries, admin.ID, session, app.SessionCategoryToggle); err != nil {
			return err
		}
		before, err := queries.LockCategoryByID(ctx, input.CategoryID)
		if errors.Is(err, pgx.ErrNoRows) {
			return app.ErrNotFound
		}
		if err != nil {
			return err
		}
		if before.Version != input.ExpectedRecord || before.IsActive == input.Active {
			return app.ErrStaleVersion
		}
		after, err := queries.SetCategoryActiveGuarded(ctx, generated.SetCategoryActiveGuardedParams{
			IsActive: input.Active, ID: input.CategoryID, ExpectedVersion: input.ExpectedRecord,
		})
		if err != nil {
			return err
		}
		action := "category.deactivated"
		if input.Active {
			action = "category.activated"
		}
		if err := insertAudit(ctx, queries, admin.ID, action, "category", after.ID,
			categorySnapshot(before), categorySnapshot(after), input.Meta); err != nil {
			return err
		}
		if err := finishSession(ctx, queries, admin.ID, session); err != nil {
			return err
		}
		if err := completeReceipt(ctx, queries, input.Meta.UpdateID); err != nil {
			return err
		}
		result = mapCategory(after)
		return nil
	})
	return result, err
}

func (s *AppStore) CreateProduct(ctx context.Context, admin app.Admin, session app.AdminSession, input app.CreateProductInput) (app.Product, error) {
	var result app.Product
	err := s.transactor.WithinTransaction(ctx, func(ctx context.Context, queries *generated.Queries) error {
		if err := requireCatalogAdmin(ctx, queries, admin); err != nil {
			return err
		}
		if _, err := lockAndVerifySession(ctx, queries, admin.ID, session, app.SessionProductCreate); err != nil {
			return err
		}
		if _, err := queries.GetCategoryByID(ctx, input.CategoryID); errors.Is(err, pgx.ErrNoRows) {
			return app.ErrNotFound
		} else if err != nil {
			return err
		}
		row, err := queries.CreateProduct(ctx, generated.CreateProductParams{
			CategoryID: input.CategoryID, Name: input.Name, Slug: input.Slug,
			Description: optionalText(input.Description), PriceVnd: input.Price.Int64(),
			DeliveryType: "inventory", ContactUrl: pgtype.Text{},
		})
		if err != nil {
			return err
		}
		if err := insertAudit(ctx, queries, admin.ID, "product.created", "product", row.ID, nil, productSnapshot(row), input.Meta); err != nil {
			return err
		}
		if err := finishSession(ctx, queries, admin.ID, session); err != nil {
			return err
		}
		if err := completeReceipt(ctx, queries, input.Meta.UpdateID); err != nil {
			return err
		}
		result = mapProduct(row)
		return nil
	})
	return result, mapConstraintError(err)
}

func (s *AppStore) UpdateProduct(ctx context.Context, admin app.Admin, session app.AdminSession, input app.UpdateProductInput) (app.Product, error) {
	var result app.Product
	err := s.transactor.WithinTransaction(ctx, func(ctx context.Context, queries *generated.Queries) error {
		if err := requireCatalogAdmin(ctx, queries, admin); err != nil {
			return err
		}
		if _, err := lockAndVerifySession(ctx, queries, admin.ID, session, app.SessionProductEdit); err != nil {
			return err
		}
		if _, err := queries.GetCategoryByID(ctx, input.CategoryID); errors.Is(err, pgx.ErrNoRows) {
			return app.ErrNotFound
		} else if err != nil {
			return err
		}
		before, err := queries.LockProductByID(ctx, input.ProductID)
		if errors.Is(err, pgx.ErrNoRows) {
			return app.ErrNotFound
		}
		if err != nil {
			return err
		}
		if before.Version != input.ExpectedRecord {
			return app.ErrStaleVersion
		}
		after, err := queries.UpdateProductDetailsGuarded(ctx, generated.UpdateProductDetailsGuardedParams{
			CategoryID: input.CategoryID, Name: input.Name, Description: optionalText(input.Description),
			PriceVnd: input.Price.Int64(), ID: input.ProductID, ExpectedVersion: input.ExpectedRecord,
		})
		if err != nil {
			return err
		}
		if err := insertAudit(ctx, queries, admin.ID, "product.updated", "product", after.ID,
			productSnapshot(before), productSnapshot(after), input.Meta); err != nil {
			return err
		}
		if err := finishSession(ctx, queries, admin.ID, session); err != nil {
			return err
		}
		if err := completeReceipt(ctx, queries, input.Meta.UpdateID); err != nil {
			return err
		}
		result = mapProduct(after)
		return nil
	})
	return result, mapConstraintError(err)
}

func (s *AppStore) SetProductActive(ctx context.Context, admin app.Admin, session app.AdminSession, input app.SetProductActiveInput) (app.Product, error) {
	var result app.Product
	err := s.transactor.WithinTransaction(ctx, func(ctx context.Context, queries *generated.Queries) error {
		if err := requireCatalogAdmin(ctx, queries, admin); err != nil {
			return err
		}
		if _, err := lockAndVerifySession(ctx, queries, admin.ID, session, app.SessionProductToggle); err != nil {
			return err
		}
		before, err := queries.LockProductByID(ctx, input.ProductID)
		if errors.Is(err, pgx.ErrNoRows) {
			return app.ErrNotFound
		}
		if err != nil {
			return err
		}
		if before.Version != input.ExpectedRecord || before.IsActive == input.Active {
			return app.ErrStaleVersion
		}
		after, err := queries.SetProductActiveGuarded(ctx, generated.SetProductActiveGuardedParams{
			IsActive: input.Active, ID: input.ProductID, ExpectedVersion: input.ExpectedRecord,
		})
		if err != nil {
			return err
		}
		action := "product.deactivated"
		if input.Active {
			action = "product.activated"
		}
		if err := insertAudit(ctx, queries, admin.ID, action, "product", after.ID,
			productSnapshot(before), productSnapshot(after), input.Meta); err != nil {
			return err
		}
		if err := finishSession(ctx, queries, admin.ID, session); err != nil {
			return err
		}
		if err := completeReceipt(ctx, queries, input.Meta.UpdateID); err != nil {
			return err
		}
		result = mapProduct(after)
		return nil
	})
	return result, err
}

func requireCatalogAdmin(ctx context.Context, queries *generated.Queries, expected app.Admin) error {
	current, err := authorizeAdmin(ctx, queries, expected.TelegramUserID)
	if err != nil {
		return app.ErrForbidden
	}
	if current.ID != expected.ID || !current.CanManageCatalog() {
		return app.ErrForbidden
	}
	return nil
}

func lockAndVerifySession(
	ctx context.Context,
	queries *generated.Queries,
	adminID int64,
	expected app.AdminSession,
	requiredState string,
) (generated.AdminSession, error) {
	row, err := queries.LockAdminSessionByID(ctx, expected.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return generated.AdminSession{}, app.ErrNotFound
	}
	if err != nil {
		return generated.AdminSession{}, err
	}
	if row.AdminID != adminID || expected.AdminID != adminID {
		return generated.AdminSession{}, app.ErrForbidden
	}
	if !row.ExpiresAt.Valid || !row.ExpiresAt.Time.After(time.Now()) {
		return generated.AdminSession{}, app.ErrSessionExpired
	}
	if row.Version != expected.Version {
		return generated.AdminSession{}, app.ErrStaleVersion
	}
	if requiredState != "" && row.State != requiredState {
		return generated.AdminSession{}, app.ErrStaleVersion
	}
	return row, nil
}

func finishSession(ctx context.Context, queries *generated.Queries, adminID int64, session app.AdminSession) error {
	_, err := queries.FinishAdminSessionGuarded(ctx, generated.FinishAdminSessionGuardedParams{
		State: "completed", ID: session.ID, AdminID: adminID, ExpectedVersion: session.Version,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return app.ErrStaleVersion
	}
	return err
}

func completeReceipt(ctx context.Context, queries *generated.Queries, updateID int64) error {
	_, err := queries.CompleteTelegramUpdateReceipt(ctx, updateID)
	if errors.Is(err, pgx.ErrNoRows) {
		return app.ErrDuplicateUpdate
	}
	return err
}

func insertAudit(
	ctx context.Context,
	queries *generated.Queries,
	adminID int64,
	action string,
	resourceType string,
	resourceID int64,
	before any,
	after any,
	meta app.RequestMeta,
) error {
	beforeJSON, err := marshalAuditValue(before)
	if err != nil {
		return err
	}
	afterJSON, err := marshalAuditValue(after)
	if err != nil {
		return err
	}
	_, err = queries.InsertAuditLog(ctx, generated.InsertAuditLogParams{
		ActorType: "admin", ActorID: pgtype.Int8{Int64: adminID, Valid: true},
		Action: action, ResourceType: resourceType,
		ResourceID: pgtype.Int8{Int64: resourceID, Valid: true},
		BeforeData: beforeJSON, AfterData: afterJSON,
		RequestID:        optionalText(meta.RequestID),
		TelegramUpdateID: pgtype.Int8{Int64: meta.UpdateID, Valid: true},
	})
	return err
}

func marshalAuditValue(value any) ([]byte, error) {
	if value == nil {
		return nil, nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("marshal audit value: %w", err)
	}
	return encoded, nil
}

func mapSession(row generated.AdminSession) app.AdminSession {
	return app.AdminSession{
		ID: row.ID, AdminID: row.AdminID, State: row.State,
		Payload: append([]byte(nil), row.Payload...), ExpiresAt: row.ExpiresAt.Time, Version: row.Version,
	}
}

func categorySnapshot(row generated.Category) app.CategorySnapshot {
	return app.CategorySnapshot{
		ID: row.ID, Name: row.Name, SortOrder: row.SortOrder,
		Active: row.IsActive, Version: row.Version,
	}
}

func productSnapshot(row generated.Product) app.ProductSnapshot {
	return app.ProductSnapshot{
		ID: row.ID, CategoryID: row.CategoryID, Name: row.Name,
		Description: row.Description.String, PriceVND: row.PriceVnd,
		Active: row.IsActive, Version: row.Version,
	}
}
