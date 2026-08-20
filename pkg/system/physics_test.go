package system

import (
	"math"
	"testing"

	"github.com/mlange-42/ark/ecs"
	"github.com/mmokit/mmokit/pkg/component"
	"github.com/mmokit/mmokit/pkg/engine"
	"github.com/mmokit/mmokit/pkg/logger"
	"github.com/mmokit/mmokit/pkg/net"
)

// newPhysicsFixture wires a PhysicsSystem against a world with the given
// gravity, and returns the world plus the system.
func newPhysicsFixture(t *testing.T, gravity float32) (*ecs.World, *PhysicsSystem) {
	t.Helper()
	world := ecs.NewWorld()
	eng := engine.New(engine.Config{TickRate: 20, Gravity: gravity}, net.NewConnManager(), logger.New())
	eng.ECS = world
	sys := &PhysicsSystem{}
	wireSystem(sys, world, eng)
	return world, sys
}

// TestPhysics_IntegratesAllThreeAxes is the core of the unit. Before it, Z was
// silently ignored: an entity with vertical velocity never moved, and nothing
// reported it.
func TestPhysics_IntegratesAllThreeAxes(t *testing.T) {
	world, sys := newPhysicsFixture(t, 0)
	posMap := ecs.NewMap1[component.Position](world)
	velMap := ecs.NewMap1[component.Velocity](world)

	e := world.NewEntity()
	posMap.Add(e, &component.Position{X: 1, Y: 2, Z: 3})
	velMap.Add(e, &component.Velocity{X: 10, Y: 20, Z: 30})

	sys.Update(0.5)

	pos := posMap.Get(e)
	if pos.X != 6 || pos.Y != 12 || pos.Z != 18 {
		t.Fatalf("position = %v,%v,%v, want 6,12,18", pos.X, pos.Y, pos.Z)
	}
}

// TestPhysics_TwoDIsUnaffected pins that three-axis integration is inert for
// an entity with no vertical velocity — which is every entity in a 2D
// profile, since nothing in pkg/ or either example writes Velocity.Z.
func TestPhysics_TwoDIsUnaffected(t *testing.T) {
	world, sys := newPhysicsFixture(t, 0)
	posMap := ecs.NewMap1[component.Position](world)
	velMap := ecs.NewMap1[component.Velocity](world)

	e := world.NewEntity()
	posMap.Add(e, &component.Position{X: 100, Y: 200})
	velMap.Add(e, &component.Velocity{X: -4, Y: 8})

	for i := 0; i < 10; i++ {
		sys.Update(0.05)
	}

	pos := posMap.Get(e)
	if pos.Z != 0 {
		t.Errorf("Z drifted to %v in a 2D-shaped entity", pos.Z)
	}
	if math.Abs(float64(pos.X-98)) > 1e-4 || math.Abs(float64(pos.Y-204)) > 1e-4 {
		t.Errorf("X/Y = %v,%v, want 98,204", pos.X, pos.Y)
	}
}

// TestPhysics_GravityRequiresOptIn — gravity applies only to entities with a
// Motion component whose mode is not MoveFly. An entity without Motion is
// untouched even in a process configured with gravity, which is what lets one
// process mix falling characters with flying ships.
func TestPhysics_GravityRequiresOptIn(t *testing.T) {
	world, sys := newPhysicsFixture(t, 10)
	posMap := ecs.NewMap1[component.Position](world)
	velMap := ecs.NewMap1[component.Velocity](world)
	motionMap := ecs.NewMap1[component.Motion](world)

	noMotion := world.NewEntity()
	posMap.Add(noMotion, &component.Position{Z: 100})
	velMap.Add(noMotion, &component.Velocity{})

	flying := world.NewEntity()
	posMap.Add(flying, &component.Position{Z: 100})
	velMap.Add(flying, &component.Velocity{})
	motionMap.Add(flying, &component.Motion{Mode: component.MoveFly})

	falling := world.NewEntity()
	posMap.Add(falling, &component.Position{Z: 100})
	velMap.Add(falling, &component.Velocity{})
	motionMap.Add(falling, &component.Motion{Mode: component.MoveBallistic, GroundZ: 0})

	sys.Update(1)

	if got := posMap.Get(noMotion).Z; got != 100 {
		t.Errorf("entity without Motion fell to %v, want 100", got)
	}
	if got := posMap.Get(flying).Z; got != 100 {
		t.Errorf("MoveFly entity fell to %v, want 100", got)
	}
	// v -= g*dt = -10, then z += v*dt = 90.
	if got := posMap.Get(falling).Z; got != 90 {
		t.Errorf("MoveBallistic entity is at %v, want 90", got)
	}
}

