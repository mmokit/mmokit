package service

import (
	"strings"
	"testing"
)

func newKind(name string, codes ...uint32) Kind {
	return Kind{
		Name:    name,
		OpCodes: codes,
		Factory: func(*Context) Service { return nil },
	}
}

func TestRegistry_Register_Validates(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(Kind{}); err == nil {
		t.Fatalf("expected error for empty Kind")
	}
	if err := r.Register(Kind{Name: "x"}); err == nil {
		t.Fatalf("expected error for missing Factory")
	}
	// Empty OpCodes is allowed (typed-op-only kinds route by typeID, not
	// per-kind code claims).
	if err := r.Register(Kind{Name: "typedonly", Factory: func(*Context) Service { return nil }}); err != nil {
		t.Fatalf("expected no error for typed-op-only kind (empty OpCodes); got %v", err)
	}
	good := newKind("chat", 50, 51)
	if err := r.Register(good); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := r.Register(good); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("expected duplicate error, got %v", err)
	}
}

func TestRegistry_Validate_OpCodeConflict(t *testing.T) {
	r := NewRegistry()
	_ = r.Register(newKind("chat", 50, 51))
	_ = r.Register(newKind("market", 51, 52))
	err := r.Validate()
	if err == nil || !strings.Contains(err.Error(), "code 51") {
		t.Fatalf("expected op code 51 conflict, got %v", err)
	}
}

// TestRegistry_Validate_DoesNotEnforceRequiresDB confirms that registry-
// level Validate is a registry-wide check (op-code overlap) and does NOT
// enforce per-kind requirements like RequiresDB. RequiresDB applies only
// to kinds being instantiated, which is checked in startServices.
func TestRegistry_Validate_DoesNotEnforceRequiresDB(t *testing.T) {
	r := NewRegistry()
	_ = r.Register(Kind{
		Name:       "needs_db",
		OpCodes:    []uint32{100},
		Factory:    func(*Context) Service { return nil },
		RequiresDB: true,
	})
	// Validate must succeed even when DB isn't configured — registration
	// is an "available kind" record, not a commitment to instantiate.
	if err := r.Validate(); err != nil {
		t.Fatalf("Validate must not enforce RequiresDB at registry level: %v", err)
	}
}

func TestRegistry_SelectKinds(t *testing.T) {
	r := NewRegistry()
	_ = r.Register(newKind("a", 1))
	_ = r.Register(newKind("b", 2))
	got, err := r.SelectKinds([]string{"a", "b"})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2, got %d", len(got))
	}
	if _, err := r.SelectKinds([]string{"a", "missing"}); err == nil {
		t.Fatalf("expected missing-kind error")
	}
}

func TestRegistry_All_OrderedByName(t *testing.T) {
	r := NewRegistry()
	_ = r.Register(newKind("zebra", 99))
	_ = r.Register(newKind("apple", 1))
	_ = r.Register(newKind("middle", 50))
	got := r.All()
	if len(got) != 3 {
		t.Fatalf("want 3, got %d", len(got))
	}
	if got[0].Name != "apple" || got[1].Name != "middle" || got[2].Name != "zebra" {
		t.Fatalf("not sorted: %v %v %v", got[0].Name, got[1].Name, got[2].Name)
	}
}
