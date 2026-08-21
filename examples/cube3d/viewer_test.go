package main

import (
	"math"
	"testing"
)

// TestFlyVelocity_MovesAlongTheCameraYaw covers the maths that produced NO
// MOVEMENT AT ALL in the first version of this example.
//
// The bug was not in here — it was an early return in the handler, which
// required the entity to have a Rotation component that Stage.Spawn never
// adds. But the reason it survived to a human bug report is that the maths
// lived inside a closure nothing could call. Extracting it is what makes this
// test possible.
func TestFlyVelocity_MovesAlongTheCameraYaw(t *testing.T) {
	for _, c := range []struct {
		name         string
		in           FlyInput
		wantX, wantY float32
		wantZ        float32
	}{
		{name: "forward at yaw 0 is +X", in: FlyInput{Forward: 1}, wantX: FlySpeed},
		{name: "back at yaw 0 is -X", in: FlyInput{Forward: -1}, wantX: -FlySpeed},
		{name: "forward at yaw 90 is +Y", in: FlyInput{Forward: 1, Yaw: math.Pi / 2}, wantY: FlySpeed},
		// Strafe is perpendicular: right of +X is -Y in a Z-up right-handed frame.
		{name: "strafe right at yaw 0 is -Y", in: FlyInput{Strafe: 1}, wantY: -FlySpeed},
		{name: "strafe left at yaw 0 is +Y", in: FlyInput{Strafe: -1}, wantY: FlySpeed},
		{name: "strafe right at yaw 90 is +X", in: FlyInput{Strafe: 1, Yaw: math.Pi / 2}, wantX: FlySpeed},
		{name: "lift is world-vertical", in: FlyInput{Lift: 1}, wantZ: FlySpeed},
		{name: "no keys is stationary", in: FlyInput{}},
	} {
		got := FlyVelocity(&c.in)
		if math.Abs(float64(got.X-c.wantX)) > 1e-3 ||
			math.Abs(float64(got.Y-c.wantY)) > 1e-3 ||
			math.Abs(float64(got.Z-c.wantZ)) > 1e-3 {
			t.Errorf("%s: got %v,%v,%v want %v,%v,%v",
				c.name, got.X, got.Y, got.Z, c.wantX, c.wantY, c.wantZ)
		}
	}
}

// Lift must stay world-vertical however the camera is pitched, or flying up
// while looking down would drive you into the ground.
func TestFlyVelocity_LiftIsIndependentOfPitch(t *testing.T) {
	for _, pitch := range []float32{-1.5, -0.5, 0, 0.5, 1.5} {
		got := FlyVelocity(&FlyInput{Lift: 1, Pitch: pitch})
		if math.Abs(float64(got.Z-FlySpeed)) > 1e-3 {
			t.Errorf("pitch %v: lift gave Z=%v, want %v", pitch, got.Z, FlySpeed)
		}
		if math.Abs(float64(got.X)) > 1e-3 || math.Abs(float64(got.Y)) > 1e-3 {
			t.Errorf("pitch %v: lift leaked into X/Y (%v, %v)", pitch, got.X, got.Y)
		}
	}
}

// A hostile client can send anything; the framework does not validate game
// input for the game.
func TestFlyVelocity_ClampsHostileAxes(t *testing.T) {
	got := FlyVelocity(&FlyInput{Forward: 1e9, Lift: -1e9})
	if got.X > FlySpeed+1e-3 {
		t.Errorf("forward 1e9 produced X=%v, want at most %v", got.X, FlySpeed)
	}
	if got.Z < -FlySpeed-1e-3 {
		t.Errorf("lift -1e9 produced Z=%v, want at least %v", got.Z, -FlySpeed)
	}
	if nan := FlyVelocity(&FlyInput{Forward: float32(math.NaN())}); nan.X != 0 {
		t.Errorf("NaN forward produced X=%v, want 0", nan.X)
	}
}

// TestFlyRotation_YawThenPitch pins the orientation the viewer replicates.
func TestFlyRotation_YawThenPitch(t *testing.T) {
	// Compared by behaviour, not by struct equality: RotationIdentity() is
	// the ZERO value {0,0,0,0}, which normalizes to {0,0,0,1}. Both are
	// identity; only one is what a constructor returns.
	if got := FlyRotation(&FlyInput{}); math.Abs(float64(got.Yaw())) > 1e-6 {
		t.Errorf("zero input gave yaw %v, want 0 (%+v)", got.Yaw(), got)
	}
	quarter := FlyRotation(&FlyInput{Yaw: math.Pi / 2})
	if math.Abs(float64(quarter.Yaw()-math.Pi/2)) > 1e-5 {
		t.Errorf("yaw 90 gave %v", quarter.Yaw())
	}
	// Pitch must actually leave the yaw-only plane, or the 3D orientation
	// path is carrying nothing a 2D one could not.
	pitched := FlyRotation(&FlyInput{Pitch: 0.5})
	if math.Abs(float64(pitched.X)) < 1e-3 {
		t.Errorf("pitch 0.5 produced no X component: %+v", pitched)
	}
}
