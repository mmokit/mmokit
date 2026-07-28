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

func (r *recordingSub) Topics() []string { return nil } // wildcard for the helper
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

// blockingSub parks the dispatcher inside its first Deliver until gate is
// closed, which is what makes the bounded-buffer behaviour deterministic: with
// the consumer provably stopped, the queue depth is a property of the buffer
// size rather than of how fast the test machine happens to drain it.
type blockingSub struct {
	entered chan struct{}
	gate    chan struct{}
	once    sync.Once

	mu     sync.Mutex
	events []busEvent
	fail   bool // when set, Deliver returns false and the bus drops us
}

func (b *blockingSub) Topics() []string { return nil }

func (b *blockingSub) Deliver(topic string, payload any, _ time.Time) bool {
	b.once.Do(func() {
		close(b.entered)
		<-b.gate
	})
	b.mu.Lock()
	defer b.mu.Unlock()
	b.events = append(b.events, busEvent{topic, payload})
	return !b.fail
}

func (b *blockingSub) Close() {}

func (b *blockingSub) count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.events)
}

// The bounded buffer guarantees two things and no more: Publish never blocks on
// a consumer that has stopped consuming, and everything past the buffer is
// dropped rather than queued. It does NOT guarantee a particular received count
// against a live consumer — the previous version of this test asserted <=4
// against a subscriber that was never actually slow, and lost that race roughly
// once in thirty runs.
func TestTopicBus_SlowSubscriberDropped(t *testing.T) {
	t.Parallel()
	const bufSize = 2
	const published = 100

	bus := NewTopicBus(bufSize)
	defer bus.Close()

	slow := &blockingSub{entered: make(chan struct{}), gate: make(chan struct{})}
	bus.Subscribe(slow, "cells")

	// Park the dispatcher inside Deliver. From here the queue can absorb exactly
	// bufSize more messages and must discard the rest.
	bus.Publish("cells", 0)
	select {
	case <-slow.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("dispatcher never reached Deliver")
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 1; i < published; i++ {
			bus.Publish("cells", i)
		}
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Publish blocked on a wedged subscriber; the queue send must stay non-blocking")
	}

	close(slow.gate)
	bus.Drain()

	// 1 in flight inside Deliver + bufSize buffered; the other 97 were dropped.
	if got, want := slow.count(), 1+bufSize; got != want {
		t.Fatalf("slow subscriber got %d events, want exactly %d (1 in flight + %d buffered of %d published)",
			got, want, bufSize, published)
	}
}

// A subscriber that reports failure is removed from the bus by the dispatcher
// itself. That self-unsubscribe must not try to join its own goroutine, so this
// also guards the deadlock that the Unsubscribe/dispatcher join invites.
func TestTopicBus_FailedDeliverUnsubscribes(t *testing.T) {
	t.Parallel()
	bus := NewTopicBus(0)
	defer bus.Close()

	sub := &blockingSub{entered: make(chan struct{}), gate: make(chan struct{}), fail: true}
	close(sub.gate) // no parking needed here
	bus.Subscribe(sub, "cells")

	bus.Publish("cells", "boom")

	// The dispatcher drops the subscriber, and a later Unsubscribe from the
	// owner (handleStream does exactly this from a defer) must return promptly
	// rather than wait out dispatcherJoinTimeout.
	deadline := time.Now().Add(10 * time.Second)
	for {
		bus.mu.RLock()
		_, still := bus.subscribers[sub]
		bus.mu.RUnlock()
		if !still {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("failed Deliver did not unsubscribe the subscriber")
		}
		time.Sleep(time.Millisecond)
	}

	unsubbed := make(chan struct{})
	go func() {
		defer close(unsubbed)
		bus.Unsubscribe(sub)
	}()
	select {
	case <-unsubbed:
	case <-time.After(10 * time.Second):
		t.Fatal("Unsubscribe of an already-dropped subscriber blocked")
	}

	bus.Publish("cells", "ignored")
	bus.Drain()
	if got := sub.count(); got != 1 {
		t.Fatalf("dropped subscriber got %d events, want 1", got)
	}
}

// Unsubscribe must not return while the dispatcher can still call into the
// subscriber. handleStream depends on it: the sseWriter it unsubscribes wraps an
// http.ResponseWriter that is illegal to touch once the handler has returned.
func TestTopicBus_UnsubscribeJoinsDispatcher(t *testing.T) {
	t.Parallel()
	bus := NewTopicBus(4)
	defer bus.Close()

	sub := &blockingSub{entered: make(chan struct{}), gate: make(chan struct{})}
	bus.Subscribe(sub, "cells")

	bus.Publish("cells", "first")
	select {
	case <-sub.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("dispatcher never reached Deliver")
	}

	returned := make(chan struct{})
	go func() {
		defer close(returned)
		bus.Unsubscribe(sub)
	}()

	// Unsubscribe cannot complete while Deliver is still running.
	select {
	case <-returned:
		t.Fatal("Unsubscribe returned while Deliver was still in flight")
	case <-time.After(50 * time.Millisecond):
	}

	close(sub.gate)
	select {
	case <-returned:
	case <-time.After(10 * time.Second):
		t.Fatal("Unsubscribe never returned after Deliver completed")
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
