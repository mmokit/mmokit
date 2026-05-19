package game

import (
	"math"

	"github.com/zenion/mmoserver/pkg/coords"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// tierForDist returns the tier of a world-space distance from the
// station. Walks tierTable from outermost inward; first band whose
// InnerRadius is <= dist wins.
func tierForDist(d float32) uint8 {
	for i := len(tierTable) - 1; i >= 0; i-- {
		if d >= tierTable[i].InnerRadius {
			return tierTable[i].Tier
		}
	}
	return tierTable[0].Tier
}

// distFromStation returns the absolute world-space distance from the
// given cell-local position to the station center.
func distFromStation(cell mmokit.CellCoord, localX, localY float32,
	stationCell mmokit.CellCoord) float32 {

	dCellX := float32(cell.CellX-stationCell.CellX) * coords.CellSize
	dCellY := float32(cell.CellY-stationCell.CellY) * coords.CellSize
	stationLocalX := coords.CellSize / 2
	stationLocalY := coords.CellSize / 2
	dx := dCellX + localX - stationLocalX
	dy := dCellY + localY - stationLocalY
	return float32(math.Sqrt(float64(dx*dx + dy*dy)))
}

// TierForCellCenter returns the tier of a cell's center.
func TierForCellCenter(cell, stationCell mmokit.CellCoord) uint8 {
	centerLocal := coords.CellSize / 2
	d := distFromStation(cell, centerLocal, centerLocal, stationCell)
	return tierForDist(d)
}

// TierForDist is the exported wrapper around tierForDist for callers
// outside this package (e.g. the operator-driven poi.spawn console verb).
func TierForDist(d float32) uint8 { return tierForDist(d) }

// DistFromStation is the exported wrapper around distFromStation for
// callers outside this package.
func DistFromStation(cell mmokit.CellCoord, localX, localY float32, stationCell mmokit.CellCoord) float32 {
	return distFromStation(cell, localX, localY, stationCell)
}
