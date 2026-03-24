package game

import "github.com/zenion/mmoserver/pkg/engine"

// TickQueue is re-exported from pkg/engine for backwards compatibility.
type TickQueue = engine.TickQueue

// NewTickQueue creates a new empty TickQueue.
var NewTickQueue = engine.NewTickQueue

// Enqueue appends an event of type T to the queue.
func Enqueue[T any](q *TickQueue, event T) {
	engine.Enqueue(q, event)
}

// Drain returns all queued events of type T and resets that queue.
func Drain[T any](q *TickQueue) []T {
	return engine.Drain[T](q)
}

// Peek returns all queued events of type T without clearing the queue.
func Peek[T any](q *TickQueue) []T {
	return engine.Peek[T](q)
}
