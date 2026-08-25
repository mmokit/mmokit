package spatial

import (
	"math"
	"sort"

	"github.com/mmokit/mmokit/pkg/component"
)

// The three-axis narrow phase.
//
// Every routine here is a BOOLEAN overlap test. Contact manifolds — normal,
// depth, point — are phase 4b unit 9; keeping the two apart means the
// separation logic is testable on its own, and it is the half that decides
// whether a character passes through a wall.
//
// NUMERICS, stated once and applied throughout:
//
//   - float64 intermediates, float32 in and out. A SAT projects half-extents
//     through a rotation matrix and compares sums; in float32 the accumulated
//     error is comparable to the epsilon that decides the answer.
//   - Fixed iteration counts. Nothing here loops until convergence, so the
//     cost of a test does not depend on the geometry — a server tick budget
//     cannot absorb a pathological pair.
//   - Dimensionless epsilons that MULTIPLY an extent rather than being added
//     to a distance, so a test behaves the same on a 1-unit crate and a
//     1000-unit wall.
//   - Symmetry by construction: where a pair is not symmetric in code, one
//     arm calls the other with the arguments swapped rather than duplicating
//     the arithmetic. QueryCollisions iterates a Go map, so an asymmetric
//     answer would be a bug that appears only for some iteration orders.

// satEpsilon guards the nine cross-product axes of a box-box test.
//
// DIMENSIONLESS, and added to the absolute rotation matrix so it scales the
// half-extents rather than a distance — which is what makes the test behave
// identically at any world scale.
//
// The failure it prevents is one-directional and severe. When two boxes have
// near-parallel edges their cross product is near zero, so the axis it
// defines is numerically meaningless; testing it anyway reports SEPARATION
// for boxes that overlap, and a false separation is an object passing through
// a wall. A false overlap, by contrast, is caught by the face axes, which are
// exact. So the epsilon is deliberately generous.
//
// Correctness does not depend on the value: when edges are parallel, any
// genuine separating axis is a FACE axis, and those are tested exactly. The
// epsilon only decides whether a redundant axis is consulted.
const satEpsilon = 1e-6

// obbAxes returns a box entry's three half-extents and its basis columns.
func obbAxes(e *Entry) (half [3]float64, axis [3]component.Vec3) {
	fwd, side, up := e.Rot.Basis()
	return [3]float64{
		float64(e.Width) / 2,
		float64(e.Height) / 2,
		float64(e.Depth) / 2,
	}, [3]component.Vec3{fwd, side, up}
}

// centerOf is an entry's center as a vector.
func centerOf(e *Entry) component.Vec3 { return component.Vec3{X: e.X, Y: e.Y, Z: e.Z} }

// overlapBoxBox3 is the 15-axis separating-axis test: three face normals per
// box, plus the nine pairwise edge cross products.
//
// Structured as Ericson's formulation — the rotation matrix R between the two
// frames, and AbsR = |R| + epsilon — because that is where the epsilon belongs:
// AbsR multiplies half-extents, so the guard is dimensionless and every one of
// the nine cross-product axes inherits it without a per-axis length check.
func overlapBoxBox3(a, b *Entry) bool {
	ha, aa := obbAxes(a)
	hb, ab := obbAxes(b)

	// R[i][j] = a_i · b_j, and AbsR its magnitude plus the guard.
	var r, absR [3][3]float64
	for i := range 3 {
		for j := range 3 {
			r[i][j] = float64(aa[i].Dot(ab[j]))
			absR[i][j] = math.Abs(r[i][j]) + satEpsilon
		}
	}

	// Translation, expressed in A's frame.
	d := centerOf(b).Sub(centerOf(a))
	var t [3]float64
	for i := range 3 {
		t[i] = float64(d.Dot(aa[i]))
	}

	// A's three face normals.
	for i := range 3 {
		ra := ha[i]
		rb := hb[0]*absR[i][0] + hb[1]*absR[i][1] + hb[2]*absR[i][2]
		if math.Abs(t[i]) > ra+rb {
			return false
		}
	}

	// B's three face normals.
	for j := range 3 {
		ra := ha[0]*absR[0][j] + ha[1]*absR[1][j] + ha[2]*absR[2][j]
		rb := hb[j]
		if math.Abs(t[0]*r[0][j]+t[1]*r[1][j]+t[2]*r[2][j]) > ra+rb {
			return false
		}
	}

	// The nine edge-pair cross products, written out rather than looped: each
	// is a distinct index permutation and a loop would need the permutation
	// table anyway, at the cost of hiding which axis a failure came from.
	type axisTest struct{ i, j int }
	for _, ax := range [9]axisTest{
		{0, 0}, {0, 1}, {0, 2},
		{1, 0}, {1, 1}, {1, 2},
		{2, 0}, {2, 1}, {2, 2},
	} {
		i, j := ax.i, ax.j
		i1, i2 := (i+1)%3, (i+2)%3
		j1, j2 := (j+1)%3, (j+2)%3
		ra := ha[i1]*absR[i2][j] + ha[i2]*absR[i1][j]
		rb := hb[j1]*absR[i][j2] + hb[j2]*absR[i][j1]
		if math.Abs(t[i2]*r[i1][j]-t[i1]*r[i2][j]) > ra+rb {
			return false
		}
	}

	return true
}

