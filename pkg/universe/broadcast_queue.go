package universe

import "sync"

// BroadcastEvent is one queued auto-broadcast event awaiting end-of-tick
// dispatch. Body is reflection-codec-encoded (use ReflectMarshal on the
// source side). Anchors are deduped Entity NetIDs on this stage whose
// positions drive the AoI filter applied at drain time.
type BroadcastEvent struct {
	TypeID  uint32
	Body    []byte
	Anchors []uint32
}

// BroadcastQueue is the per-stage per-tick queue. Drained by the framework
// at end-of-tick, AoI-filtered per viewer, and emitted as typed-event
// frames on the channel-0x00 wire.
type BroadcastQueue struct {
	mu     sync.Mutex
	events []BroadcastEvent
}

// Push appends e to the queue. Safe for concurrent producers.
func (q *BroadcastQueue) Push(e BroadcastEvent) {
	q.mu.Lock()
	q.events = append(q.events, e)
	q.mu.Unlock()
}

// Drain returns the queued events and resets the queue. Called once per
// tick by the framework's network/replication phase.
func (q *BroadcastQueue) Drain() []BroadcastEvent {
	q.mu.Lock()
	out := q.events
	q.events = nil
	q.mu.Unlock()
	return out
}

// Len returns the current number of queued events without draining.
// Primarily a diagnostics / test helper.
func (q *BroadcastQueue) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.events)
}
