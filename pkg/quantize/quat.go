package quantize

import "math"

// Quaternion orientation, encoded with the "smallest three" technique: the
// largest-magnitude component is dropped and reconstructed on decode from the
// unit-length constraint, and the remaining three are quantized over
// [-1/sqrt2, +1/sqrt2] rather than [-1, +1].
//
// Both halves of that buy precision. Dropping a component saves a quarter of
// the payload outright, and the three that survive are bounded by 1/sqrt2 —
// because the dropped one is the largest — so the same bit count covers a
// range 1.41x narrower.
//
// Sizing, against the alternatives measured for this schema:
//
//	smallest-three 10-bit, 4B : ~2.4e-3 rad  (the common industry tuning)
//	smallest-three 15-bit, 6B : ~1.4e-4 rad  (measured, not estimated)
//	four int16,            8B : ~6.1e-5 rad
//	smallest-three 18-bit, 7B : ~1.9e-5 rad  (this)
//
// The bar is the qangle bucket, 9.6e-5 rad, which this sits beside in the same
// schema: a 3D profile whose orientation is coarser than the 2D yaw it
// replaces would be a regression dressed as a feature. The 4-byte tuning
// misses that by 25x and the 6-byte tuning still misses it by 1.4x — the
// worst case is not the average one, it is the all-components-equal rotation
// where reconstructing the dropped component amplifies the other three's
// error. 18-bit clears it by 5x, is finer than the 8-byte four-int16
// alternative while being a byte smaller, and packs into exactly 56 bits with
// no wasted space. It is also comfortably under phase 1's measured simulation
// drift (2.4e-4 rad over 12000 ticks), so the encoder is never the dominant
// error term.
const (
	// QuatWireSize is the encoded width in bytes. It is a compile-time
	// constant because pkg/quantize's delta encoder builds its offset table
	// from type-level field widths.
	QuatWireSize = 7

	// quatBits is the width of each transmitted component. 2 + 3*18 = 56,
	// exactly QuatWireSize bytes with no padding.
	quatBits = 18
	// quatCodeMask masks one encoded component out of the packed value.
	quatCodeMask = (1 << quatBits) - 1 // 262143

	// quatHalf is the code assigned to 0.0, and the number of steps either
	// side of it. Using an ODD number of levels centred on quatHalf costs one
	// of the 32768 codes and buys an exact representation of zero — without
	// it the midpoint falls between two codes, identity decodes to
	// 2.2e-5 per component, and every near-zero component carries a
	// systematic bias.
	quatHalf = (1 << (quatBits - 1)) - 1 // 131071
)

// quatRange is the bound on the three transmitted components: if a component
// were larger than 1/sqrt2 it would be the largest one, and the largest is
// never transmitted.
const quatRange = math.Sqrt2 / 2

// Quat encodes a quaternion as a 56-bit smallest-three value.
//
// The input need not be normalized; a zero-norm quaternion encodes as
// identity, matching component.Rotation's zero value.
//
// The quaternion is sign-canonicalized so the dropped component is always
// non-negative, which is what lets UnQuat reconstruct it as a positive square
// root. That also makes q and -q — the same rotation — produce identical
// bytes, so a float-jitter sign flip can never spend a delta field.
//
// Layout, big-endian, bit 55 is the MSB of the first byte:
//
//	[55:54]  index of the dropped (largest) component, 0=X 1=Y 2=Z 3=W
//	[53:36]  first remaining component
//	[35:18]  second remaining component
//	[17:0]   third remaining component
func Quat(x, y, z, w float32) uint64 {
	// Normalize. Zero-norm maps to identity rather than propagating NaN.
	n := math.Sqrt(float64(x)*float64(x) + float64(y)*float64(y) +
		float64(z)*float64(z) + float64(w)*float64(w))
	var q [4]float64
	if n == 0 {
		q = [4]float64{0, 0, 0, 1}
	} else {
		q = [4]float64{float64(x) / n, float64(y) / n, float64(z) / n, float64(w) / n}
	}

	// Find the largest-magnitude component; that is the one we drop.
	largest := 0
	for i := 1; i < 4; i++ {
		if math.Abs(q[i]) > math.Abs(q[largest]) {
			largest = i
		}
	}
	// Canonicalize so the dropped component is non-negative. q and -q are the
	// same rotation, so this is free.
	if q[largest] < 0 {
		for i := range q {
			q[i] = -q[i]
		}
	}

	out := uint64(largest) << 54
	shift := 36
	for i := 0; i < 4; i++ {
		if i == largest {
			continue
		}
		out |= uint64(quantizeQuatComponent(q[i])) << shift
		shift -= quatBits
	}
	return out
}

// UnQuat decodes a 56-bit smallest-three value back to a unit quaternion.
func UnQuat(v uint64) (x, y, z, w float32) {
	largest := int((v >> 54) & 0x3)

	var q [4]float64
	sumSq := 0.0
	shift := 36
	for i := 0; i < 4; i++ {
		if i == largest {
			continue
		}
		c := dequantizeQuatComponent(uint32((v >> shift) & quatCodeMask))
		q[i] = c
		sumSq += c * c
		shift -= quatBits
	}
	// The dropped component is whatever makes the quaternion unit-length. It
	// is non-negative by construction — Quat canonicalized the sign.
	if rem := 1 - sumSq; rem > 0 {
		q[largest] = math.Sqrt(rem)
	} else {
		q[largest] = 0
	}

	// Renormalize: three independently-rounded components plus a square root
	// do not land exactly on the unit sphere.
	n := math.Sqrt(q[0]*q[0] + q[1]*q[1] + q[2]*q[2] + q[3]*q[3])
	if n == 0 {
		return 0, 0, 0, 1
	}
	return float32(q[0] / n), float32(q[1] / n), float32(q[2] / n), float32(q[3] / n)
}

// quantizeQuatComponent maps [-quatRange, +quatRange] onto [0, quatMax].
// Rounds rather than truncates — this encoding has no golden to preserve, and
// rounding halves the worst-case error.
func quantizeQuatComponent(v float64) uint32 {
	r := math.Round(v/quatRange*quatHalf) + quatHalf
	if r < 0 {
		return 0
	}
	if r > 2*quatHalf {
		return 2 * quatHalf
	}
	return uint32(r)
}

// dequantizeQuatComponent is quantizeQuatComponent's inverse.
func dequantizeQuatComponent(q uint32) float64 {
	return (float64(q) - quatHalf) / quatHalf * quatRange
}

// QQuat encodes a quaternion and writes it as 7 big-endian bytes.
func (w *SnapshotWriter) QQuat(x, y, z, wq float32) {
	v := Quat(x, y, z, wq)
	w.Uint8(uint8(v >> 48))
	w.Uint16(uint16(v >> 32))
	w.Uint32(uint32(v))
}

// UnQQuat reads QuatWireSize bytes and decodes them to a unit quaternion.
func (r *SnapshotReader) UnQQuat() (x, y, z, w float32) {
	hi := uint64(r.Uint8())
	mid := uint64(r.Uint16())
	lo := uint64(r.Uint32())
	return UnQuat(hi<<48 | mid<<32 | lo)
}
