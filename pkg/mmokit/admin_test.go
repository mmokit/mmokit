package mmokit

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/zenion/mmokit/pkg/universe"
)

// fakeSub captures topics + payloads delivered by the TopicBus. Implements
// admin.Subscriber.
type fakeSub struct {
	mu       sync.Mutex
	topics   []string
	received []received
	notify   chan struct{}
}

type received struct {
	topic   string
	payload any
}

func (f *fakeSub) Topics() []string { return f.topics }

func (f *fakeSub) Deliver(topic string, payload any, _ time.Time) bool {
	f.mu.Lock()
	f.received = append(f.received, received{topic: topic, payload: payload})
	f.mu.Unlock()
	select {
	case f.notify <- struct{}{}:
	default:
	}
	return true
}

func (f *fakeSub) Close() {}

func (f *fakeSub) wait(t *testing.T, n int, timeout time.Duration) {
	t.Helper()
	deadline := time.After(timeout)
	for {
		f.mu.Lock()
		got := len(f.received)
		f.mu.Unlock()
		if got >= n {
			return
		}
		select {
		case <-f.notify:
		case <-deadline:
			t.Fatalf("waited for %d deliveries, got %d", n, got)
		}
	}
}

func TestPublishAdminTopic_RoutesToBus(t *testing.T) {
	t.Parallel()
	// Use a non-nil pointer key — adminBus only needs *Process for map
	// identity, not for any field access.
	proc := &universe.Process{}
	t.Cleanup(func() {
		adminBusMu.Lock()
		delete(adminBusMap, proc)
		adminBusMu.Unlock()
	})

	bus := adminBus(proc)
	sub := &fakeSub{topics: []string{"bots"}, notify: make(chan struct{}, 8)}
	bus.Subscribe(sub, sub.topics...)
	t.Cleanup(func() { bus.Unsubscribe(sub) })

	PublishAdminTopic(proc, "bots", []int{1, 2, 3})
	sub.wait(t, 1, 500*time.Millisecond)

	sub.mu.Lock()
	defer sub.mu.Unlock()
	if len(sub.received) != 1 {
		t.Fatalf("got %d deliveries, want 1", len(sub.received))
	}
	got := sub.received[0]
	if got.topic != "bots" {
		t.Fatalf("topic = %q, want \"bots\"", got.topic)
	}
	payload, ok := got.payload.([]int)
	if !ok || len(payload) != 3 || payload[0] != 1 {
		t.Fatalf("payload = %#v, want [1 2 3]", got.payload)
	}
}

func TestAdminBus_PerProcessIsolation(t *testing.T) {
	t.Parallel()
	procA := &universe.Process{}
	procB := &universe.Process{}
	t.Cleanup(func() {
		adminBusMu.Lock()
		delete(adminBusMap, procA)
		delete(adminBusMap, procB)
		adminBusMu.Unlock()
	})

	subA := &fakeSub{topics: []string{"T"}, notify: make(chan struct{}, 8)}
	subB := &fakeSub{topics: []string{"T"}, notify: make(chan struct{}, 8)}
	adminBus(procA).Subscribe(subA, "T")
	adminBus(procB).Subscribe(subB, "T")
	t.Cleanup(func() {
		adminBus(procA).Unsubscribe(subA)
		adminBus(procB).Unsubscribe(subB)
	})

	PublishAdminTopic(procA, "T", "A-only")
	subA.wait(t, 1, 500*time.Millisecond)

	subB.mu.Lock()
	defer subB.mu.Unlock()
	if len(subB.received) != 0 {
		t.Fatalf("procB sub received %d deliveries, want 0", len(subB.received))
	}
}

// TestRemoteAdminTopicBridge_PublishesRawJSON proves the coordinator-side
// bridge re-publishes a forwarded payload verbatim as json.RawMessage, so SSE
// subscribers observe the identical shape a local publish would produce.
func TestRemoteAdminTopicBridge_PublishesRawJSON(t *testing.T) {
	t.Parallel()
	proc := &universe.Process{}
	t.Cleanup(func() {
		adminBusMu.Lock()
		delete(adminBusMap, proc)
		adminBusMu.Unlock()
	})

	bus := adminBus(proc)
	sub := &fakeSub{topics: []string{"tunables"}, notify: make(chan struct{}, 1)}
	bus.Subscribe(sub, sub.topics...)
	t.Cleanup(func() { bus.Unsubscribe(sub) })

	fn := remoteAdminTopicBridge(proc)
	want := `{"system":"wave","rows":[]}`
	fn("tunables", []byte(want))

	sub.wait(t, 1, 2*time.Second)
	sub.mu.Lock()
	defer sub.mu.Unlock()
	if sub.received[0].topic != "tunables" {
		t.Errorf("topic = %q, want %q", sub.received[0].topic, "tunables")
	}
	raw, ok := sub.received[0].payload.(json.RawMessage)
	if !ok {
		t.Fatalf("payload type = %T, want json.RawMessage", sub.received[0].payload)
	}
	if string(raw) != want {
		t.Errorf("payload = %s, want %s", raw, want)
	}
}
