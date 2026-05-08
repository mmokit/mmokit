package universe

import (
	"reflect"
	"testing"
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
