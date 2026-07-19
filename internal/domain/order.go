package domain

// OrderStatus is the persisted lifecycle state of an order.
type OrderStatus string

const (
	OrderStatusPendingPayment OrderStatus = "pending_payment"
	OrderStatusPaymentReview  OrderStatus = "payment_review"
	OrderStatusPaid           OrderStatus = "paid"
	OrderStatusReserving      OrderStatus = "reserving"
	OrderStatusDelivering     OrderStatus = "delivering"
	OrderStatusDelivered      OrderStatus = "delivered"
	OrderStatusExpired        OrderStatus = "expired"
	OrderStatusCancelled      OrderStatus = "cancelled"
	OrderStatusOutOfStock     OrderStatus = "out_of_stock"
	OrderStatusDeliveryFailed OrderStatus = "delivery_failed"
	OrderStatusRefunded       OrderStatus = "refunded"
)

var orderTransitions = map[OrderStatus]map[OrderStatus]struct{}{
	OrderStatusPendingPayment: {
		OrderStatusPaid:          {},
		OrderStatusPaymentReview: {},
		OrderStatusExpired:       {},
		OrderStatusCancelled:     {},
	},
	OrderStatusPaymentReview: {
		OrderStatusPaid:      {},
		OrderStatusRefunded:  {},
		OrderStatusCancelled: {},
	},
	OrderStatusPaid: {
		OrderStatusReserving:     {},
		OrderStatusPaymentReview: {},
	},
	OrderStatusReserving: {
		OrderStatusDelivering: {},
		OrderStatusOutOfStock: {},
	},
	OrderStatusDelivering: {
		OrderStatusDelivered:      {},
		OrderStatusDeliveryFailed: {},
	},
	OrderStatusDelivered: {
		OrderStatusRefunded: {},
	},
	OrderStatusExpired: {
		OrderStatusPaymentReview: {},
	},
	OrderStatusOutOfStock: {
		OrderStatusReserving: {},
		OrderStatusRefunded:  {},
	},
	OrderStatusDeliveryFailed: {
		OrderStatusDelivering: {},
		OrderStatusRefunded:   {},
	},
}

func (s OrderStatus) IsValid() bool {
	switch s {
	case OrderStatusPendingPayment,
		OrderStatusPaymentReview,
		OrderStatusPaid,
		OrderStatusReserving,
		OrderStatusDelivering,
		OrderStatusDelivered,
		OrderStatusExpired,
		OrderStatusCancelled,
		OrderStatusOutOfStock,
		OrderStatusDeliveryFailed,
		OrderStatusRefunded:
		return true
	default:
		return false
	}
}

// IsTerminal reports whether no domain transition may leave this state.
func (s OrderStatus) IsTerminal() bool {
	return s == OrderStatusCancelled || s == OrderStatusRefunded
}

// IsCustomerTerminal reports whether Phase 5 customer actions may leave this
// state. Later payment reconciliation may still handle an expired order.
func (s OrderStatus) IsCustomerTerminal() bool {
	return s == OrderStatusExpired || s == OrderStatusCancelled
}

// ValidateCustomerOrderTransition limits customer mutations to cancellation.
// Payment and delivery transitions are never accepted from customer input.
func ValidateCustomerOrderTransition(from, to OrderStatus) error {
	if !from.IsValid() || !to.IsValid() {
		return ErrInvalidStatus
	}
	if from != OrderStatusPendingPayment || to != OrderStatusCancelled {
		return ErrInvalidOrderTransition
	}
	return nil
}

// CanTransitionOrder reports whether the domain state machine permits a change.
func CanTransitionOrder(from, to OrderStatus) bool {
	if !from.IsValid() || !to.IsValid() || from == to {
		return false
	}
	_, allowed := orderTransitions[from][to]
	return allowed
}

// ValidateOrderTransition returns a sentinel error for forbidden transitions.
func ValidateOrderTransition(from, to OrderStatus) error {
	if !from.IsValid() || !to.IsValid() {
		return ErrInvalidStatus
	}
	if !CanTransitionOrder(from, to) {
		return ErrInvalidOrderTransition
	}
	return nil
}
