package game

import (
	"math"
	"math/rand/v2"

	"github.com/mlange-42/ark/ecs"

	comp "github.com/zenion/mmoserver/pkg/component"
	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/pkg/coords"
	"github.com/zenion/mmoserver/pkg/engine"
)

type asteroidMappers struct {
	base    *ecs.Map6[comp.Position, comp.Velocity, comp.Rotation, comp.Collider, comp.NetworkID, comp.EntityKind]
	minable *ecs.Map1[gamecomp.Minable]
}

func initAsteroidEntity(gw *GameWorld) {
	m := &asteroidMappers{
		base:    ecs.NewMap6[comp.Position, comp.Velocity, comp.Rotation, comp.Collider, comp.NetworkID, comp.EntityKind](gw.ECS),
		minable: ecs.NewMap1[gamecomp.Minable](gw.ECS),
	}

	gw.Registry.Register(engine.EntityDef{
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
	belts := GenerateBelts(gw.Sector)
	total := 0
	for _, belt := range belts {
		for i := 0; i < belt.Count; i++ {
			angle := rand.Float32() * 2 * math.Pi
			dist := rand.Float32() * belt.Radius
			x := belt.CenterX + float32(math.Cos(float64(angle)))*dist
			y := belt.CenterY + float32(math.Sin(float64(angle)))*dist
			// Clamp within sector
			if x < 0 {
				x = 0
			}
			if y < 0 {
				y = 0
			}
			if x >= coords.SectorSize {
				x = coords.SectorSize - 1
			}
			if y >= coords.SectorSize {
				y = coords.SectorSize - 1
			}
			// Resource type: 75% dominant, 25% random
			var resType uint8
			if rand.Float32() < 0.75 {
				resType = belt.ResourceTypes[rand.IntN(len(belt.ResourceTypes))]
			} else {
				resType = uint8(rand.IntN(4))
			}
			gw.spawnAsteroidWithType(x, y, resType)
		}
		total += belt.Count
	}
	gw.Log.Log(CatSpawn, "spawned %d asteroids in %d belts for sector (%d,%d)",
		total, len(belts), gw.Sector.SX, gw.Sector.SY)
}

func (gw *GameWorld) spawnAsteroid(x, y float32) {
	gw.spawnAsteroidWithType(x, y, uint8(rand.IntN(4)))
}

func (gw *GameWorld) spawnAsteroidWithType(x, y float32, resType uint8) {
	m := gw.Registry.ByType(gamecomp.TypeAsteroid).Mappers.(*asteroidMappers)
	netID := gw.NextNetID()
	radius := gw.Config.AsteroidMinRadius + rand.Float32()*(gw.Config.AsteroidMaxRadius-gw.Config.AsteroidMinRadius)

	layer := gamecomp.LayerTerrain
	if resType == gamecomp.ResourceGas {
		layer = 0
	}

	entity := m.base.NewEntity(
		&comp.Position{X: x, Y: y},
		&comp.Velocity{},
		&comp.Rotation{Angle: rand.Float32() * 2 * math.Pi},
		&comp.Collider{Radius: radius, Layer: layer},
		&comp.NetworkID{ID: netID},
		&comp.EntityKind{Type: gamecomp.TypeAsteroid},
	)

	gw.C.SectorCoord.Add(entity, &comp.SectorCoord{SX: gw.Sector.SX, SY: gw.Sector.SY})
	m.minable.Add(entity, &gamecomp.Minable{
		ResourceType: resType,
		Remaining:    radius * 5,
	})
}
