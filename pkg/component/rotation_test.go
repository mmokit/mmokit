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

// TestBasis_IsOrthonormal is the property a separating-axis test depends on.
// Non-orthonormal columns scale projected half-extents, and the symptom is a
// razor-edge separation that flips — two boxes that overlap reporting
// separated, i.e. an object passing through a wall.
func TestBasis_IsOrthonormal(t *testing.T) {
	rots := []Rotation{
		RotationIdentity(),
		RotationFromYaw(0.75),
		RotationFromAxisAngle(1, 0, 0, 1.1),
		RotationFromAxisAngle(0.3, -0.7, 0.2, 2.4),
		RotationFromAxisAngle(1, 1, 1, math.Pi),
	}
	for i, r := range rots {
		fwd, side, up := r.Basis()
		for name, v := range map[string]Vec3{"fwd": fwd, "side": side, "up": up} {
			n := math.Sqrt(float64(v.X*v.X + v.Y*v.Y + v.Z*v.Z))
			if math.Abs(n-1) > 1e-5 {
				t.Errorf("rot %d: |%s| = %v, want 1", i, name, n)
			}
		}
		for name, d := range map[string]float64{
			"fwd·side": dot3(fwd, side), "fwd·up": dot3(fwd, up), "side·up": dot3(side, up),
		} {
			if math.Abs(d) > 1e-5 {
				t.Errorf("rot %d: %s = %v, want 0", i, name, d)
			}
		}
	}
}

// A non-unit quaternion is reachable through the admin console's field walk
// (`entity modify Rotation W 5`). Basis must renormalize it, or the scale error
// propagates straight into projected half-extents.
func TestBasis_RenormalizesANonUnitQuaternion(t *testing.T) {
	drifted := Rotation{X: 0, Y: 0, Z: 0, W: 5}
	fwd, _, _ := drifted.Basis()
	n := math.Sqrt(float64(fwd.X*fwd.X + fwd.Y*fwd.Y + fwd.Z*fwd.Z))
	if math.Abs(n-1) > 1e-6 {
		t.Errorf("|fwd| = %v for a W=5 quaternion, want 1 — the scale would reach the SAT", n)
	}
}

// Basis's forward column must agree with Yaw for a pure-yaw rotation, or the
// 2D profile and the 3D one disagree about where an entity faces.
func TestBasis_AgreesWithYawInThePlane(t *testing.T) {
	for _, yaw := range []float32{0, 0.5, 2, -1.25, 3} {
		r := RotationFromYaw(yaw)
		fwd, _, _ := r.Basis()
		wantX := float32(math.Cos(float64(yaw)))
		wantY := float32(math.Sin(float64(yaw)))
		if math.Abs(float64(fwd.X-wantX)) > 1e-6 || math.Abs(float64(fwd.Y-wantY)) > 1e-6 {
			t.Errorf("yaw %v: basis fwd = (%v, %v), want (%v, %v)", yaw, fwd.X, fwd.Y, wantX, wantY)
		}
		if math.Abs(float64(fwd.Z)) > 1e-6 {
			t.Errorf("yaw %v: basis fwd has Z = %v, want 0 in the plane", yaw, fwd.Z)
		}
	}
}

// Rotate and Inverse must round-trip, which is what a world→local transform
// and back depends on.
func TestRotate_InverseRoundTrips(t *testing.T) {
	r := RotationFromAxisAngle(0.3, -0.7, 0.2, 2.4)
	v := Vec3{X: 3, Y: -4, Z: 5}
	back := r.Inverse().Rotate(r.Rotate(v))
	for name, d := range map[string]float32{"X": back.X - v.X, "Y": back.Y - v.Y, "Z": back.Z - v.Z} {
		if math.Abs(float64(d)) > 1e-4 {
			t.Errorf("round trip %s off by %v", name, d)
		}
	}
}

// Rotate must agree with Basis: rotating a unit axis is that basis column.
func TestRotate_AgreesWithBasis(t *testing.T) {
	r := RotationFromAxisAngle(0.3, -0.7, 0.2, 2.4)
	fwd, side, up := r.Basis()
	for _, c := range []struct {
		axis Vec3
		want Vec3
		name string
	}{
		{Vec3{X: 1}, fwd, "fwd"}, {Vec3{Y: 1}, side, "side"}, {Vec3{Z: 1}, up, "up"},
	} {
		got := r.Rotate(c.axis)
		if math.Abs(float64(got.X-c.want.X)) > 1e-5 ||
			math.Abs(float64(got.Y-c.want.Y)) > 1e-5 ||
			math.Abs(float64(got.Z-c.want.Z)) > 1e-5 {
			t.Errorf("Rotate(%s axis) = %+v, want basis %s %+v", c.name, got, c.name, c.want)
		}
	}
}

func dot3(a, b Vec3) float64 {
	return float64(a.X)*float64(b.X) + float64(a.Y)*float64(b.Y) + float64(a.Z)*float64(b.Z)
}
