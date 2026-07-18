package domain

import (
	"errors"
	"testing"
)

func TestOrderTransitions(t *testing.T) {
	allowed := [][2]OrderStatus{
		{OrderStatusPendingPayment, OrderStatusPaid},
		{OrderStatusPendingPayment, OrderStatusExpired},
		{OrderStatusExpired, OrderStatusPaymentReview},
		{OrderStatusPaymentReview, OrderStatusPaid},
		{OrderStatusPaid, OrderStatusReserving},
		{OrderStatusReserving, OrderStatusDelivering},
		{OrderStatusDelivering, OrderStatusDelivered},
		{OrderStatusDelivered, OrderStatusRefunded},
		{OrderStatusDeliveryFailed, OrderStatusDelivering},
	}
	for _, transition := range allowed {
		if err := ValidateOrderTransition(transition[0], transition[1]); err != nil {
			t.Errorf("transition %s -> %s rejected: %v", transition[0], transition[1], err)
		}
	}
}

func TestOrderTransitionsRejectUnsafeChanges(t *testing.T) {
	forbidden := [][2]OrderStatus{
		{OrderStatusDelivered, OrderStatusPendingPayment},
		{OrderStatusExpired, OrderStatusDelivered},
		{OrderStatusCancelled, OrderStatusPaid},
		{OrderStatusRefunded, OrderStatusDelivering},
		{OrderStatusPendingPayment, OrderStatusDelivered},
		{OrderStatusPaid, OrderStatusPaid},
	}
	for _, transition := range forbidden {
		err := ValidateOrderTransition(transition[0], transition[1])
		if !errors.Is(err, ErrInvalidOrderTransition) {
			t.Errorf("transition %s -> %s error = %v, want ErrInvalidOrderTransition", transition[0], transition[1], err)
		}
	}
}

func TestLatePaymentRequiresReview(t *testing.T) {
	if CanTransitionOrder(OrderStatusExpired, OrderStatusPaid) {
		t.Fatal("expired order transitioned directly to paid")
	}
	if !CanTransitionOrder(OrderStatusExpired, OrderStatusPaymentReview) {
		t.Fatal("expired order cannot enter payment review")
	}
}

func TestOrderTerminalStates(t *testing.T) {
	for _, status := range []OrderStatus{OrderStatusCancelled, OrderStatusRefunded} {
		if !status.IsTerminal() {
			t.Errorf("%s is not terminal", status)
		}
		for _, target := range allOrderStatuses() {
			if CanTransitionOrder(status, target) {
				t.Errorf("terminal status %s transitions to %s", status, target)
			}
		}
	}
}

func TestInvalidOrderStatus(t *testing.T) {
	err := ValidateOrderTransition(OrderStatus("unknown"), OrderStatusPaid)
	if !errors.Is(err, ErrInvalidStatus) {
		t.Fatalf("error = %v, want ErrInvalidStatus", err)
	}
}

func allOrderStatuses() []OrderStatus {
	return []OrderStatus{
		OrderStatusPendingPayment,
		OrderStatusPaymentReview,
		OrderStatusPaid,
		OrderStatusReserving,
		OrderStatusDelivering,
		OrderStatusDelivered,
		OrderStatusExpired,
		OrderStatusCancelled,
		OrderStatusOutOfStock,
		OrderStatusDeliveryFailed,
		OrderStatusRefunded,
	}
}
