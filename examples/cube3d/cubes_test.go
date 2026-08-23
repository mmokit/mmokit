package main

import (
	"math"
	"testing"
)

// TestCubeCollider_RadiusBoundsTheCube pins the contract
// component.Collider.Radius states: the bounding radius must contain the shape
// in every axis the profile uses.
//
// This example shipped with CubeSize/2, which is the radius of the sphere
// INSIDE the cube rather than around it. Nothing could see it while the broad
// phase was a column test. Once that gate gained a Z term, an under-sized
// radius rejects pairs that genuinely overlap — before any contact test runs,
// so there is nothing downstream to notice.
func TestCubeCollider_RadiusBoundsTheCube(t *testing.T) {
	c := cubeCollider()
	want := math.Sqrt(float64(c.Width*c.Width+c.Height*c.Height+c.Depth*c.Depth)) / 2
	if float64(c.Radius) < want-1e-4 {
		t.Errorf("Radius = %v, want at least %v (half the box diagonal) — "+
			"the broad phase would reject overlapping cubes", c.Radius, want)
	}
	// And not wastefully large: a radius far beyond the diagonal makes the
	// broad phase admit pairs it should reject, which costs narrow-phase work.
	if float64(c.Radius) > want*1.05 {
		t.Errorf("Radius = %v is more than 5%% beyond the box diagonal %v", c.Radius, want)
	}
}
