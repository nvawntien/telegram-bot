package domain

import "testing"

func TestInventoryTransitions(t *testing.T) {
	allowed := [][2]InventoryStatus{
		{InventoryStatusAvailable, InventoryStatusReserved},
		{InventoryStatusAvailable, InventoryStatusDisabled},
		{InventoryStatusDisabled, InventoryStatusAvailable},
		{InventoryStatusReserved, InventoryStatusAvailable},
	}
	for _, transition := range allowed {
		if !CanTransitionInventory(transition[0], transition[1]) {
			t.Errorf("transition %s -> %s denied", transition[0], transition[1])
		}
	}
	denied := [][2]InventoryStatus{
		{InventoryStatusSold, InventoryStatusAvailable},
		{InventoryStatusReserved, InventoryStatusDisabled},
		{InventoryStatusSold, InventoryStatusDisabled},
		{InventoryStatusDisabled, InventoryStatusReserved},
	}
	for _, transition := range denied {
		if CanTransitionInventory(transition[0], transition[1]) {
			t.Errorf("transition %s -> %s allowed", transition[0], transition[1])
		}
	}
}

func TestInventoryReleaseSafetyPolicy(t *testing.T) {
	safe := map[OrderStatus]InventoryReleaseReason{
		OrderStatusCancelled:  InventoryReleaseOrderCancelled,
		OrderStatusExpired:    InventoryReleaseOrderExpired,
		OrderStatusRefunded:   InventoryReleaseOrderRefunded,
		OrderStatusOutOfStock: InventoryReleaseOutOfStock,
	}
	for status, want := range safe {
		reason, ok := InventoryReleaseReasonForOrder(status)
		if !ok || reason != want || !reason.IsValid() {
			t.Errorf("safe status %s = %s, %t", status, reason, ok)
		}
	}
	for _, status := range []OrderStatus{
		OrderStatusPendingPayment, OrderStatusPaymentReview, OrderStatusPaid,
		OrderStatusReserving, OrderStatusDelivering, OrderStatusDelivered,
		OrderStatusDeliveryFailed,
	} {
		if reason, ok := InventoryReleaseReasonForOrder(status); ok || reason != "" {
			t.Errorf("unsafe status %s returned %s, %t", status, reason, ok)
		}
	}
}
