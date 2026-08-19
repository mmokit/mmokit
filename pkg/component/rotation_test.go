package component

import (
	"math"
	"testing"
)

// The zero value must be identity. The framework zero-fills components on
// several paths and the game constructs Rotation{} directly, so a zero-norm
// quaternion yielding NaN would surface as entities facing nowhere.
func TestRotationZeroValueIsIdentity(t *testing.T) {
	var r Rotation
	if got := r.Yaw(); got != 0 {
		t.Errorf("zero Rotation Yaw() = %v, want 0", got)
	}
	x, y := r.Forward()
	if x != 1 || y != 0 {
		t.Errorf("zero Rotation Forward() = (%v,%v), want (1,0)", x, y)
	}
}

// The yaw round-trip must be EXACT for the literals the exact-equality
// fixtures use — system_network_prediction_test.go compares != 0.75 and the
// transfer golden compares != 3.0. float64 intermediates are what make that
// hold; a float32 implementation fails both.
func TestRotationYawRoundTripIsExactForFixtureLiterals(t *testing.T) {
	for _, yaw := range []float32{0, 0.25, 0.5, 0.72, 0.75, 1.0, 1.5, 3.0} {
		if got := RotationFromYaw(yaw).Yaw(); got != yaw {
			t.Errorf("RotationFromYaw(%v).Yaw() = %v, want exactly %v", yaw, got, yaw)
		}
	}
}

// Not every yaw round-trips bit-exactly — measured, about 85% do, worst case
// ~1.2e-7. Stated as a bound rather than claimed as exactness, because the
// fixtures above rely on specific literals and nothing should rely on more.
//
// Compared as an ANGULAR distance, not a raw difference: -pi and +pi are the
// same rotation, and the quaternion legitimately returns the other
// representative at that boundary. A raw comparison reports 2*pi of "error"
// there and would be measuring the wrong thing.
func TestRotationYawRoundTripIsAccurateEverywhere(t *testing.T) {
	const worst = 2e-7
	for i := range 2000 {
		yaw := float32((float64(i)/2000)*2*math.Pi - math.Pi)
		got := RotationFromYaw(yaw).Yaw()
		if d := angularDistance(float64(got), float64(yaw)); d > worst {
			t.Fatalf("yaw %v round-tripped to %v (angular error %g > %g)", yaw, got, d, worst)
		}
	}
}

// angularDistance is the shortest signed separation between two angles,
// magnitude in [0, pi].
func angularDistance(a, b float64) float64 {
	d := math.Mod(a-b, 2*math.Pi)
	if d > math.Pi {
		d -= 2 * math.Pi
	}
	if d < -math.Pi {
		d += 2 * math.Pi
	}
	return math.Abs(d)
}

// A hand-written non-unit quaternion must still yield a sane rotation. This is
// reachable in production: the admin console's `entity.modify Rotation W 5`
// walks to the raw field through fieldpath's exported-scalar traversal.
func TestRotationRenormalizesNonUnitInput(t *testing.T) {
	r := Rotation{Z: 5, W: 5} // same direction as yaw=pi/2, wrong magnitude
	got := r.Yaw()
	if math.Abs(float64(got)-math.Pi/2) > 1e-6 {
		t.Errorf("non-unit quaternion Yaw() = %v, want ~pi/2", got)
	}
	if math.IsNaN(float64(got)) {
		t.Error("non-unit quaternion produced NaN")
	}
}

// The reason the quaternion is an improvement rather than a risk: the scalar it
// replaces accumulated unbounded float32 error under repeated turning, while
// this renormalizes. The qangle wire bucket is ~9.6e-5, so the drift must stay
// far inside it.
func TestRotationDoesNotDriftUnderRepeatedTurning(t *testing.T) {
	const (
		ticks = 12000
		step  = 0.01
	)
	var r Rotation
	for range ticks {
		r.AddYaw(step)
	}
	want := math.Mod(float64(ticks)*step, 2*math.Pi)
	if want > math.Pi {
		want -= 2 * math.Pi
	}
	if d := math.Abs(float64(r.Yaw()) - want); d > 1e-3 {
		t.Errorf("after %d turns of %v, yaw = %v, want ~%v (drift %g)", ticks, step, r.Yaw(), want, d)
	}
}

// Yaw wraps, which the unbounded scalar did not. This is the one behavioural
// difference, and it is the fix rather than the risk.
func TestRotationYawWraps(t *testing.T) {
	r := RotationFromYaw(3.0)
	r.AddYaw(1.0) // 4.0 rad is past pi
	if got := r.Yaw(); got > math.Pi || got < -math.Pi {
		t.Errorf("yaw %v escaped [-pi,pi]", got)
	}
	x, y := r.Forward()
	wx, wy := float32(math.Cos(4.0)), float32(math.Sin(4.0))
	if math.Abs(float64(x-wx)) > 1e-6 || math.Abs(float64(y-wy)) > 1e-6 {
		t.Errorf("Forward() = (%v,%v), want (%v,%v) — wrapping changed the direction", x, y, wx, wy)
	}
}
