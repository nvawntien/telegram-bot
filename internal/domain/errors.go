package domain

import "errors"

var (
	ErrInvalidMoney           = errors.New("money must be non-negative")
	ErrMoneyOverflow          = errors.New("money calculation overflow")
	ErrInvalidQuantity        = errors.New("quantity must be positive")
	ErrInvalidStatus          = errors.New("invalid domain status")
	ErrInvalidOrderTransition = errors.New("invalid order state transition")
)
