package universe

import (
	"reflect"
	"sync/atomic"
	"testing"

	meshpb "github.com/mmokit/mmokit/gen/go/meshpb"
	"github.com/mmokit/mmokit/pkg/service"
)

func TestServiceEventRouter_UpdateAndSnapshot(t *testing.T) {
	r := newServiceEventRouter()
	r.UpdateProcess("proc-A", []string{"foo.Event", "bar.Event"})
	r.UpdateProcess("proc-B", []string{"foo.Event"})
	got := r.Snapshot()
	want := map[string][]string{
		"foo.Event": {"proc-A", "proc-B"},
		"bar.Event": {"proc-A"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("snapshot mismatch:\n got=%v\nwant=%v", got, want)
	}
}

func TestServiceEventRouter_UpdateReplacesWholeSet(t *testing.T) {
	r := newServiceEventRouter()
	r.UpdateProcess("proc-A", []string{"foo.Event", "bar.Event"})
	r.UpdateProcess("proc-A", []string{"baz.Event"}) // bar/foo dropped
	got := r.Snapshot()
	if len(got["foo.Event"]) != 0 || len(got["bar.Event"]) != 0 {
		t.Fatalf("update didn't drop previous entries: %v", got)
	}
	if len(got["baz.Event"]) != 1 || got["baz.Event"][0] != "proc-A" {
		t.Fatalf("update didn't add new entry: %v", got)
	}
}

func TestServiceEventRouter_RemoveProcess(t *testing.T) {
	r := newServiceEventRouter()
	r.UpdateProcess("proc-A", []string{"foo.Event"})
	r.UpdateProcess("proc-B", []string{"foo.Event"})
	r.RemoveProcess("proc-A")
	got := r.Snapshot()
	if len(got["foo.Event"]) != 1 || got["foo.Event"][0] != "proc-B" {
		t.Fatalf("RemoveProcess didn't drop A: %v", got)
	}
}

func TestServiceEventRouter_EmptyTypeNamesRemoves(t *testing.T) {
	r := newServiceEventRouter()
	r.UpdateProcess("proc-A", []string{"foo.Event"})
	r.UpdateProcess("proc-A", nil)
	if len(r.Snapshot()) != 0 {
		t.Fatalf("empty typeNames should remove process")
	}
}

func TestProcess_HasServiceEventRouterAfterNew(t *testing.T) {
	p := New(Config{Headless: true, Mode: "all", CellsX: 1, CellsY: 1})
	if p.serviceEventRouter == nil {
		t.Fatal("Process.serviceEventRouter is nil after New")
	}
	// Sanity: router behaves correctly when called via the same path the
	// recv-loop uses (UpdateProcess with hostID + type names).
	p.serviceEventRouter.UpdateProcess("host-1", []string{"some.Event"})
	got := p.serviceEventRouter.Snapshot()
	if len(got["some.Event"]) != 1 || got["some.Event"][0] != "host-1" {
		t.Fatalf("router not updated via recv-path call: %v", got)
	}
}

func TestBuildPeerList_IncludesEventRouting(t *testing.T) {
	p := New(Config{Headless: true, Mode: "all", CellsX: 1, CellsY: 1, ControlListen: "127.0.0.1:0"})
	p.Build()
	p.serviceEventRouter.UpdateProcess("host-1", []string{"foo.Event", "bar.Event"})
	p.serviceEventRouter.UpdateProcess("host-2", []string{"foo.Event"})
	pl := p.assignmentEngine.buildPeerList().GetPeerList()
	if pl.GetEventRouting() == nil {
		t.Fatal("EventRouting not populated")
	}
	if got := pl.GetEventRouting()["foo.Event"]; got == nil || len(got.GetProcessIds()) != 2 {
		t.Fatalf("foo.Event routing: %v", got)
	}
	if got := pl.GetEventRouting()["bar.Event"]; got == nil || len(got.GetProcessIds()) != 1 {
		t.Fatalf("bar.Event routing: %v", got)
	}
}

func TestBroadcastPeerList_PopulatesLocalBusRoutingCache(t *testing.T) {
	p := New(Config{Headless: true, Mode: "all", CellsX: 1, CellsY: 1, ControlListen: "127.0.0.1:0"})
	p.Build()
	defer p.Shutdown()
	p.serviceEventRouter.UpdateProcess("host-1", []string{"foo.Event"})

	// Trigger a broadcast, which is also the path that updates the local cache.
	p.assignmentEngine.broadcastPeerList()

	got := p.bus.RoutingCacheSnapshot()
	if len(got["foo.Event"]) != 1 || got["foo.Event"][0] != "host-1" {
		t.Fatalf("local bus cache not refreshed: %v", got)
	}
}

// tinyDispatchEvent is a payload type for the dispatch smoke test —
// declared at package level so service.RegisterEventType can introspect
// it before the Bus dispatch path tries to resolve its name. Field type
// is int32 so ReflectMarshal (no platform-int codec) can serialize it.
type tinyDispatchEvent struct{ N int32 }

func TestProcess_DispatchServiceEvent_NoPeers_NoCrash(t *testing.T) {
	p := New(Config{Headless: true, Mode: "all", CellsX: 1, CellsY: 1, ControlListen: "127.0.0.1:0"})
	p.Build()
	defer p.Shutdown()

	// Publish a typed event with no remote subscribers in the routing
	// cache. The RemotePublishFunc is wired (Phase 3 Task 7) but with
	// peerIDsExceptSelf returning empty, notifyRemote skips the
	// dispatcher entirely. This pins the empty-cache happy path —
	// confirms no panic / no nil deref through the new glue.
	service.RegisterEventType[tinyDispatchEvent]()
	service.Publish(p.bus, tinyDispatchEvent{N: 1})
}

func TestDeliverServiceEvent_DecodesAndRepublishesLocally(t *testing.T) {
	p := New(Config{Headless: true, Mode: "all", CellsX: 1, CellsY: 1, ControlListen: "127.0.0.1:0"})
	p.Build()
	defer p.Shutdown()

	// Subscribe a handler so we can detect the redispatch.
	service.RegisterEventType[tinyDispatchEvent]()
	var got int32
	service.Subscribe(p.bus, func(ev tinyDispatchEvent) {
		atomic.AddInt32(&got, ev.N)
	})

	// Build a wire ServiceEvent as if it had arrived from a peer.
	payload := mustMarshal(t, &tinyDispatchEvent{N: 7})
	se := &meshpb.ServiceEvent{
		SourceProcessId: "remote-peer", // not us — must not be self-echo-skipped
		TypeName:        "github.com/mmokit/mmokit/pkg/universe.tinyDispatchEvent",
		Payload:         payload,
		Sequence:        1,
	}

	// In `all` mode without TestHosts, the colocated host doesn't allocate
	// a HostNetwork (cell I/O is direct). Synthesize a minimal HostNetwork
	// bound to the live Process so deliverServiceEvent can walk n.coord.bus.
	hn := &HostNetwork{
		hostID: "test-host",
		log:    p.Log,
		coord:  p,
	}
	hn.deliverServiceEvent(se)

	if g := atomic.LoadInt32(&got); g != 7 {
		t.Fatalf("local subscriber not invoked: got=%d want=7", g)
	}
}