// overlapBoxSphere3 tests an oriented box against a sphere by transforming the
// sphere's center into the box's frame and clamping to the half-extents.
//
// The 2D version of this projected onto a yaw plane and ignored Z entirely, so
// a sphere directly above a box collided with it.
func overlapBoxSphere3(box, sphere *Entry) bool {
	half, axis := obbAxes(box)
	d := centerOf(sphere).Sub(centerOf(box))

	var dsq float64
	for i := range 3 {
		// Distance along this axis, clamped to the slab.
		p := float64(d.Dot(axis[i]))
		excess := math.Abs(p) - half[i]
		if excess > 0 {
			dsq += excess * excess
		}
	}
	r := float64(sphere.Radius)
	return dsq <= r*r
}

// capsuleSegment returns a capsule's segment endpoints and its cap radius.
//
// Depth is TOTAL TIP-TO-TIP height, so the half-length is Depth/2 - Radius and
// a capsule with Depth <= 2*Radius degenerates to a sphere with a zero-length
// segment — which every routine below handles without a special case, because
// a zero-length segment's closest point is its own center.
func capsuleSegment(e *Entry) (p0, p1 component.Vec3, radius float32) {
	_, _, up := e.Rot.Basis()
	half := float64(e.Depth)/2 - float64(e.Radius)
	if half < 0 {
		half = 0
	}
	c := centerOf(e)
	off := up.Scale(float32(half))
	return c.Sub(off), c.Add(off), e.Radius
}

// overlapCapsuleSphere tests a capsule against a sphere: the distance from the
// sphere's center to the capsule's segment against the summed radii.
func overlapCapsuleSphere(capsule, sphere *Entry) bool {
	p0, p1, r := capsuleSegment(capsule)
	d := pointSegmentDistSq(centerOf(sphere), p0, p1)
	sum := float64(r) + float64(sphere.Radius)
	return d <= sum*sum
}

// overlapCapsuleCapsule reduces to the closest approach of two segments.
func overlapCapsuleCapsule(a, b *Entry) bool {
	a0, a1, ra := capsuleSegment(a)
	b0, b1, rb := capsuleSegment(b)
	d := segmentSegmentDistSq(a0, a1, b0, b1)
	sum := float64(ra) + float64(rb)
	return d <= sum*sum
}

// pointSegmentDistSq is the squared distance from p to segment [a,b].
func pointSegmentDistSq(p, a, b component.Vec3) float64 {
	ab := b.Sub(a)
	denom := float64(ab.LenSq())
	if denom == 0 {
		return float64(p.Sub(a).LenSq())
	}
	t := float64(p.Sub(a).Dot(ab)) / denom
	t = clamp01(t)
	closest := a.Add(ab.Scale(float32(t)))
	return float64(p.Sub(closest).LenSq())
}

