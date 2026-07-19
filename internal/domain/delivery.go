package domain

// DeliveryJobStatus is the durable delivery queue lifecycle. It is distinct
// from the order and inventory state machines.
type DeliveryJobStatus string

const (
	DeliveryJobPending         DeliveryJobStatus = "pending"
	DeliveryJobProcessing      DeliveryJobStatus = "processing"
	DeliveryJobRetryableFailed DeliveryJobStatus = "retryable_failed"
	DeliveryJobAmbiguous       DeliveryJobStatus = "ambiguous"
	DeliveryJobManualReview    DeliveryJobStatus = "manual_review"
	DeliveryJobPermanentFailed DeliveryJobStatus = "permanent_failed"
	DeliveryJobCompleted       DeliveryJobStatus = "completed"
	DeliveryJobCancelled       DeliveryJobStatus = "cancelled"
)

// DeliveryResultClass describes whether a Telegram send result can be retried
// automatically without risking a duplicate credential delivery.
type DeliveryResultClass string

const (
	DeliveryResultSuccess   DeliveryResultClass = "success"
	DeliveryResultRetryable DeliveryResultClass = "retryable"
	DeliveryResultPermanent DeliveryResultClass = "permanent"
	DeliveryResultAmbiguous DeliveryResultClass = "ambiguous"
)

func (s DeliveryJobStatus) IsValid() bool {
	switch s {
	case DeliveryJobPending, DeliveryJobProcessing, DeliveryJobRetryableFailed,
		DeliveryJobAmbiguous, DeliveryJobManualReview, DeliveryJobPermanentFailed,
		DeliveryJobCompleted, DeliveryJobCancelled:
		return true
	default:
		return false
	}
}

func (s DeliveryJobStatus) AutomaticallyClaimable() bool {
	return s == DeliveryJobPending || s == DeliveryJobRetryableFailed
}

func (s DeliveryJobStatus) Terminal() bool {
	return s == DeliveryJobCompleted || s == DeliveryJobCancelled
}

func CanTransitionDelivery(from, to DeliveryJobStatus, manual bool) bool {
	if !from.IsValid() || !to.IsValid() || from == to {
		return false
	}
	switch from {
	case DeliveryJobPending, DeliveryJobRetryableFailed:
		return to == DeliveryJobProcessing
	case DeliveryJobProcessing:
		return to == DeliveryJobCompleted || to == DeliveryJobRetryableFailed ||
			to == DeliveryJobAmbiguous || to == DeliveryJobPermanentFailed ||
			to == DeliveryJobManualReview
	case DeliveryJobAmbiguous, DeliveryJobManualReview:
		return manual && (to == DeliveryJobPending || to == DeliveryJobCompleted || to == DeliveryJobCancelled)
	case DeliveryJobPermanentFailed:
		return manual && (to == DeliveryJobPending || to == DeliveryJobCancelled)
	default:
		return false
	}
}
