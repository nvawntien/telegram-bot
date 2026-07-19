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

	ErrInventoryNotFound        = errors.New("inventory item not found")
	ErrInventoryUnavailable     = errors.New("inventory item unavailable")
	ErrInsufficientInventory    = errors.New("insufficient inventory")
	ErrInventoryAlreadyExists   = errors.New("inventory item already exists")
	ErrInvalidInventoryState    = errors.New("invalid inventory state")
	ErrReservationNotOwned      = errors.New("reservation is not owned by order")
	ErrReservationExpired       = errors.New("inventory reservation expired")
	ErrUnsafeReservationRelease = errors.New("inventory reservation requires recovery")
	ErrEncryptionFailed         = errors.New("inventory encryption failed")
	ErrDecryptionFailed         = errors.New("inventory decryption failed")
	ErrUnknownKeyVersion        = errors.New("unknown inventory key version")
	ErrImportLimitExceeded      = errors.New("inventory import limit exceeded")
	ErrInvalidInventoryPayload  = errors.New("invalid inventory payload")
)