// segmentSegmentDistSq is the squared distance between the closest points of
// two segments.
//
// Ericson's closed form, in float64. NOT iterative: it is a fixed sequence of
// arithmetic with two clamps, so its cost does not depend on the geometry.
// The parallel case falls out of the denominator guard rather than needing a
// branch of its own.
func segmentSegmentDistSq(p1, q1, p2, q2 component.Vec3) float64 {
	d1 := q1.Sub(p1) // direction of segment 1
	d2 := q2.Sub(p2) // direction of segment 2
	r := p1.Sub(p2)

	a := float64(d1.LenSq())
	e := float64(d2.LenSq())
	f := float64(d2.Dot(r))

	const eps = 1e-12
	var s, t float64

	switch {
	case a <= eps && e <= eps:
		// Both degenerate to points.
		return float64(p1.Sub(p2).LenSq())
	case a <= eps:
		s = 0
		t = clamp01(f / e)
	default:
		c := float64(d1.Dot(r))
		if e <= eps {
			t = 0
			s = clamp01(-c / a)
		} else {
			b := float64(d1.Dot(d2))
			denom := a*e - b*b
			if denom != 0 {
				s = clamp01((b*f - c*e) / denom)
			} else {
				// Parallel: any s is as good, so take the start and let the
				// clamps below place t.
				s = 0
			}
			t = (b*s + f) / e
			if t < 0 {
				t = 0
				s = clamp01(-c / a)
			} else if t > 1 {
				t = 1
				s = clamp01((b - c) / a)
			}
		}
	}

	c1 := p1.Add(d1.Scale(float32(s)))
	c2 := p2.Add(d2.Scale(float32(t)))
	return float64(c1.Sub(c2).LenSq())
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// overlapCapsuleBox tests a capsule against an oriented box.
//
// The one pair with no closed-form distance in the literature, and the reason
// the phase plan budgeted an iterative method for it. It does not need one.
func overlapCapsuleBox(capsule, b *Entry) bool {
	half, axis := obbAxes(b)
	center := centerOf(b)
	p0w, p1w, r := capsuleSegment(capsule)

	// Into the box's frame, where it is an axis-aligned box centred at the
	// origin. Projecting onto the basis columns IS the inverse rotation,
	// without building one.
	toLocal := func(p component.Vec3) component.Vec3 {
		d := p.Sub(center)
		return component.Vec3{X: d.Dot(axis[0]), Y: d.Dot(axis[1]), Z: d.Dot(axis[2])}
	}
	d2 := segmentBoxDistSq(toLocal(p0w), toLocal(p1w), half)
	return d2 <= float64(r)*float64(r)
}

// segmentBoxDistSq is the squared distance from segment [p0,p1] to an
// axis-aligned box centred at the origin with the given half-extents.
//
// EXACT, and at fixed cost. The plan called for alternating projection — clamp
// onto the box, clamp back onto the segment, repeat — and that is what this
// was. Measured against a dense-sampling oracle it converges far too slowly to
// use: at twelve iterations the worst overestimate over 4000 random
// configurations was still 0.91 world units, and the error direction is the
// dangerous one. Each iteration gives an UPPER bound, so stopping early
// reports no overlap where there is one — a character through a wall.
//
// It does not need to iterate. Squared distance to a convex set is a convex
// function of the point, and the segment is affine in t, so
//
//	f(t) = sum_i max(0, |a_i + t*d_i| - h_i)^2
//
// is CONVEX and PIECEWISE QUADRATIC in t. Its pieces meet where the segment
// crosses one of the six slab planes — at most six values of t — and on each
// piece it is an ordinary quadratic whose minimum is either at a piece
// boundary or at its vertex. Evaluating f at the endpoints, the crossings and
// each piece's vertex therefore finds the exact minimum, in at most fifteen
// evaluations, with no tolerance and nothing to tune.
func segmentBoxDistSq(p0, p1 component.Vec3, half [3]float64) float64 {
	a := [3]float64{float64(p0.X), float64(p0.Y), float64(p0.Z)}
	d := [3]float64{float64(p1.X - p0.X), float64(p1.Y - p0.Y), float64(p1.Z - p0.Z)}

	f := func(t float64) float64 {
		var sum float64
		for i := range 3 {
			if excess := math.Abs(a[i]+t*d[i]) - half[i]; excess > 0 {
				sum += excess * excess
			}
		}
		return sum
	}

	// Piece boundaries: the segment's crossings of the six slab planes, plus
	// the segment's own ends.
	breaks := make([]float64, 0, 8)
	breaks = append(breaks, 0, 1)
	for i := range 3 {
		if d[i] == 0 {
			continue
		}
		for _, plane := range [2]float64{half[i], -half[i]} {
			if t := (plane - a[i]) / d[i]; t > 0 && t < 1 {
				breaks = append(breaks, t)
			}
		}
	}
	sort.Float64s(breaks)

	best := math.Inf(1)
	for _, t := range breaks {
		if v := f(t); v < best {
			best = v
		}
	}

	// Within each piece, the active terms are fixed, so f is a quadratic
	// A*t^2 + B*t + C and its vertex is -B/2A. Determine the active set from
	// the piece's midpoint rather than its endpoints, which are exactly the
	// points where a term switches on or off.
	for k := 0; k+1 < len(breaks); k++ {
		lo, hi := breaks[k], breaks[k+1]
		if hi <= lo {
			continue
		}
		mid := (lo + hi) / 2
		var qa, qb float64
		for i := range 3 {
			v := a[i] + mid*d[i]
			switch {
			case v > half[i]:
				c := a[i] - half[i]
				qa += d[i] * d[i]
				qb += 2 * c * d[i]
			case v < -half[i]:
				c := a[i] + half[i]
				qa += d[i] * d[i]
				qb += 2 * c * d[i]
			}
		}
		if qa <= 0 {
			continue // f is flat or zero on this piece; the boundaries covered it
		}
		if t := -qb / (2 * qa); t > lo && t < hi {
			if v := f(t); v < best {
				best = v
			}
		}
	}
	return best
}

// clampToBox is the closest point to p inside an origin-centred box.
func clampToBox(p component.Vec3, half [3]float64) component.Vec3 {
	return component.Vec3{
		X: float32(clampAbs(float64(p.X), half[0])),
		Y: float32(clampAbs(float64(p.Y), half[1])),
		Z: float32(clampAbs(float64(p.Z), half[2])),
	}
}

// pointBoxDistSq is the exact squared distance from a point to an
// origin-centred box.
func pointBoxDistSq(p component.Vec3, half [3]float64) float64 {
	var sum float64
	for i, v := range [3]float64{float64(p.X), float64(p.Y), float64(p.Z)} {
		if excess := math.Abs(v) - half[i]; excess > 0 {
			sum += excess * excess
		}
	}
	return sum
}

func clampAbs(v, limit float64) float64 {
	if v > limit {
		return limit
	}
	if v < -limit {
		return -limit
	}
	return v
}
