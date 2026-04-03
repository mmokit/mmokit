package game

import (
	"math"
	"math/rand/v2"

	"github.com/mlange-42/ark/ecs"

	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/internal/item"
	"github.com/zenion/mmoserver/pkg/coords"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

type asteroidMappers struct {
	base    *ecs.Map6[mmokit.Position, mmokit.Velocity, mmokit.Rotation, mmokit.Collider, mmokit.NetworkID, mmokit.EntityKind]
	minable *ecs.Map1[gamecomp.Minable]
}

func initAsteroidEntity(gw *GameWorld) {
	m := &asteroidMappers{
		base:    ecs.NewMap6[mmokit.Position, mmokit.Velocity, mmokit.Rotation, mmokit.Collider, mmokit.NetworkID, mmokit.EntityKind](gw.ECS),
		minable: ecs.NewMap1[gamecomp.Minable](gw.ECS),
	}

	gw.Registry.Register(mmokit.EntityDef{
		Name:        "asteroid",
		Description: "mineable asteroid",
		EntityType:  gamecomp.TypeAsteroid,
		Spawnable:   true,
		Mappers:     m,
		Spawn: func(x, y float32) {
			gw.spawnAsteroid(x, y)
		},
	})
}

func (gw *GameWorld) spawnAsteroids() {
	belts := GenerateBelts(gw.Cell, gw.Config.StationCell)
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
	gw.Log.Log(CatPlayerSpawn, "spawned %d asteroids in %d belts for cell (%d,%d)",
		total, len(belts), gw.Cell.CellX, gw.Cell.CellY)
}

func (gw *GameWorld) spawnAsteroid(x, y float32) {
	allRes := item.ResourceIDs()
	gw.spawnAsteroidWithItem(x, y, allRes[rand.IntN(len(allRes))])
}

func (gw *GameWorld) spawnAsteroidWithItem(x, y float32, itemID uint32) {
	m := gw.Registry.ByType(gamecomp.TypeAsteroid).Mappers.(*asteroidMappers)
	netID := gw.NextNetID()
	radius := gw.Config.AsteroidMinRadius + rand.Float32()*(gw.Config.AsteroidMaxRadius-gw.Config.AsteroidMinRadius)

	layer := gamecomp.LayerTerrain
	if def := item.Get(itemID); def != nil && def.Gaseous {
		layer = 0
	}

	entity := m.base.NewEntity(
		&mmokit.Position{X: x, Y: y},
		&mmokit.Velocity{},
		&mmokit.Rotation{Angle: rand.Float32() * 2 * math.Pi},
		&mmokit.Collider{Radius: radius, Layer: layer},
		&mmokit.NetworkID{ID: netID},
		&mmokit.EntityKind{Type: gamecomp.TypeAsteroid},
	)

	gw.C.CellCoord.Add(entity, &mmokit.CellCoord{CellX: gw.Cell.CellX, CellY: gw.Cell.CellY})
	m.minable.Add(entity, &gamecomp.Minable{
		ItemID:    itemID,
		Remaining: radius * 5,
	})
}
