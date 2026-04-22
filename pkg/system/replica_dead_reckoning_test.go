package system

import (
	"testing"

	"github.com/mlange-42/ark/ecs"
	"github.com/zenion/mmoserver/pkg/component"
)

const drDT = float32(0.05)

// --- Replica tests ---

func TestReplicaDR_AdvancesOnUpdate(t *testing.T) {
	world := ecs.NewWorld()
	sys := &ReplicaDeadReckoningSystem{}
	sys.SetDeps(world, nil, nil)
	sys.Init()

	mapper := ecs.NewMap3[component.Position, component.Velocity, component.Replica](world)
	entity := mapper.NewEntity(
		&component.Position{X: 0, Y: 0},
		&component.Velocity{X: 100, Y: 0},
		&component.Replica{UpdatedThisTick: true},
	)

	sys.Update(drDT)

	posMap := ecs.NewMap1[component.Position](world)
	repMap := ecs.NewMap1[component.Replica](world)
	pos := posMap.Get(entity)
	rep := repMap.Get(entity)

	if pos.X != 100*drDT {
		t.Errorf("expected X=%f after advance, got %f", 100*drDT, pos.X)
	}
	if rep.MissedTicks != 0 {
		t.Errorf("expected MissedTicks=0 after update, got %d", rep.MissedTicks)
	}
}

func TestReplicaDR_GraceAdvancesOnSingleMissedTick(t *testing.T) {
	world := ecs.NewWorld()
	sys := &ReplicaDeadReckoningSystem{}
	sys.SetDeps(world, nil, nil)
	sys.Init()

	mapper := ecs.NewMap3[component.Position, component.Velocity, component.Replica](world)
	entity := mapper.NewEntity(
		&component.Position{X: 0, Y: 0},
		&component.Velocity{X: 100, Y: 0},
		&component.Replica{UpdatedThisTick: true},
	)

	repMap := ecs.NewMap1[component.Replica](world)
	posMap := ecs.NewMap1[component.Position](world)

	// Tick 1: updated — advance normally.
	sys.Update(drDT)

	pos1 := posMap.Get(entity)
	if pos1.X != 100*drDT {
		t.Errorf("tick1: expected X=%f, got %f", 100*drDT, pos1.X)
	}

	// Tick 2: NOT updated — MissedTicks becomes 1, grace advance.
	repMap.Get(entity).UpdatedThisTick = false
	sys.Update(drDT)

	rep2 := repMap.Get(entity)
	pos2 := posMap.Get(entity)
	if rep2.MissedTicks != 1 {
		t.Errorf("tick2: expected MissedTicks=1, got %d", rep2.MissedTicks)
	}
	if pos2.X != 2*100*drDT {
		t.Errorf("tick2: expected grace advance to X=%f, got %f", 2*100*drDT, pos2.X)
	}

	// Tick 3: still NOT updated — MissedTicks becomes 2, frozen.
	repMap.Get(entity).UpdatedThisTick = false
	sys.Update(drDT)

	rep3 := repMap.Get(entity)
	pos3 := posMap.Get(entity)
	if rep3.MissedTicks != 2 {
		t.Errorf("tick3: expected MissedTicks=2, got %d", rep3.MissedTicks)
	}
	if pos3.X != 2*100*drDT {
		t.Errorf("tick3: expected frozen X=%f (no advance), got %f", 2*100*drDT, pos3.X)
	}
}

func TestReplicaDR_ResetsMissedTicksOnUpdate(t *testing.T) {
	world := ecs.NewWorld()
	sys := &ReplicaDeadReckoningSystem{}
	sys.SetDeps(world, nil, nil)
	sys.Init()

	mapper := ecs.NewMap3[component.Position, component.Velocity, component.Replica](world)
	entity := mapper.NewEntity(
		&component.Position{X: 0, Y: 0},
		&component.Velocity{X: 100, Y: 0},
		&component.Replica{UpdatedThisTick: true},
	)

	repMap := ecs.NewMap1[component.Replica](world)

	// Tick 1: updated.
	sys.Update(drDT)

	// Tick 2: missed — grace, MissedTicks=1.
	repMap.Get(entity).UpdatedThisTick = false
	sys.Update(drDT)

	rep2 := repMap.Get(entity)
	if rep2.MissedTicks != 1 {
		t.Errorf("after miss: expected MissedTicks=1, got %d", rep2.MissedTicks)
	}

	// Tick 3: updated again — MissedTicks resets to 0.
	repMap.Get(entity).UpdatedThisTick = true
	sys.Update(drDT)

	rep3 := repMap.Get(entity)
	if rep3.MissedTicks != 0 {
		t.Errorf("after re-update: expected MissedTicks=0, got %d", rep3.MissedTicks)
	}
}

