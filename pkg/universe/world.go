package universe

// NeighborInfo describes a neighbor cell's offset relative to the current cell.
type NeighborInfo struct {
	CellID string
	DX, DY int32 // cell offset from this cell
}
