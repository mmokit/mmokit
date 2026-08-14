package game

import (
	"math"
	"math/rand/v2"

	gamecomp "github.com/mmokit/mmokit/examples/space/internal/component"
	"github.com/mmokit/mmokit/examples/space/internal/item"
	"github.com/mmokit/mmokit/pkg/mmokit"
	"github.com/mmokit/mmokit/pkg/spatial"
)

// AsteroidBundle is the entity-kind component bundle for asteroids.
// Asteroids deliberately do not carry LockedBy: the "being locked" ring
// is a private alarm for ships only, and replicating LockedBy on
// asteroids would leak the locking player's identity to every viewer.
//
// PlacedID is only present on asteroids spawned as part of a world-manifest
// belt — the world editor uses it to despawn the whole belt with one
// DespawnPlacedByID sweep. Asteroids spawned for dungeon-debris scatter /
// tests leave the field absent; `mmokit:"optional"` lets the framework
// treat it as transfer-preserved but spawn-omittable.
type AsteroidBundle struct {
	Minable  *gamecomp.Minable
	PlacedID *gamecomp.PlacedID `mmokit:"optional"`
}

// spawnAsteroid is the random-resource convenience used by tests and
// the dungeon procgen wall-asteroid scatter. The per-cell bootstrap no
// longer calls it directly — asteroids spawn from explicit world.Belt
// entries via SpawnBelt (Task 8).
func (gw *GameWorld) spawnAsteroid(x, y float32) {
	allRes := item.ResourceIDs()
	gw.spawnAsteroidWithItem(x, y, allRes[rand.IntN(len(allRes))])
}

// spawnAsteroidWithItem creates a single asteroid carrying the given
// resource item. Kept as a private helper for Task 8's belt-scatter
// loop and for the standalone unit tests that need a deterministic
// resource type.
func (gw *GameWorld) spawnAsteroidWithItem(x, y float32, itemID uint32) {
	radius := gw.Config.AsteroidMinRadius + rand.Float32()*(gw.Config.AsteroidMaxRadius-gw.Config.AsteroidMinRadius)
	gw.spawnAsteroidAt(x, y, itemID, radius, rand.Float32()*2*math.Pi, "")
}

// spawnAsteroidSeeded creates a single asteroid with resource, radius,
// and rotation all drawn from the supplied RNG. Used by tests + dungeon
// debris scatter (no PlacedID tag — the asteroid isn't owned by a
// world-manifest belt).
func (gw *GameWorld) spawnAsteroidSeeded(x, y float32, rng *rand.Rand) {
	allRes := item.ResourceIDs()
	if len(allRes) == 0 {
		return
	}
	itemID := allRes[rng.IntN(len(allRes))]
	radius := gw.Config.AsteroidMinRadius + rng.Float32()*(gw.Config.AsteroidMaxRadius-gw.Config.AsteroidMinRadius)
	gw.spawnAsteroidAt(x, y, itemID, radius, rng.Float32()*2*math.Pi, "")
}

// spawnAsteroidSeededTagged is the SpawnBelt variant: same RNG-driven
// scatter as spawnAsteroidSeeded but tags the asteroid with a
// PlacedID{placedID} so DespawnPlacedByID cleans up the whole belt.
func (gw *GameWorld) spawnAsteroidSeededTagged(x, y float32, rng *rand.Rand, placedID string) {
	allRes := item.ResourceIDs()
	if len(allRes) == 0 {
		return
	}
	itemID := allRes[rng.IntN(len(allRes))]
	radius := gw.Config.AsteroidMinRadius + rng.Float32()*(gw.Config.AsteroidMaxRadius-gw.Config.AsteroidMinRadius)
	gw.spawnAsteroidAt(x, y, itemID, radius, rng.Float32()*2*math.Pi, placedID)
}

// spawnAsteroidAt is the shared core: place a single asteroid with the
// caller-chosen item, radius, and rotation. Layer is derived from the
// item's Gaseous flag. An empty placedID skips the PlacedID tag.
func (gw *GameWorld) spawnAsteroidAt(x, y float32, itemID uint32, radius, angle float32, placedID string) {
	layer := spatial.LayerProp
	if def := item.Get(itemID); def != nil && def.Gaseous {
		layer = 0
	}

	components := []any{
		mmokit.Position{X: x, Y: y},
		mmokit.EntityKind{Type: gamecomp.KindAsteroid},
		mmokit.Collider{Radius: radius, Shape: spatial.ShapeCircle, Layer: layer},
		mmokit.Rotation{Angle: angle},
		gamecomp.Minable{ItemID: itemID, Remaining: radius * 5},
	}
	if placedID != "" {
		components = append(components, gamecomp.PlacedID{ID: placedID})
	}
	gw.stage.Spawn(components...)
}
