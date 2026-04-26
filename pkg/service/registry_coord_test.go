package service

import (
	"strings"
	"testing"
	"time"
)

func ci(kind, id, host string, codes ...uint32) CoordInstance {
	return CoordInstance{Kind: kind, InstanceID: id, HostID: host, OpCodes: codes, JoinedAt: time.Now()}
}

func TestCoordRegistry_Register_Duplicate(t *testing.T) {
	r := NewCoordRegistry()
	if err := r.Register(ci("echo", "i1", "h", 1)); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(ci("echo", "i1", "h", 1)); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("want duplicate error, got %v", err)
	}
}

func TestCoordRegistry_Register_KindCodeMismatch(t *testing.T) {
	r := NewCoordRegistry()
	_ = r.Register(ci("echo", "i1", "h1", 100, 101))
	err := r.Register(ci("echo", "i2", "h2", 100, 999))
	if err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("want kind-mismatch error, got %v", err)
	}
}

func TestCoordRegistry_Register_KindCodeMatch_Allowed(t *testing.T) {
	r := NewCoordRegistry()
	if err := r.Register(ci("echo", "i1", "h1", 100, 101)); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(ci("echo", "i2", "h2", 101, 100)); err != nil {
		t.Fatalf("want same-set allowed (different order is fine), got %v", err)
	}
}

func TestCoordRegistry_Register_CrossKindConflict(t *testing.T) {
	r := NewCoordRegistry()
	_ = r.Register(ci("a", "i1", "h", 50))
	err := r.Register(ci("b", "i2", "h", 50))
	if err == nil || !strings.Contains(err.Error(), "claimed by kind") {
		t.Fatalf("want cross-kind error, got %v", err)
	}
}

func TestCoordRegistry_Unregister_ReleasesOpCodes(t *testing.T) {
	r := NewCoordRegistry()
	_ = r.Register(ci("echo", "i1", "h", 100))
	r.Unregister("i1")
	if r.LookupByOpCode(100) != "" {
		t.Fatalf("want unclaimed after last instance left, got %q", r.LookupByOpCode(100))
	}
	// New kind can now claim the same code:
	if err := r.Register(ci("other", "i2", "h", 100)); err != nil {
		t.Fatalf("expected reuse: %v", err)
	}
}

func TestCoordRegistry_Unregister_KeepsOpCodesIfOthersRemain(t *testing.T) {
	r := NewCoordRegistry()
	_ = r.Register(ci("echo", "i1", "h-a", 100))
	_ = r.Register(ci("echo", "i2", "h-b", 100))
	r.Unregister("i1")
	if r.LookupByOpCode(100) != "echo" {
		t.Fatalf("op-code claim lost while sibling instance lives, got %q", r.LookupByOpCode(100))
	}
}

func TestCoordRegistry_UnregisterByHost(t *testing.T) {
	r := NewCoordRegistry()
	_ = r.Register(ci("echo", "i1", "host-a", 100))
	_ = r.Register(ci("echo", "i2", "host-b", 100))
	r.UnregisterByHost("host-a")
	if got := r.InstancesOfKind("echo"); len(got) != 1 || got[0].InstanceID != "i2" {
		t.Fatalf("want only i2, got %+v", got)
	}
}

func TestCoordRegistry_Snapshot_SortedAndEpochBumps(t *testing.T) {
	r := NewCoordRegistry()
	e0 := r.Epoch()
	_ = r.Register(ci("z", "z-i", "h", 200))
	_ = r.Register(ci("a", "a-i", "h", 100))
	if r.Epoch() <= e0 {
		t.Fatalf("expected epoch bump")
	}
	got := r.Snapshot()
	if len(got) != 2 || got[0].Kind != "a" || got[1].Kind != "z" {
		t.Fatalf("expected sorted by kind, got %+v", got)
	}
}

func TestCoordRegistry_Kinds_Sorted(t *testing.T) {
	r := NewCoordRegistry()
	_ = r.Register(ci("zebra", "z", "h", 1))
	_ = r.Register(ci("apple", "a", "h", 2))
	got := r.Kinds()
	if len(got) != 2 || got[0] != "apple" || got[1] != "zebra" {
		t.Fatalf("want [apple zebra], got %v", got)
	}
}
