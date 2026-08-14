package system

import (
	"math"
	"testing"

	"github.com/mlange-42/ark/ecs"
	"github.com/zenion/mmokit/pkg/component"
)

func TestDirectionMoveActive(t *testing.T) {
	world := ecs.NewWorld()
	sys := &DirectionMoveSystem{}
	wireSystem(sys, world, nil)

	mapper := ecs.NewMap4[component.Position, component.Velocity, component.DirectionInput, component.MoveParams](world)
	entity := mapper.NewEntity(
		&component.Position{},
		&component.Velocity{},
		&component.DirectionInput{X: 1, Y: 0, Active: true},
		&component.MoveParams{MaxSpeed: 200},
	)

	sys.Update(0.05)

	velMap := ecs.NewMap1[component.Velocity](world)
	vel := velMap.Get(entity)
	if math.Abs(float64(vel.X)-200) > 1 {
		t.Errorf("expected X velocity ~200, got %f", vel.X)
	}
	if math.Abs(float64(vel.Y)) > 0.001 {
		t.Errorf("expected zero Y velocity, got %f", vel.Y)
	}
}

func TestDirectionMoveInactive(t *testing.T) {
	world := ecs.NewWorld()
	sys := &DirectionMoveSystem{}
	wireSystem(sys, world, nil)

	mapper := ecs.NewMap3[component.Position, component.Velocity, component.DirectionInput](world)
	entity := mapper.NewEntity(
		&component.Position{},
		&component.Velocity{X: 100, Y: 100},
		&component.DirectionInput{Active: false},
	)

	sys.Update(0.05)

	velMap := ecs.NewMap1[component.Velocity](world)
	vel := velMap.Get(entity)
	if vel.X != 0 || vel.Y != 0 {
		t.Errorf("expected zero velocity when inactive, got (%f, %f)", vel.X, vel.Y)
	}
}

func TestDirectionMoveNormalization(t *testing.T) {
	world := ecs.NewWorld()
	sys := &DirectionMoveSystem{}
	wireSystem(sys, world, nil)

	mapper := ecs.NewMap4[component.Position, component.Velocity, component.DirectionInput, component.MoveParams](world)
	entity := mapper.NewEntity(
		&component.Position{},
		&component.Velocity{},
		&component.DirectionInput{X: 1, Y: 1, Active: true},
		&component.MoveParams{MaxSpeed: 100},
	)

	sys.Update(0.05)

	velMap := ecs.NewMap1[component.Velocity](world)
	vel := velMap.Get(entity)
	speed := math.Sqrt(float64(vel.X*vel.X + vel.Y*vel.Y))
	if math.Abs(speed-100) > 1 {
		t.Errorf("expected speed ~100, got %f", speed)
	}
}
