package game

import "math"

// boundingRadius computes the half-diagonal of a rectangle (bounding circle radius).
func boundingRadius(width, height float32) float32 {
	halfW := width / 2
	halfH := height / 2
	return float32(math.Sqrt(float64(halfW*halfW + halfH*halfH)))
}
