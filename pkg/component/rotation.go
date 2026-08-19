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
// identity, so this exists for readability at construction sites rather than
// out of necessity.
func RotationIdentity() Rotation { return Rotation{} }

// RotationFromYaw builds a rotation facing the given yaw in radians.
func RotationFromYaw(yaw float32) Rotation { return Rotation{Angle: yaw} }

// Yaw returns the rotation about the up axis, in radians.
func (r Rotation) Yaw() float32 { return r.Angle }

// SetYaw replaces the rotation with one facing yaw.
func (r *Rotation) SetYaw(yaw float32) { r.Angle = yaw }

// AddYaw turns by delta radians.
func (r *Rotation) AddYaw(delta float32) { r.Angle += delta }

// Forward is the unit direction the rotation faces, in the horizontal plane.
// Collapses the cos/sin pair that was written out at each call site.
func (r Rotation) Forward() (x, y float32) {
	yaw := float64(r.Yaw())
	return float32(math.Cos(yaw)), float32(math.Sin(yaw))
}
