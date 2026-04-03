// Package quantize provides pure functions for quantizing floating-point values
// into fixed-width integers for compact binary serialization. All functions are
// stateless and take parameters — no globals.
package quantize

import "math"

// Pos quantizes a cell-local position in [0, cellSize) to uint16 [0, 65535].
func Pos(pos, cellSize float32) uint16 {
	if pos < 0 {
		pos = 0
	} else if pos >= cellSize {
		pos = cellSize - 1e-6
	}
	return uint16(pos / cellSize * 65535)
}

// UnPos dequantizes a uint16 back to a cell-local position in [0, cellSize).
func UnPos(q uint16, cellSize float32) float32 {
	return float32(q) / 65535 * cellSize
}

// Angle quantizes an angle in radians to uint16 [0, 65535].
// The input is normalized to [-pi, pi] first (handles accumulated angles
// that drift outside this range, e.g. from += rotation deltas).
func Angle(radians float32) uint16 {
	// Normalize to [-pi, pi] using atan2(sin, cos) — handles any range.
	radians = float32(math.Atan2(math.Sin(float64(radians)), math.Cos(float64(radians))))
	// Map [-pi, pi] -> [0, 1]
	normalized := (radians + math.Pi) / (2 * math.Pi)
	return uint16(normalized * 65535)
}

// UnAngle dequantizes a uint16 back to radians in [-pi, pi].
func UnAngle(q uint16) float32 {
	normalized := float32(q) / 65535
	return normalized*(2*math.Pi) - math.Pi
}

// Norm quantizes a value in [0, 1] to uint8 [0, 255].
func Norm(v float32) uint8 {
	if v < 0 {
		v = 0
	} else if v > 1 {
		v = 1
	}
	return uint8(v * 255)
}

// UnNorm dequantizes a uint8 back to a value in [0, 1].
func UnNorm(q uint8) float32 {
	return float32(q) / 255
}

// Vel quantizes a velocity component with a configurable scale (max absolute value)
// to int16 [-32767, 32767].
func Vel(v, scale float32) int16 {
	if scale <= 0 {
		return 0
	}
	ratio := v / scale
	if ratio < -1 {
		ratio = -1
	} else if ratio > 1 {
		ratio = 1
	}
	return int16(ratio * 32767)
}

// UnVel dequantizes an int16 back to a velocity component.
func UnVel(q int16, scale float32) float32 {
	return float32(q) / 32767 * scale
}

// Rel quantizes a viewer-relative offset in [-halfRange, +halfRange] to int16.
func Rel(offset, halfRange float32) int16 {
	if halfRange <= 0 {
		return 0
	}
	ratio := offset / halfRange
	if ratio < -1 {
		ratio = -1
	} else if ratio > 1 {
		ratio = 1
	}
	return int16(ratio * 32767)
}

// UnRel dequantizes an int16 back to a viewer-relative offset.
func UnRel(q int16, halfRange float32) float32 {
	return float32(q) / 32767 * halfRange
}
