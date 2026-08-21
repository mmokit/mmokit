// Command cube3d is the framework's headless 3D reference process.
//
// It exists to prove the 3D profile end to end: entities carry Z, fall under
// gravity, replicate through the 3D engine binding set, and survive a cell
// split with their vertical state intact. examples/space remains the 2D
// regression bed; this is deliberately the smallest thing that exercises a
// dimension the reference game does not.
//
// No database, no frontend, no client. That is a property worth keeping: it
// means the 3D acceptance test runs anywhere `go test` runs.
package main

import (
	"github.com/mmokit/mmokit"
)

// World geometry. Small and even so a split produces four children whose
// quadrants are trivial to reason about in the acceptance test.
const (
	CellsX    = 2
	CellsY    = 2
	CellSize  = 1000
	TickRate  = 20
	AoIRadius = 500

	// Gravity is Earth-ish. The exact value does not matter; that it is
	// non-zero is what makes this a 3D process — Build refuses gravity in a
	// 2D profile.
	Gravity = -9.81

	// GroundZ is the plane MoveWalk clamps to.
	GroundZ = 0

	// CubesPerCell is how many cubes each cell bootstraps.
	CubesPerCell = 16

	// KindCube is the only entity kind.
	KindCube uint8 = 1
)

// Spin is the cube's one game component: how fast it tumbles, in radians per
// second about each axis. It is the only thing the game replicates — position,
// velocity, collider extents and ORIENTATION all come from the 3D engine
// binding set, which is the point of the example.
type Spin struct {
	X, Y, Z float32 `net:"qvel"`
}

// CubeBundle is the entity kind's bundle. It carries no core component:
// Position, Velocity, Rotation and Collider are framework-owned and
// RegisterKind rejects them as bundle fields.
type CubeBundle struct {
	Spin *Spin
}

// NewProcess builds the cube3d process.
//
// Shared by main and by the acceptance test on purpose. A test that
// constructed its own process would drift from the binary, and the one thing
// this example exists to assert is that a REAL 3D process survives a split.
//
// Built through mmokit.New rather than universe.New, which is load-bearing:
// the facade installs a Protocol unconditionally, and without one the
// process's schema fingerprint is 0 — which the mesh admission treats as
// "no protocol" and which would silently opt this example out of the
// dimension-agreement gate that phase 2 unit 5 added.
// headless is what separates the acceptance test from the binary: the test
// wants no listeners at all, while `go run ./examples/cube3d` has to serve a
// browser. One constructor either way, so the two cannot drift in anything
// else — which is the property the split test depends on.
func NewProcess(headless bool) *mmokit.Process {
	cfg := mmokit.Config{
		Name:      "cube3d",
		Dimension: mmokit.Dimension3D,
		Gravity:   Gravity,
		CellsX:    CellsX,
		CellsY:    CellsY,
		CellSize:  CellSize,
		TickRate:  TickRate,
		AoIRadius: AoIRadius,

		AnonymousAuth: true,
	}
	if headless {
		cfg.Headless = true
		cfg.HTTPPort = -1
	}
	process := mmokit.New(cfg)

	mmokit.RegisterKind[CubeBundle](process, KindCube, "Cube")
	registerViewer(process)

	// System order is semantic and Network stays last, matching the rule
	// examples/space/internal/game/factory.go documents.
	//
	// Spatial and Network were BOTH missing until phase 3 unit 6: cube3d
	// simulated 3D correctly and replicated nothing at all, so the 3D engine
	// binding set was schema-pinned but had never produced a byte on the
	// wire. The split acceptance test did not catch it, because a cell
	// transfer travels through TransferFrame rather than through replication.
	process.AddSystem(mmokit.NewPhysicsSystem())
	process.AddSystem(mmokit.NewSystem(&TumbleSystem{}))
	process.AddSystem(mmokit.NewSpatialSystem())
	process.AddSystem(mmokit.NewNetworkSystem())

	process.OnStageInit(func(stage *mmokit.Stage) {
		// A split-created stage receives its entities by transfer. Spawning
		// here too would duplicate every cube the parent handed over, which
		// is exactly the bug FromSplit exists to prevent — and it would make
		// the acceptance test's entity count meaningless.
		if stage.FromSplit() {
			return
		}
		bootstrapCubes(stage)
	})

	return process
}
