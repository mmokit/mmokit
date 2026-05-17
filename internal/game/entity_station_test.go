package game

import (
	"testing"

	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/pkg/mmokit"
	"github.com/zenion/mmoserver/pkg/spatial"
)

// TestStation_HasLayerStatic ensures SpawnStation assigns the correct
// collision layer + shape. Stations are walls — they must block LOS,
// projectiles, and target locks per the dungeon-POI spec. We assert
// against the Collider component directly because SpatialSystem (which
// copies Collider.Layer/Shape into the spatial grid Entry each tick) is
// not running in the unit-test harness.
func TestStation_HasLayerStatic(t *testing.T) {
	gw, _ := newTestGameWorld()

	gw.SpawnStation()

	var stationE mmokit.Entity
	var found bool
	mmokit.ForEach1(gw.stage, func(e mmokit.Entity, _ *gamecomp.Station) {
		stationE = e
		found = true
	})
	if !found {
		t.Fatal("no station entity found after SpawnStation")
	}

	col := mmokit.Get[mmokit.Collider](stationE)
	if col == nil {
		t.Fatal("station entity missing Collider component")
	}
	if col.Layer != spatial.LayerStatic {
		t.Errorf("station Collider.Layer = %d, want %d (LayerStatic)", col.Layer, spatial.LayerStatic)
	}
	if col.Shape != spatial.ShapeCircle {
		t.Errorf("station Collider.Shape = %d, want %d (ShapeCircle)", col.Shape, spatial.ShapeCircle)
	}
}