// --- Shadow tests ---

func TestShadowDR_AdvancesOnUpdate(t *testing.T) {
	world := ecs.NewWorld()
	sys := &ReplicaDeadReckoningSystem{}
	sys.SetDeps(world, nil, nil)
	sys.Init()

	mapper := ecs.NewMap3[component.Position, component.Velocity, component.Shadow](world)
	entity := mapper.NewEntity(
		&component.Position{X: 0, Y: 0},
		&component.Velocity{X: 100, Y: 0},
		&component.Shadow{UpdatedThisTick: true},
	)

	sys.Update(drDT)

	posMap := ecs.NewMap1[component.Position](world)
	shadowMap := ecs.NewMap1[component.Shadow](world)
	pos := posMap.Get(entity)
	sh := shadowMap.Get(entity)

	if pos.X != 100*drDT {
		t.Errorf("expected X=%f after advance, got %f", 100*drDT, pos.X)
	}
	if sh.MissedTicks != 0 {
		t.Errorf("expected MissedTicks=0 after update, got %d", sh.MissedTicks)
	}
}

func TestShadowDR_GraceAdvancesOnSingleMissedTick(t *testing.T) {
	world := ecs.NewWorld()
	sys := &ReplicaDeadReckoningSystem{}
	sys.SetDeps(world, nil, nil)
	sys.Init()

	mapper := ecs.NewMap3[component.Position, component.Velocity, component.Shadow](world)
	entity := mapper.NewEntity(
		&component.Position{X: 0, Y: 0},
		&component.Velocity{X: 100, Y: 0},
		&component.Shadow{UpdatedThisTick: true},
	)

	shadowMap := ecs.NewMap1[component.Shadow](world)
	posMap := ecs.NewMap1[component.Position](world)

	// Tick 1: updated.
	sys.Update(drDT)

	pos1 := posMap.Get(entity)
	if pos1.X != 100*drDT {
		t.Errorf("tick1: expected X=%f, got %f", 100*drDT, pos1.X)
	}

	// Tick 2: NOT updated — MissedTicks becomes 1, grace advance.
	shadowMap.Get(entity).UpdatedThisTick = false
	sys.Update(drDT)

	sh2 := shadowMap.Get(entity)
	pos2 := posMap.Get(entity)
	if sh2.MissedTicks != 1 {
		t.Errorf("tick2: expected MissedTicks=1, got %d", sh2.MissedTicks)
	}
	if pos2.X != 2*100*drDT {
		t.Errorf("tick2: expected grace advance to X=%f, got %f", 2*100*drDT, pos2.X)
	}

	// Tick 3: still NOT updated — MissedTicks becomes 2, frozen.
	shadowMap.Get(entity).UpdatedThisTick = false
	sys.Update(drDT)

	sh3 := shadowMap.Get(entity)
	pos3 := posMap.Get(entity)
	if sh3.MissedTicks != 2 {
		t.Errorf("tick3: expected MissedTicks=2, got %d", sh3.MissedTicks)
	}
	if pos3.X != 2*100*drDT {
		t.Errorf("tick3: expected frozen X=%f (no advance), got %f", 2*100*drDT, pos3.X)
	}
}

func TestShadowDR_ResetsMissedTicksOnUpdate(t *testing.T) {
	world := ecs.NewWorld()
	sys := &ReplicaDeadReckoningSystem{}
	sys.SetDeps(world, nil, nil)
	sys.Init()

	mapper := ecs.NewMap3[component.Position, component.Velocity, component.Shadow](world)
	entity := mapper.NewEntity(
		&component.Position{X: 0, Y: 0},
		&component.Velocity{X: 100, Y: 0},
		&component.Shadow{UpdatedThisTick: true},
	)

	shadowMap := ecs.NewMap1[component.Shadow](world)

	// Tick 1: updated.
	sys.Update(drDT)

	// Tick 2: missed — grace, MissedTicks=1.
	shadowMap.Get(entity).UpdatedThisTick = false
	sys.Update(drDT)

	sh2 := shadowMap.Get(entity)
	if sh2.MissedTicks != 1 {
		t.Errorf("after miss: expected MissedTicks=1, got %d", sh2.MissedTicks)
	}

	// Tick 3: updated again — MissedTicks resets to 0.
	shadowMap.Get(entity).UpdatedThisTick = true
	sys.Update(drDT)

	sh3 := shadowMap.Get(entity)
	if sh3.MissedTicks != 0 {
		t.Errorf("after re-update: expected MissedTicks=0, got %d", sh3.MissedTicks)
	}
}
