package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/nvawntien/telegram-bot/internal/domain"
)

const (
	SessionCategoryCreate  = "category.create"
	SessionCategoryEdit    = "category.edit"
	SessionCategoryToggle  = "category.toggle"
	SessionProductCreate   = "product.create"
	SessionProductEdit     = "product.edit"
	SessionProductToggle   = "product.toggle"
	SessionInventoryImport = "inventory.import"
	SessionInventoryToggle = "inventory.toggle"
)

type AdminRepository interface {
	BootstrapAdmin(context.Context, int64) error
	AuthorizeAdmin(context.Context, int64) (Admin, error)
	StartSession(context.Context, Admin, string, json.RawMessage, time.Time, RequestMeta) (AdminSession, error)
	GetActiveSession(context.Context, int64) (AdminSession, error)
	AdvanceSession(context.Context, Admin, AdminSession, string, json.RawMessage, time.Time, RequestMeta) (AdminSession, error)
	CancelSession(context.Context, Admin, AdminSession, RequestMeta) error
	CreateCategory(context.Context, Admin, AdminSession, CreateCategoryInput) (Category, error)
	UpdateCategory(context.Context, Admin, AdminSession, UpdateCategoryInput) (Category, error)
	SetCategoryActive(context.Context, Admin, AdminSession, SetCategoryActiveInput) (Category, error)
	CreateProduct(context.Context, Admin, AdminSession, CreateProductInput) (Product, error)
	UpdateProduct(context.Context, Admin, AdminSession, UpdateProductInput) (Product, error)
	SetProductActive(context.Context, Admin, AdminSession, SetProductActiveInput) (Product, error)
}

type AdminService struct {
	repository AdminRepository
	sessionTTL time.Duration
	clock      func() time.Time
}

func NewAdminService(repository AdminRepository, sessionTTL time.Duration) *AdminService {
	return &AdminService{repository: repository, sessionTTL: sessionTTL, clock: time.Now}
}

func (s *AdminService) Bootstrap(ctx context.Context, telegramIDs []int64) error {
	seen := make(map[int64]struct{}, len(telegramIDs))
	for _, telegramID := range telegramIDs {
		if telegramID <= 0 {
			return ErrInvalidInput
		}
		if _, exists := seen[telegramID]; exists {
			continue
		}
		seen[telegramID] = struct{}{}
		if err := s.repository.BootstrapAdmin(ctx, telegramID); err != nil {
			return fmt.Errorf("bootstrap admin %d: %w", telegramID, err)
		}
	}
	return nil
}

func (s *AdminService) Authorize(ctx context.Context, telegramID int64, mutation bool) (Admin, error) {
	if telegramID <= 0 {
		return Admin{}, ErrUnauthorized
	}
	admin, err := s.repository.AuthorizeAdmin(ctx, telegramID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return Admin{}, ErrForbidden
		}
		return Admin{}, fmt.Errorf("authorize admin: %w", err)
	}
	if !admin.CanOpenAdmin() || (mutation && !admin.CanManageCatalog()) {
		return Admin{}, ErrForbidden
	}
	return admin, nil
}

func (s *AdminService) StartSession(
	ctx context.Context,
	telegramID int64,
	state string,
	payload any,
	meta RequestMeta,
) (AdminSession, error) {
	if !validSessionState(state) {
		return AdminSession{}, ErrInvalidInput
	}
	admin, err := s.Authorize(ctx, telegramID, true)
	if err != nil {
		return AdminSession{}, err
	}
	encoded, err := encodePayload(payload)
	if err != nil {
		return AdminSession{}, err
	}
	return s.repository.StartSession(ctx, admin, state, encoded, s.clock().Add(s.sessionTTL), meta)
}

func (s *AdminService) LoadSession(ctx context.Context, telegramID int64) (AdminSession, error) {
	admin, err := s.Authorize(ctx, telegramID, true)
	if err != nil {
		return AdminSession{}, err
	}
	session, err := s.repository.GetActiveSession(ctx, admin.ID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return AdminSession{}, ErrSessionExpired
		}
		return AdminSession{}, err
	}
	if !session.ExpiresAt.After(s.clock()) {
		return AdminSession{}, ErrSessionExpired
	}
	return session, nil
}

func (s *AdminService) AdvanceSession(
	ctx context.Context,
	telegramID int64,
	session AdminSession,
	state string,
	payload any,
	meta RequestMeta,
) (AdminSession, error) {
	if !validSessionState(state) {
		return AdminSession{}, ErrInvalidInput
	}
	admin, err := s.Authorize(ctx, telegramID, true)
	if err != nil {
		return AdminSession{}, err
	}
	encoded, err := encodePayload(payload)
	if err != nil {
		return AdminSession{}, err
	}
	return s.repository.AdvanceSession(ctx, admin, session, state, encoded, s.clock().Add(s.sessionTTL), meta)
}

func (s *AdminService) CancelSession(ctx context.Context, telegramID int64, session AdminSession, meta RequestMeta) error {
	admin, err := s.Authorize(ctx, telegramID, true)
	if err != nil {
		return err
	}
	return s.repository.CancelSession(ctx, admin, session, meta)
}

type CreateCategoryInput struct {
	Name      string
	Slug      string
	SortOrder int32
	Meta      RequestMeta
}

type UpdateCategoryInput struct {
	CategoryID     int64
	ExpectedRecord int64
	Name           string
	SortOrder      int32
	Meta           RequestMeta
}

type SetCategoryActiveInput struct {
	CategoryID     int64
	ExpectedRecord int64
	Active         bool
	Meta           RequestMeta
}

