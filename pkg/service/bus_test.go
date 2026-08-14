package service_test

import (
	"sync/atomic"
	"testing"

	"github.com/zenion/mmokit/pkg/service"
)

type ping struct{ N int }

func TestBus_SubscribePublishRoundtrip(t *testing.T) {
	b := service.NewBus("test-proc")
	var got int32
	service.Subscribe(b, func(ev ping) {
		atomic.AddInt32(&got, int32(ev.N))
	})
	service.Publish(b, ping{N: 7})
	if g := atomic.LoadInt32(&got); g != 7 {
		t.Fatalf("handler not invoked: got=%d want=7", g)
	}
}

func TestBus_MultiHandlerOrdering(t *testing.T) {
	b := service.NewBus("test-proc")
	var calls []int
	service.Subscribe(b, func(ev ping) { calls = append(calls, 1) })
	service.Subscribe(b, func(ev ping) { calls = append(calls, 2) })
	service.Subscribe(b, func(ev ping) { calls = append(calls, 3) })
	service.Publish(b, ping{N: 1})
	if len(calls) != 3 || calls[0] != 1 || calls[1] != 2 || calls[2] != 3 {
		t.Fatalf("handlers not invoked in registration order: %v", calls)
	}
}

func TestBus_Unsubscribe(t *testing.T) {
	b := service.NewBus("test-proc")
	var n int32
	unsub := service.Subscribe(b, func(ev ping) { atomic.AddInt32(&n, 1) })
	service.Publish(b, ping{})
	unsub()
	service.Publish(b, ping{})
	unsub() // idempotent — must not panic
	if got := atomic.LoadInt32(&n); got != 1 {
		t.Fatalf("unsub failed: got=%d want=1", got)
	}
}

func TestBus_PanicRecovered(t *testing.T) {
	b := service.NewBus("test-proc")
	var afterRan int32
	service.Subscribe(b, func(ev ping) { panic("boom") })
	service.Subscribe(b, func(ev ping) { atomic.AddInt32(&afterRan, 1) })
	service.Publish(b, ping{})
	if got := atomic.LoadInt32(&afterRan); got != 1 {
		t.Fatalf("second handler did not run after first panicked: got=%d", got)
	}
}
