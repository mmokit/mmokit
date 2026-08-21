package main

import (
	"math"

	"github.com/mmokit/mmokit"
)

// bootstrapCubes fills one cell with cubes at varied heights.
//
// Heights are the point. A cube resting at Z=0 would replicate and transfer
// identically in a 2D profile, so the acceptance test could not tell a working
// 3D pipeline from a broken one. Every cube starts airborne, at a height
// derived from its index so the assertion can be exact rather than statistical.
func bootstrapCubes(stage *mmokit.Stage) {
	for i := 0; i < CubesPerCell; i++ {
		x, y := cubeXY(i)
		stage.Spawn(
			mmokit.EntityKind{Type: KindCube},
			mmokit.Position{X: x, Y: y, Z: CubeHeight(i)},
			mmokit.Velocity{},
			mmokit.RotationIdentity(),
			mmokit.Collider{
				Shape:  mmokit.ShapeBox,
				Radius: 10,
				Width:  20,
				Height: 20,
				Depth:  20,
			},
			mmokit.Motion{Mode: mmokit.MoveWalk, GroundZ: GroundZ},
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
	return float32(1 + i*7)
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
