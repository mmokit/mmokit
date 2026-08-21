package component

import (
	"math"
	"testing"
)

func angleOf(t *testing.T, r Rotation, wantAxisX, wantAxisY, wantAxisZ float64) float64 {
	t.Helper()
	n := math.Sqrt(float64(r.X*r.X + r.Y*r.Y + r.Z*r.Z))
	return 2 * math.Atan2(n, math.Abs(float64(r.W)))
}

// TestRotationFromAxisAngle covers the constructor a 3D game needs and the 2D
// profile never did: without it no game can express pitch or roll, so the 3D
// orientation wire path is unreachable from game code.
func TestRotationFromAxisAngle(t *testing.T) {
	// A yaw built from the Z axis must equal the yaw helper exactly enough to
	// be interchangeable — the two are different routes to the same rotation.
	for _, yaw := range []float32{0, 0.75, 1.5, 3.0, -2.25} {
		a := RotationFromYaw(yaw)
		b := RotationFromAxisAngle(0, 0, 1, yaw)
		if math.Abs(float64(a.Yaw()-b.Yaw())) > 1e-6 {
			t.Errorf("yaw %v: axis-angle gives %v, RotationFromYaw gives %v", yaw, b.Yaw(), a.Yaw())
		}
	}

	// A non-unit axis must behave like its normalized form.
	long := RotationFromAxisAngle(0, 0, 7, 1.0)
	unit := RotationFromAxisAngle(0, 0, 1, 1.0)
	if math.Abs(float64(long.Z-unit.Z)) > 1e-6 || math.Abs(float64(long.W-unit.W)) > 1e-6 {
		t.Errorf("non-unit axis %v != unit axis %v", long, unit)
	}

	// A zero axis is identity rather than NaN.
	if got := RotationFromAxisAngle(0, 0, 0, 1.0); got != (Rotation{W: 1}) {
		t.Errorf("zero axis = %v, want identity", got)
	}

	if got := angleOf(t, RotationFromAxisAngle(1, 0, 0, 1.0), 1, 0, 0); math.Abs(got-1.0) > 1e-6 {
		t.Errorf("angle about X = %v, want 1.0", got)
	}
}

// TestRotationMul covers composition, including the drift that makes
// renormalizing on the way out load-bearing.
func TestRotationMul(t *testing.T) {
	if got := RotationIdentity().Mul(RotationIdentity()); got != (Rotation{W: 1}) {
		t.Errorf("identity * identity = %v, want identity", got)
	}

	// Composing two half-turns about Z is a full turn: yaw returns to ~0.
	half := RotationFromAxisAngle(0, 0, 1, math.Pi)
	full := half.Mul(half)
	if y := math.Abs(float64(full.Yaw())); y > 1e-5 && math.Abs(y-2*math.Pi) > 1e-5 {
		t.Errorf("two half-turns give yaw %v, want 0 or 2pi", full.Yaw())
	}

	// Repeated composition must stay on the unit sphere. This is the case that
	// motivates normalizing in Mul: 20000 float32 compositions without it
	// drift measurably off unit length, and every downstream matrix silently
	// scales.
	r := RotationIdentity()
	step := RotationFromAxisAngle(0.3, 0.7, 1.1, 0.01)
	for i := 0; i < 20000; i++ {
		r = r.Mul(step)
	}
	n := math.Sqrt(float64(r.X*r.X + r.Y*r.Y + r.Z*r.Z + r.W*r.W))
	if math.Abs(n-1) > 1e-5 {
		t.Fatalf("norm after 20000 compositions = %v, want 1", n)
	}
}
