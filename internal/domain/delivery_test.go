package domain

import "testing"

func TestDeliveryStateTransitions(t *testing.T) {
	t.Parallel()
	allowed := []struct {
		from, to DeliveryJobStatus
		manual   bool
	}{
		{DeliveryJobPending, DeliveryJobProcessing, false},
		{DeliveryJobRetryableFailed, DeliveryJobProcessing, false},
		{DeliveryJobProcessing, DeliveryJobCompleted, false},
		{DeliveryJobProcessing, DeliveryJobRetryableFailed, false},
		{DeliveryJobProcessing, DeliveryJobAmbiguous, false},
		{DeliveryJobProcessing, DeliveryJobPermanentFailed, false},
		{DeliveryJobAmbiguous, DeliveryJobPending, true},
		{DeliveryJobAmbiguous, DeliveryJobCompleted, true},
	}
	for _, transition := range allowed {
		if !CanTransitionDelivery(transition.from, transition.to, transition.manual) {
			t.Errorf("expected %s -> %s (manual=%t) to be allowed", transition.from, transition.to, transition.manual)
		}
	}
	forbidden := []struct {
		from, to DeliveryJobStatus
		manual   bool
	}{
		{DeliveryJobAmbiguous, DeliveryJobPending, false},
		{DeliveryJobCompleted, DeliveryJobProcessing, true},
		{DeliveryJobPermanentFailed, DeliveryJobProcessing, false},
		{DeliveryJobPending, DeliveryJobCompleted, false},
		{DeliveryJobStatus("unknown"), DeliveryJobPending, false},
	}
	for _, transition := range forbidden {
		if CanTransitionDelivery(transition.from, transition.to, transition.manual) {
			t.Errorf("expected %s -> %s (manual=%t) to be forbidden", transition.from, transition.to, transition.manual)
		}
	}
	if DeliveryJobAmbiguous.AutomaticallyClaimable() {
		t.Fatal("ambiguous delivery must never be automatically claimable")
	}
	if !DeliveryJobCompleted.Terminal() {
		t.Fatal("completed delivery must be terminal")
	}
}
