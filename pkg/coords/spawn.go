package coords

// SpawnPoint is an absolute world-space coordinate. Used for the login
// fallback position and any other game-defined anchor point that must
// survive cell split/merge without re-computation.
type SpawnPoint struct {
	X, Y float32
}

// WorldCenterOfCell returns the world-space center of the given base-cell
// coordinate as a SpawnPoint. Topology-independent: if the cell has been
// split or merged at spawn time, the returned world point still lands in
// whichever child/parent currently owns it.
//
// Example: WorldCenterOfCell(1, 2) with CellSize=8192 → SpawnPoint{X:12288, Y:20480}
func WorldCenterOfCell(cellX, cellY int32) SpawnPoint {
	return SpawnPoint{
		X: float32(cellX)*CellSize + CellSize/2,
		Y: float32(cellY)*CellSize + CellSize/2,
	}
}
