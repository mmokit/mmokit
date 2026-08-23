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
	CellsX   = 2
	CellsY   = 2
	CellSize = 1000
	TickRate = 20
	// Wide enough to see the whole 2000x2000 world from the middle of it.
	// At 500 the viewer's OWN cell corners are 566 units away and fall
	// outside AoI, so most of the scene simply never arrives.
	//
	// Area of interest is a SPHERE since roadmap §7.5 phase 4a, so height
	// eats into horizontal reach: from the world centre at spawn height, the
	// far corner of the cube field is 1377 units away rather than 1358, and
	// flying to the top of the field puts it at 1443. Still inside 1500, but
	// the margin is now the thing that shrinks when you climb — which is the
	// behaviour this example exists to make visible.
	AoIRadius = 1500

	// Gravity, in world units per second squared.
	//
	// Earth-ish AT THIS WORLD'S SCALE, which is the part that matters and
	// which -9.81 got wrong. A cube is 40 units across and reads as roughly
	// a metre of object, so a unit is about 2.5 cm and Earth is ~400 u/s².
	// At 9.81 a cube dropped from 490 units takes ten seconds to land: the
	// physics is right, and what you see is a scene drifting downward rather
	// than anything falling. Non-zero is what makes this a 3D process — Build
	// refuses gravity in a 2D profile — but only a scale-correct value makes
	// gravity legible.
	Gravity = -400

	// GroundZ is the plane MoveWalk clamps to.
	GroundZ = 0

	// CubesPerCell is how many cubes each cell bootstraps, split evenly
	// between the bouncing and drifting roles — see bootstrapCubes.
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
//
// Bounce is on EVERY cube, including the drifting half that never bounces,
// and a zero Launch is what "does not bounce" means. That is not a style
// choice: a kind's component set is uniform after a transfer. The destination
// calls Stage.EnsureEntityKindComponents, which adds a zero value for every
// component the kind declares — so declaring Bounce `mmokit:"optional"` and
// omitting it at spawn does NOT keep a drifter without one. It keeps it
// without one until the first time it crosses a cell line, and then eight of
// them silently acquire a Bounce nobody spawned. Making the field's zero mean
// something is the version where both states are reachable on purpose.
//
// It is a registered kind component rather than local state because a bouncing
// cube must keep its own apex across a boundary. It has no net: tag, so it
// costs nothing on the wire — this bundle's client-visible layout is Spin's
// three fields, the same as before it was added.
type CubeBundle struct {
	Spin   *Spin
	Bounce *Bounce
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
	// Both run AFTER physics and both correct what integration just did:
	// Bounce reflects a cube that has been pushed below the ground plane,
	// Drift turns one around before it would leave the world. Ordering them
	// before physics would act on a position a tick out of date, which at
	// 600 u/s is 30 units of overshoot per bounce.
	process.AddSystem(mmokit.NewSystem(&BounceSystem{}))
	process.AddSystem(mmokit.NewSystem(&DriftSystem{}))
	process.AddSystem(mmokit.NewSpatialSystem())
	// Broadcasts the cell topology to viewers holding the "topology" debug
	// grant. The client draws its grid from that rather than guessing, and
	// colours entities by which cell they are in. Adding it changes no
	// schema: DebugInfo and CellChange are engine-default server events and
	// were already in cube3d's protocol — the broadcaster only makes them
	// actually get sent.
	process.AddSystem(mmokit.NewDebugBroadcaster())
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
