// Package app contains transport-independent application services.
package app

import "errors"

var (
	ErrInvalidInput     = errors.New("invalid input")
	ErrNotFound         = errors.New("resource not found")
	ErrUnauthorized     = errors.New("unauthorized")
	ErrForbidden        = errors.New("forbidden")
	ErrUserBlocked      = errors.New("user is blocked")
	ErrSessionExpired   = errors.New("admin session expired")
	ErrStaleVersion     = errors.New("stale version")
	ErrConflict         = errors.New("resource conflict")
	ErrDuplicateUpdate  = errors.New("update already completed")
	ErrUpdateInProgress = errors.New("update is already processing")
)
