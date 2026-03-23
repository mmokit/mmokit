package game

import "reflect"

// TickQueue is a generic per-tick event queue. Each event type T gets its own
// internal slice. All operations are single-threaded (game loop only).
type TickQueue struct {
	slots map[reflect.Type]*queueSlot
}

type queueSlot struct {
	data     any    // *[]T
	clearFn  func() // resets slice to [:0]
}

// NewTickQueue creates a new empty TickQueue.
func NewTickQueue() *TickQueue {
	return &TickQueue{slots: make(map[reflect.Type]*queueSlot)}
}

// Enqueue appends an event of type T to the queue.
func Enqueue[T any](q *TickQueue, event T) {
	t := reflect.TypeFor[T]()
	if slot, ok := q.slots[t]; ok {
		sp := slot.data.(*[]T)
		*sp = append(*sp, event)
	} else {
		s := make([]T, 1, 8)
		s[0] = event
		slot := &queueSlot{data: &s}
		slot.clearFn = func() { s = s[:0] }
		q.slots[t] = slot
	}
}

// Drain returns all queued events of type T and resets that queue.
func Drain[T any](q *TickQueue) []T {
	t := reflect.TypeFor[T]()
	slot, ok := q.slots[t]
	if !ok {
		return nil
	}
	sp := slot.data.(*[]T)
	result := *sp
	*sp = (*sp)[:0]
	return result
}

// Peek returns all queued events of type T without clearing the queue.
func Peek[T any](q *TickQueue) []T {
	t := reflect.TypeFor[T]()
	slot, ok := q.slots[t]
	if !ok {
		return nil
	}
	return *slot.data.(*[]T)
}

// ClearAll resets all queues. Called at the start of each tick as a safety net.
func (q *TickQueue) ClearAll() {
	for _, slot := range q.slots {
		slot.clearFn()
	}
}
