package main

import (
	"math"
	"testing"
)

// TestBounceLaunch_ReachesTheApex is the reason the launch speed is derived
// from gravity rather than tuned by eye: the two must agree, and a change to
// Gravity that silently left the cube field launching to a different height
// would look like a rendering bug.
func TestBounceLaunch_ReachesTheApex(t *testing.T) {
	for _, apex := range []float32{40, 190, 490} {
		v := BounceLaunch(apex)
		// v² = 2gh, so the apex a launch at v reaches is v²/2g.
		got := float64(v*v) / (2 * math.Abs(Gravity))
		if math.Abs(got-float64(apex)) > 0.01 {
			t.Errorf("BounceLaunch(%v) = %v reaches %.3f, want apex %v", apex, v, got, apex)
		}
	}
}

func TestBounceLaunch_ZeroApex(t *testing.T) {
	if got := BounceLaunch(0); got != 0 {
		t.Errorf("BounceLaunch(0) = %v, want 0", got)
	}
	// Negative is not reachable from bootstrapCubes, but Sqrt of it is NaN,
	// and a NaN velocity poisons the position on the next integration and
	// then every quantized frame that carries it.
	if got := BounceLaunch(-10); got != 0 {
		t.Errorf("BounceLaunch(-10) = %v, want 0", got)
	}
}

// TestReflectZ_PreservesOvershoot is the difference between this and the
// clamp MoveWalk does. Clamping discards the overshoot, so a cube loses a
// little height on every single bounce and the field sinks.
func TestReflectZ_PreservesOvershoot(t *testing.T) {
	if got := ReflectZ(-30, 0); got != 30 {
		t.Errorf("ReflectZ(-30, 0) = %v, want 30", got)
	}
	if got := ReflectZ(70, 100); got != 130 {
		t.Errorf("ReflectZ(70, 100) = %v, want 130", got)
	}
	if got := ReflectZ(0, 0); got != 0 {
		t.Errorf("ReflectZ(0, 0) = %v, want 0", got)
	}
}

func TestBounceSpeed_DecaysThenRelaunches(t *testing.T) {
	const launch = 400

	// A fast impact returns a decayed climb, always upward.
	if got := BounceSpeed(-300, launch); got <= 0 {
		t.Fatalf("BounceSpeed(-300, %v) = %v, want a positive climb", launch, got)
	}
	// Tolerance, not equality: the constant folds exactly at compile time
	// while the function multiplies two float32s at runtime.
	if got, want := BounceSpeed(-300, launch), float32(300*BounceRestitution); math.Abs(float64(got-want)) > 1e-3 {
		t.Errorf("BounceSpeed(-300, %v) = %v, want %v", launch, got, want)
	}

	// A slow one re-launches to full height instead of decaying further.
	if got := BounceSpeed(-10, launch); got != launch {
		t.Errorf("BounceSpeed(-10, %v) = %v, want a re-launch at %v", launch, got, launch)
	}
}

// TestBounceSpeed_Terminates is the property that matters at runtime: however
// many times a cube bounces, it never converges on zero and stops. Without the
// re-launch the sequence is geometric and the whole field is resting at Z=0
// within about fifteen seconds — which is exactly what the all-falling
// arrangement this replaced looked like.
func TestBounceSpeed_Terminates(t *testing.T) {
	const launch = 400
	v := float32(launch)
	relaunches := 0
	for range 200 {
		v = BounceSpeed(-v, launch)
		if v == launch {
			relaunches++
		}
		if v < launch*RelaunchFraction {
			t.Fatalf("bounce decayed to %v, below the re-launch floor %v", v, launch*RelaunchFraction)
		}
	}
	if relaunches == 0 {
		t.Fatal("200 bounces and never re-launched — the cube field would be resting on the ground")
	}
}

// TestBounceSystem_ContractWithPhysics pins the two assumptions BounceSystem
// makes about the framework, both of which are invisible in its own code:
// bouncers must be MoveBallistic (MoveWalk would zero the impact speed before
// this system ran), and the launch speed must be derived from the same apex
// the cube was spawned at.
func TestBounceSystem_ContractWithPhysics(t *testing.T) {
	for i := range CubesPerCell {
		if !IsBouncer(i) {
			continue
		}
		apex := CubeHeight(i)
		if got, want := BounceLaunch(apex), BounceLaunch(CubeHeight(i)); got != want {
			t.Fatalf("cube %d: launch %v disagrees with its spawn apex %v", i, got, apex)
		}
	}
	if !hasBouncer() {
		t.Fatal("no cube bounces — the gravity showcase is inert")
	}
	if !hasDrifter() {
		t.Fatal("no cube drifts — nothing crosses a cell line on its own")
	}
}

func hasBouncer() bool {
	for i := range CubesPerCell {
		if IsBouncer(i) {
			return true
		}
	}
	return false
}

func hasDrifter() bool {
	for i := range CubesPerCell {
		if !IsBouncer(i) {
			return true
		}
	}
	return false
}
