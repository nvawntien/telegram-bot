package domain

import "math"

// Money represents a non-negative VND amount in the smallest supported unit.
// VND has no fractional unit in this application.
type Money int64

// NewMoney validates and creates a VND amount.
func NewMoney(value int64) (Money, error) {
	if value < 0 {
		return 0, ErrInvalidMoney
	}
	return Money(value), nil
}

// Int64 returns the database representation.
func (m Money) Int64() int64 {
	return int64(m)
}

// Add returns the sum after validating both operands and integer overflow.
func (m Money) Add(other Money) (Money, error) {
	if m < 0 || other < 0 {
		return 0, ErrInvalidMoney
	}
	if m > Money(math.MaxInt64)-other {
		return 0, ErrMoneyOverflow
	}
	return m + other, nil
}

// Multiply returns a line total for a positive quantity.
func (m Money) Multiply(quantity int32) (Money, error) {
	if m < 0 {
		return 0, ErrInvalidMoney
	}
	if quantity <= 0 {
		return 0, ErrInvalidQuantity
	}
	if m != 0 && int64(quantity) > math.MaxInt64/int64(m) {
		return 0, ErrMoneyOverflow
	}
	return m * Money(quantity), nil
}
