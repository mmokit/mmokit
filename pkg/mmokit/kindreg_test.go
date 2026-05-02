package mmokit

import (
	"testing"

	"github.com/mlange-42/ark/ecs"
	"github.com/zenion/mmoserver/pkg/system"
	"github.com/zenion/mmoserver/pkg/universe"
)

// ---------------------------------------------------------------------------
// Test component + bundle types
// ---------------------------------------------------------------------------

type kindRegTestNameComp struct {
	Name string `net:"initial"`
}

type kindRegTestHealthComp struct {
	HP float32 `net:"f32"`
}

type kindRegTestInputComp struct {
	Cmd uint8
}

type kindRegTestExtraComp struct {
	Tag uint16 `net:"u16"`
}

type kindRegTestBundle struct {
	Name   *kindRegTestNameComp
	Health *kindRegTestHealthComp
}

// ---------------------------------------------------------------------------
// Test harness
// ---------------------------------------------------------------------------

func newTestProcess(t *testing.T) *universe.Process {
	t.Helper()
	mmo := New(Config{
		CellsX:    1,
		CellsY:    1,
		CellSize:  1000,
		TickRate:  20,
		AoIRadius: 100,
		Headless:  true,
	})
	return mmo
}

// firstCell returns the first cell on the process. Useful since the test
// process has only one cell and the map is keyed by ID strings.
func firstCell(p *universe.Process) *universe.Cell {
	for _, c := range p.Cells {
		return c
	}
	return nil
}

// ---------------------------------------------------------------------------
// Bundle reflection tests
// ---------------------------------------------------------------------------

func TestRegisterKind_BundleReflection(t *testing.T) {
	mmo := newTestProcess(t)
	RegisterKind[kindRegTestBundle](mmo, 100, "TestKind")
	mmo.Build()
	t.Cleanup(func() { mmo.Shutdown() })

	cell := firstCell(mmo)
	if cell == nil {
		t.Fatal("expected at least one cell")
	}
	defs := cell.Stage.EntityKindDefs()
	def, ok := defs[100]
	if !ok {
		t.Fatalf("kind 100 not registered on cell %s", cell.MeshID)
	}
	if def.Name != "TestKind" {
		t.Errorf("expected kind name TestKind, got %q", def.Name)
	}
	if def.Components() != 2 {
		t.Errorf("expected 2 components, got %d", def.Components())
	}
	if len(def.NetworkBindings) != 2 {
		t.Errorf("expected 2 NetworkBindings, got %d", len(def.NetworkBindings))
	}
	// Both components should be in the transfer registry.
	if reg := cell.Stage.ReplicationRegistry(); reg.Len() < 2 {
		t.Errorf("expected >= 2 components in transfer registry, got %d", reg.Len())
	}
}

// TestRegisterKind_WithFieldOverride verifies that WithBinding propagates a
// caller-supplied ComponentBinding all the way to def.NetworkBindings via
// pointer-identity. The sentinel binding is built from the cell's ECS world
// after Build, then a SECOND process registers a kind with WithBinding
// targeting the sentinel — the kind's NetworkBindings must contain the
// exact pointer we passed in.
func TestRegisterKind_WithFieldOverride(t *testing.T) {
	// First process: bootstrap a world so we can build a real ComponentBinding
	// that we control. We'll then re-use the binding (its identity, not its
	// world wiring) on a second process where the bundle expects a Health field.
	bootstrap := newTestProcess(t)
	bootstrap.Build()
	t.Cleanup(func() { bootstrap.Shutdown() })

	w := firstCell(bootstrap).Stage.ECSWorld()
	healthMap := ecs.NewMap1[kindRegTestHealthComp](w)
	sentinel := system.Component(healthMap)

	// Second process: register an override kind with WithBinding(sentinel).
	mmo2 := newTestProcess(t)
	RegisterKind[kindRegTestBundle](mmo2, 102, "OverrideKind",
		WithField[kindRegTestHealthComp](WithBinding(sentinel)),
	)
	mmo2.Build()
	t.Cleanup(func() { mmo2.Shutdown() })

	defB := firstCell(mmo2).Stage.EntityKindDefs()[102]
	if defB == nil {
		t.Fatal("kind 102 not registered on override process")
	}
	if len(defB.NetworkBindings) != 2 {
		t.Fatalf("override kind: expected 2 NetworkBindings, got %d", len(defB.NetworkBindings))
	}
	// One of the bindings must be the sentinel (pointer-identity).
	foundSentinel := false
	for _, b := range defB.NetworkBindings {
		if b == sentinel {
			foundSentinel = true
			break
		}
	}
	if !foundSentinel {
		t.Errorf("override kind: sentinel binding not present in NetworkBindings; WithBinding override did not propagate")
	}
}

func TestRegisterKind_LocalOnlyTag(t *testing.T) {
	type localTagBundle struct {
		Name  *kindRegTestNameComp
		Input *kindRegTestInputComp `mmokit:"local"`
	}
	mmo := newTestProcess(t)
	RegisterKind[localTagBundle](mmo, 102, "LocalTagKind")
	mmo.Build()
	t.Cleanup(func() { mmo.Shutdown() })

	cell := firstCell(mmo)
	def := cell.Stage.EntityKindDefs()[102]
	if def == nil {
		t.Fatal("kind 102 not registered")
	}
	if len(def.NetworkBindings) != 1 {
		t.Errorf("expected 1 NetworkBinding (Name only; Input is local), got %d", len(def.NetworkBindings))
	}
	// The transfer registry should have only the Name component (Input is local-only).
	if reg := cell.Stage.ReplicationRegistry(); reg.Len() != 1 {
		t.Errorf("expected 1 component in transfer registry (Name only), got %d", reg.Len())
	}
}

