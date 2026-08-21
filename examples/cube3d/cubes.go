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

// bootstrapCubes fills one cell with cubes at varied heights.
//
// Heights are the point. A cube resting at Z=0 would replicate and transfer
// identically in a 2D profile, so the acceptance test could not tell a working
// 3D pipeline from a broken one. Every cube starts airborne, at a height
// derived from its index so the assertion can be exact rather than statistical.
//
// Half of them HOVER and half FALL, alternating by index. All-falling was the
// original arrangement and it collapses to a flat plane of cubes within five
// seconds — correct physics, and a scene with no depth left in it. The hovering
// half keeps a lattice in the air to fly through; the falling half is what
// makes gravity visible.
func bootstrapCubes(stage *mmokit.Stage) {
	for i := 0; i < CubesPerCell; i++ {
		x, y := cubeXY(i)
		motion := mmokit.Motion{Mode: mmokit.MoveWalk, GroundZ: GroundZ}
		if i%2 == 0 {
			motion = mmokit.Motion{Mode: mmokit.MoveFly}
		}
		stage.Spawn(
			mmokit.EntityKind{Type: KindCube},
			mmokit.Position{X: x, Y: y, Z: CubeHeight(i)},
			mmokit.Velocity{},
			mmokit.RotationIdentity(),
			mmokit.Collider{
				Shape:  mmokit.ShapeBox,
				Radius: CubeSize / 2,
				Width:  CubeSize,
				Height: CubeSize,
				Depth:  CubeSize,
			},
			motion,
			Spin{X: 0.3, Y: 0.7, Z: 1.1},
		)
	}
}

// CubeHeight is the starting height of cube i. Exported so the acceptance test
// asserts against the same function the process spawns with, rather than a
// copy that could drift.
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
