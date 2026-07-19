package domain

// InventoryReleaseReason is persisted with a released order-to-inventory
// mapping. It describes the already-established safe order outcome.
type InventoryReleaseReason string

const (
	InventoryReleaseOrderCancelled InventoryReleaseReason = "order_cancelled"
	InventoryReleaseOrderExpired   InventoryReleaseReason = "order_expired"
	InventoryReleaseOrderRefunded  InventoryReleaseReason = "order_refunded"
	InventoryReleaseOutOfStock     InventoryReleaseReason = "order_out_of_stock"
)

func (r InventoryReleaseReason) IsValid() bool {
	switch r {
	case InventoryReleaseOrderCancelled, InventoryReleaseOrderExpired,
		InventoryReleaseOrderRefunded, InventoryReleaseOutOfStock:
		return true
	default:
		return false
	}
}

// InventoryReleaseReasonForOrder returns the only safe release reason for an
// order state. Sensitive and in-progress states deliberately have no reason.
func InventoryReleaseReasonForOrder(status OrderStatus) (InventoryReleaseReason, bool) {
	switch status {
	case OrderStatusCancelled:
		return InventoryReleaseOrderCancelled, true
	case OrderStatusExpired:
		return InventoryReleaseOrderExpired, true
	case OrderStatusRefunded:
		return InventoryReleaseOrderRefunded, true
	case OrderStatusOutOfStock:
		return InventoryReleaseOutOfStock, true
	default:
		return "", false
	}
}

// CanTransitionInventory captures the Phase 4 status transitions. Selling is
// reserved for a later delivery transaction and is therefore not exposed here.
func CanTransitionInventory(from, to InventoryStatus) bool {
	switch {
	case from == InventoryStatusAvailable && (to == InventoryStatusReserved || to == InventoryStatusDisabled):
		return true
	case from == InventoryStatusDisabled && to == InventoryStatusAvailable:
		return true
	case from == InventoryStatusReserved && to == InventoryStatusAvailable:
		return true
	default:
		return false
	}
}
