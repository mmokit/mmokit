package game

import (
	"testing"

	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// TestPOIGen_Deterministic verifies that the same input produces the
// same output across runs.
func TestPOIGen_Deterministic(t *testing.T) {
	cfg := DefaultGameConfig()
	cell := mmokit.CellCoord{CellX: 3, CellY: 7}
	stationCell := mmokit.CellCoord{CellX: 0, CellY: 0}

	a := GeneratePOIs(cell, stationCell, &cfg, nil)
	b := GeneratePOIs(cell, stationCell, &cfg, nil)

	if len(a) != len(b) {
		t.Fatalf("nondeterministic count: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i] != b[i] {
			t.Errorf("nondeterministic POI[%d]: %+v vs %+v", i, a[i], b[i])
		}
	}
}

// TestPOIGen_StationCellEmpty verifies the station cell has no POIs —
// the testsite dungeon replaces the legacy station-cell combat POI as
// starter PvE content.
func TestPOIGen_StationCellEmpty(t *testing.T) {
	cfg := DefaultGameConfig()
	cell := mmokit.CellCoord{CellX: 0, CellY: 0}
	pois := GeneratePOIs(cell, cell, &cfg, nil)
	if len(pois) != 0 {
		t.Errorf("station cell should have 0 POIs (dungeon replaces it), got %d", len(pois))
	}
}

// TestPOIGen_RespectsClearance verifies POIs avoid belt centers.
func TestPOIGen_RespectsClearance(t *testing.T) {
	cfg := DefaultGameConfig()
	cell := mmokit.CellCoord{CellX: 5, CellY: 5}
	stationCell := mmokit.CellCoord{CellX: 0, CellY: 0}
	belt := AsteroidBelt{CenterX: 5000, CenterY: 5000, Radius: 100}
	pois := GeneratePOIs(cell, stationCell, &cfg, []AsteroidBelt{belt})
	for _, p := range pois {
		dx := p.X - belt.CenterX
		dy := p.Y - belt.CenterY
		distSq := dx*dx + dy*dy
		minDist := belt.Radius + cfg.POIBeltClearance
		if distSq < minDist*minDist {
			t.Errorf("POI too close to belt: dist^2=%.0f, min^2=%.0f", distSq, minDist*minDist)
		}
	}
}

// TestGeneratePOIs_T1CellYieldsMultiplePOIs verifies a cell adjacent to
// the station (T1) yields 3-5 POIs per the tier table.
func TestGeneratePOIs_T1CellYieldsMultiplePOIs(t *testing.T) {
	station := mmokit.CellCoord{CellX: 0, CellY: 0}
	cell := mmokit.CellCoord{CellX: 1, CellY: 0}
	cfg := DefaultGameConfig()
	defs := GeneratePOIs(cell, station, &cfg, nil)
	if len(defs) < 3 || len(defs) > 5 {
		t.Fatalf("T1 cell POIs = %d, want 3..5", len(defs))
	}
}

// TestGeneratePOIs_T3CellYieldsFewPOIs verifies a far cell (T3) yields
// 0-1 POIs per the tier table.
func TestGeneratePOIs_T3CellYieldsFewPOIs(t *testing.T) {
	station := mmokit.CellCoord{CellX: 0, CellY: 0}
	cell := mmokit.CellCoord{CellX: 5, CellY: 0} // ~40k units out, T3
	cfg := DefaultGameConfig()
	defs := GeneratePOIs(cell, station, &cfg, nil)
	if len(defs) > 1 {
		t.Fatalf("T3 cell POIs = %d, want 0..1", len(defs))
	}
}

// TestGeneratePOIs_IntraCellClearance verifies POIs in the same cell
// respect POIIntraCellClearance from each other.
func TestGeneratePOIs_IntraCellClearance(t *testing.T) {
	station := mmokit.CellCoord{CellX: 0, CellY: 0}
	cell := mmokit.CellCoord{CellX: 1, CellY: 0}
	cfg := DefaultGameConfig()
	defs := GeneratePOIs(cell, station, &cfg, nil)
	for i, a := range defs {
		for j, b := range defs {
			if i >= j {
				continue
			}
			dx := a.X - b.X
			dy := a.Y - b.Y
			d2 := dx*dx + dy*dy
			min2 := cfg.POIIntraCellClearance * cfg.POIIntraCellClearance
			if d2 < min2 {
				t.Fatalf("POIs %d,%d too close: %v < %v", i, j, d2, min2)
			}
		}
	}
}

// TestGeneratePOIs_StationCellReturnsNil verifies the station cell
// returns nil (dungeon replaces legacy combat POI there).
func TestGeneratePOIs_StationCellReturnsNil(t *testing.T) {
	station := mmokit.CellCoord{CellX: 0, CellY: 0}
	cfg := DefaultGameConfig()
	defs := GeneratePOIs(station, station, &cfg, nil)
	if defs != nil {
		t.Fatalf("station cell defs = %v, want nil", defs)
	}
}

func TestSpawnPOI_AppliesTierStatMultiplier(t *testing.T) {
	gw, _ := newTestGameWorld()
	baseHP := gw.Config.BrawlerHP

	// Spawn a tier-3 POI; tier label is what matters for the test.
	poiNetID := gw.SpawnPOI(100, 100, 0, StarterArenaIdx, 3)

	// Walk roster NPCs anchored to THIS POI; verify Brawler members
	// have HP scaled by T3 mul. The test world spawns a starter
	// dungeon at (0,0) too, so we must filter by anchor.
	t3Mul := tierDef(3).StatMultiplier
	expected := baseHP * t3Mul

	var brawlerFound bool
	mmokit.ForEach1(gw.stage, func(e mmokit.Entity, ai *gamecomp.NPCAI) {
		if ai.Archetype != ArchetypeBrawler {
			return
		}
		anchor := mmokit.Get[gamecomp.DungeonAnchor](e)
		if anchor == nil || anchor.DungeonNetID != poiNetID {
			return
		}
		h := mmokit.Get[gamecomp.Health](e)
		if h == nil {
			return
		}
		if absf(h.Max-expected) > 0.1 {
			t.Fatalf("T3 Brawler Health.Max = %v, want %v", h.Max, expected)
		}
		brawlerFound = true
	})
	if !brawlerFound {
		t.Fatal("no Brawler in spawned roster — fixture issue")
	}
}
