package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/nvawntien/telegram-bot/internal/domain"
)

type UserRepository interface {
	UpsertTelegramUser(context.Context, TelegramProfile) (User, error)
}

type UserService struct {
	repository UserRepository
}

func NewUserService(repository UserRepository) *UserService {
	return &UserService{repository: repository}
}

func (s *UserService) Sync(ctx context.Context, profile TelegramProfile) (User, error) {
	if profile.TelegramUserID <= 0 {
		return User{}, fmt.Errorf("telegram user id: %w", ErrInvalidInput)
	}
	profile.Username = strings.TrimSpace(profile.Username)
	profile.FirstName = strings.TrimSpace(profile.FirstName)
	profile.LastName = strings.TrimSpace(profile.LastName)
	user, err := s.repository.UpsertTelegramUser(ctx, profile)
	if err != nil {
		return User{}, fmt.Errorf("sync Telegram user: %w", err)
	}
	if user.Status == domain.UserStatusBanned || user.Status == domain.UserStatusDisabled {
		return user, ErrUserBlocked
	}
	return user, nil
}
