package universe

import (
	"reflect"
	"testing"

	"github.com/mlange-42/ark/ecs"

	"github.com/mmokit/mmokit/pkg/component"
	"github.com/mmokit/mmokit/pkg/engine"
	"github.com/mmokit/mmokit/pkg/logger"
	"github.com/mmokit/mmokit/pkg/net"
)

func newTestStage(t *testing.T) *Stage {
	t.Helper()
	return newTestStageAt(t, CellID{X: 0, Y: 0})
}

// newTestStageAt builds a test stage rooted at an arbitrary grid cell. Use
// when a test must distinguish base-cell-local from world coordinates (e.g.
// the border-context regression guard, which only catches a root-origin
// offset bug at a non-(0,0) root).
func newTestStageAt(t *testing.T, cell CellID) *Stage {
	t.Helper()
	eng := engine.New(engine.DefaultConfig(), net.NewConnManager(), logger.New())
	return NewStage(eng, cell, 100, nil, globalWire)
}

// TestSpawn_AttachesEveryComponent verifies that Stage.Spawn attaches each
// variadic component value to the entity and returns a usable Entity wrapper.
func TestSpawn_AttachesEveryComponent(t *testing.T) {
	stage := newTestStage(t)

	type marker struct{ Tag uint8 }
	_ = ecs.NewMap1[marker](stage.ECSWorld()) // pre-register

	e := stage.Spawn(
		component.Position{X: 100, Y: 200},
		component.Collider{Radius: 5, Layer: 1},
		marker{Tag: 42},
	)

	if e.NetID() == 0 {
		t.Fatal("expected non-zero netID on spawned entity")
	}
	if !e.Alive() {
		t.Fatal("expected spawned entity to be Alive")
	}
	posMap := ecs.NewMap1[component.Position](stage.ECSWorld())
	if !posMap.HasAll(e.Handle()) {
		t.Fatal("expected Position attached")
	}
	if p := posMap.Get(e.Handle()); p.X != 100 || p.Y != 200 {
		t.Errorf("expected Position{100,200}, got {%v,%v}", p.X, p.Y)
	}
	colMap := ecs.NewMap1[component.Collider](stage.ECSWorld())
	if !colMap.HasAll(e.Handle()) {
		t.Fatal("expected Collider attached")
	}
	if c := colMap.Get(e.Handle()); c.Radius != 5 || c.Layer != 1 {
		t.Errorf("Collider not copied verbatim: %+v", c)
	}
	mkMap := ecs.NewMap1[marker](stage.ECSWorld())
	if !mkMap.HasAll(e.Handle()) || mkMap.Get(e.Handle()).Tag != 42 {
		t.Errorf("marker component missing or wrong: %+v", mkMap.Get(e.Handle()))
	}
}

// TestSpawn_RollsBackOnDuplicateNetIDInStrictMode verifies that under
// strictNetIDIndex, a Spawn that collides on netID rolls back the entity
// and returns the zero-value Entity, matching SpawnEntity's behavior.
func TestSpawn_RollsBackOnDuplicateNetIDInStrictMode(t *testing.T) {
	stage := newTestStage(t)
	stage.strictNetIDIndex = true

	// First spawn — succeeds.
	e1 := stage.Spawn(component.Position{X: 1, Y: 2})
	if e1.NetID() == 0 {
		t.Fatal("expected first spawn to succeed")
	}

	// Force the NEXT spawn's netID to collide. The engine hands out monotonic
	// netIDs from NextNetID() (atomic counter, base + 1, 2, 3, ...) so the
	// next call after e1 will return e1.NetID()+1. Pre-register that value
	// as Live so the upcoming Spawn's Enter(...) returns ActionDuplicate.
	nextNID := e1.NetID() + 1
	stage.netIDIdx.Enter(nextNID, ecs.Entity{}, PresenceLive)

	// Second spawn — would collide. Under strict mode, Spawn must roll back.
	e2 := stage.Spawn(component.Position{X: 3, Y: 4})
	if e2.NetID() != 0 {
		t.Errorf("expected zero Entity on rollback, got netID=%d alive=%v", e2.NetID(), e2.Alive())
	}
}

// TestSpawn_PanicsWithoutPosition verifies that omitting Position is
// a programmer error caught at call time.
func TestSpawn_PanicsWithoutPosition(t *testing.T) {
	stage := newTestStage(t)
	type marker struct{}
	_ = ecs.NewMap1[marker](stage.ECSWorld())

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic when Position is missing")
		}
	}()

	stage.Spawn(marker{})
}

// TestSpawn_PanicsOnDuplicateComponentType verifies that passing the same
// component type twice is a programmer error caught at call time.
func TestSpawn_PanicsOnDuplicateComponentType(t *testing.T) {
	stage := newTestStage(t)

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic when same component type passed twice")
		}
	}()

	stage.Spawn(
		component.Position{X: 1, Y: 2},
		component.Position{X: 3, Y: 4},
	)
}

// TestSpawn_PanicsOnMissingRequiredKindComponent verifies that under
// InvariantPanic mode, kinded spawns missing a required Bundle component
// fail loudly at spawn time.
func TestSpawn_PanicsOnMissingRequiredKindComponent(t *testing.T) {
	stage := newTestStage(t)
	if stage.coord == nil {
		stage.coord = &Process{invariantMode: InvariantPanic}
	} else {
		stage.coord.invariantMode = InvariantPanic
	}

	type health struct{ HP int32 }
	_ = ecs.NewMap1[health](stage.ECSWorld())

	// Register a kind that requires `health`.
	w := stage.ECSWorld()
	def := EntityKindDef{Kind: 99, Name: "TestKind"}
	healthType := reflect.TypeFor[health]()
	id := ecs.TypeID(w, healthType)
	KindComponentByID(&def, w, id, healthType, KindComponentRequired)
	stage.RegisterEntityKind(def)

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic when kinded spawn omits required component")
		}
	}()

	stage.Spawn(
		component.Position{},
		component.EntityKind{Type: 99},
		// missing health — should panic
	)
}
