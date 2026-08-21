package component

import (
	"math"
	"math/rand/v2"
	"testing"
)

func slerpAngle(a, b Rotation) float64 {
	na, nb := a.normalized(), b.normalized()
	dot := float64(na.X*nb.X + na.Y*nb.Y + na.Z*nb.Z + na.W*nb.W)
	// Relative quaternion form: well-conditioned near zero, unlike acos(dot).
	// See pkg/quantize/quat_test.go for why that matters at these magnitudes.
	if dot < 0 {
		nb = Rotation{-nb.X, -nb.Y, -nb.Z, -nb.W}
	}
	rw := float64(nb.W*na.W + nb.X*na.X + nb.Y*na.Y + nb.Z*na.Z)
	rx := float64(-nb.W*na.X + nb.X*na.W - nb.Y*na.Z + nb.Z*na.Y)
	ry := float64(-nb.W*na.Y + nb.X*na.Z + nb.Y*na.W - nb.Z*na.X)
	rz := float64(-nb.W*na.Z - nb.X*na.Y + nb.Y*na.X + nb.Z*na.W)
	return 2 * math.Atan2(math.Sqrt(rx*rx+ry*ry+rz*rz), math.Abs(rw))
}

func TestRotation_Slerp_EndpointsExact(t *testing.T) {
	a := RotationFromAxisAngle(1, 0, 0, 0.5)
	b := RotationFromAxisAngle(0, 1, 0, 2.0)
	if got := a.Slerp(b, 0); slerpAngle(got, a) > 1e-6 {
		t.Errorf("t=0 gave %+v, want %+v", got, a)
	}
	if got := a.Slerp(b, 1); slerpAngle(got, b) > 1e-6 {
		t.Errorf("t=1 gave %+v, want %+v", got, b)
	}
}

func TestRotation_Slerp_ClampsT(t *testing.T) {
	a := RotationFromAxisAngle(1, 0, 0, 0.5)
	b := RotationFromAxisAngle(0, 1, 0, 2.0)
	if got := a.Slerp(b, -0.5); slerpAngle(got, a) > 1e-6 {
		t.Errorf("t=-0.5 must clamp to the start, got %+v", got)
	}
	if got := a.Slerp(b, 1.5); slerpAngle(got, b) > 1e-6 {
		t.Errorf("t=1.5 must clamp to the end, got %+v", got)
	}
}

func TestRotation_Slerp_Midpoint90AboutZ(t *testing.T) {
	a := RotationIdentity()
	b := RotationFromAxisAngle(0, 0, 1, math.Pi/2)
	mid := a.Slerp(b, 0.5)
	want := RotationFromAxisAngle(0, 0, 1, math.Pi/4)
	if got := slerpAngle(mid, want); got > 1e-6 {
		t.Fatalf("midpoint is %v rad from 45-about-Z, want < 1e-6 (mid=%+v)", got, mid)
	}
}

// TestRotation_Slerp_ShortestArcAcrossHemispheres — q and -q are the same
// rotation, so interpolating toward a sign-flipped target must still take the
// short way. Without the dot<0 negation this sweeps nearly a full turn.
func TestRotation_Slerp_ShortestArcAcrossHemispheres(t *testing.T) {
	a := RotationFromAxisAngle(0, 0, 1, 0.2)
	b := RotationFromAxisAngle(0, 0, 1, 0.6)
	flipped := Rotation{-b.X, -b.Y, -b.Z, -b.W}

	direct := slerpAngle(a, b)
	swept := 0.0
	prev := a
	for i := 1; i <= 20; i++ {
		cur := a.Slerp(flipped, float32(i)/20)
		swept += slerpAngle(prev, cur)
		prev = cur
	}
	if swept > direct+1e-3 {
		t.Fatalf("swept %v rad toward a sign-flipped target, want the short arc %v", swept, direct)
	}
}

// TestRotation_Slerp_ThresholdSeamIsContinuous covers the branch that is the
// most likely place for an independent port to diverge: either side of
// SlerpDotThreshold must produce indistinguishable output.
func TestRotation_Slerp_ThresholdSeamIsContinuous(t *testing.T) {
	a := RotationIdentity()
	for _, dot := range []float64{0.99949, SlerpDotThreshold, 0.99951} {
		theta := 2 * math.Acos(dot)
		b := RotationFromAxisAngle(0, 0, 1, float32(theta))
		mid := a.Slerp(b, 0.5)
		want := RotationFromAxisAngle(0, 0, 1, float32(theta/2))
		if got := slerpAngle(mid, want); got > 1e-6 {
			t.Errorf("dot %v: midpoint is %v rad off the true half-angle", dot, got)
		}
	}
}

func TestRotation_Slerp_AlwaysUnitLength(t *testing.T) {
	rng := rand.New(rand.NewPCG(11, 22))
	for i := 0; i < 10000; i++ {
		a := randomRotation(rng)
		b := randomRotation(rng)
		for _, tt := range []float32{0, 0.25, 0.5, 0.75, 1} {
			got := a.Slerp(b, tt)
			n := math.Sqrt(float64(got.X*got.X + got.Y*got.Y + got.Z*got.Z + got.W*got.W))
			if math.Abs(n-1) > 1e-6 {
				t.Fatalf("norm %v at t=%v (a=%+v b=%+v)", n, tt, a, b)
			}
		}
	}
}

func TestRotation_Slerp_ZeroNormIsIdentity(t *testing.T) {
	var zero Rotation
	b := RotationFromAxisAngle(0, 0, 1, 1.0)
	if got := zero.Slerp(b, 0); slerpAngle(got, RotationIdentity()) > 1e-6 {
		t.Errorf("zero-norm start at t=0 gave %+v, want identity", got)
	}
}

func randomRotation(rng *rand.Rand) Rotation {
	u1, u2, u3 := rng.Float64(), rng.Float64(), rng.Float64()
	s1, s2 := math.Sqrt(1-u1), math.Sqrt(u1)
	return Rotation{
		X: float32(s1 * math.Sin(2*math.Pi*u2)),
		Y: float32(s1 * math.Cos(2*math.Pi*u2)),
		Z: float32(s2 * math.Sin(2*math.Pi*u3)),
		W: float32(s2 * math.Cos(2*math.Pi*u3)),
	}
}
