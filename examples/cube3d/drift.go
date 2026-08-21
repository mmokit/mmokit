package main

import (
	"math"

	"github.com/mmokit/mmokit"
)

// World geometry in world coordinates. Entity positions are BASE-cell-local,
// so a system comparing against these has to add its base cell's origin first
// — see DriftSystem.Update.
const (
	WorldSizeX = CellsX * CellSize
	WorldSizeY = CellsY * CellSize

	// DriftMargin is how far inside the world edge a cube turns around.
	//
	// It has to exceed one tick of travel plus the framework's own edge
	// margin. BoundarySystem clamps an entity that leaves the world and
	// ZEROES the offending velocity component, so a cube that reaches the
	// edge does not bounce off it — it stops dead there, permanently, and
	// the drifting half of the field slowly accumulates against the walls.
	DriftMargin = 40

	// DriftSpeed is a drifting cube's horizontal speed, in units per second.
	// At 55 a cube crosses a 1000-unit cell in about eighteen seconds, which
	// is slow enough to watch a handoff happen and fast enough that you do
	// not have to wait for one.
	DriftSpeed = 55
)

// DriftVelocity is the horizontal velocity of cube i.
//
// Directions come from the golden angle rather than a random source: the
// process must spawn an identical world on every host in a distributed run,
// and rand would give each host a different one.
func DriftVelocity(i int) (vx, vy float32) {
	const goldenAngle = 2.399963229728653 // radians, ~137.5°
	theta := goldenAngle * float64(i)
	return float32(math.Cos(theta) * DriftSpeed), float32(math.Sin(theta) * DriftSpeed)
}

// ReflectAxis turns a velocity around at the world edge.
//
// Gated on the sign of v as well as the position, so a cube sitting just
// outside the margin — which happens for one tick after a turn — is not
// flipped back and forth every tick into a standing vibration.
func ReflectAxis(p, v, min, max float32) float32 {
	if p <= min && v < 0 {
		return -v
	}
	if p >= max && v > 0 {
		return -v
	}
	return v
}

// DriftSystem keeps the drifting cubes inside the world.
//
// It is what makes cross-cell handoff visible with a single player watching:
// cubes leave the cell they were bootstrapped into, are transferred to the
// neighbour that owns where they went, and change colour in the browser at the
// moment they do. Nothing else in this example crosses a cell line on its own
// — before this, the only entity that ever did was the viewer.
//
// The query carries Spin, which only cubes have. That is the filter: without
// it the viewer matches too, and reversing a player's velocity at the world
// edge fights their own input twenty times a second.
//
// There is no "is this a drifter" test, because ReflectAxis is already a
// no-op for a zero velocity — the bouncing half simply falls through it.
type DriftSystem struct {
	mmokit.SystemBase
	cubes mmokit.Query[struct {
		Position *mmokit.Position
		Velocity *mmokit.Velocity
		Spin     *Spin
	}]
}

func (s *DriftSystem) Update(dt float32) {
	// Positions are local to the DEPTH-0 ancestor, not to this cell: a cube
	// living in a child cell after a split still carries coordinates in its
	// base cell's frame. Using this cell's own origin would misplace every
	// cube in a split world by up to a cell.
	base := s.Stage().Cell()
	for base.Depth > 0 {
		base = base.Parent()
	}
	originX, originY := base.WorldOrigin(s.Stage().CellSize())

	for _, cube := range s.cubes.Iter {
		worldX := originX + cube.Position.X
		worldY := originY + cube.Position.Y
		cube.Velocity.X = ReflectAxis(worldX, cube.Velocity.X, DriftMargin, WorldSizeX-DriftMargin)
		cube.Velocity.Y = ReflectAxis(worldY, cube.Velocity.Y, DriftMargin, WorldSizeY-DriftMargin)
	}
}
