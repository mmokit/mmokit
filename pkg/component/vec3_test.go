package component

import (
	"math"
	"testing"
)

// A 2D-only phase gives Vec3 no execution coverage, so it gets direct tests
// rather than shipping unexercised.
func TestVec3Math(t *testing.T) {
	a := Vec3{1, 2, 3}
	b := Vec3{4, 5, 6}

	if got := a.Add(b); got != (Vec3{5, 7, 9}) {
		t.Errorf("Add = %v", got)
	}
	if got := b.Sub(a); got != (Vec3{3, 3, 3}) {
		t.Errorf("Sub = %v", got)
	}
	if got := a.Scale(2); got != (Vec3{2, 4, 6}) {
		t.Errorf("Scale = %v", got)
	}
	if got := a.Dot(b); got != 32 {
		t.Errorf("Dot = %v, want 32", got)
	}
	// Right-handed: x cross y = z.
	if got := (Vec3{1, 0, 0}).Cross(Vec3{0, 1, 0}); got != (Vec3{0, 0, 1}) {
		t.Errorf("Cross = %v, want {0,0,1}", got)
	}
	if got := (Vec3{3, 4, 0}).Len(); got != 5 {
		t.Errorf("Len = %v, want 5", got)
	}
	if got := (Vec3{3, 4, 0}).LenSq(); got != 25 {
		t.Errorf("LenSq = %v, want 25", got)
	}
	if got := (Vec3{0, 0, 2}).Normalize(); got != (Vec3{0, 0, 1}) {
		t.Errorf("Normalize = %v", got)
	}
	if got := a.Lerp(b, 0.5); got != (Vec3{2.5, 3.5, 4.5}) {
		t.Errorf("Lerp = %v", got)
	}
}

// A zero vector has no direction. Returning zero rather than NaN keeps a
// degenerate input from propagating through a whole frame of motion.
func TestVec3NormalizeZeroIsZeroNotNaN(t *testing.T) {
	got := Vec3{}.Normalize()
	if got != (Vec3{}) {
		t.Fatalf("Normalize of the zero vector = %v, want the zero vector", got)
	}
	if math.IsNaN(float64(got.X)) {
		t.Error("Normalize produced NaN")
	}
}

// The conversions are the reason Position keeps sibling scalars instead of
// embedding a Vec3: they cost nothing, so the flat walker keeps working and
// callers still get vector math.
func TestPositionVelocityConvertToVec3(t *testing.T) {
	p := Position{X: 1, Y: 2, Z: 3}
	if got := p.Vec(); got != (Vec3{1, 2, 3}) {
		t.Errorf("Position.Vec = %v", got)
	}
	p.SetVec(Vec3{7, 8, 9})
	if p != (Position{X: 7, Y: 8, Z: 9}) {
		t.Errorf("Position.SetVec = %v", p)
	}

	v := Velocity{X: 1, Y: 2, Z: 3}
	if got := v.Vec(); got != (Vec3{1, 2, 3}) {
		t.Errorf("Velocity.Vec = %v", got)
	}
	v.SetVec(Vec3{7, 8, 9})
	if v != (Velocity{X: 7, Y: 8, Z: 9}) {
		t.Errorf("Velocity.SetVec = %v", v)
	}
}
