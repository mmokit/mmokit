package main

import (
	"math"

	"github.com/mmokit/mmokit"
)

// Bounce is a bouncing cube's simulation state.
//
// Launch carries NO net: tag, deliberately. It is server-side state that no
// client renders — a client sees the trajectory the bounce produces, not the
// rule that produced it. An untagged field on a registered kind component
// still crosses a cell boundary (that is exactly what separates it from
// `mmokit:"local"`), so a cube handed to a neighbouring cell mid-flight keeps
// its own apex rather than resetting to a default one. The wire cost is zero:
// adding this leaves cube3d's schema fingerprint untouched.
type Bounce struct {
	// Launch is the upward speed a re-launch uses, in units per second.
	Launch float32
}

// Bounce tuning.
const (
	// BounceRestitution is the fraction of impact speed returned as climb.
	// Below ~0.6 the cube settles before you can watch a second bounce;
	// above ~0.85 it barely decays and the re-launch never reads as a
	// deliberate event.
	BounceRestitution = 0.72

	// RelaunchFraction is the share of Launch below which a bounce is
	// re-launched to full height instead of decaying further. Without it
	// every cube converges to a jitter at Z=0 within about fifteen seconds
	// — correct physics, and a scene where gravity has stopped being
	// visible, which is the failure mode the original all-falling
	// arrangement had.
	RelaunchFraction = 0.35
)

// BounceLaunch is the launch speed that lifts a cube to apex, under this
// process's gravity. Derived rather than tuned, so a change to Gravity keeps
// the cube field at the heights bootstrapCubes chose.
func BounceLaunch(apex float32) float32 {
	if apex <= 0 {
		return 0
	}
	return float32(math.Sqrt(2 * math.Abs(Gravity) * float64(apex)))
}

// ReflectZ mirrors a below-ground height back above the plane, preserving the
// distance the entity overshot by.
//
// Clamping to the ground instead — which is what MoveWalk does — loses that
// overshoot, and at 20 Hz a cube arriving at 600 u/s overshoots by 30 units.
// Repeated every bounce that is a visible downward drift in a cube that should
// return to the same apex forever.
func ReflectZ(z, ground float32) float32 {
	return ground + (ground - z)
}

// BounceSpeed is the upward speed after an impact at vz, which is negative.
//
// Pure, and tested as such: the arithmetic is three lines and every one of
// them is a sign error waiting to happen, in a system whose only other
// observer is a browser.
func BounceSpeed(vz, launch float32) float32 {
	up := -vz * BounceRestitution
	if up < launch*RelaunchFraction {
		return launch
	}
	return up
}

// BounceSystem gives gravity something to keep doing.
//
// Bouncing cubes are MoveBallistic — gravity applies and physics does NOT
// clamp them to the ground plane, which is what leaves the impact for this
// system to handle. MoveWalk would zero the downward velocity before this
// system ever saw it, and the impact speed is the one number a bounce needs.
//
// It must run AFTER PhysicsSystem: it corrects a position that integration has
// already pushed below the plane.
type BounceSystem struct {
	mmokit.SystemBase
	cubes mmokit.Query[struct {
		Position *mmokit.Position
		Velocity *mmokit.Velocity
		Motion   *mmokit.Motion
		Bounce   *Bounce
	}]
}

func (s *BounceSystem) Update(dt float32) {
	for _, cube := range s.cubes.Iter {
		// A zero Launch is a cube that does not bounce. Every cube carries
		// the component — a kind's component set is uniform after a transfer
		// — so this, not the component's presence, is the test.
		if cube.Bounce.Launch <= 0 {
			continue
		}
		// Rising, or still above the plane: nothing to do. The velocity
		// test is not redundant with the height test — without it the tick
		// immediately after a re-launch, when the cube is still at ground
		// level but climbing, would re-launch it a second time.
		if cube.Position.Z > cube.Motion.GroundZ || cube.Velocity.Z > 0 {
			continue
		}
		cube.Position.Z = ReflectZ(cube.Position.Z, cube.Motion.GroundZ)
		cube.Velocity.Z = BounceSpeed(cube.Velocity.Z, cube.Bounce.Launch)
	}
}
