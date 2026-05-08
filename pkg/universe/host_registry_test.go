package universe

import (
	"testing"
	"time"

	"github.com/zenion/mmoserver/pkg/logger"
)

func newTestRegistry(t *testing.T) *HostRegistry {
	t.Helper()
	return NewHostRegistry(logger.New())
}

func TestHostRegistryRegister(t *testing.T) {
	r := newTestRegistry(t)
	r.Register("host-a", "10.0.0.1:9000", false, false)
	h := r.Get("host-a")
	if h == nil {
		t.Fatal("expected host entry, got nil")
	}
	if h.ID != "host-a" {
		t.Errorf("ID: got %q, want %q", h.ID, "host-a")
	}
	if h.GrpcAddr != "10.0.0.1:9000" {
		t.Errorf("GrpcAddr: got %q, want %q", h.GrpcAddr, "10.0.0.1:9000")
	}
	if h.State != RemoteHostRegistered {
		t.Errorf("State: got %v, want Registered", h.State)
	}
	if h.OwnedCells == nil {
		t.Error("OwnedCells should not be nil after Register")
	}
}

func TestHostRegistryTouchTransitions(t *testing.T) {
	r := newTestRegistry(t)
	r.Register("host-b", "10.0.0.2:9000", false, false)

	before := time.Now()
	time.Sleep(time.Millisecond) // ensure LastHeartbeat advances
	r.Touch("host-b")

	h := r.Get("host-b")
	if h.State != RemoteHostLive {
		t.Errorf("State after Touch: got %v, want Live", h.State)
	}
	if !h.LastHeartbeat.After(before) {
		t.Error("LastHeartbeat should have advanced after Touch")
	}
}

func TestHostRegistryTouchUnknown(t *testing.T) {
	r := newTestRegistry(t)
	// Should not panic.
	r.Touch("nonexistent")
}

func TestHostRegistryMarkDeadKeepsEntry(t *testing.T) {
	r := newTestRegistry(t)
	r.Register("host-c", "10.0.0.3:9000", false, false)
	if err := r.AssignCell("host-c", "cell_0_0"); err != nil {
		t.Fatalf("AssignCell: %v", err)
	}

	r.MarkDead("host-c")

	h := r.Get("host-c")
	if h == nil {
		t.Fatal("entry should survive MarkDead")
	}
	if h.State != RemoteHostDead {
		t.Errorf("State: got %v, want Dead", h.State)
	}
	if !h.OwnedCells["cell_0_0"] {
		t.Error("OwnedCells should be preserved after MarkDead")
	}
}

func TestHostRegistryRemoveIdempotent(t *testing.T) {
	r := newTestRegistry(t)

	// Remove an unknown host is a no-op.
	r.Remove("ghost")

	r.Register("host-d", "10.0.0.4:9000", false, false)
	r.Remove("host-d")
	if r.Get("host-d") != nil {
		t.Error("Get should return nil after Remove")
	}

	// Second Remove is a no-op.
	r.Remove("host-d")
}

func TestHostRegistryAssignAndReleaseCell(t *testing.T) {
	r := newTestRegistry(t)
	r.Register("host-e", "10.0.0.5:9000", false, false)

	if err := r.AssignCell("host-e", "cell_0_0"); err != nil {
		t.Fatalf("AssignCell: %v", err)
	}
	h := r.Get("host-e")
	if !h.OwnedCells["cell_0_0"] {
		t.Error("OwnedCells should contain cell_0_0 after AssignCell")
	}

	r.ReleaseCell("host-e", "cell_0_0")
	h = r.Get("host-e")
	if h.OwnedCells["cell_0_0"] {
		t.Error("OwnedCells should not contain cell_0_0 after ReleaseCell")
	}

	// AssignCell on unknown host returns an error.
	if err := r.AssignCell("unknown-host", "cell_0_0"); err == nil {
		t.Error("AssignCell on unknown host should return an error")
	}
}

func TestHostRegistryHostForCell(t *testing.T) {
	r := newTestRegistry(t)
	r.Register("host-f", "10.0.0.6:9000", false, false)
	r.Register("host-g", "10.0.0.7:9000", false, false)

	if err := r.AssignCell("host-f", "cell_0_0"); err != nil {
		t.Fatalf("AssignCell: %v", err)
	}
	if err := r.AssignCell("host-g", "cell_1_0"); err != nil {
		t.Fatalf("AssignCell: %v", err)
	}

	if got := r.HostForCell("cell_0_0"); got != "host-f" {
		t.Errorf("HostForCell(cell_0_0): got %q, want %q", got, "host-f")
	}
	if got := r.HostForCell("cell_1_0"); got != "host-g" {
		t.Errorf("HostForCell(cell_1_0): got %q, want %q", got, "host-g")
	}
	if got := r.HostForCell("cell_9_9"); got != "" {
		t.Errorf("HostForCell(unknown): got %q, want \"\"", got)
	}
}

func TestHostRegistryGetReturnsCopy(t *testing.T) {
	r := newTestRegistry(t)
	r.Register("host-h", "10.0.0.8:9000", false, false)
	if err := r.AssignCell("host-h", "cell_0_0"); err != nil {
		t.Fatalf("AssignCell: %v", err)
	}

	h1 := r.Get("host-h")
	// Mutate the returned copy's OwnedCells.
	h1.OwnedCells["cell_injected"] = true

	h2 := r.Get("host-h")
	if h2.OwnedCells["cell_injected"] {
		t.Error("mutation of returned copy leaked into registry (Get did not deep-copy)")
	}
}

func TestHostRegistryLiveHostsSnapshot(t *testing.T) {
	r := newTestRegistry(t)
	r.Register("host-i", "10.0.0.9:9000", false, false)
	r.Register("host-j", "10.0.0.10:9000", false, false)
	r.Register("host-k", "10.0.0.11:9000", false, false)

	hosts := r.LiveHosts()
	if len(hosts) != 3 {
		t.Fatalf("LiveHosts: got %d entries, want 3", len(hosts))
	}

	// Mutate one of the returned entries.
	hosts[0].OwnedCells["cell_injected"] = true

	// Re-fetch; mutation must not have propagated.
	hosts2 := r.LiveHosts()
	for _, h := range hosts2 {
		if h.OwnedCells["cell_injected"] {
			t.Errorf("mutation of LiveHosts entry leaked into registry for host %q", h.ID)
		}
	}
}
