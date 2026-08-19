package component

import "math"

// Rotation accessors.
//
// Every read and write of orientation goes through these rather than touching
// the storage directly. That indirection is the point: it lets the underlying
// representation change without a second sweep of the call sites, and it gives
// yaw one definition instead of a cos/sin pair repeated at each use.
//
// Yaw is rotation about the up axis — the only component a 2D profile has, and
// the one a 3D profile still needs for facing.

// RotationIdentity is the zero rotation. The zero value of Rotation is also
// identity — normalized() maps a zero-norm quaternion to {0,0,0,1} — so this
// exists for readability at construction sites rather than out of necessity.
func RotationIdentity() Rotation { return Rotation{} }

// RotationFromYaw builds a rotation facing the given yaw in radians.
//
// float64 intermediates throughout. That is not incidental: it is what keeps
// the yaw round-trip exact for the literals the exact-equality fixtures use
// (0.75 in the prediction test, 3.0 in the transfer golden). A float32-
// intermediate implementation fails both.
func RotationFromYaw(yaw float32) Rotation {
	h := float64(yaw) / 2
	return Rotation{Z: float32(math.Sin(h)), W: float32(math.Cos(h))}
}

// normalized does two jobs, and both are load-bearing.
//
// A zero-norm quaternion becomes identity, which is what keeps `Rotation{}`
// valid in every framework zero-fill path and at the game's own construction
// sites. A non-unit one is renormalized, which closes the hole the admin
// console opens: `entity.modify Rotation W 5` reaches the raw field through
// fieldpath's exported-scalar walk, and without this a hand-written value
// would yield garbage rather than a rotation.
func (r Rotation) normalized() Rotation {
	n := math.Sqrt(float64(r.X*r.X + r.Y*r.Y + r.Z*r.Z + r.W*r.W))
	if n == 0 {
		return Rotation{W: 1}
	}
	if math.Abs(n-1) < 1e-6 {
		return r
	}
	inv := float32(1 / n)
	return Rotation{X: r.X * inv, Y: r.Y * inv, Z: r.Z * inv, W: r.W * inv}
}

// Yaw returns the rotation about the up axis, in radians, in [-pi, pi].
func (r Rotation) Yaw() float32 {
	q := r.normalized()
	siny := 2 * (float64(q.W)*float64(q.Z) + float64(q.X)*float64(q.Y))
	cosy := 1 - 2*(float64(q.Y)*float64(q.Y)+float64(q.Z)*float64(q.Z))
	return float32(math.Atan2(siny, cosy))
}

// SetYaw replaces the rotation with one facing yaw.
func (r *Rotation) SetYaw(yaw float32) { *r = RotationFromYaw(yaw) }

// AddYaw turns by delta radians.
//
// Unlike the unbounded float32 angle this replaces, the result stays wrapped
// and renormalized, so repeated turning does not accumulate error.
func (r *Rotation) AddYaw(delta float32) { r.SetYaw(r.Yaw() + delta) }

// Forward is the unit direction the rotation faces, in the horizontal plane.
// Collapses the cos/sin pair that was written out at each call site.
func (r Rotation) Forward() (x, y float32) {
	yaw := float64(r.Yaw())
	return float32(math.Cos(yaw)), float32(math.Sin(yaw))
}
