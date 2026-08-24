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
	// Straight off the basis. This used to go Yaw() — a sqrt and an atan2 —
	// and then back through Cos and Sin, three transcendentals to recover two
	// numbers the quaternion already contains. Three hot paths in
	// examples/space call it.
	fwd, _, _ := r.Basis()
	return fwd.X, fwd.Y
}

// RotationFromAxisAngle builds a rotation of angle radians about the given
// axis. The axis need not be unit length; a zero-length axis yields identity.
//
// Phase 1 gave Rotation quaternion storage but only yaw accessors, which is
// all a 2D profile needs. A 3D profile needs to express pitch and roll, and
// without a general constructor a game cannot produce any orientation the yaw
// helpers cannot — so the 3D orientation wire path would be unreachable from
// game code.
//
// float64 intermediates for the same reason RotationFromYaw uses them.
func RotationFromAxisAngle(x, y, z, angle float32) Rotation {
	n := math.Sqrt(float64(x)*float64(x) + float64(y)*float64(y) + float64(z)*float64(z))
	if n == 0 {
		return Rotation{W: 1}
	}
	h := float64(angle) / 2
	s := math.Sin(h) / n
	return Rotation{
		X: float32(float64(x) * s),
		Y: float32(float64(y) * s),
		Z: float32(float64(z) * s),
		W: float32(math.Cos(h)),
	}
}

// Mul composes two rotations: the result applies o first, then r.
//
// Quaternion multiplication does not commute, and the order chosen here is the
// one that reads correctly at a call site — r.Mul(delta) means "r, then a
// further delta" only if delta is applied in r's frame. Renormalized on the
// way out, because repeated composition is exactly where a float32 quaternion
// drifts off the unit sphere.
func (r Rotation) Mul(o Rotation) Rotation {
	rx, ry, rz, rw := float64(r.X), float64(r.Y), float64(r.Z), float64(r.W)
	ox, oy, oz, ow := float64(o.X), float64(o.Y), float64(o.Z), float64(o.W)
	return Rotation{
		X: float32(rw*ox + rx*ow + ry*oz - rz*oy),
		Y: float32(rw*oy - rx*oz + ry*ow + rz*ox),
		Z: float32(rw*oz + rx*oy - ry*ox + rz*ow),
		W: float32(rw*ow - rx*ox - ry*oy - rz*oz),
	}.normalized()
}

// RotateAxis returns r turned by angle radians about the given axis.
func (r Rotation) RotateAxis(x, y, z, angle float32) Rotation {
	return r.Mul(RotationFromAxisAngle(x, y, z, angle))
}

// SlerpDotThreshold is the |dot| above which Slerp falls back to a
// normalized linear interpolation.
//
// Exported because it is the single most port-divergent line in the whole
// orientation path: a TypeScript or C# implementation that picks a different
// threshold produces visibly different intermediate frames near the seam, and
// nothing else would catch it. The client cores use this value.
const SlerpDotThreshold = 0.9995

// Slerp interpolates from r to o along the shortest arc.
//
// The contract is pinned in unusual detail because three independent
// implementations of it exist — here, in the TypeScript SDK, and in the C#
// SDK — and every clause below is somewhere the three could disagree:
//
//   - t is clamped to [0,1] inside, so an extrapolating caller gets an
//     endpoint rather than a quaternion off the arc.
//   - A negative dot negates o (and the dot) first. q and -q are the same
//     rotation, so without this the interpolation takes the long way round —
//     up to a full turn where a fraction of one was intended.
//   - Above SlerpDotThreshold the arc is short enough that sin(theta)
//     underflows toward zero and the trigonometric form loses precision, so
//     it degrades to a normalized lerp. The two branches agree to well within
//     the quaternion wire quantum at the seam.
//   - The result is always normalized, and a zero-norm input is identity,
//     matching the convention quantize.Quat and Rotation.normalized already
//     use.
//
// float64 internals, float32 out, for the reason RotationFromYaw documents.
func (r Rotation) Slerp(o Rotation, t float32) Rotation {
	if t <= 0 {
		return r.normalized()
	}
	if t >= 1 {
		return o.normalized()
	}
	a := r.normalized()
	b := o.normalized()

	ax, ay, az, aw := float64(a.X), float64(a.Y), float64(a.Z), float64(a.W)
	bx, by, bz, bw := float64(b.X), float64(b.Y), float64(b.Z), float64(b.W)

	dot := ax*bx + ay*by + az*bz + aw*bw
	if dot < 0 {
		bx, by, bz, bw = -bx, -by, -bz, -bw
		dot = -dot
	}
	if dot > 1 {
		dot = 1
	}

	ft := float64(t)
	var wa, wb float64
	if dot > SlerpDotThreshold {
		wa, wb = 1-ft, ft
	} else {
		theta := math.Acos(dot)
		sin := math.Sin(theta)
		wa = math.Sin((1-ft)*theta) / sin
		wb = math.Sin(ft*theta) / sin
	}

	return Rotation{
		X: float32(wa*ax + wb*bx),
		Y: float32(wa*ay + wb*by),
		Z: float32(wa*az + wb*bz),
		W: float32(wa*aw + wb*bw),
	}.normalized()
}

