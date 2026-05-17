package game

import (
	"encoding/binary"
	"hash/fnv"
	"math"
	"math/rand/v2"

	"github.com/zenion/mmoserver/pkg/coords"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// POIDef is the procgen output for a single POI to be spawned in a cell.
type POIDef struct {
	X, Y      float32
	Type      uint8
	RosterIdx uint16
}

// GeneratePOIs returns the procgen list of POIs for a cell. Mirrors
// GenerateBelts: deterministic FNV(cellX, cellY, "poi") seed. The
// station cell intentionally returns no POIs — the testsite dungeon
// (see SpawnDungeonFromGraph) is the starter PvE content for that
// cell. Non-station cells get 0–1 POIs with POIPerCellProbability.
func GeneratePOIs(cell, stationCell mmokit.CellCoord, cfg *GameConfig, belts []AsteroidBelt) []POIDef {
	h := fnv.New64a()
	buf := make([]byte, 12)
	binary.LittleEndian.PutUint32(buf[0:4], uint32(cell.CellX))
	binary.LittleEndian.PutUint32(buf[4:8], uint32(cell.CellY))
	copy(buf[8:12], []byte("poi_"))
	h.Write(buf)
	rng := rand.New(rand.NewPCG(h.Sum64(), 1))

	if cell == stationCell {
		// Dungeon replaces the legacy station-cell combat POI.
		return nil
	}

	if rng.Float32() > cfg.POIPerCellProbability {
		return nil
	}

	margin := cfg.POIPlacementMargin
	usable := coords.CellSize - margin*2

	for attempt := 0; attempt < 30; attempt++ {
		x := margin + rng.Float32()*usable
		y := margin + rng.Float32()*usable

		// Stay away from belt centers.
		bad := false
		for _, b := range belts {
			dx := x - b.CenterX
			dy := y - b.CenterY
			if float32(math.Sqrt(float64(dx*dx+dy*dy))) < b.Radius+cfg.POIBeltClearance {
				bad = true
				break
			}
		}
		if bad {
			continue
		}

		return []POIDef{{X: x, Y: y, Type: 0 /* combat */, RosterIdx: 0}}
	}
	return nil
}
