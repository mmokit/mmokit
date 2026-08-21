package main

import (
	"github.com/mmokit/mmokit"
)

// TumbleSystem rotates every cube about all three axes each tick.
//
// It exists so orientation is not static: a quaternion that never changes
// would encode identically every tick, and the delta encoder would never send
// the rot field at all — so the 3D orientation path would look healthy while
// being completely untested.
type TumbleSystem struct {
	mmokit.SystemBase
	cubes mmokit.Query[struct {
		Rotation *mmokit.Rotation
		Spin     *Spin
	}]
}

func (s *TumbleSystem) Update(dt float32) {
	for _, cube := range s.cubes.Iter {
		*cube.Rotation = cube.Rotation.
			RotateAxis(1, 0, 0, cube.Spin.X*dt).
			RotateAxis(0, 1, 0, cube.Spin.Y*dt).
			RotateAxis(0, 0, 1, cube.Spin.Z*dt)
	}
}
