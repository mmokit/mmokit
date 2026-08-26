package spatial

import (
	"math"

	"github.com/mmokit/mmokit/pkg/component"
)

// Contact describes how two overlapping shapes are penetrating.
//
// DIRECTION CONVENTION, stated once and asserted by test: Normal is a unit
// vector pointing from B toward A, so moving A by Normal*Depth separates the
// pair. Every routine here produces that orientation, and the swapped arms in
// the dispatch table negate rather than recomputing — a manifold whose sign
// depends on which entry a bucket scan reached first would push a character
// INTO the wall on half the frames, and QueryCollisions iterates a Go map.
type Contact struct {
	// Normal is unit length, from B toward A.
	Normal component.Vec3
	// Depth is how far the shapes interpenetrate along Normal. Always > 0 for
	// a real contact; a zero depth means touching, which is not a contact.
	Depth float32
	// Point is the world-space contact position, taken midway through the
	// overlap so that push-out from either side is symmetric.
	Point component.Vec3
}

// flip returns the same contact seen from the other shape.
func (c Contact) flip() Contact {
	return Contact{
		Normal: c.Normal.Scale(-1),
		Depth:  c.Depth,
		Point:  c.Point,
	}
}

// contactFromClosest builds a manifold from the closest points on two
// separated surfaces plus their radii.
//
// pa is the closest point on A's core (a center, or a point on its segment),
// pb the same for B. This is where every sphere and capsule pair converges,
// because for those shapes the surface is a fixed offset from the core.
//
// A ZERO-LENGTH SEPARATION is the case that has to be handled rather than
// normalized: two spheres at exactly the same position have no direction
// between them, and Normalize would return a zero vector, which propagates as
// a push of zero and an object that never separates. The fallback is world up
// — arbitrary, but deterministic and unit length, which are the two properties
// anything downstream needs.
func contactFromClosest(pa, pb component.Vec3, ra, rb float32) (Contact, bool) {
	d := pa.Sub(pb)
	distSq := float64(d.LenSq())
	sum := float64(ra) + float64(rb)
	if distSq > sum*sum {
		return Contact{}, false
	}

	dist := math.Sqrt(distSq)
	var n component.Vec3
	if dist > 1e-9 {
		n = d.Scale(float32(1 / dist))
	} else {
		n = component.Vec3{Z: 1}
	}
	depth := float32(sum - dist)
	if depth <= 0 {
		return Contact{}, false
	}
	// Midway through the overlap: A's surface is pa - n*ra, B's is pb + n*rb,
	// and the midpoint of those two is the same as stepping from B's core by
	// rb - depth/2.
	point := pb.Add(n.Scale(float32(float64(rb) - float64(depth)/2)))
	return Contact{Normal: n, Depth: depth, Point: point}, true
}

// pointBoxClosest returns the closest point of an origin-centred box to p, the
// squared distance to it, and whether p is INSIDE.
//
// The inside case is the one that needs care and is the one a character in a
// wall actually hits. Clamping returns p itself, which gives a zero distance
// and no direction — so instead it finds the nearest face and reports the exit
// point on it. That is the minimum-translation direction, which is what a
// push-out wants.
func pointBoxClosest(p component.Vec3, half [3]float64) (q component.Vec3, distSq float64, inside bool) {
	v := [3]float64{float64(p.X), float64(p.Y), float64(p.Z)}
	var out [3]float64
	inside = true
	for i := range 3 {
		out[i] = clampAbs(v[i], half[i])
		if math.Abs(v[i]) > half[i] {
			inside = false
		}
	}
	if !inside {
		q = component.Vec3{X: float32(out[0]), Y: float32(out[1]), Z: float32(out[2])}
		return q, float64(p.Sub(q).LenSq()), false
	}

	// Inside: exit through the nearest face.
	bestAxis, bestGap := 0, math.Inf(1)
	for i := range 3 {
		if gap := half[i] - math.Abs(v[i]); gap < bestGap {
			bestGap, bestAxis = gap, i
		}
	}
	out[bestAxis] = math.Copysign(half[bestAxis], v[bestAxis])
	if v[bestAxis] == 0 {
		// Exactly on the mid-plane: pick the positive face, deterministically.
		out[bestAxis] = half[bestAxis]
	}
	q = component.Vec3{X: float32(out[0]), Y: float32(out[1]), Z: float32(out[2])}
	return q, float64(p.Sub(q).LenSq()), true
}

