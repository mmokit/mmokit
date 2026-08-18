package coords

import "math"

// DefaultCellSize is the cell width/height used when a process configures none.
// 8192 (2^13) gives excellent float32 precision (~0.001 units worst case).
//
// A constant, deliberately. It is the fallback for an unset Config.CellSize,
// never a place to store the live value: cell geometry belongs to a process,
// and a mutable package global cannot express that — two Processes in one
// binary would silently share it, last writer winning. Ask a Stage or a
// Process (both have CellSize()) for the live value.
const DefaultCellSize float32 = 8192.0

// CellCoord identifies a cell in the infinite grid.
type CellCoord struct {
	CellX, CellY int32
}

// WorldPos is a position in the infinite universe: cell index + local offset.
// LocalX, LocalY are always in [0, cellSize).
type WorldPos struct {
	CellX, CellY   int32
	LocalX, LocalY float32
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
