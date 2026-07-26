package net

// SendDisposition reports what happened when a transport was asked to take
// ownership of an outbound application frame. A queued result is deliberately
// distinct from delivery: it means the frame entered the transport's outbound
// path, not that the remote client acknowledged it.
type SendDisposition uint8

const (
	// SendFailed is the zero value so an uninitialized result never looks like
	// a successful enqueue.
	SendFailed SendDisposition = iota
	SendQueued
	SendBackpressure
	SendClosed
	SendNoRoute
	// SendIndeterminate means ownership was accepted by an intermediate
	// queue, but a timeout, cancellation, or write error left downstream
	// delivery unknown. Retrying may duplicate the frame.
	SendIndeterminate
)

// DeliveryClass describes the ordering/reliability guarantee of the path that
// accepted a frame. The values are ordered from weakest to strongest so
// Supports can compare them directly.
type DeliveryClass uint8

const (
	DeliveryBestEffort DeliveryClass = iota
	DeliveryOrdered
	DeliveryReliableOrdered
)

// SendResult is returned by every outbound transport boundary.
//
// Err is optional diagnostic context for any non-queued disposition. Callers
// should branch on Disposition rather than parsing error text. On SendQueued,
// the transport owns the supplied byte slice until it has finished with the
// frame; callers must not mutate it.
type SendResult struct {
	Disposition SendDisposition
	Delivery    DeliveryClass
	Err         error
}

// Queued reports whether the outbound path accepted ownership of the frame.
func (r SendResult) Queued() bool {
	return r.Disposition == SendQueued
}

// Supports reports whether the frame was queued on a path providing at least
// the requested delivery class.
func (r SendResult) Supports(required DeliveryClass) bool {
	return r.Queued() && r.Delivery >= required
}

func queuedSend(delivery DeliveryClass) SendResult {
	return SendResult{Disposition: SendQueued, Delivery: delivery}
}