// contactCapsuleLikeBox is the manifold for a capsule against an oriented box,
// and for a sphere too — a sphere is a capsule whose segment has zero length.
//
// ALONG A BOX FACE AXIS, and that choice is what makes it correct rather than
// merely plausible. The obvious construction — take the closest point on the
// segment, take the closest point on the box, push along the line between them
// — separates THAT PAIR OF POINTS and nothing else. For a long capsule lying
// across a box the rest of the segment stays inside, and the push-out test
// catches it immediately.
//
// Projecting both shapes onto one of the box's own axes is exact: the box
// spans [-h, +h] there by definition, and the capsule spans the segment's
// extent widened by its radius. If those intervals are disjoint on any axis
// the shapes are disjoint, by the separating-axis theorem — so pushing along
// the axis of SMALLEST overlap by that overlap provably separates them. Not
// the true minimum translation, which would need the edge axes too, but always
// sufficient and never short.
//
// It is also the behaviour a character controller wants. A face normal makes a
// character slide along a wall; a true MTV in a corner shoves it diagonally.
func contactCapsuleLikeBox(p0w, p1w component.Vec3, r float32, b *Entry) (Contact, bool) {
	half, axis := obbAxes(b)
	center := centerOf(b)
	p0 := toBoxFrame(p0w, center, axis)
	p1 := toBoxFrame(p1w, center, axis)

	lo := [3]float64{float64(p0.X), float64(p0.Y), float64(p0.Z)}
	hi := lo
	for i, v := range [3]float64{float64(p1.X), float64(p1.Y), float64(p1.Z)} {
		lo[i] = math.Min(lo[i], v)
		hi[i] = math.Max(hi[i], v)
	}

	bestAxis, bestOverlap, bestSign := -1, math.Inf(1), 1.0
	for i := range 3 {
		capLo := lo[i] - float64(r)
		capHi := hi[i] + float64(r)
		// Overlap of [capLo, capHi] with [-half, +half].
		ov := math.Min(capHi, half[i]) - math.Max(capLo, -half[i])
		if ov <= 0 {
			// Disjoint on this axis, so disjoint outright.
			return Contact{}, false
		}
		// Cheaper to leave through the near face or the far one?
		outPositive := half[i] - capLo // push +: capLo must clear +half
		outNegative := capHi + half[i] // push -: capHi must clear -half
		push, sign := outPositive, 1.0
		if outNegative < outPositive {
			push, sign = outNegative, -1.0
		}
		if push < bestOverlap {
			bestOverlap, bestAxis, bestSign = push, i, sign
		}
	}
	if bestAxis < 0 || bestOverlap <= 0 {
		return Contact{}, false
	}

	n := axis[bestAxis].Scale(float32(bestSign))
	// Contact point: the closest approach on the segment, projected onto the
	// box. segmentBoxClosest already finds it exactly, so the manifold and the
	// boolean test cannot disagree about where the pair touches.
	t := segmentBoxClosestT(p0, p1, half)
	s := p0.Add(p1.Sub(p0).Scale(float32(t)))
	q, _, _ := pointBoxClosest(s, half)
	return Contact{
		Normal: n,
		Depth:  float32(bestOverlap),
		Point:  fromBoxFrame(q, center, axis),
	}, true
}

func contactSphereBox(sphere, b *Entry) (Contact, bool) {
	c := centerOf(sphere)
	return contactCapsuleLikeBox(c, c, sphere.Radius, b)
}

func contactCapsuleBox(capsule, b *Entry) (Contact, bool) {
	p0, p1, r := capsuleSegment(capsule)
	return contactCapsuleLikeBox(p0, p1, r, b)
}

// contactSphereSphere, contactCapsuleSphere and contactCapsuleCapsule all
// reduce to contactFromClosest once the cores are located.
func contactSphereSphere(a, b *Entry) (Contact, bool) {
	return contactFromClosest(centerOf(a), centerOf(b), a.Radius, b.Radius)
}

func contactCapsuleSphere(capsule, sphere *Entry) (Contact, bool) {
	p0, p1, r := capsuleSegment(capsule)
	s := closestOnSegment(centerOf(sphere), p0, p1)
	return contactFromClosest(s, centerOf(sphere), r, sphere.Radius)
}

func contactCapsuleCapsule(a, b *Entry) (Contact, bool) {
	a0, a1, ra := capsuleSegment(a)
	b0, b1, rb := capsuleSegment(b)
	ca, cb := segmentSegmentClosest(a0, a1, b0, b1)
	return contactFromClosest(ca, cb, ra, rb)
}

// toBoxFrame expresses a world point in a box's local frame. Projecting onto
// the basis columns IS the inverse rotation, without building one.
func toBoxFrame(p, center component.Vec3, axis [3]component.Vec3) component.Vec3 {
	d := p.Sub(center)
	return component.Vec3{X: d.Dot(axis[0]), Y: d.Dot(axis[1]), Z: d.Dot(axis[2])}
}

// fromBoxFrame is toBoxFrame's inverse.
func fromBoxFrame(p, center component.Vec3, axis [3]component.Vec3) component.Vec3 {
	return center.
		Add(axis[0].Scale(p.X)).
		Add(axis[1].Scale(p.Y)).
		Add(axis[2].Scale(p.Z))
}

// closestOnSegment is the point of [a,b] nearest p.
func closestOnSegment(p, a, b component.Vec3) component.Vec3 {
	ab := b.Sub(a)
	denom := float64(ab.LenSq())
	if denom == 0 {
		return a
	}
	t := clamp01(float64(p.Sub(a).Dot(ab)) / denom)
	return a.Add(ab.Scale(float32(t)))
}
