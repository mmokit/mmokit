package mmokit

import (
	"testing"

	ecs "github.com/mlange-42/ark/ecs"
	"github.com/zenion/mmoserver/pkg/universe"
)

type initTestNameComp struct{ Name string }
type initTestHealthComp struct{ HP float32 }
type initTestBundle struct {
	Name   *initTestNameComp
	Health *initTestHealthComp
}

func TestInit_PopulatesBundleAfterSpawn(t *testing.T) {
	mmo := New(Config{
		CellsX: 1, CellsY: 1, CellSize: 1000, TickRate: 20, AoIRadius: 100,
		Headless: true,
	})
	RegisterKind[initTestBundle](mmo, 50, "InitTest")
	mmo.Build()
	t.Cleanup(func() { mmo.Shutdown() })

	var stage *universe.Stage
	for _, c := range mmo.Cells {
		stage = c.Stage
		break
	}
	if stage == nil {
		t.Fatal("no cells")
	}

	var captured *initTestBundle
	e := stage.SpawnEntity(Position{X: 1, Y: 2},
		WithEntityKind(50),
		Init(func(b *initTestBundle) {
			b.Name.Name = "alice"
			b.Health.HP = 42
			captured = b
		}),
	)
	if captured == nil {
		t.Fatal("Init callback never fired")
	}

	// Verify the values landed on the actual entity (not just on a stale bundle).
	nameMap := ecs.NewMap1[initTestNameComp](stage.ECSWorld())
	if nameMap.Get(e).Name != "alice" {
		t.Errorf("expected name 'alice', got %q", nameMap.Get(e).Name)
	}
	healthMap := ecs.NewMap1[initTestHealthComp](stage.ECSWorld())
	if healthMap.Get(e).HP != 42 {
		t.Errorf("expected HP 42, got %v", healthMap.Get(e).HP)
	}
}

func TestInit_NotProvided_KindComponentsStillAttach(t *testing.T) {
	// WithEntityKind alone (no Init) — kind components are auto-attached and
	// accessible via the standard ecs.NewMap1 path. This is mostly redundant
	// with other tests but kept here for the Init test family's completeness.
	mmo := New(Config{
		CellsX: 1, CellsY: 1, CellSize: 1000, TickRate: 20, AoIRadius: 100,
		Headless: true,
	})
	RegisterKind[initTestBundle](mmo, 60, "Init_NoInit")
	mmo.Build()
	t.Cleanup(func() { mmo.Shutdown() })

	var stage *universe.Stage
	for _, c := range mmo.Cells {
		stage = c.Stage
		break
	}

	e := stage.SpawnEntity(Position{X: 0, Y: 0}, WithEntityKind(60))

	nameMap := ecs.NewMap1[initTestNameComp](stage.ECSWorld())
	if !nameMap.HasAll(e) {
		t.Fatal("expected initTestNameComp attached when WithEntityKind is set")
	}
}

func TestInit_PointersAreNonNilWhenCallbackFires(t *testing.T) {
	// Init's callback fires AFTER kind components are attached — so each
	// bundle field pointer is non-nil. This locks the ordering invariant
	// (auto-attach → Init).
	mmo := New(Config{
		CellsX: 1, CellsY: 1, CellSize: 1000, TickRate: 20, AoIRadius: 100,
		Headless: true,
	})
	RegisterKind[initTestBundle](mmo, 61, "Init_PointersNonNil")
	mmo.Build()
	t.Cleanup(func() { mmo.Shutdown() })

	var stage *universe.Stage
	for _, c := range mmo.Cells {
		stage = c.Stage
		break
	}

	var nameWasNil, healthWasNil bool
	stage.SpawnEntity(Position{X: 0, Y: 0},
		WithEntityKind(61),
		Init(func(b *initTestBundle) {
			nameWasNil = (b.Name == nil)
			healthWasNil = (b.Health == nil)
		}),
	)

	if nameWasNil {
		t.Error("expected b.Name to be non-nil at Init callback time")
	}
	if healthWasNil {
		t.Error("expected b.Health to be non-nil at Init callback time")
	}
}

func TestInit_WithoutEntityKind_PanicsBecauseComponentsMissing(t *testing.T) {
	// Init expects the bundle's components to be present on the entity. If
	// WithEntityKind is omitted, no auto-attach runs, so Unsafe.Get panics
	// when fetching a component the entity doesn't have. Lock this behavior
	// so future changes don't silently allow Init without WithEntityKind.
	mmo := New(Config{
		CellsX: 1, CellsY: 1, CellSize: 1000, TickRate: 20, AoIRadius: 100,
		Headless: true,
	})
	RegisterKind[initTestBundle](mmo, 62, "Init_NoKind")
	mmo.Build()
	t.Cleanup(func() { mmo.Shutdown() })

	var stage *universe.Stage
	for _, c := range mmo.Cells {
		stage = c.Stage
		break
	}

	defer func() {
		if recover() == nil {
			t.Fatal("expected panic when Init is used without WithEntityKind")
		}
	}()
	stage.SpawnEntity(Position{X: 0, Y: 0},
		// no WithEntityKind — kind components are NOT attached
		Init(func(b *initTestBundle) {
			// Accessing b.Name would deref a bad pointer, but the panic
			// typically fires earlier inside Unsafe.Get when ark tries to
			// fetch a component ID that the entity doesn't have.
			_ = b.Name
		}),
	)
}
