package admin

import (
	"sync"
	"testing"
	"time"
)

type recordingSub struct {
	mu     sync.Mutex
	events []busEvent
}

type busEvent struct {
	topic   string
	payload any
}

func (r *recordingSub) Topics() []string                                  { return nil } // wildcard for the helper
func (r *recordingSub) Deliver(topic string, payload any, _ time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, busEvent{topic, payload})
	return true
}

func (r *recordingSub) Close() {}

func TestTopicBus_Fanout(t *testing.T) {
	t.Parallel()
	bus := NewTopicBus(0)
	defer bus.Close()

	a := &recordingSub{}
	b := &recordingSub{}
	bus.Subscribe(a, "cells", "events")
	bus.Subscribe(b, "events")

	bus.Publish("cells", "snap1")
	bus.Publish("events", "ev1")
	bus.Drain()

	if got := len(a.events); got != 2 {
		t.Fatalf("a got %d events, want 2", got)
	}
	if got := len(b.events); got != 1 {
		t.Fatalf("b got %d events, want 1 (only events topic)", got)
	}
}

func TestTopicBus_SlowSubscriberDropped(t *testing.T) {
	t.Parallel()
	bus := NewTopicBus(2)
	defer bus.Close()

	slow := &recordingSub{}
	bus.Subscribe(slow, "cells")

	for i := 0; i < 100; i++ {
		bus.Publish("cells", i)
	}
	time.Sleep(20 * time.Millisecond)
	slow.mu.Lock()
	got := len(slow.events)
	slow.mu.Unlock()
	if got > 4 {
		t.Fatalf("slow subscriber got %d events, want <=4 (bounded buffer + transient)", got)
	}
}

func TestTopicBus_Unsubscribe(t *testing.T) {
	t.Parallel()
	bus := NewTopicBus(0)
	defer bus.Close()

	s := &recordingSub{}
	bus.Subscribe(s, "cells")
	bus.Publish("cells", "first")
	bus.Drain()
	bus.Unsubscribe(s)
	bus.Publish("cells", "second")
	bus.Drain()

	s.mu.Lock()
	got := len(s.events)
	s.mu.Unlock()
	if got != 1 {
		t.Fatalf("expected 1 event after unsubscribe, got %d", got)
	}
}
