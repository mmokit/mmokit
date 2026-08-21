package main

import (
	"math"

	"github.com/mmokit/mmokit"
)

// CubeSize is a cube's edge length in world units.
//
// Large relative to the 1000-unit cell on purpose: at 20 units a cube a
// thousand units away subtends under a degree and reads as a dot.
const CubeSize = 40

// bootstrapCubes fills one cell with cubes in two roles.
//
// Each role exists to make one framework behaviour visible, and neither can
// show the other's:
//
//   - BOUNCERS are MoveBallistic and stationary horizontally. They fall, hit
//     the ground plane, bounce, and re-launch forever. Gravity is a permanent
//     feature of the scene rather than something that happened during the
//     first five seconds.
//   - DRIFTERS are MoveFly at a fixed height and move horizontally. They walk
//     out of the cell they were bootstrapped into and are handed off to the
//     neighbour that owns where they went, which is the one framework
//     behaviour a single player could not previously see happen.
//
// Heights are the point for both. A cube resting at Z=0 would replicate and
// transfer identically in a 2D profile, so the acceptance test could not tell
// a working 3D pipeline from a broken one. The drifters hold their height
// exactly, which is what lets that test assert on Z rather than merely count
// entities.
func bootstrapCubes(stage *mmokit.Stage) {
	for i := range CubesPerCell {
		x, y := cubeXY(i)
		if IsBouncer(i) {
			spawnBouncer(stage, i, x, y)
			continue
		}
		spawnDrifter(stage, i, x, y)
	}
}

// IsBouncer reports whether cube i falls or floats. Exported so the acceptance
// test partitions the field the same way the process spawns it.
func IsBouncer(i int) bool { return i%2 == 1 }

// spawnBouncer creates a cube that falls, bounces and re-launches forever.
func spawnBouncer(stage *mmokit.Stage, i int, x, y float32) {
	apex := CubeHeight(i)
	stage.Spawn(
		mmokit.EntityKind{Type: KindCube},
		mmokit.Position{X: x, Y: y, Z: apex},
		mmokit.Velocity{},
		mmokit.RotationIdentity(),
		cubeCollider(),
		// Ballistic, not walking: MoveWalk clamps to the ground and zeroes
		// the downward velocity, which destroys the impact speed a bounce
		// is computed from. See BounceSystem.
		mmokit.Motion{Mode: mmokit.MoveBallistic, GroundZ: GroundZ},
		Bounce{Launch: BounceLaunch(apex)},
		Spin{X: 0.3, Y: 0.7, Z: 1.1},
	)
}

// spawnDrifter creates a cube that holds its height and roams the world.
func spawnDrifter(stage *mmokit.Stage, i int, x, y float32) {
	vx, vy := DriftVelocity(i)
	stage.Spawn(
		mmokit.EntityKind{Type: KindCube},
		mmokit.Position{X: x, Y: y, Z: CubeHeight(i)},
		mmokit.Velocity{X: vx, Y: vy},
		mmokit.RotationIdentity(),
		cubeCollider(),
		// MoveFly so gravity does not act on it: this cube's job is to hold
		// one height while it crosses cell lines, so that a Z arriving wrong
		// on the far side of a handoff is unambiguous.
		mmokit.Motion{Mode: mmokit.MoveFly},
		Spin{X: 0.9, Y: 0.2, Z: 0.4},
	)
}

func cubeCollider() mmokit.Collider {
	return mmokit.Collider{
		Shape:  mmokit.ShapeBox,
		Radius: CubeSize / 2,
		Width:  CubeSize,
		Height: CubeSize,
		Depth:  CubeSize,
	}
}

// CubeHeight is the starting height of cube i — a bouncer's apex, a drifter's
// cruising altitude. Exported so the acceptance test asserts against the same
// function the process spawns with, rather than a copy that could drift.
//
// The +1 floor matters: a cube spawned exactly at GroundZ is indistinguishable
// from one whose Z was dropped in transit, which is the failure this example
// exists to detect.
func CubeHeight(i int) float32 {
	return float32(40 + i*30)
}

// cubeXY spreads cubes over the cell so a split distributes them across all
// four children rather than piling them into one quadrant.
func cubeXY(i int) (float32, float32) {
	// A coarse lattice inside the cell, inset so nothing lands on a boundary.
	const inset = 100
	span := float32(CellSize - 2*inset)
	side := int(math.Ceil(math.Sqrt(float64(CubesPerCell))))
	col := i % side
	row := i / side
	step := span / float32(side-1)
	return inset + float32(col)*step, inset + float32(row)*step
}
