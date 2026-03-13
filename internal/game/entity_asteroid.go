package game

import (
	"math"
	"math/rand/v2"

	"github.com/mlange-42/ark/ecs"

	"github.com/zenion/mmoserver/internal/component"
)

type asteroidMappers struct {
	base    *ecs.Map6[component.Position, component.Velocity, component.Rotation, component.Collider, component.NetworkID, component.EntityKind]
	minable *ecs.Map1[component.Minable]
}

func initAsteroidEntity(gw *GameWorld) {
	gw.asteroidMappers = &asteroidMappers{
		base:    ecs.NewMap6[component.Position, component.Velocity, component.Rotation, component.Collider, component.NetworkID, component.EntityKind](gw.ECS),
		minable: ecs.NewMap1[component.Minable](gw.ECS),
	}

	gw.Registry.Register(EntityDef{
		Name:        "asteroid",
		Description: "mineable asteroid",
		EntityType:  component.TypeAsteroid,
		Spawnable:   true,
		Spawn: func(x, y float32) {
			gw.spawnAsteroid(x, y)
		},
	})
}

func (gw *GameWorld) spawnAsteroids() {
	// Keep asteroids away from the station at origin
	exclusion := gw.Config.StationRadius * 5
	exclusionSq := exclusion * exclusion

	for range gw.Config.AsteroidCount {
		var x, y float32
		for {
			x = (rand.Float32()*2 - 1) * gw.Config.WorldWidth
			y = (rand.Float32()*2 - 1) * gw.Config.WorldHeight
			if x*x+y*y > exclusionSq {
				break
			}
		}
		gw.spawnAsteroid(x, y)
	}
	gw.Log.Log(CatSpawn, "spawned %d asteroids (exclusion=%.0f)", gw.Config.AsteroidCount, exclusion)
}

func (gw *GameWorld) spawnAsteroid(x, y float32) {
	m := gw.asteroidMappers
	netID := gw.NextNetID()
	radius := gw.Config.AsteroidMinRadius + rand.Float32()*(gw.Config.AsteroidMaxRadius-gw.Config.AsteroidMinRadius)

	resType := uint8(rand.IntN(4))
	layer := component.LayerTerrain
	if resType == component.ResourceGas {
		layer = 0
	}

	entity := m.base.NewEntity(
		&component.Position{X: x, Y: y},
		&component.Velocity{},
		&component.Rotation{Angle: rand.Float32() * 2 * math.Pi},
		&component.Collider{Radius: radius, Layer: layer},
		&component.NetworkID{ID: netID},
		&component.EntityKind{Type: component.TypeAsteroid},
	)

	m.minable.Add(entity, &component.Minable{
		ResourceType: resType,
		Remaining:    radius * 5,
	})
}
