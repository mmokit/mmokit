package game

import (
	"math"
	"math/rand/v2"

	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/internal/item"
	"github.com/zenion/mmoserver/pkg/mmokit"
	"github.com/zenion/mmoserver/pkg/spatial"
)

// AsteroidBundle is the entity-kind component bundle for asteroids.
// Asteroids deliberately do not carry LockedBy: the "being locked" ring
// is a private alarm for ships only, and replicating LockedBy on
// asteroids would leak the locking player's identity to every viewer.
type AsteroidBundle struct {
	Minable *gamecomp.Minable
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

	layer := spatial.LayerProp
	if def := item.Get(itemID); def != nil && def.Gaseous {
		layer = 0
	}

	gw.stage.Spawn(
		mmokit.Position{X: x, Y: y},
		mmokit.EntityKind{Type: gamecomp.KindAsteroid},
		mmokit.Collider{Radius: radius, Shape: spatial.ShapeCircle, Layer: layer},
		mmokit.Rotation{Angle: rand.Float32() * 2 * math.Pi},
		gamecomp.Minable{ItemID: itemID, Remaining: radius * 5},
	)
}
