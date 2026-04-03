package game

import (
	"encoding/binary"
	"hash/fnv"
	"math"
	"math/rand/v2"

	"github.com/zenion/mmoserver/internal/item"
	"github.com/zenion/mmoserver/pkg/coords"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// AsteroidBelt defines a cluster of asteroids within a cell.
type AsteroidBelt struct {
	CenterX, CenterY float32
	Radius           float32
	ResourceItemIDs  []uint32 // 1-2 dominant resource item IDs
	Count            int
}

// meshTestBeltRadius is the spread of asteroids in each cell's portion
// of the cross-boundary test belt.
const meshTestBeltRadius = 20

// meshTestBeltCount is the number of asteroids per cell in the test belt.
const meshTestBeltCount = 25

// meshTestBeltOffset is how far from the shared corner the belt center sits
// (in local coords, inward into the cell).
const meshTestBeltOffset = 15

// meshTestBelt returns a belt positioned near the corner shared by cells
// (0,0), (1,0), (0,1), and (1,1) — if this cell is one of those four.
// Returns nil otherwise.
func meshTestBelt(cell mmokit.CellCoord) *AsteroidBelt {
	var cx, cy float32
	switch {
	case cell.CellX == 0 && cell.CellY == 0:
		cx = coords.CellSize - meshTestBeltOffset
		cy = coords.CellSize - meshTestBeltOffset
	case cell.CellX == 1 && cell.CellY == 0:
		cx = meshTestBeltOffset
		cy = coords.CellSize - meshTestBeltOffset
	case cell.CellX == 0 && cell.CellY == 1:
		cx = coords.CellSize - meshTestBeltOffset
		cy = meshTestBeltOffset
	case cell.CellX == 1 && cell.CellY == 1:
		cx = meshTestBeltOffset
		cy = meshTestBeltOffset
	default:
		return nil
	}
	// Pick first two resource types deterministically.
	resIDs := item.ResourceIDs()
	types := resIDs
	if len(types) > 2 {
		types = types[:2]
	}
	return &AsteroidBelt{
		CenterX:         cx,
		CenterY:         cy,
		Radius:          meshTestBeltRadius,
		ResourceItemIDs: types,
		Count:           meshTestBeltCount,
	}
}

// GenerateBelts creates deterministic asteroid belts for a cell.
func GenerateBelts(cell, stationCell mmokit.CellCoord) []AsteroidBelt {
	// Deterministic seed from cell coords
	h := fnv.New64a()
	buf := make([]byte, 8)
	binary.LittleEndian.PutUint32(buf[0:4], uint32(cell.CellX))
	binary.LittleEndian.PutUint32(buf[4:8], uint32(cell.CellY))
	h.Write(buf)
	rng := rand.New(rand.NewPCG(h.Sum64(), 0))

	isStation := cell == stationCell
	numBelts := 2 + rng.IntN(2) // 2-3 belts
	if isStation {
		numBelts = 1 + rng.IntN(2) // 1-2 belts for station cell
	}

	margin := float32(200)
	usable := coords.CellSize - margin*2
	stationCenter := coords.CellSize / 2

	belts := make([]AsteroidBelt, 0, numBelts)
	for i := 0; i < numBelts; i++ {
		cx := margin + rng.Float32()*usable
		cy := margin + rng.Float32()*usable

		// In station cell, keep belts away from station at center
		if isStation {
			for attempts := 0; attempts < 20; attempts++ {
				dx := cx - stationCenter
				dy := cy - stationCenter
				if float32(math.Sqrt(float64(dx*dx+dy*dy))) > 30 {
					break
				}
				cx = margin + rng.Float32()*usable
				cy = margin + rng.Float32()*usable
			}
		}

		// 1-2 dominant resource types
		allRes := item.ResourceIDs()
		numTypes := 1 + rng.IntN(2)
		types := make([]uint32, numTypes)
		for t := range types {
			types[t] = allRes[rng.IntN(len(allRes))]
		}

		radius := float32(30 + rng.IntN(50))
		count := 20 + rng.IntN(40)

		if isStation {
			radius *= 0.6
			count = count * 2 / 3
		}

		belts = append(belts, AsteroidBelt{
			CenterX:         cx,
			CenterY:         cy,
			Radius:          radius,
			ResourceItemIDs: types,
			Count:           count,
		})
	}
	// Append the cross-boundary meshing test belt if this is one of the 4 cells.
	if mb := meshTestBelt(cell); mb != nil {
		belts = append(belts, *mb)
	}
	return belts
}
