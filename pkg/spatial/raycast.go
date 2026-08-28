package spatial

import (
	"math"
)

// Vec2 is a 2D vector in world coordinates.
type Vec2 struct {
	X, Y float32
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
