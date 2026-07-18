package app

import (
	"context"
	"errors"
	"testing"

	"github.com/nvawntien/telegram-bot/internal/domain"
)

type userRepositoryFunc func(context.Context, TelegramProfile) (User, error)

func (f userRepositoryFunc) UpsertTelegramUser(ctx context.Context, profile TelegramProfile) (User, error) {
	return f(ctx, profile)
}

func TestUserServiceValidatesStatusAndProfile(t *testing.T) {
	service := NewUserService(userRepositoryFunc(func(_ context.Context, profile TelegramProfile) (User, error) {
		if profile.Username != "tester" || profile.FirstName != "Test" || profile.LastName != "User" {
			t.Fatalf("profile was not normalized: %#v", profile)
		}
		return User{TelegramUserID: profile.TelegramUserID, Status: domain.UserStatusActive}, nil
	}))
	user, err := service.Sync(context.Background(), TelegramProfile{
		TelegramUserID: 10, Username: " tester ", FirstName: " Test ", LastName: " User ",
	})
	if err != nil || user.TelegramUserID != 10 {
		t.Fatalf("Sync() = %#v, %v", user, err)
	}
}

func TestUserServiceRejectsBlockedAndInvalidUsers(t *testing.T) {
	service := NewUserService(userRepositoryFunc(func(_ context.Context, profile TelegramProfile) (User, error) {
		return User{TelegramUserID: profile.TelegramUserID, Status: domain.UserStatusBanned}, nil
	}))
	if _, err := service.Sync(context.Background(), TelegramProfile{TelegramUserID: 0}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("invalid user error = %v", err)
	}
	if _, err := service.Sync(context.Background(), TelegramProfile{TelegramUserID: 10}); !errors.Is(err, ErrUserBlocked) {
		t.Fatalf("banned user error = %v", err)
	}
}
