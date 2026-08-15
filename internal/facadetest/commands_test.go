package facadetest

import (
	"testing"

	"github.com/mlange-42/ark/ecs"

	"github.com/mmokit/mmokit"
	"github.com/mmokit/mmokit/pkg/component"
)

// TestAddComponent_AppliesAtFlush verifies that AddComponent queues
// the op and applies it when Flush runs.
func TestAddComponent_AppliesAtFlush(t *testing.T) {
	stage, _ := newTestStage(t)
	w := stage.ECSWorld()
	mapper := ecs.NewMap2[component.Position, component.NetworkID](w)
	h := mapper.NewEntity(
		&component.Position{X: 1, Y: 1},
		&component.NetworkID{ID: 1},
	)
	stage.RegisterLiveNetID(1, h)
	e := mmokit.EntityFromECS(stage, h)

	mmokit.AddComponent(stage.Commands(), e, component.Velocity{X: 10, Y: 20})

	// Before Flush, Velocity is not yet on the entity.
	velMap := ecs.NewMap1[component.Velocity](w)
	if velMap.HasAll(h) {
		t.Fatal("AddComponent applied before Flush")
	}

	stage.Commands().Flush()

	if !velMap.HasAll(h) {
		t.Fatal("AddComponent did not apply after Flush")
	}
	v := velMap.Get(h)
	if v.X != 10 || v.Y != 20 {
		t.Fatalf("Velocity = %+v, want {X:10, Y:20}", *v)
	}
}

// TestAddComponent_OverwritesExisting verifies that AddComponent on an
// entity that already has component T overwrites the existing value
// rather than panicking or no-oping.
func TestAddComponent_OverwritesExisting(t *testing.T) {
	stage, _ := newTestStage(t)
	w := stage.ECSWorld()
	mapper := ecs.NewMap3[component.Position, component.NetworkID, component.Velocity](w)
	h := mapper.NewEntity(
		&component.Position{X: 0, Y: 0},
		&component.NetworkID{ID: 2},
		&component.Velocity{X: 1, Y: 1},
	)
	stage.RegisterLiveNetID(2, h)
	e := mmokit.EntityFromECS(stage, h)

	mmokit.AddComponent(stage.Commands(), e, component.Velocity{X: 99, Y: 99})
	stage.Commands().Flush()

	velMap := ecs.NewMap1[component.Velocity](w)
	v := velMap.Get(h)
	if v.X != 99 || v.Y != 99 {
		t.Fatalf("AddComponent did not overwrite; got %+v want {99 99}", *v)
	}
}

// TestRemoveComponent_AppliesAtFlush verifies RemoveComponent drops
// the component at flush time.
func TestRemoveComponent_AppliesAtFlush(t *testing.T) {
	stage, _ := newTestStage(t)
	w := stage.ECSWorld()
	mapper := ecs.NewMap3[component.Position, component.NetworkID, component.Velocity](w)
	h := mapper.NewEntity(
		&component.Position{X: 1, Y: 1},
		&component.NetworkID{ID: 3},
		&component.Velocity{X: 10, Y: 20},
	)
	stage.RegisterLiveNetID(3, h)
	e := mmokit.EntityFromECS(stage, h)

	mmokit.RemoveComponent[component.Velocity](stage.Commands(), e)

	velMap := ecs.NewMap1[component.Velocity](w)
	if !velMap.HasAll(h) {
		t.Fatal("RemoveComponent applied before Flush")
	}

	stage.Commands().Flush()

	if velMap.HasAll(h) {
		t.Fatal("RemoveComponent did not drop component at Flush")
	}
}

// TestAddRemoveComponent_NoopOnDeadEntity verifies that ops on a dead
// entity are silent no-ops, not panics.
func TestAddRemoveComponent_NoopOnDeadEntity(t *testing.T) {
	stage, _ := newTestStage(t)
	w := stage.ECSWorld()
	mapper := ecs.NewMap2[component.Position, component.NetworkID](w)
	h := mapper.NewEntity(
		&component.Position{X: 1, Y: 1},
		&component.NetworkID{ID: 4},
	)
	stage.RegisterLiveNetID(4, h)
	e := mmokit.EntityFromECS(stage, h)
	w.RemoveEntity(h) // kill the entity before flush

	mmokit.AddComponent(stage.Commands(), e, component.Velocity{X: 99, Y: 99})
	mmokit.RemoveComponent[component.Position](stage.Commands(), e)

	// Should not panic.
	stage.Commands().Flush()
}

// TestSystemBase_CommandsShortcut verifies s.Commands() returns the
// same buffer as s.Stage().Commands() and ops queued via the shortcut
// apply through Stage.TickOne (which mirrors the engine's per-system
// flush contract).
func TestSystemBase_CommandsShortcut(t *testing.T) {
	stage, eng := newTestStage(t)
	w := stage.ECSWorld()

	mapper := ecs.NewMap2[component.Position, component.NetworkID](w)
	h := mapper.NewEntity(
		&component.Position{X: 0, Y: 0},
		&component.NetworkID{ID: 42},
	)
	stage.RegisterLiveNetID(42, h)

	sys := &commandsTestSystem{handle: h}
	mmokit.WireSystem(sys, w, eng, stage) // also calls sys.Init()

	// TickOne runs Update then Flushes — the system's queued AddComponent
	// should land before TickOne returns.
	stage.TickOne(sys, 0.05)

	velMap := ecs.NewMap1[component.Velocity](w)
	if !velMap.HasAll(h) {
		t.Fatal("system's queued AddComponent did not apply at TickOne")
	}
	v := velMap.Get(h)
	if v.X != 7 || v.Y != 11 {
		t.Fatalf("Velocity = %+v, want {X:7, Y:11}", *v)
	}
}

// Minimal fake system that uses s.Commands() inside Update.
type commandsTestSystem struct {
	mmokit.SystemBase
	handle ecs.Entity
}

func (s *commandsTestSystem) Init() {}

func (s *commandsTestSystem) Update(dt float32) {
	e := mmokit.EntityFromECS(s.Stage(), s.handle)
	mmokit.AddComponent(s.Commands(), e, component.Velocity{X: 7, Y: 11})
}