func TestRegisterKind_LocalOnlyOption(t *testing.T) {
	type localOptBundle struct {
		Name  *kindRegTestNameComp
		Input *kindRegTestInputComp
	}
	mmo := newTestProcess(t)
	RegisterKind[localOptBundle](mmo, 103, "LocalOptKind",
		WithField[kindRegTestInputComp](LocalOnly()),
	)
	mmo.Build()
	t.Cleanup(func() { mmo.Shutdown() })

	cell := firstCell(mmo)
	def := cell.Stage.EntityKindDefs()[103]
	if def == nil {
		t.Fatal("kind 103 not registered")
	}
	if len(def.NetworkBindings) != 1 {
		t.Errorf("expected 1 NetworkBinding (Name only; Input is local), got %d", len(def.NetworkBindings))
	}
	if reg := cell.Stage.ReplicationRegistry(); reg.Len() != 1 {
		t.Errorf("expected 1 component in transfer registry (Name only), got %d", reg.Len())
	}
}

// TestRegisterKind_WithExtraBinding builds a real binding from a bootstrap
// process and verifies it's appended to def.NetworkBindings via WithExtraBinding.
func TestRegisterKind_WithExtraBinding(t *testing.T) {
	bootstrap := newTestProcess(t)
	bootstrap.Build()
	t.Cleanup(func() { bootstrap.Shutdown() })
	w := firstCell(bootstrap).Stage.ECSWorld()
	extraMap := ecs.NewMap1[kindRegTestExtraComp](w)
	extra := system.Component(extraMap)

	mmo := newTestProcess(t)
	RegisterKind[kindRegTestBundle](mmo, 104, "ExtraKind",
		WithExtraBinding(extra),
	)
	mmo.Build()
	t.Cleanup(func() { mmo.Shutdown() })

	cell := firstCell(mmo)
	def := cell.Stage.EntityKindDefs()[104]
	if def == nil {
		t.Fatal("kind 104 not registered")
	}
	// 2 from the bundle + 1 extra
	if len(def.NetworkBindings) != 3 {
		t.Errorf("expected 3 NetworkBindings (2 bundle + 1 extra), got %d", len(def.NetworkBindings))
	}
	// The extra binding should be appended last.
	last := def.NetworkBindings[len(def.NetworkBindings)-1]
	if last != extra {
		t.Errorf("expected extra binding to be the last NetworkBinding")
	}
}

func TestRegisterKind_WithFieldUnmatched(t *testing.T) {
	type unrelatedComp struct{ X int }
	mmo := newTestProcess(t)
	defer func() {
		mmo.Shutdown()
		r := recover()
		if r == nil {
			t.Fatal("expected panic from unmatched WithField")
		}
		msg := formatAny(r)
		if !contains(msg, "does not match") {
			t.Errorf("expected panic message to contain 'does not match', got %q", msg)
		}
	}()
	RegisterKind[kindRegTestBundle](mmo, 105, "UnmatchedKind",
		WithField[unrelatedComp](LocalOnly()),
	)
}

func TestRegisterKind_NoFields(t *testing.T) {
	type emptyBundle struct{}
	mmo := newTestProcess(t)
	defer func() {
		mmo.Shutdown()
		if recover() == nil {
			t.Fatal("expected panic on empty bundle")
		}
	}()
	RegisterKind[emptyBundle](mmo, 106, "EmptyKind")
}

func TestRegisterKind_NonPointerField(t *testing.T) {
	type badBundle struct {
		Name kindRegTestNameComp // value, not pointer
	}
	mmo := newTestProcess(t)
	defer func() {
		mmo.Shutdown()
		r := recover()
		if r == nil {
			t.Fatal("expected panic on non-pointer field")
		}
		msg := formatAny(r)
		if !contains(msg, "must be a pointer") {
			t.Errorf("expected panic message to mention 'must be a pointer', got %q", msg)
		}
	}()
	RegisterKind[badBundle](mmo, 107, "BadKind")
}

func TestRegisterKind_DashTag(t *testing.T) {
	type dashBundle struct {
		Name    *kindRegTestNameComp
		Skipped *kindRegTestHealthComp `mmokit:"-"`
	}
	mmo := newTestProcess(t)
	RegisterKind[dashBundle](mmo, 108, "DashKind")
	mmo.Build()
	t.Cleanup(func() { mmo.Shutdown() })

	cell := firstCell(mmo)
	def := cell.Stage.EntityKindDefs()[108]
	if def == nil {
		t.Fatal("kind 108 not registered")
	}
	// Only the Name field — Skipped is fully dropped.
	if def.Components() != 1 {
		t.Errorf("expected 1 component (Skipped excluded), got %d", def.Components())
	}
	if len(def.NetworkBindings) != 1 {
		t.Errorf("expected 1 NetworkBinding (Skipped excluded), got %d", len(def.NetworkBindings))
	}
	if reg := cell.Stage.ReplicationRegistry(); reg.Len() != 1 {
		t.Errorf("expected 1 component in transfer registry, got %d", reg.Len())
	}
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// contains reports whether substr is in s. Avoids importing strings just
// for this so the test file stays a single small dependency surface.
func contains(s, substr string) bool {
	if len(substr) == 0 {
		return true
	}
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// formatAny stringifies a recovered panic value uniformly.
func formatAny(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case error:
		return x.Error()
	default:
		return ""
	}
}
