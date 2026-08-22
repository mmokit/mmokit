package spatial

import (
	"math"

	"github.com/mlange-42/ark/ecs"

	"github.com/mmokit/mmokit/pkg/component"
)

// Vec2 is a 2D vector in world coordinates.
type Vec2 struct {
	X, Y float32
}

// Raycast walks the spatial hash buckets along the segment from→to and
// returns the nearest collider whose Layer&layerMask != 0.
//
// hitPoint is the precise contact point on the collider surface. dist
// is the distance from `from` to hitPoint. ok is false on miss; the
// other return values are zero on miss.
func (g *HashGrid) Raycast(from, to Vec2, layerMask uint8) (ecs.Entity, Vec2, float32, bool) {
	dx := to.X - from.X
	dy := to.Y - from.Y
	rayLen := float32(math.Sqrt(float64(dx*dx + dy*dy)))
	if rayLen < 1e-6 {
		return ecs.Entity{}, Vec2{}, 0, false
	}

	var bestEntity ecs.Entity
	var bestHit Vec2
	bestT := float32(2.0) // > 1 so any valid t in [0,1] wins
	found := false

	// Walk every bucket the ray crosses via a 2D DDA. For each bucket,
	// narrow-test every entry whose Layer matches the mask.
	for _, key := range g.bucketsAlongRay(from, to) {
		b := g.buckets[key]
		if b == nil {
			continue
		}
		for _, e := range b.tracked {
			if e.Layer&layerMask == 0 {
				continue
			}
			if e.Shape == component.ShapeSphere {
				t, hitOK := rayCircleHit(from, to, e.X, e.Y, e.Radius)
				if !hitOK {
					continue
				}
				if t < bestT {
					bestT = t
					bestEntity = e.Entity
					bestHit = Vec2{from.X + dx*t, from.Y + dy*t}
					found = true
				}
			}
			if e.Shape == component.ShapeBox {
				t, hitOK := rayRectHit(from, to, e)
				if !hitOK {
					continue
				}
				if t < bestT {
					bestT = t
					bestEntity = e.Entity
					bestHit = Vec2{from.X + dx*t, from.Y + dy*t}
					found = true
				}
			}
		}
	}
	if !found {
		return ecs.Entity{}, Vec2{}, 0, false
	}
	return bestEntity, bestHit, bestT * rayLen, true
}

// bucketsAlongRay returns the bucket keys touched by the segment from→to
// in order of increasing distance from `from`. Uses Amanatides-Woo DDA.
func (g *HashGrid) bucketsAlongRay(from, to Vec2) []BucketKey {
	bx0 := int32(math.Floor(float64(from.X * g.invBucketSize)))
	by0 := int32(math.Floor(float64(from.Y * g.invBucketSize)))
	bx1 := int32(math.Floor(float64(to.X * g.invBucketSize)))
	by1 := int32(math.Floor(float64(to.Y * g.invBucketSize)))

	keys := []BucketKey{{bx0, by0}}
	if bx0 == bx1 && by0 == by1 {
		return keys
	}

	dx := to.X - from.X
	dy := to.Y - from.Y
	stepX, stepY := int32(1), int32(1)
	if dx < 0 {
		stepX = -1
	}
	if dy < 0 {
		stepY = -1
	}

	nextBoundaryX := float32(bx0+(stepX+1)/2) * g.bucketSize
	nextBoundaryY := float32(by0+(stepY+1)/2) * g.bucketSize

	tMaxX := float32(math.Inf(1))
	tMaxY := float32(math.Inf(1))
	if dx != 0 {
		tMaxX = (nextBoundaryX - from.X) / dx
	}
	if dy != 0 {
		tMaxY = (nextBoundaryY - from.Y) / dy
	}
	tDeltaX := float32(math.Inf(1))
	tDeltaY := float32(math.Inf(1))
	if dx != 0 {
		tDeltaX = g.bucketSize / float32(math.Abs(float64(dx)))
	}
	if dy != 0 {
		tDeltaY = g.bucketSize / float32(math.Abs(float64(dy)))
	}

	bx, by := bx0, by0
	for {
		if tMaxX < tMaxY {
			bx += stepX
			tMaxX += tDeltaX
		} else {
			by += stepY
			tMaxY += tDeltaY
		}
		keys = append(keys, BucketKey{bx, by})
		if bx == bx1 && by == by1 {
			return keys
		}
		// Guard against NaN/Inf propagation in degenerate inputs — the DDA
		// terminates naturally for valid float32 endpoints.
		if len(keys) > 4096 {
			return keys
		}
	}
}

// rayCircleHit returns the parametric t∈[0,1] of the nearest entry into
// the circle along the segment from→to, or (0,false) if no hit.
func rayCircleHit(from, to Vec2, cx, cy, r float32) (float32, bool) {
	dx := to.X - from.X
	dy := to.Y - from.Y
	fx := from.X - cx
	fy := from.Y - cy
	a := dx*dx + dy*dy
	b := 2 * (fx*dx + fy*dy)
	c := fx*fx + fy*fy - r*r
	disc := b*b - 4*a*c
	if disc < 0 {
		return 0, false
	}
	sq := float32(math.Sqrt(float64(disc)))
	t1 := (-b - sq) / (2 * a)
	t2 := (-b + sq) / (2 * a)
	// Want the nearest hit in [0,1].
	if t1 >= 0 && t1 <= 1 {
		return t1, true
	}
	if t2 >= 0 && t2 <= 1 {
		return t2, true
	}
	return 0, false
}

// rayRectHit returns t∈[0,1] for the nearest ray-vs-OBB entry, or false.
// Transforms the ray into the rect's local frame and runs a 2D slab test.
func rayRectHit(from, to Vec2, e Entry) (float32, bool) {
	cosA := float32(math.Cos(float64(-e.Rotation)))
	sinA := float32(math.Sin(float64(-e.Rotation)))
	fx := from.X - e.X
	fy := from.Y - e.Y
	tx := to.X - e.X
	ty := to.Y - e.Y
	lfx := cosA*fx - sinA*fy
	lfy := sinA*fx + cosA*fy
	ltx := cosA*tx - sinA*ty
	lty := sinA*tx + cosA*ty
	dx := ltx - lfx
	dy := lty - lfy

	hx := e.Width / 2
	hy := e.Height / 2

	tMin := float32(-math.MaxFloat32)
	tMax := float32(math.MaxFloat32)

	// X slab
	if math.Abs(float64(dx)) < 1e-6 {
		if lfx < -hx || lfx > hx {
			return 0, false
		}
	} else {
		t1 := (-hx - lfx) / dx
		t2 := (hx - lfx) / dx
		if t1 > t2 {
			t1, t2 = t2, t1
		}
		if t1 > tMin {
			tMin = t1
		}
		if t2 < tMax {
			tMax = t2
		}
		if tMin > tMax {
			return 0, false
		}
	}
	// Y slab
	if math.Abs(float64(dy)) < 1e-6 {
		if lfy < -hy || lfy > hy {
			return 0, false
		}
	} else {
		t1 := (-hy - lfy) / dy
		t2 := (hy - lfy) / dy
		if t1 > t2 {
			t1, t2 = t2, t1
		}
		if t1 > tMin {
			tMin = t1
		}
		if t2 < tMax {
			tMax = t2
		}
		if tMin > tMax {
			return 0, false
		}
	}
	if tMin < 0 || tMin > 1 {
		return 0, false
	}
	return tMin, true
}
