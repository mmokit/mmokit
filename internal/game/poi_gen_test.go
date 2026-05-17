package game

import (
	"testing"

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
