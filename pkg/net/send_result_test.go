package net

import "testing"

func TestSendResult_ZeroValueIsNotQueued(t *testing.T) {
	var result SendResult
	if result.Queued() {
		t.Fatal("zero-value SendResult must not report a successful enqueue")
	}
	if result.Supports(DeliveryBestEffort) {
		t.Fatal("zero-value SendResult must not satisfy a delivery requirement")
	}
}

func TestSendResult_SupportsDeliveryClass(t *testing.T) {
	result := SendResult{Disposition: SendQueued, Delivery: DeliveryOrdered}
	if !result.Supports(DeliveryBestEffort) {
		t.Fatal("ordered enqueue should satisfy best-effort delivery")
	}
	if !result.Supports(DeliveryOrdered) {
		t.Fatal("ordered enqueue should satisfy ordered delivery")
	}
	if result.Supports(DeliveryReliableOrdered) {
		t.Fatal("ordered enqueue must not claim reliable-ordered delivery")
	}

	rejected := SendResult{Disposition: SendBackpressure, Delivery: DeliveryReliableOrdered}
	if rejected.Supports(DeliveryBestEffort) {
		t.Fatal("rejected send must not satisfy any delivery class")
	}

	indeterminate := SendResult{Disposition: SendIndeterminate, Delivery: DeliveryReliableOrdered}
	if indeterminate.Queued() || indeterminate.Supports(DeliveryBestEffort) {
		t.Fatal("indeterminate send must not be safe to commit or retry as queued")
	}
}
