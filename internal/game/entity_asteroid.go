package game

import (
	"math"
	"math/rand/v2"

	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/internal/item"
	"github.com/zenion/mmoserver/pkg/coords"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// AsteroidBundle is the entity-kind component bundle for asteroids.
// Asteroids deliberately do not carry LockedBy: the "being locked" ring
// is a private alarm for ships only, and replicating LockedBy on
// asteroids would leak the locking player's identity to every viewer.
type AsteroidBundle struct {
	Minable   *gamecomp.Minable
	DebugInfo *mmokit.DebugInfo
}

func (gw *GameWorld) spawnAsteroids() {
	belts := GenerateBelts(gw.RootCell, gw.Config.StationCell)
	total := 0
	for _, belt := range belts {
		for i := 0; i < belt.Count; i++ {
			angle := rand.Float32() * 2 * math.Pi
			dist := rand.Float32() * belt.Radius
			x := belt.CenterX + float32(math.Cos(float64(angle)))*dist
			y := belt.CenterY + float32(math.Sin(float64(angle)))*dist
			// Clamp within cell
			if x < 0 {
				x = 0
			}
			if y < 0 {
				y = 0
			}
			if x >= coords.CellSize {
				x = coords.CellSize - 1
			}
			if y >= coords.CellSize {
				y = coords.CellSize - 1
			}
			// Resource type: 75% dominant, 25% random
			allRes := item.ResourceIDs()
			var itemID uint32
			if rand.Float32() < 0.75 {
				itemID = belt.ResourceItemIDs[rand.IntN(len(belt.ResourceItemIDs))]
			} else {
				itemID = allRes[rand.IntN(len(allRes))]
			}
			gw.spawnAsteroidWithItem(x, y, itemID)
		}
		total += belt.Count
	}
	gw.eng.Log.Log(CatPlayerSpawn, "spawned %d asteroids in %d belts for cell (%d,%d)",
		total, len(belts), gw.RootCell.CellX, gw.RootCell.CellY)
}

func (gw *GameWorld) spawnAsteroid(x, y float32) {
	allRes := item.ResourceIDs()
	gw.spawnAsteroidWithItem(x, y, allRes[rand.IntN(len(allRes))])
}

func (gw *GameWorld) spawnAsteroidWithItem(x, y float32, itemID uint32) {
	radius := gw.Config.AsteroidMinRadius + rand.Float32()*(gw.Config.AsteroidMaxRadius-gw.Config.AsteroidMinRadius)

	layer := gamecomp.LayerTerrain
	if def := item.Get(itemID); def != nil && def.Gaseous {
		layer = 0
	}

	entity := gw.SpawnEntity(
		mmokit.Position{X: x, Y: y},
		mmokit.WithEntityKind(gamecomp.TypeAsteroid),
		mmokit.WithCollider(radius),
		mmokit.WithRotation(rand.Float32()*2*math.Pi),
		mmokit.WithComponents(),
	)

	gw.C.Collider.Get(entity).Layer = layer
	min := gw.C.Minable.Get(entity)
	min.ItemID = itemID
	min.Remaining = radius * 5
}
