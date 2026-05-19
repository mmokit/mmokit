package game

import (
	"encoding/binary"
	"hash/fnv"
	"math/rand/v2"

	"github.com/zenion/mmoserver/pkg/coords"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// POIDef is the procgen output for a single POI to be spawned in a cell.
type POIDef struct {
	X, Y      float32
	Type      uint8
	RosterIdx uint16
	Tier      uint8 // 1..3
}

// GeneratePOIs returns the procgen list of POIs for a cell. Mirrors
// GenerateBelts: deterministic FNV(cellX, cellY, "poi") seed. The
// station cell intentionally returns no POIs — the testsite dungeon
// (see SpawnDungeonFromGraph) is the starter PvE content for that
// cell. Non-station cells get a tier-driven count of POIs; each POI's
// tier is computed from its actual world position so boundary cells
// can yield mixed-tier rosters.
func GeneratePOIs(cell, stationCell mmokit.CellCoord, cfg *GameConfig, belts []AsteroidBelt) []POIDef {
	if cell == stationCell {
		return nil
	}

	cellTier := TierForCellCenter(cell, stationCell)
	td := tierDef(cellTier)

	h := fnv.New64a()
	buf := make([]byte, 12)
	binary.LittleEndian.PutUint32(buf[0:4], uint32(cell.CellX))
	binary.LittleEndian.PutUint32(buf[4:8], uint32(cell.CellY))
	copy(buf[8:12], []byte("poi_"))
	h.Write(buf)
	rng := rand.New(rand.NewPCG(h.Sum64(), 1))

	n := td.POIsPerCell[0]
	span := td.POIsPerCell[1] - td.POIsPerCell[0]
	if span > 0 {
		n += rng.IntN(span + 1)
	}
	if n == 0 {
		return nil
	}

	defs := make([]POIDef, 0, n)
	for i := 0; i < n; i++ {
		x, y, ok := placePOI(rng, cfg, belts, defs)
		if !ok {
			continue
		}
		worldDist := distFromStation(cell, x, y, stationCell)
		poiTier := tierForDist(worldDist)
		roster := pickRosterForTier(rng, poiTier)
		defs = append(defs, POIDef{X: x, Y: y, Type: 0, RosterIdx: roster, Tier: poiTier})
	}
	return defs
}

// placePOI rejection-samples a position inside the cell, respecting belt
// clearance, placement margin, and clearance from already-placed POIs in
// the same cell. Returns (x, y, true) on success, (0, 0, false) after
// the attempt budget is exhausted.
func placePOI(rng *rand.Rand, cfg *GameConfig, belts []AsteroidBelt, existing []POIDef) (float32, float32, bool) {
	margin := cfg.POIPlacementMargin
	usable := coords.CellSize - margin*2
	intraMin2 := cfg.POIIntraCellClearance * cfg.POIIntraCellClearance

	for attempt := 0; attempt < 30; attempt++ {
		x := margin + rng.Float32()*usable
		y := margin + rng.Float32()*usable

		bad := false
		for _, b := range belts {
			dx := x - b.CenterX
			dy := y - b.CenterY
			if dx*dx+dy*dy < (b.Radius+cfg.POIBeltClearance)*(b.Radius+cfg.POIBeltClearance) {
				bad = true
				break
			}
		}
		if bad {
			continue
		}

		for _, p := range existing {
			dx := x - p.X
			dy := y - p.Y
			if dx*dx+dy*dy < intraMin2 {
				bad = true
				break
			}
		}
		if bad {
			continue
		}

		return x, y, true
	}
	return 0, 0, false
}

// pickRosterForTier returns one of the rosters eligible at the given tier.
// Currently uniform selection; weights can be added later.
func pickRosterForTier(rng *rand.Rand, tier uint8) uint16 {
	td := tierDef(tier)
	if len(td.Rosters) == 0 {
		return StarterArenaIdx
	}
	return td.Rosters[rng.IntN(len(td.Rosters))]
}