func (s *AdminService) CreateCategory(ctx context.Context, telegramID int64, session AdminSession, input CreateCategoryInput) (Category, error) {
	input.Name = strings.TrimSpace(input.Name)
	input.Slug = strings.TrimSpace(input.Slug)
	if err := validateCategory(input.Name, input.Slug, input.SortOrder); err != nil {
		return Category{}, err
	}
	admin, err := s.Authorize(ctx, telegramID, true)
	if err != nil {
		return Category{}, err
	}
	return s.repository.CreateCategory(ctx, admin, session, input)
}

func (s *AdminService) UpdateCategory(ctx context.Context, telegramID int64, session AdminSession, input UpdateCategoryInput) (Category, error) {
	input.Name = strings.TrimSpace(input.Name)
	if input.CategoryID <= 0 || input.ExpectedRecord <= 0 || input.Name == "" || len([]rune(input.Name)) > 120 || input.SortOrder < 0 {
		return Category{}, ErrInvalidInput
	}
	admin, err := s.Authorize(ctx, telegramID, true)
	if err != nil {
		return Category{}, err
	}
	return s.repository.UpdateCategory(ctx, admin, session, input)
}

func (s *AdminService) SetCategoryActive(ctx context.Context, telegramID int64, session AdminSession, input SetCategoryActiveInput) (Category, error) {
	if input.CategoryID <= 0 || input.ExpectedRecord <= 0 {
		return Category{}, ErrInvalidInput
	}
	admin, err := s.Authorize(ctx, telegramID, true)
	if err != nil {
		return Category{}, err
	}
	return s.repository.SetCategoryActive(ctx, admin, session, input)
}

type CreateProductInput struct {
	CategoryID  int64
	Name        string
	Slug        string
	Description string
	Price       domain.Money
	Meta        RequestMeta
}

type UpdateProductInput struct {
	ProductID      int64
	ExpectedRecord int64
	CategoryID     int64
	Name           string
	Description    string
	Price          domain.Money
	Meta           RequestMeta
}

type SetProductActiveInput struct {
	ProductID      int64
	ExpectedRecord int64
	Active         bool
	Meta           RequestMeta
}

func (s *AdminService) CreateProduct(ctx context.Context, telegramID int64, session AdminSession, input CreateProductInput) (Product, error) {
	normalizeProduct(&input.Name, &input.Slug, &input.Description)
	if err := validateProduct(input.CategoryID, input.Name, input.Slug, input.Description, input.Price); err != nil {
		return Product{}, err
	}
	admin, err := s.Authorize(ctx, telegramID, true)
	if err != nil {
		return Product{}, err
	}
	return s.repository.CreateProduct(ctx, admin, session, input)
}

func (s *AdminService) UpdateProduct(ctx context.Context, telegramID int64, session AdminSession, input UpdateProductInput) (Product, error) {
	input.Name = strings.TrimSpace(input.Name)
	input.Description = strings.TrimSpace(input.Description)
	if input.ProductID <= 0 || input.ExpectedRecord <= 0 || validateProduct(input.CategoryID, input.Name, "valid", input.Description, input.Price) != nil {
		return Product{}, ErrInvalidInput
	}
	admin, err := s.Authorize(ctx, telegramID, true)
	if err != nil {
		return Product{}, err
	}
	return s.repository.UpdateProduct(ctx, admin, session, input)
}

func (s *AdminService) SetProductActive(ctx context.Context, telegramID int64, session AdminSession, input SetProductActiveInput) (Product, error) {
	if input.ProductID <= 0 || input.ExpectedRecord <= 0 {
		return Product{}, ErrInvalidInput
	}
	admin, err := s.Authorize(ctx, telegramID, true)
	if err != nil {
		return Product{}, err
	}
	return s.repository.SetProductActive(ctx, admin, session, input)
}

func ParseMoneyInput(raw string) (domain.Money, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.ContainsAny(raw, ".,") {
		return 0, ErrInvalidInput
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, ErrInvalidInput
	}
	money, err := domain.NewMoney(value)
	if err != nil {
		return 0, ErrInvalidInput
	}
	return money, nil
}

func validateCategory(name, slug string, sortOrder int32) error {
	if name == "" || len([]rune(name)) > 120 || slug == "" || len(slug) > 160 || sortOrder < 0 {
		return ErrInvalidInput
	}
	for _, char := range slug {
		if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '-' {
			return ErrInvalidInput
		}
	}
	return nil
}

func validateProduct(categoryID int64, name, slug, description string, price domain.Money) error {
	if categoryID <= 0 || name == "" || len([]rune(name)) > 160 || slug == "" || len(slug) > 180 || len([]rune(description)) > 2000 || price < 0 {
		return ErrInvalidInput
	}
	return nil
}

func normalizeProduct(name, slug, description *string) {
	*name = strings.TrimSpace(*name)
	*slug = strings.TrimSpace(*slug)
	*description = strings.TrimSpace(*description)
}

func validSessionState(state string) bool {
	switch state {
	case SessionCategoryCreate, SessionCategoryEdit, SessionCategoryToggle,
		SessionProductCreate, SessionProductEdit, SessionProductToggle,
		SessionInventoryImport, SessionInventoryToggle:
		return true
	default:
		return false
	}
}

func encodePayload(payload any) (json.RawMessage, error) {
	if payload == nil {
		return json.RawMessage(`{}`), nil
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode session payload: %w", err)
	}
	if len(encoded) > 16*1024 {
		return nil, ErrInvalidInput
	}
	return encoded, nil
}
