package universe

import (
	"encoding/binary"
	"hash/fnv"
	"math"
	"math/rand/v2"

	"github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/pkg/coords"
)

// AsteroidBelt defines a cluster of asteroids within a sector.
type AsteroidBelt struct {
	CenterX, CenterY float32
	Radius            float32
	ResourceTypes     []uint8 // 1-2 dominant types
	Count             int
}

// GenerateBelts creates deterministic asteroid belts for a sector.
func GenerateBelts(sector component.SectorCoord) []AsteroidBelt {
	// Deterministic seed from sector coords
	h := fnv.New64a()
	buf := make([]byte, 8)
	binary.LittleEndian.PutUint32(buf[0:4], uint32(sector.SX))
	binary.LittleEndian.PutUint32(buf[4:8], uint32(sector.SY))
	h.Write(buf)
	rng := rand.New(rand.NewPCG(h.Sum64(), 0))

	isStation := sector.SX == 0 && sector.SY == 0
	numBelts := 2 + rng.IntN(2) // 2-3 belts
	if isStation {
		numBelts = 1 + rng.IntN(2) // 1-2 belts for station sector
	}

	margin := float32(200)
	usable := coords.SectorSize - margin*2
	stationCenter := coords.SectorSize / 2

	belts := make([]AsteroidBelt, 0, numBelts)
	for i := 0; i < numBelts; i++ {
		cx := margin + rng.Float32()*usable
		cy := margin + rng.Float32()*usable

		// In station sector, keep belts away from station at center
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
		numTypes := 1 + rng.IntN(2)
		types := make([]uint8, numTypes)
		for t := range types {
			types[t] = uint8(rng.IntN(4))
		}

		radius := float32(30 + rng.IntN(50))
		count := 20 + rng.IntN(40)

		if isStation {
			radius *= 0.6
			count = count * 2 / 3
		}

		belts = append(belts, AsteroidBelt{
			CenterX:       cx,
			CenterY:       cy,
			Radius:        radius,
			ResourceTypes: types,
			Count:         count,
		})
	}
	return belts
}
