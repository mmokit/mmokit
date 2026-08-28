package spatial

import (
	"math"

	"github.com/mlange-42/ark/ecs"

	"github.com/mmokit/mmokit/pkg/component"
)

// Three-dimensional ray casting.
//
// THE BUCKET WALK STAYS TWO-DIMENSIONAL, and that is a proof rather than an
// approximation. Buckets are columns keyed on X and Y with no Z term, so the
// set of buckets a 3D ray passes through is exactly the set its XY projection
// passes through. The existing DDA already enumerates those, so it needs no Z
// handling at all — only the per-entry test becomes three-dimensional.

// RayFilter reports whether an entry is eligible to be hit. Return false to
// make the ray pass through it as though it were not there.
//
// THIS EXISTS BECAUSE "ignore my own collider" CANNOT BE DONE AFTERWARDS. A
// raycast returns the NEAREST hit, so a caller that casts and then checks
// `hit == self` is only correct while self is never actually the nearest hit.
// The moment it is — a caster on a masked layer, standing at the ray origin —
// that check reports a clear line and masks every wall behind it. Excluding
// during the walk is the only version that composes.
type RayFilter func(e *Entry) bool

// RayHit describes where a ray met a collider.
type RayHit struct {
	Entity ecs.Entity
	// Point is the world-space contact point on the collider's surface.
	Point component.Vec3
	// T is the parameter along from→to, in [0,1].
	T float32
	// Dist is the distance from the ray's origin to Point.
	Dist float32
}

// Raycast returns the nearest collider along from→to whose Layer intersects
// layerMask and which filter accepts. A nil filter accepts everything.
func (g *HashGrid) Raycast(from, to component.Vec3, layerMask uint8, filter RayFilter) (RayHit, bool) {
	d := to.Sub(from)
	rayLen := float64(d.Len())
	if rayLen < 1e-6 {
		return RayHit{}, false
	}

	best := RayHit{T: 2} // > 1 so any valid t wins
	found := false

	// The XY projection of the ray determines the columns it crosses — see
	// the package note above.
	fromXY := Vec2{X: from.X, Y: from.Y}
	toXY := Vec2{X: to.X, Y: to.Y}

	for _, key := range g.bucketsAlongRay(fromXY, toXY) {
		b := g.buckets[key]
		if b == nil {
			continue
		}
		// Indexed, not ranged by value: Entry is 56 bytes and this runs for
		// every entry in every bucket along the ray, with no distance
		// prefilter ahead of it.
		for i := range b.tracked {
			e := &b.tracked[i]
			if e.Layer&layerMask == 0 {
				continue
			}
			if filter != nil && !filter(e) {
				continue
			}
			t, ok := rayTable[e.Shape](from, to, e)
			if !ok || t >= best.T {
				continue
			}
			best = RayHit{
				Entity: e.Entity,
				Point:  from.Add(d.Scale(t)),
				T:      t,
				Dist:   t * float32(rayLen),
			}
			found = true
		}
	}
	if !found {
		return RayHit{}, false
	}
	return best, true
}

// --- per-shape ray tests ---------------------------------------------------

// rayHitSphere solves |from + t*d - c|^2 = r^2 for the smaller root in [0,1].
//
// An origin INSIDE the sphere returns t=0 rather than the exit root: a ray
// starting inside a collider has already hit it, and reporting the far side
// would let a caster shoot out through the shape it is standing in.
func rayHitSphere(from, to component.Vec3, e *Entry) (float32, bool) {
	d := to.Sub(from)
	m := from.Sub(centerOf(e))
	a := float64(d.LenSq())
	if a == 0 {
		return 0, false
	}
	b := float64(m.Dot(d))
	c := float64(m.LenSq()) - float64(e.Radius)*float64(e.Radius)

	if c <= 0 {
		return 0, true // origin inside
	}
	disc := b*b - a*c
	if disc < 0 {
		return 0, false
	}
	t := (-b - math.Sqrt(disc)) / a
	if t < 0 || t > 1 {
		return 0, false
	}
	return float32(t), true
}

// rayHitBox is a slab test in the box's own frame, where it is an
// axis-aligned box centred at the origin.
func rayHitBox(from, to component.Vec3, e *Entry) (float32, bool) {
	half, axis := obbAxes(e)
	center := centerOf(e)
	o := toBoxFrame(from, center, axis)
	// A direction is a difference of points, so it rotates without the
	// translation.
	dir := toBoxFrame(to, center, axis).Sub(o)

	op := [3]float64{float64(o.X), float64(o.Y), float64(o.Z)}
	dp := [3]float64{float64(dir.X), float64(dir.Y), float64(dir.Z)}

	tmin, tmax := 0.0, 1.0
	for i := range 3 {
		if math.Abs(dp[i]) < 1e-12 {
			// Parallel to this slab: a miss only if the origin is outside it.
			if math.Abs(op[i]) > half[i] {
				return 0, false
			}
			continue
		}
		inv := 1 / dp[i]
		t1 := (-half[i] - op[i]) * inv
		t2 := (half[i] - op[i]) * inv
		if t1 > t2 {
			t1, t2 = t2, t1
		}
		tmin = math.Max(tmin, t1)
		tmax = math.Min(tmax, t2)
		if tmin > tmax {
			return 0, false
		}
	}
	return float32(tmin), true
}

// rayHitCapsule finds the first parameter at which the ray's distance to the
// capsule's segment falls to its radius.
//
// Decomposed as the infinite cylinder about the segment, clipped to the
// segment's extent, plus the two cap spheres — the standard construction,
// because the closest-approach distance between a ray and a segment is not
// invertible in closed form but each of those three pieces is.
func rayHitCapsule(from, to component.Vec3, e *Entry) (float32, bool) {
	p, q, r := capsuleSegment(e)
	best, found := float32(2), false

	// The two caps.
	for _, c := range [2]component.Vec3{p, q} {
		cap := Entry{X: c.X, Y: c.Y, Z: c.Z, Radius: r}
		if t, ok := rayHitSphere(from, to, &cap); ok && t < best {
			best, found = t, true
		}
	}

	// The finite cylinder.
	d := q.Sub(p)     // axis
	m := from.Sub(p)  // origin relative to the axis base
	n := to.Sub(from) // ray direction over t in [0,1]
	dd := float64(d.LenSq())
	if dd < 1e-12 {
		// Degenerate segment: the caps above already covered it.
		if found {
			return best, true
		}
		return 0, false
	}
	nd := float64(n.Dot(d))
	md := float64(m.Dot(d))
	nn := float64(n.LenSq())
	mn := float64(m.Dot(n))
	rr := float64(r) * float64(r)

	a := dd*nn - nd*nd
	c := dd*(float64(m.LenSq())-rr) - md*md

	if math.Abs(a) > 1e-12 {
		b := dd*mn - nd*md
		disc := b*b - a*c
		if disc >= 0 {
			t := (-b - math.Sqrt(disc)) / a
			if t < 0 {
				t = 0
			}
			if t <= 1 {
				// Inside the segment's extent?
				if along := md + t*nd; along >= 0 && along <= dd {
					if float32(t) < best {
						best, found = float32(t), true
					}
				}
			}
		}
	} else if c <= 0 {
		// Parallel and already within the cylinder radius: the caps decide.
		if found {
			return best, true
		}
	}

	if !found {
		return 0, false
	}
	return best, true
}
