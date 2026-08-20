package quantize

import (
	"math"
	"math/rand/v2"
	"testing"
)

// angleBetween returns the rotation angle between two quaternions.
//
// It deliberately does NOT use 2*acos(dot). That form is catastrophically
// ill-conditioned here: at the errors this encoding produces, dot is
// 1 - 2.5e-10, and acos amplifies any perturbation by 1/sin(theta) — about
// 44000x. The float32 inputs are not exactly unit-norm, so a 1e-7 norm error
// alone reports an 8e-4 rad angle for a 4.5e-5 rad rotation. Measured, not
// theorised: the acos form claimed this codec was 10x worse than it is.
//
// The relative quaternion r = b * conj(a) with theta = 2*atan2(|r_vec|, |r_w|)
// is well-conditioned near zero, which is exactly where every measurement here
// lives.
func angleBetween(ax, ay, az, aw, bx, by, bz, bw float32) float64 {
	na := math.Sqrt(float64(ax)*float64(ax) + float64(ay)*float64(ay) + float64(az)*float64(az) + float64(aw)*float64(aw))
	nb := math.Sqrt(float64(bx)*float64(bx) + float64(by)*float64(by) + float64(bz)*float64(bz) + float64(bw)*float64(bw))
	if na == 0 || nb == 0 {
		return 0
	}
	a0, a1, a2, a3 := float64(ax)/na, float64(ay)/na, float64(az)/na, float64(aw)/na
	b0, b1, b2, b3 := float64(bx)/nb, float64(by)/nb, float64(bz)/nb, float64(bw)/nb

	// r = b * conj(a)
	rw := b3*a3 + b0*a0 + b1*a1 + b2*a2
	rx := -b3*a0 + b0*a3 - b1*a2 + b2*a1
	ry := -b3*a1 + b0*a2 + b1*a3 - b2*a0
	rz := -b3*a2 - b0*a1 + b1*a0 + b2*a3

	return 2 * math.Atan2(math.Sqrt(rx*rx+ry*ry+rz*rz), math.Abs(rw))
}

func randomUnitQuat(rng *rand.Rand) (float32, float32, float32, float32) {
	// Shoemake's uniform random rotation.
	u1, u2, u3 := rng.Float64(), rng.Float64(), rng.Float64()
	s1, s2 := math.Sqrt(1-u1), math.Sqrt(u1)
	return float32(s1 * math.Sin(2*math.Pi*u2)),
		float32(s1 * math.Cos(2*math.Pi*u2)),
		float32(s2 * math.Sin(2*math.Pi*u3)),
		float32(s2 * math.Cos(2*math.Pi*u3))
}

// TestQuat_PrecisionBeatsQAngle is the assertion that justifies 6 bytes rather
// than the common 4-byte tuning. The 3D profile's orientation must not be
// coarser than the 2D yaw path it replaces, and QAngle's bucket is 2*pi/65536
// = 9.6e-5 rad. It must also stay under phase 1's measured simulation drift
// (2.4e-4 rad over 12000 ticks) so the encoder is not the dominant error term.
func TestQuat_PrecisionBeatsQAngle(t *testing.T) {
	const qangleBucket = 2 * math.Pi / 65536

	rng := rand.New(rand.NewPCG(1, 2))
	worst := 0.0
	for i := 0; i < 200000; i++ {
		x, y, z, w := randomUnitQuat(rng)
		dx, dy, dz, dw := UnQuat(Quat(x, y, z, w))
		if a := angleBetween(x, y, z, w, dx, dy, dz, dw); a > worst {
			worst = a
		}
	}
	t.Logf("worst-case angular error over 200000 uniform rotations: %.3e rad (qangle bucket %.3e)", worst, qangleBucket)
	if worst >= qangleBucket {
		t.Errorf("worst angular error %v >= qangle bucket %v — the 3D profile would be coarser than the 2D yaw it replaces", worst, qangleBucket)
	}
	if worst >= 2.4e-4 {
		t.Errorf("worst angular error %v exceeds phase 1's measured drift budget 2.4e-4", worst)
	}
	// Guard the other direction too: if a future retune makes this
	// dramatically finer, the extra bytes are not buying anything the
	// simulation can express.
	if worst < 1e-6 {
		t.Errorf("worst angular error %v is far below the drift budget — the encoding is oversized", worst)
	}
}

// TestQuat_SignCanonicalIsStable pins that q and -q produce identical bytes.
// Without it a float-jitter sign flip on an otherwise-static orientation would
// spend a whole delta field every time it crossed zero.
func TestQuat_SignCanonicalIsStable(t *testing.T) {
	rng := rand.New(rand.NewPCG(3, 4))
	for i := 0; i < 10000; i++ {
		x, y, z, w := randomUnitQuat(rng)
		if a, b := Quat(x, y, z, w), Quat(-x, -y, -z, -w); a != b {
			t.Fatalf("q and -q encode differently: %#x vs %#x (q=%v,%v,%v,%v)", a, b, x, y, z, w)
		}
	}
}

