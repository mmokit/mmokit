package coords

import "math"

// SectorSize is the width/height of each sector in local units.
// Defaults to 8192 (2^13) for excellent float32 precision (~0.001 units worst case).
// Call SetSectorSize during initialization to use a different value.
var SectorSize float32 = 8192.0

// SetSectorSize overrides the default sector size. Must be called before any
// coordinate operations (typically during game initialization).
func SetSectorSize(size float32) {
	SectorSize = size
}

// SectorCoord identifies a sector in the infinite grid.
type SectorCoord struct {
	SX, SY int32
}

// WorldPos is a position in the infinite universe: sector index + local offset.
// LX, LY are always in [0, SectorSize).
type WorldPos struct {
	SX, SY int32
	LX, LY float32
}

// Normalize wraps LX/LY into [0, SectorSize) and adjusts sector indices.
func Normalize(w *WorldPos) {
	for w.LX >= SectorSize {
		w.LX -= SectorSize
		w.SX++
	}
	for w.LX < 0 {
		w.LX += SectorSize
		w.SX--
	}
	for w.LY >= SectorSize {
		w.LY -= SectorSize
		w.SY++
	}
	for w.LY < 0 {
		w.LY += SectorSize
		w.SY--
	}
}

// RelativeOffset returns the position of 'to' relative to 'from' as a float32 pair.
// Safe for entities within a few sectors of each other (AoI range).
func RelativeOffset(from, to WorldPos) (float32, float32) {
	dx := float32(to.SX-from.SX)*SectorSize + (to.LX - from.LX)
	dy := float32(to.SY-from.SY)*SectorSize + (to.LY - from.LY)
	return dx, dy
}

// Distance returns the Euclidean distance between two world positions.
func Distance(a, b WorldPos) float32 {
	dx, dy := RelativeOffset(a, b)
	return float32(math.Sqrt(float64(dx*dx + dy*dy)))
}

// FromFlat converts a flat float64 coordinate pair to a WorldPos.
// Used for legacy data migration and admin commands.
func FromFlat(x, y float64) WorldPos {
	sx := int32(math.Floor(x / float64(SectorSize)))
	sy := int32(math.Floor(y / float64(SectorSize)))
	lx := float32(x - float64(sx)*float64(SectorSize))
	ly := float32(y - float64(sy)*float64(SectorSize))
	return WorldPos{SX: sx, SY: sy, LX: lx, LY: ly}
}

// ToFlat converts a WorldPos to flat float64 coordinates.
// Used for logging, persistence, and admin display.
func (w WorldPos) ToFlat() (float64, float64) {
	return float64(w.SX)*float64(SectorSize) + float64(w.LX),
		float64(w.SY)*float64(SectorSize) + float64(w.LY)
}
