package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/nvawntien/telegram-bot/internal/app"
	"github.com/nvawntien/telegram-bot/internal/domain"
	"github.com/nvawntien/telegram-bot/internal/postgres/generated"
)

func (s *AppStore) ManualAcceptPayment(ctx context.Context, manual app.ManualPaymentCommand, acceptedAt time.Time, reservationTTL time.Duration) (app.PaymentAcceptanceResult, error) {
	var result app.PaymentAcceptanceResult
	err := s.transactor.WithinTransaction(ctx, func(ctx context.Context, queries *generated.Queries) error {
		admin, err := authorizePaymentAdmin(ctx, queries, manual.AdminTelegramID)
		if err != nil {
			return err
		}
		if _, err := lockAndVerifySession(ctx, queries, admin.ID, manual.Session, app.SessionPaymentManual); err != nil {
			return err
		}
		command := app.AcceptPaymentCommand{
			Provider: "manual", ProviderTransactionID: manual.ProviderTransactionID,
			Reference: manual.Reference, Amount: manual.Amount, Currency: manual.Currency,
			OccurredAt: manual.OccurredAt, Actor: app.PaymentActor{Type: "admin", ID: admin.ID},
			RequestID: manual.Meta.RequestID,
		}
		if err := acceptPaymentWithinTransaction(ctx, queries, command, acceptedAt, reservationTTL, &result); err != nil {
			return err
		}
		note, _ := json.Marshal(map[string]any{"note": manual.Note, "decision": result.Decision, "reason": result.Reason})
		if _, err := queries.InsertAuditLog(ctx, generated.InsertAuditLogParams{ActorType: "admin", ActorID: requiredInt8(admin.ID), Action: "payment.manual_confirmed", ResourceType: "payment", ResourceID: optionalInt8(result.PaymentID), AfterData: note, RequestID: optionalText(manual.Meta.RequestID), TelegramUpdateID: optionalInt8(manual.Meta.UpdateID)}); err != nil {
			return err
		}
		if err := finishSession(ctx, queries, admin.ID, manual.Session); err != nil {
			return err
		}
		return completeReceipt(ctx, queries, manual.Meta.UpdateID)
	})
	return result, mapConstraintError(err)
}

func (s *AppStore) ListPaymentReviews(ctx context.Context, adminTelegramID int64, offset, limit int32) ([]app.PaymentReviewCase, int64, error) {
	if _, err := s.AuthorizeAdmin(ctx, adminTelegramID); err != nil {
		return nil, 0, err
	}
	total, err := s.queries.CountOpenPaymentReviews(ctx)
	if err != nil {
		return nil, 0, err
	}
	rows, err := s.queries.ListOpenPaymentReviews(ctx, generated.ListOpenPaymentReviewsParams{PageLimit: limit, PageOffset: offset})
	if err != nil {
		return nil, 0, err
	}
	items := make([]app.PaymentReviewCase, 0, len(rows))
	for _, row := range rows {
		items = append(items, mapPaymentReview(row))
	}
	return items, total, nil
}

func (s *AppStore) ResolvePaymentReview(ctx context.Context, command app.ResolvePaymentReviewCommand) (app.PaymentReviewCase, error) {
	var result app.PaymentReviewCase
	err := s.transactor.WithinTransaction(ctx, func(ctx context.Context, queries *generated.Queries) error {
		admin, err := authorizePaymentAdmin(ctx, queries, command.AdminTelegramID)
		if err != nil {
			return err
		}
		if _, err := lockAndVerifySession(ctx, queries, admin.ID, command.Session, app.SessionPaymentReview); err != nil {
			return err
		}
		row, err := queries.ResolvePaymentReview(ctx, generated.ResolvePaymentReviewParams{Status: command.Status, ResolutionNote: optionalText(command.Note), AdminID: requiredInt8(admin.ID), ID: command.ReviewID})
		if errors.Is(err, pgx.ErrNoRows) {
			return app.ErrNotFound
		}
		if err != nil {
			return err
		}
		result = mapPaymentReview(row)
		after, _ := json.Marshal(map[string]any{"status": command.Status, "note": command.Note, "reason": row.Reason})
		if _, err := queries.InsertAuditLog(ctx, generated.InsertAuditLogParams{ActorType: "admin", ActorID: requiredInt8(admin.ID), Action: "payment.review_resolved", ResourceType: "payment_review", ResourceID: requiredInt8(row.ID), AfterData: after, RequestID: optionalText(command.Meta.RequestID), TelegramUpdateID: optionalInt8(command.Meta.UpdateID)}); err != nil {
			return err
		}
		if err := finishSession(ctx, queries, admin.ID, command.Session); err != nil {
			return err
		}
		return completeReceipt(ctx, queries, command.Meta.UpdateID)
	})
	return result, err
}

func authorizePaymentAdmin(ctx context.Context, queries *generated.Queries, telegramID int64) (app.Admin, error) {
	row, err := queries.GetAdminAuthorizationByTelegramID(ctx, telegramID)
	if errors.Is(err, pgx.ErrNoRows) {
		return app.Admin{}, app.ErrUnauthorized
	}
	if err != nil {
		return app.Admin{}, err
	}
	admin := app.Admin{ID: row.AdminID, UserID: row.UserID, TelegramUserID: row.TelegramUserID, UserStatus: domain.UserStatus(row.UserStatus), Role: row.Role, Active: row.IsActive}
	if !admin.CanManageCatalog() {
		return app.Admin{}, app.ErrForbidden
	}
	return admin, nil
}

func mapPaymentReview(row generated.PaymentReviewCase) app.PaymentReviewCase {
	return app.PaymentReviewCase{ID: row.ID, Provider: row.Provider, MaskedTransactionID: maskTransactionID(row.ProviderTransactionID.String), Reference: row.PaymentReference, Amount: domain.Money(row.AmountVnd), Currency: row.Currency, OccurredAt: row.OccurredAt.Time, OrderID: row.OrderID.Int64, TopupID: row.WalletTopupID.Int64, Reason: row.Reason, Status: row.Status}
}

func maskTransactionID(value string) string {
	if len(value) <= 6 {
		return "***"
	}
	return value[:2] + "***" + value[len(value)-4:]
}

var _ app.PaymentAdminRepository = (*AppStore)(nil)
