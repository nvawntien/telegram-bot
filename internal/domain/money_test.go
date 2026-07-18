package domain

import (
	"errors"
	"math"
	"testing"
)

func TestMoneyValidationAndArithmetic(t *testing.T) {
	tests := []struct {
		name      string
		run       func() (Money, error)
		want      Money
		wantError error
	}{
		{name: "construct zero", run: func() (Money, error) { return NewMoney(0) }, want: 0},
		{name: "reject negative", run: func() (Money, error) { return NewMoney(-1) }, wantError: ErrInvalidMoney},
		{name: "add", run: func() (Money, error) { return Money(10).Add(20) }, want: 30},
		{name: "add overflow", run: func() (Money, error) { return Money(math.MaxInt64).Add(1) }, wantError: ErrMoneyOverflow},
		{name: "multiply", run: func() (Money, error) { return Money(12_000).Multiply(3) }, want: 36_000},
		{name: "reject zero quantity", run: func() (Money, error) { return Money(1).Multiply(0) }, wantError: ErrInvalidQuantity},
		{name: "multiply overflow", run: func() (Money, error) { return Money(math.MaxInt64).Multiply(2) }, wantError: ErrMoneyOverflow},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := test.run()
			if !errors.Is(err, test.wantError) {
				t.Fatalf("error = %v, want %v", err, test.wantError)
			}
			if got != test.want {
				t.Fatalf("money = %d, want %d", got, test.want)
			}
		})
	}
}
