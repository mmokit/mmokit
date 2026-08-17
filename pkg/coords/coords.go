package coords

import "math"

// DefaultCellSize is the cell width/height used when a process configures none.
// 8192 (2^13) gives excellent float32 precision (~0.001 units worst case).
//
// A constant, deliberately. It is the fallback for an unset Config.CellSize,
// not a place to store the live value — see CellSize below for why that
// distinction is being drawn.
const DefaultCellSize float32 = 8192.0

// CellSize is the width/height of each cell in local units.
//
// DEPRECATED, and being removed by CE-010. A process's cell geometry is a
// property of that process, and a mutable package global cannot express that:
// two Processes in one binary silently share it, last writer wins, while
// (*Process).CellSize() and Stage.CellSize() give each the right answer.
// Every remaining reader of this var is a site still to convert. Do not add
// new ones.
var CellSize float32 = DefaultCellSize

// SetCellSize overrides the default cell size. Must be called before any
// coordinate operations (typically during game initialization).
//
// DEPRECATED alongside CellSize: set Config.CellSize instead, which scopes the
// value to one process.
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
func FromFlat(x, y float64, cellSize float32) WorldPos {
	sx := int32(math.Floor(x / float64(cellSize)))
	sy := int32(math.Floor(y / float64(cellSize)))
	lx := float32(x - float64(sx)*float64(cellSize))
	ly := float32(y - float64(sy)*float64(cellSize))
	return WorldPos{CellX: sx, CellY: sy, LocalX: lx, LocalY: ly}
}

// ToFlat converts a WorldPos to flat float64 coordinates.
// Used for logging, persistence, and admin display.
func (w WorldPos) ToFlat() (float64, float64) {
	return float64(w.CellX)*float64(CellSize) + float64(w.LocalX),
		float64(w.CellY)*float64(CellSize) + float64(w.LocalY)
}
