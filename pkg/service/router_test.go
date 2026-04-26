package service

import (
	"strings"
	"testing"
)

func TestRoutingIndex_Apply_LookupAndPick(t *testing.T) {
	r := NewRoutingIndex()
	err := r.Apply([]ServiceRecord{
		{Kind: "echo", InstanceID: "host-b-echo-0", HostID: "host-b", OpCodes: []uint32{300, 301}},
		{Kind: "echo", InstanceID: "host-a-echo-0", HostID: "host-a", OpCodes: []uint32{300, 301}},
	})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if r.LookupKind(300) != "echo" {
		t.Fatalf("want echo, got %q", r.LookupKind(300))
	}
	if r.LookupKind(999) != "" {
		t.Fatalf("expected unclaimed, got %q", r.LookupKind(999))
	}
	// Stable ordering: connID=0 picks the lex-smallest instance.
	got, ok := r.PickInstance("echo", 0)
	if !ok || got.InstanceID != "host-a-echo-0" {
		t.Fatalf("want host-a-echo-0, got %+v ok=%v", got, ok)
	}
	got, ok = r.PickInstance("echo", 1)
	if !ok || got.InstanceID != "host-b-echo-0" {
		t.Fatalf("want host-b-echo-0, got %+v ok=%v", got, ok)
	}
	if _, ok := r.PickInstance("missing", 0); ok {
		t.Fatalf("expected no instance for missing kind")
	}
}

func TestRoutingIndex_Apply_OpCodeConflict(t *testing.T) {
	r := NewRoutingIndex()
	err := r.Apply([]ServiceRecord{
		{Kind: "a", InstanceID: "i1", HostID: "h1", OpCodes: []uint32{50}},
		{Kind: "b", InstanceID: "i2", HostID: "h2", OpCodes: []uint32{50}},
	})
	if err == nil || !strings.Contains(err.Error(), "code 50") {
		t.Fatalf("expected code 50 conflict, got %v", err)
	}
}

func TestRoutingIndex_Apply_HashAffinityDeterministic(t *testing.T) {
	r := NewRoutingIndex()
	_ = r.Apply([]ServiceRecord{
		{Kind: "x", InstanceID: "x-2", HostID: "h", OpCodes: []uint32{1}},
		{Kind: "x", InstanceID: "x-0", HostID: "h", OpCodes: []uint32{1}},
		{Kind: "x", InstanceID: "x-1", HostID: "h", OpCodes: []uint32{1}},
	})
	// connID=42 picks instance index 42%3=0 → "x-0" (lex-smallest)
	got, _ := r.PickInstance("x", 42)
	if got.InstanceID != "x-0" {
		t.Fatalf("want x-0, got %s", got.InstanceID)
	}
	got, _ = r.PickInstance("x", 43)
	if got.InstanceID != "x-1" {
		t.Fatalf("want x-1, got %s", got.InstanceID)
	}
	got, _ = r.PickInstance("x", 44)
	if got.InstanceID != "x-2" {
		t.Fatalf("want x-2, got %s", got.InstanceID)
	}
}

func TestRoutingIndex_AffinityHoldsAcrossManyOps(t *testing.T) {
	r := NewRoutingIndex()
	_ = r.Apply([]ServiceRecord{
		{Kind: "k", InstanceID: "a", HostID: "h", OpCodes: []uint32{1}},
		{Kind: "k", InstanceID: "b", HostID: "h", OpCodes: []uint32{1}},
		{Kind: "k", InstanceID: "c", HostID: "h", OpCodes: []uint32{1}},
	})
	// 100 ops from the same connID should all land on the same instance.
	connID := uint32(12345)
	first, _ := r.PickInstance("k", connID)
	for i := 0; i < 100; i++ {
		got, _ := r.PickInstance("k", connID)
		if got.InstanceID != first.InstanceID {
			t.Fatalf("affinity broken at iter %d: want %s, got %s", i, first.InstanceID, got.InstanceID)
		}
	}
}