// TestQuat_WidthAndPacking pins the wire shape: 2 + 3*18 = 56 bits, exactly
// QuatWireSize bytes with no padding.
func TestQuat_WidthAndPacking(t *testing.T) {
	if QuatWireSize != 7 {
		t.Fatalf("QuatWireSize = %d, want 7", QuatWireSize)
	}
	if 2+3*quatBits != QuatWireSize*8 {
		t.Fatalf("2 + 3*%d = %d bits does not fill %d bytes", quatBits, 2+3*quatBits, QuatWireSize)
	}
	rng := rand.New(rand.NewPCG(5, 6))
	for i := 0; i < 10000; i++ {
		x, y, z, w := randomUnitQuat(rng)
		if v := Quat(x, y, z, w); v >= 1<<56 {
			t.Fatalf("encoded value %#x overflows %d bytes", v, QuatWireSize)
		}
	}
}

// TestQuat_IdentityAndZero covers the two values the framework zero-fills.
func TestQuat_IdentityAndZero(t *testing.T) {
	for _, c := range []struct {
		name           string
		x, y, z, w     float32
		wx, wy, wz, ww float32
	}{
		{name: "identity", x: 0, y: 0, z: 0, w: 1, wx: 0, wy: 0, wz: 0, ww: 1},
		{name: "zero-norm decodes as identity", x: 0, y: 0, z: 0, w: 0, wx: 0, wy: 0, wz: 0, ww: 1},
	} {
		gx, gy, gz, gw := UnQuat(Quat(c.x, c.y, c.z, c.w))
		if gx != c.wx || gy != c.wy || gz != c.wz || gw != c.ww {
			t.Errorf("%s: got %v,%v,%v,%v want %v,%v,%v,%v", c.name, gx, gy, gz, gw, c.wx, c.wy, c.wz, c.ww)
		}
	}
}

// TestQuat_DecodedIsUnitLength — every decode must land on the unit sphere, or
// downstream matrix math silently scales.
func TestQuat_DecodedIsUnitLength(t *testing.T) {
	rng := rand.New(rand.NewPCG(7, 8))
	for i := 0; i < 50000; i++ {
		x, y, z, w := randomUnitQuat(rng)
		dx, dy, dz, dw := UnQuat(Quat(x, y, z, w))
		n := math.Sqrt(float64(dx)*float64(dx) + float64(dy)*float64(dy) +
			float64(dz)*float64(dz) + float64(dw)*float64(dw))
		if math.Abs(n-1) > 1e-6 {
			t.Fatalf("decoded quaternion has norm %v", n)
		}
	}
}

// TestQuat_GoldenVectors pins exact bytes so the TypeScript and C# decoders
// phase 3 writes have a fixture to agree with.
func TestQuat_GoldenVectors(t *testing.T) {
	for _, c := range []struct {
		name       string
		x, y, z, w float32
		want       uint64
	}{
		{name: "identity", x: 0, y: 0, z: 0, w: 1, want: 0x00dffff7fffdffff},
		{name: "180 about X", x: 1, y: 0, z: 0, w: 0, want: 0x001ffff7fffdffff},
		{name: "90 about Z", x: 0, y: 0, z: 0.70710678, w: 0.70710678, want: 0x009ffff7fffffffe},
		{name: "120 about (1,1,1)", x: 0.5, y: 0.5, z: 0.5, w: 0.5, want: 0x0036a08da8236a08},
	} {
		if got := Quat(c.x, c.y, c.z, c.w); got != c.want {
			t.Errorf("%s: Quat = %#x, want %#x", c.name, got, c.want)
		}
	}
}

// TestQQuat_WriterReaderRoundTrip covers the SnapshotWriter/Reader pair and
// confirms it consumes exactly QuatWireSize bytes.
func TestQQuat_WriterReaderRoundTrip(t *testing.T) {
	buf := make([]byte, 16)
	w := NewSnapshotWriter(buf)
	w.QQuat(0, 0, 0.70710678, 0.70710678)
	out := w.Bytes()
	if len(out) != QuatWireSize {
		t.Fatalf("QQuat wrote %d bytes, want %d", len(out), QuatWireSize)
	}

	r := NewSnapshotReader(out)
	x, y, z, wq := r.UnQQuat()
	if a := angleBetween(0, 0, 0.70710678, 0.70710678, x, y, z, wq); a > 1e-4 {
		t.Fatalf("round trip through the writer drifted %v rad", a)
	}
}
