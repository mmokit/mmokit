package component

import "math"

// Vec3 is the math type for positions, velocities and offsets.
//
// Its fields match Position and Velocity in name, type and order, so Go permits
// direct conversion between all three — Vec3(pos), Position(v), Velocity(v) —
// with no allocation and no copying helper. That is why Position keeps sibling
// scalars rather than embedding a Vec3: the binding walker is flat and refuses
// a struct field at construction, and conversion gives the ergonomics a nested
// field would have without the wire consequence.
type Vec3 struct {
	X, Y, Z float32
}

// Vec returns p as a Vec3.
func (p Position) Vec() Vec3 { return Vec3(p) }

// SetVec overwrites p from a Vec3.
func (p *Position) SetVec(v Vec3) { *p = Position(v) }

// Vec returns v as a Vec3.
func (v Velocity) Vec() Vec3 { return Vec3(v) }

// SetVec overwrites v from a Vec3.
func (v *Velocity) SetVec(o Vec3) { *v = Velocity(o) }

func (v Vec3) Add(o Vec3) Vec3      { return Vec3{v.X + o.X, v.Y + o.Y, v.Z + o.Z} }
func (v Vec3) Sub(o Vec3) Vec3      { return Vec3{v.X - o.X, v.Y - o.Y, v.Z - o.Z} }
func (v Vec3) Scale(s float32) Vec3 { return Vec3{v.X * s, v.Y * s, v.Z * s} }
func (v Vec3) Dot(o Vec3) float32   { return v.X*o.X + v.Y*o.Y + v.Z*o.Z }

func (v Vec3) Cross(o Vec3) Vec3 {
	return Vec3{
		v.Y*o.Z - v.Z*o.Y,
		v.Z*o.X - v.X*o.Z,
		v.X*o.Y - v.Y*o.X,
	}
}

// LenSq avoids the square root; prefer it for comparisons.
func (v Vec3) LenSq() float32 { return v.Dot(v) }

func (v Vec3) Len() float32 { return float32(math.Sqrt(float64(v.LenSq()))) }

// Normalize returns the unit vector, or the zero vector when v has no
// direction. Returning zero rather than NaN keeps a degenerate input from
// propagating through a whole frame of motion.
func (v Vec3) Normalize() Vec3 {
	l := v.Len()
	if l == 0 {
		return Vec3{}
	}
	return v.Scale(1 / l)
}

// Lerp is the straight-line interpolation from v to o at t, unclamped.
func (v Vec3) Lerp(o Vec3, t float32) Vec3 {
	return Vec3{
		v.X + (o.X-v.X)*t,
		v.Y + (o.Y-v.Y)*t,
		v.Z + (o.Z-v.Z)*t,
	}
}
