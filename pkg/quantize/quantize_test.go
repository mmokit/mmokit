package quantize

import (
	"math"
	"testing"
)

func TestPosRoundTrip(t *testing.T) {
	cellSizes := []float32{1024, 4096, 8192, 16384}
	for _, cs := range cellSizes {
		tolerance := cs / 65535

		// Zero
		got := UnPos(Pos(0, cs), cs)
		if abs32(got) > tolerance {
			t.Errorf("Pos round-trip 0 at cs=%v: got %v, want ~0", cs, got)
		}

		// Near max
		val := cs - 1
		got = UnPos(Pos(val, cs), cs)
		if abs32(got-val) > tolerance {
			t.Errorf("Pos round-trip %v at cs=%v: got %v, delta %v > %v", val, cs, got, abs32(got-val), tolerance)
		}

		// Mid-range
		val = cs / 2
		got = UnPos(Pos(val, cs), cs)
		if abs32(got-val) > tolerance {
			t.Errorf("Pos round-trip %v at cs=%v: got %v, delta %v > %v", val, cs, got, abs32(got-val), tolerance)
		}

		// Quarter
		val = cs / 4
		got = UnPos(Pos(val, cs), cs)
		if abs32(got-val) > tolerance {
			t.Errorf("Pos round-trip %v at cs=%v: got %v, delta %v > %v", val, cs, got, abs32(got-val), tolerance)
		}
	}
}

func TestPosClamp(t *testing.T) {
	// Negative clamped to 0
	q := Pos(-10, 1024)
	if q != 0 {
		t.Errorf("Pos(-10, 1024) = %d, want 0", q)
	}

	// Over cellSize clamped
	q = Pos(2000, 1024)
	got := UnPos(q, 1024)
	if got < 1023 {
		t.Errorf("Pos(2000, 1024) round-trip = %v, want ~1024", got)
	}
}

func TestAngleRoundTrip(t *testing.T) {
	tolerance := float32(2 * math.Pi / 65535)

	// Avoid exact ±π (maps to boundary of uint16 range, ambiguous direction).
	cases := []float32{0, math.Pi * 0.99, -math.Pi * 0.99, math.Pi / 2, -math.Pi / 2, math.Pi / 4, -math.Pi / 4}
	for _, angle := range cases {
		got := UnAngle(Angle(angle))
		if abs32(got-angle) > tolerance {
			t.Errorf("Angle round-trip %v: got %v, delta %v", angle, got, abs32(got-angle))
		}
	}
}

func TestAngleWrap(t *testing.T) {
	tolerance := float32(2 * math.Pi / 65535)

	// Angles beyond [-π, π] are normalized (wrapped), not clamped.
	// π+1 wraps to approximately -π+1 = -2.14
	got := UnAngle(Angle(float32(math.Pi) + 1))
	expected := float32(math.Pi+1) - 2*math.Pi // ≈ -2.14
	if abs32(got-expected) > tolerance {
		t.Errorf("Angle(pi+1) wrap: got %v, want ~%v", got, expected)
	}

	// -π-1 wraps to approximately π-1 = 2.14
	got = UnAngle(Angle(float32(-math.Pi) - 1))
	expected = float32(-math.Pi-1) + 2*math.Pi
	if abs32(got-expected) > tolerance {
		t.Errorf("Angle(-pi-1) wrap: got %v, want ~%v", got, expected)
	}
}

func TestNormRoundTrip(t *testing.T) {
	tolerance := float32(1.0 / 255)

	cases := []float32{0, 0.5, 1, 0.25, 0.75}
	for _, v := range cases {
		got := UnNorm(Norm(v))
		if abs32(got-v) > tolerance {
			t.Errorf("Norm round-trip %v: got %v, delta %v", v, got, abs32(got-v))
		}
	}
}

func TestNormClamp(t *testing.T) {
	if Norm(-0.5) != 0 {
		t.Errorf("Norm(-0.5) = %d, want 0", Norm(-0.5))
	}
	if Norm(1.5) != 255 {
		t.Errorf("Norm(1.5) = %d, want 255", Norm(1.5))
	}
}

func TestVelRoundTrip(t *testing.T) {
	scale := float32(2000)
	tolerance := scale / 32767

	cases := []float32{0, 1000, -1000, 2000, -2000, 500, -500}
	for _, v := range cases {
		got := UnVel(Vel(v, scale), scale)
		if abs32(got-v) > tolerance {
			t.Errorf("Vel round-trip %v (scale=%v): got %v, delta %v", v, scale, got, abs32(got-v))
		}
	}
}

func TestVelClamp(t *testing.T) {
	scale := float32(1000)
	// Beyond max clamped
	q := Vel(2000, scale)
	if q != 32767 {
		t.Errorf("Vel(2000, 1000) = %d, want 32767", q)
	}
	q = Vel(-2000, scale)
	if q != -32767 {
		t.Errorf("Vel(-2000, 1000) = %d, want -32767", q)
	}
}

func TestVelZeroScale(t *testing.T) {
	if Vel(100, 0) != 0 {
		t.Errorf("Vel with zero scale should return 0")
	}
}

func TestRelRoundTrip(t *testing.T) {
	halfRange := float32(3000)
	tolerance := halfRange / 32767

	cases := []float32{0, 1500, -1500, 3000, -3000, 100, -100}
	for _, v := range cases {
		got := UnRel(Rel(v, halfRange), halfRange)
		if abs32(got-v) > tolerance {
			t.Errorf("Rel round-trip %v (halfRange=%v): got %v, delta %v", v, halfRange, got, abs32(got-v))
		}
	}
}

func TestRelClamp(t *testing.T) {
	halfRange := float32(1000)
	q := Rel(2000, halfRange)
	if q != 32767 {
		t.Errorf("Rel(2000, 1000) = %d, want 32767", q)
	}
	q = Rel(-2000, halfRange)
	if q != -32767 {
		t.Errorf("Rel(-2000, 1000) = %d, want -32767", q)
	}
}

func TestRelZeroHalfRange(t *testing.T) {
	if Rel(100, 0) != 0 {
		t.Errorf("Rel with zero halfRange should return 0")
	}
}

func abs32(v float32) float32 {
	if v < 0 {
		return -v
	}
	return v
}
