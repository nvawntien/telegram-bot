package app

import (
	"encoding/json"
	"time"

	"github.com/nvawntien/telegram-bot/internal/domain"
)

const (
	DefaultPageSize = 8
	MaxPageSize     = 20
)

type User struct {
	ID             int64
	TelegramUserID int64
	Username       string
	DisplayName    string
	Status         domain.UserStatus
}

type TelegramProfile struct {
	TelegramUserID int64
	Username       string
	FirstName      string
	LastName       string
}

type Admin struct {
	ID             int64
	UserID         int64
	TelegramUserID int64
	Role           string
	Active         bool
	UserStatus     domain.UserStatus
}

func (a Admin) CanOpenAdmin() bool {
	return a.Active && a.UserStatus == domain.UserStatusActive
}

func (a Admin) CanManageCatalog() bool {
	if !a.CanOpenAdmin() {
		return false
	}
	switch a.Role {
	case "owner", "admin", "operator":
		return true
	default:
		return false
	}
}

type Category struct {
	ID        int64
	Name      string
	Slug      string
	Emoji     string
	SortOrder int32
	Active    bool
	Version   int64
}

type Product struct {
	ID           int64
	CategoryID   int64
	Name         string
	Slug         string
	Description  string
	Price        domain.Money
	DeliveryType string
	ContactURL   string
	Active       bool
	Version      int64
}

type PageInfo struct {
	Page       int
	PageSize   int
	TotalItems int64
	TotalPages int
}

type CategoryPage struct {
	Items []Category
	Page  PageInfo
}

type ProductPage struct {
	Items []Product
	Page  PageInfo
}

type AdminSession struct {
	ID        int64
	AdminID   int64
	State     string
	Payload   json.RawMessage
	ExpiresAt time.Time
	Version   int64
}

type RequestMeta struct {
	RequestID string
	UpdateID  int64
}

type SessionCommand struct {
	AdminTelegramID int64
	SessionID       int64
	ExpectedVersion int64
	Meta            RequestMeta
}

type CategorySnapshot struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	SortOrder int32  `json:"sort_order"`
	Active    bool   `json:"active"`
	Version   int64  `json:"version"`
}

type ProductSnapshot struct {
	ID          int64  `json:"id"`
	CategoryID  int64  `json:"category_id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	PriceVND    int64  `json:"price_vnd"`
	Active      bool   `json:"active"`
	Version     int64  `json:"version"`
}

func pageInfo(page, size int, total int64) PageInfo {
	totalPages := 0
	if total > 0 {
		totalPages = int((total + int64(size) - 1) / int64(size))
	}
	return PageInfo{Page: page, PageSize: size, TotalItems: total, TotalPages: totalPages}
}