// TestPhysics_WalkClampsToGround covers the mode that has a ground plane, plus
// the Grounded flag games gate jumping on.
func TestPhysics_WalkClampsToGround(t *testing.T) {
	world, sys := newPhysicsFixture(t, 10)
	posMap := ecs.NewMap1[component.Position](world)
	velMap := ecs.NewMap1[component.Velocity](world)
	motionMap := ecs.NewMap1[component.Motion](world)

	e := world.NewEntity()
	posMap.Add(e, &component.Position{Z: 5})
	velMap.Add(e, &component.Velocity{})
	motionMap.Add(e, &component.Motion{Mode: component.MoveWalk, GroundZ: 2})

	// One second of fall would reach 5 - 10 = -5, well below ground.
	sys.Update(1)

	if got := posMap.Get(e).Z; got != 2 {
		t.Errorf("walker settled at %v, want the ground plane 2", got)
	}
	if !motionMap.Get(e).Grounded {
		t.Error("walker resting on the ground is not marked Grounded")
	}
	if got := velMap.Get(e).Z; got != 0 {
		t.Errorf("grounded walker kept downward velocity %v", got)
	}

	// A jump on the tick after landing must survive: only DOWNWARD velocity
	// is zeroed, so an upward impulse is not eaten by the clamp.
	velMap.Get(e).Z = 20
	sys.Update(0.1)
	if got := posMap.Get(e).Z; got <= 2 {
		t.Errorf("jump was swallowed by the ground clamp: Z = %v", got)
	}
	if motionMap.Get(e).Grounded {
		t.Error("airborne walker is still marked Grounded")
	}
}

// TestPhysics_BallisticDoesNotClamp — the difference between the two gravity
// modes is exactly the ground clamp.
func TestPhysics_BallisticDoesNotClamp(t *testing.T) {
	world, sys := newPhysicsFixture(t, 10)
	posMap := ecs.NewMap1[component.Position](world)
	velMap := ecs.NewMap1[component.Velocity](world)
	motionMap := ecs.NewMap1[component.Motion](world)

	e := world.NewEntity()
	posMap.Add(e, &component.Position{Z: 1})
	velMap.Add(e, &component.Velocity{})
	motionMap.Add(e, &component.Motion{Mode: component.MoveBallistic, GroundZ: 0})

	sys.Update(1)

	if got := posMap.Get(e).Z; got >= 0 {
		t.Errorf("ballistic entity stopped at %v; it must pass through GroundZ", got)
	}
}

// TestPhysics_ZeroGravityIgnoresMotion — a 2D process cannot set gravity
// (Build refuses it), so a Motion component that somehow exists there must
// still be inert.
func TestPhysics_ZeroGravityIgnoresMotion(t *testing.T) {
	world, sys := newPhysicsFixture(t, 0)
	posMap := ecs.NewMap1[component.Position](world)
	velMap := ecs.NewMap1[component.Velocity](world)
	motionMap := ecs.NewMap1[component.Motion](world)

	e := world.NewEntity()
	posMap.Add(e, &component.Position{Z: 50})
	velMap.Add(e, &component.Velocity{})
	motionMap.Add(e, &component.Motion{Mode: component.MoveWalk, GroundZ: 0})

	sys.Update(1)

	if got := posMap.Get(e).Z; got != 50 {
		t.Errorf("entity moved to %v with gravity 0", got)
	}
}
