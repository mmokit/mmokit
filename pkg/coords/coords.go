package coords

import "math"

// CellSize is the width/height of each cell in local units.
// Defaults to 8192 (2^13) for excellent float32 precision (~0.001 units worst case).
// Call SetCellSize during initialization to use a different value.
var CellSize float32 = 8192.0

// SetCellSize overrides the default cell size. Must be called before any
// coordinate operations (typically during game initialization).
func SetCellSize(size float32) {
	CellSize = size
}

// CellCoord identifies a cell in the infinite grid.
type CellCoord struct {
	CellX, CellY int32
}

// WorldPos is a position in the infinite universe: cell index + local offset.
// LocalX, LocalY are always in [0, CellSize).
type WorldPos struct {
	CellX, CellY   int32
	LocalX, LocalY float32
}

// Normalize wraps LocalX/LocalY into [0, CellSize) and adjusts cell indices.
func Normalize(w *WorldPos) {
	for w.LocalX >= CellSize {
		w.LocalX -= CellSize
		w.CellX++
	}
	for w.LocalX < 0 {
		w.LocalX += CellSize
		w.CellX--
	}
	for w.LocalY >= CellSize {
		w.LocalY -= CellSize
		w.CellY++
	}
	for w.LocalY < 0 {
		w.LocalY += CellSize
		w.CellY--
	}
}

// RelativeOffset returns the position of 'to' relative to 'from' as a float32 pair.
// Safe for entities within a few cells of each other (AoI range).
func RelativeOffset(from, to WorldPos) (float32, float32) {
	dx := float32(to.CellX-from.CellX)*CellSize + (to.LocalX - from.LocalX)
	dy := float32(to.CellY-from.CellY)*CellSize + (to.LocalY - from.LocalY)
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
	sx := int32(math.Floor(x / float64(CellSize)))
	sy := int32(math.Floor(y / float64(CellSize)))
	lx := float32(x - float64(sx)*float64(CellSize))
	ly := float32(y - float64(sy)*float64(CellSize))
	return WorldPos{CellX: sx, CellY: sy, LocalX: lx, LocalY: ly}
}

// ToFlat converts a WorldPos to flat float64 coordinates.
// Used for logging, persistence, and admin display.
func (w WorldPos) ToFlat() (float64, float64) {
	return float64(w.CellX)*float64(CellSize) + float64(w.LocalX),
		float64(w.CellY)*float64(CellSize) + float64(w.LocalY)
}