// Normalized returns r scaled to unit length. Exported because the spatial
// narrow phase must renormalize before deriving a basis; see Basis.
//
// Returns identity for a zero-norm quaternion, which is what makes
// Rotation's zero value usable as identity.
func (r Rotation) Normalized() Rotation { return r.normalized() }

// Basis returns r's three orthonormal column vectors: forward (local X), side
// (local Y) and up (local Z). This is the orientation a three-axis separating-
// axis test needs — the axes themselves, not an angle.
//
// RENORMALIZES UNCONDITIONALLY, in float64, and that is not defensive
// boilerplate. normalized() returns r unchanged when |n-1| < 1e-6, so a
// quaternion legitimately in circulation can carry up to ~2e-6 of scale error
// — and that error multiplies through into the projected half-extents of an
// OBB. It is larger than the parallel-axis epsilon a SAT uses, so leaving it
// in turns a geometric tolerance into a rounding guard and the failure is a
// razor-edge separation that flips: two boxes that overlap report separated,
// which is an object passing through a wall.
//
// A non-unit quaternion is reachable, not hypothetical: entity.modify reaches
// the raw W field through fieldpath's exported-scalar walk, so `entity modify
// Rotation W 5` produces one.
//
// No trigonometry. For a pure-yaw quaternion this specialises exactly to
// cos = 1-2z², sin = 2wz with zeros in the right places, so there is no
// separate 2D path and no branch.
func (r Rotation) Basis() (fwd, side, up Vec3) {
	q := r.normalized()
	x, y, z, w := float64(q.X), float64(q.Y), float64(q.Z), float64(q.W)

	fwd = Vec3{
		X: float32(1 - 2*(y*y+z*z)),
		Y: float32(2 * (x*y + w*z)),
		Z: float32(2 * (x*z - w*y)),
	}
	side = Vec3{
		X: float32(2 * (x*y - w*z)),
		Y: float32(1 - 2*(x*x+z*z)),
		Z: float32(2 * (y*z + w*x)),
	}
	up = Vec3{
		X: float32(2 * (x*z + w*y)),
		Y: float32(2 * (y*z - w*x)),
		Z: float32(1 - 2*(x*x+y*y)),
	}
	return fwd, side, up
}

// Rotate returns v rotated by r, via the standard v + 2w(q x v) + 2(q x (q x v))
// form. float64 intermediates for the same reason Basis renormalizes.
func (r Rotation) Rotate(v Vec3) Vec3 {
	q := r.normalized()
	qx, qy, qz, qw := float64(q.X), float64(q.Y), float64(q.Z), float64(q.W)
	vx, vy, vz := float64(v.X), float64(v.Y), float64(v.Z)

	// t = 2 * (q_vec x v)
	tx := 2 * (qy*vz - qz*vy)
	ty := 2 * (qz*vx - qx*vz)
	tz := 2 * (qx*vy - qy*vx)

	return Vec3{
		X: float32(vx + qw*tx + (qy*tz - qz*ty)),
		Y: float32(vy + qw*ty + (qz*tx - qx*tz)),
		Z: float32(vz + qw*tz + (qx*ty - qy*tx)),
	}
}

// Inverse returns the rotation that undoes r. For a unit quaternion that is
// the conjugate; r is normalized first so a drifted value still inverts.
func (r Rotation) Inverse() Rotation {
	q := r.normalized()
	return Rotation{X: -q.X, Y: -q.Y, Z: -q.Z, W: q.W}
}
