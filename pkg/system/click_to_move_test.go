package system

import (
	"math"
	"testing"

	"github.com/mlange-42/ark/ecs"
	"github.com/zenion/mmoserver/pkg/component"
	"github.com/zenion/mmoserver/pkg/coords"
)

func TestClickToMoveBasic(t *testing.T) {
	world := ecs.NewWorld()
	sys := &ClickToMoveSystem{}
	sys.SetDeps(world, nil, nil)
	sys.Init()

	mapper := ecs.NewMap5[component.Position, component.Velocity, component.MoveTarget, component.CellCoord, component.MoveParams](world)
	entity := mapper.NewEntity(
		&component.Position{X: 0, Y: 0},
		&component.Velocity{},
		&component.MoveTarget{LocalX: 100, LocalY: 0, Active: true},
		&component.CellCoord{},
		&component.MoveParams{MaxSpeed: 300},
	)

	sys.Update(0.05)

	velMap := ecs.NewMap1[component.Velocity](world)
	vel := velMap.Get(entity)
	if vel.X <= 0 {
		t.Errorf("expected positive X velocity, got %f", vel.X)
	}
	if math.Abs(float64(vel.Y)) > 0.001 {
		t.Errorf("expected zero Y velocity, got %f", vel.Y)
	}
	if math.Abs(float64(vel.X)-300) > 1 {
		t.Errorf("expected X velocity ~300, got %f", vel.X)
	}
}

func TestClickToMoveArrival(t *testing.T) {
	world := ecs.NewWorld()
	sys := &ClickToMoveSystem{}
	sys.SetDeps(world, nil, nil)
	sys.Init()

	mapper := ecs.NewMap4[component.Position, component.Velocity, component.MoveTarget, component.CellCoord](world)
	entity := mapper.NewEntity(
		&component.Position{X: 100, Y: 100},
		&component.Velocity{X: 50, Y: 50},
		&component.MoveTarget{LocalX: 100.5, LocalY: 100, Active: true},
		&component.CellCoord{},
	)

	sys.Update(0.05)

	velMap := ecs.NewMap1[component.Velocity](world)
	vel := velMap.Get(entity)
	mtMap := ecs.NewMap1[component.MoveTarget](world)
	mt := mtMap.Get(entity)

	if mt.Active {
		t.Error("expected MoveTarget.Active = false after arrival")
	}
	if vel.X != 0 || vel.Y != 0 {
		t.Errorf("expected zero velocity after arrival, got (%f, %f)", vel.X, vel.Y)
	}
}

func TestClickToMoveInactive(t *testing.T) {
	world := ecs.NewWorld()
	sys := &ClickToMoveSystem{}
	sys.SetDeps(world, nil, nil)
	sys.Init()

	mapper := ecs.NewMap4[component.Position, component.Velocity, component.MoveTarget, component.CellCoord](world)
	entity := mapper.NewEntity(
		&component.Position{X: 0, Y: 0},
		&component.Velocity{X: 99, Y: 99},
		&component.MoveTarget{Active: false},
		&component.CellCoord{},
	)

	sys.Update(0.05)

	velMap := ecs.NewMap1[component.Velocity](world)
	vel := velMap.Get(entity)
	if vel.X != 99 || vel.Y != 99 {
		t.Errorf("expected velocity unchanged, got (%f, %f)", vel.X, vel.Y)
	}
}

func TestSetMoveTarget(t *testing.T) {
	coords.SetCellSize(2000)
	defer coords.SetCellSize(8192) // restore default

	mt := &component.MoveTarget{}
	SetMoveTarget(mt, 3500, -500)

	if mt.CellX != 1 {
		t.Errorf("CellX = %d, want 1", mt.CellX)
	}
	if mt.CellY != -1 {
		t.Errorf("CellY = %d, want -1", mt.CellY)
	}
	if !mt.Active {
		t.Error("expected Active = true")
	}
	if math.Abs(float64(mt.LocalX)-1500) > 0.01 {
		t.Errorf("LocalX = %f, want 1500", mt.LocalX)
	}
	if math.Abs(float64(mt.LocalY)-1500) > 0.01 {
		t.Errorf("LocalY = %f, want 1500", mt.LocalY)
	}
}

func TestCancelMoveTarget(t *testing.T) {
	mt := &component.MoveTarget{Active: true}
	CancelMoveTarget(mt)
	if mt.Active {
		t.Error("expected Active = false")
	}
}
