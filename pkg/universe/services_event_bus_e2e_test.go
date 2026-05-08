package universe_test

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zenion/mmoserver/pkg/logger"
	"github.com/zenion/mmoserver/pkg/net"
	"github.com/zenion/mmoserver/pkg/service"
	. "github.com/zenion/mmoserver/pkg/universe"
)

// e2eBusEvent is the wire-eligible event type used by the multi-process
// service-event-bus acceptance test. Wire registration runs in init so
// the codec entry exists on every process linking this test binary —
// publisher (coord+gateway) AND subscriber (svc-A) MUST share the registry
// for cross-process decode to land.
type e2eBusEvent struct {
	Tag int32
	Msg string
}

func init() { service.RegisterWireEvent[e2eBusEvent]() }

// TestServiceEventBus_E2E_SubscribeAndPublish is the Phase 3 capstone:
// stand up three processes (coord+gateway, svc-A, svc-B), have svc-A
// subscribe to a wire-eligible event, publish from coord+gateway, and
// assert delivery to svc-A. svc-B's exclusion is verified via the
// routing-cache snapshot — coord's table contains svc-A but not svc-B
// for this type, so the dispatcher's PeerIDs slice excludes svc-B.
//
// Timeline (all real wire / gRPC):
//   - svc-A.Subscribe → Bus.SubscriptionFlush → 50ms debounce →
//     MeshControl.ServiceEventSubscribe → coord updates router → coord
//     re-broadcasts PeerList → svc-A sees its own subscription reflected
//     in the routing table (one round trip ≈ 50–200ms).
//   - coord.Publish → routing-cache lookup yields [svc-A] → MeshFrame
//     wrapped → MeshData.SendOrdered to svc-A → svc-A's HostNetwork
//     delivers via PublishAny on local bus.
func TestServiceEventBus_E2E_SubscribeAndPublish(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// === 1. Coord+gateway on an ephemeral MeshControl port.
	coord := New(Config{
		Mode:                "coordinator,gateway",
		ControlListen:       "127.0.0.1:0",
		HTTPPort:            -1, // disable client HTTP listener — port collision
		Headless:            true,
		ShutdownGracePeriod: 100 * time.Millisecond,
		SettleWindow:        50 * time.Millisecond,
		ConnManager:         net.NewConnManager(),
		Logger:              logger.New(),
		CellsX:              1,
		CellsY:              1,
	})
	coord.Build()
	t.Cleanup(coord.Shutdown)

	coordAddr := coord.ControlListenAddr()
	if coordAddr == "" {
		t.Fatal("coord did not bind a MeshControl listener — check ControlListen + role wiring")
	}

	// === 2. svc-A: pure --mode=service that subscribes.
	svcA := New(Config{
		Mode:                "service",
		CoordinatorAddr:     coordAddr,
		HostID:              "svc-A",
		HTTPPort:            -1,
		Headless:            true,
		ShutdownGracePeriod: 100 * time.Millisecond,
		ConnManager:         net.NewConnManager(),
		Logger:              logger.New(),
	})
	svcA.Build()
	t.Cleanup(svcA.Shutdown)

	var received int32
	var lastTag int32
	service.Subscribe(svcA.Bus(), func(ev e2eBusEvent) {
		atomic.StoreInt32(&lastTag, ev.Tag)
		atomic.AddInt32(&received, 1)
	})

	// === 3. svc-B: pure --mode=service, no subscribers.
	svcB := New(Config{
		Mode:                "service",
		CoordinatorAddr:     coordAddr,
		HostID:              "svc-B",
		HTTPPort:            -1,
		Headless:            true,
		ShutdownGracePeriod: 100 * time.Millisecond,
		ConnManager:         net.NewConnManager(),
		Logger:              logger.New(),
	})
	svcB.Build()
	t.Cleanup(svcB.Shutdown)

	// === 4. Wait for both service-hosts to register with coord. Belt-and-
	// suspenders: the controlClient.send slow-path now blocks (briefly) on
	// the first-stream-open signal, so the subscribe-flush from step 2 will
	// land regardless of dial timing. We still wait for RegisterHost so
	// the routing-cache assertions below have a deterministic anchor.
	if err := coord.HarnessWaitForHost(ctx, "svc-A"); err != nil {
		t.Fatalf("svc-A failed to register with coord: %v", err)
	}
	if err := coord.HarnessWaitForHost(ctx, "svc-B"); err != nil {
		t.Fatalf("svc-B failed to register with coord: %v", err)
	}

	// === 5. Wait for svc-A's subscription to propagate through coord's
	// router and back into coord's own bus.routingCache via the PeerList
	// re-broadcast. The TypeName uses the canonical PkgPath.Name form:
	// for an external-package test type, that's the _test package.
	wantTypeName := "github.com/zenion/mmoserver/pkg/universe_test.e2eBusEvent"
	if err := waitFor(ctx, 2*time.Second, func() bool {
		got := coord.Bus().RoutingCacheSnapshot()
		for _, id := range got[wantTypeName] {
			if id == "svc-A" {
				return true
			}
		}
		return false
	}); err != nil {
		t.Fatalf("svc-A never appeared in coord routing cache for %q within 2s: %v\nsnapshot=%+v",
			wantTypeName, err, coord.Bus().RoutingCacheSnapshot())
	}

	// === 6. Routing-table negative assertion: svc-B is registered with
	// coord (verified in step 4) but never called Subscribe. The router's
	// snapshot for this event MUST contain svc-A and exclude svc-B.
	cache := coord.Bus().RoutingCacheSnapshot()
	for _, id := range cache[wantTypeName] {
		if id == "svc-B" {
			t.Fatalf("svc-B leaked into routing for %q: %v", wantTypeName, cache[wantTypeName])
		}
	}

	// === 7. Publish from coord+gateway. Bus.Publish does local fan-out
	// (no local subscriber on coord — fine) and remote fan-out to every
	// peer in the routing-cache slice.
	service.Publish(coord.Bus(), e2eBusEvent{Tag: 42, Msg: "hello"})

	// === 8. Assert svc-A receives within 2s. The dispatch path is one
	// MeshFrame over MeshData (TCP loopback) — ~milliseconds in practice.
	if err := waitFor(ctx, 2*time.Second, func() bool {
		return atomic.LoadInt32(&received) == 1
	}); err != nil {
		t.Fatalf("svc-A did not receive e2eBusEvent within 2s (received=%d)",
			atomic.LoadInt32(&received))
	}

	if got := atomic.LoadInt32(&lastTag); got != 42 {
		t.Fatalf("svc-A received wrong tag: got=%d want=42", got)
	}
}

// waitFor polls fn until it returns true or max elapses. Returns nil on
// success, fmt.Errorf on timeout.
func waitFor(ctx context.Context, max time.Duration, fn func() bool) error {
	deadline := time.Now().Add(max)
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		if fn() {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("waitFor: condition never satisfied within %s", max)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
}
