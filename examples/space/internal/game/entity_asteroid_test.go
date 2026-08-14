package game

import (
	"testing"

	gamecomp "github.com/mmokit/mmokit/examples/space/internal/component"
	"github.com/mmokit/mmokit/pkg/mmokit"
	"github.com/mmokit/mmokit/pkg/spatial"
)

// TestAsteroid_HasLayerProp ensures spawnAsteroid assigns the correct
// collision layer + shape. Asteroids are props — they block projectiles
// + target locks per the dungeon-POI spec, but are transparent to LOS
// raycasts for sight/selection. We assert against the Collider
// component directly because SpatialSystem (which copies
// Collider.Layer/Shape into the spatial grid Entry each tick) is not
// running in the unit-test harness.
func TestAsteroid_HasLayerProp(t *testing.T) {
	gw, _ := newTestGameWorld()

	// Use spawnAsteroidWithItem with a non-gaseous resource (Ore=2) so
	// the test is deterministic. spawnAsteroid picks a random resource,
	// which has a 1-in-N chance of landing on the gaseous "Gas" item
	// (which intentionally sets layer=0 — see entity_asteroid.go).
	gw.spawnAsteroidWithItem(100, 100, 2)

	var asteroidE mmokit.Entity
	var found bool
	mmokit.ForEach1(gw.stage, func(e mmokit.Entity, _ *gamecomp.Minable) {
		asteroidE = e
		found = true
	})
	if !found {
		t.Fatal("no asteroid entity found after spawnAsteroid")
	}

	col := mmokit.Get[mmokit.Collider](asteroidE)
	if col == nil {
		t.Fatal("asteroid entity missing Collider component")
	}
	if col.Layer != spatial.LayerProp {
		t.Errorf("asteroid Collider.Layer = %d, want %d (LayerProp)", col.Layer, spatial.LayerProp)
	}
	if col.Shape != spatial.ShapeCircle {
		t.Errorf("asteroid Collider.Shape = %d, want %d (ShapeCircle)", col.Shape, spatial.ShapeCircle)
	}
}
