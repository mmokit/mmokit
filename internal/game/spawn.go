package game

import (
	"math"
	"math/rand/v2"
	"time"

	"github.com/mlange-42/ark/ecs"
	"google.golang.org/protobuf/proto"

	gamepb "github.com/zenion/gameserver/gen/go"
	"github.com/zenion/gameserver/internal/component"
	"github.com/zenion/gameserver/pkg/logger"
	"github.com/zenion/gameserver/pkg/spatial"
)

// SpawnPlayer creates a new player ship entity.
// If the player has saved data (from a previous session), restores position, inventory, and flux.
func (gw *GameWorld) SpawnPlayer(connID uint32) {
	netID := gw.NextNetID()

	// Check for saved player data
	var x, y float32
	var savedInv [4]float32
	username := gw.ConnToUsername[connID]
	pdata := gw.PlayerDB.GetOrCreate(username)
	pdata.LastLogin = time.Now()
	gw.PlayerDB.MarkDirty(username)

	if pdata.HasSave {
		x = pdata.X
		y = pdata.Y
		savedInv = pdata.Resources
	} else {
		// Random spawn position near station (origin)
		x = (rand.Float32() - 0.5) * 500
		y = (rand.Float32() - 0.5) * 500
	}

	// Bounding radius = half-diagonal of the ship rect
	halfW := gw.Config.ShipWidth / 2
	halfH := gw.Config.ShipHeight / 2
	boundingRadius := float32(math.Sqrt(float64(halfW*halfW + halfH*halfH)))

	entity := gw.shipMapper.NewEntity(
		&component.Position{X: x, Y: y},
		&component.Velocity{},
		&component.Rotation{},
		&component.Collider{
			Radius: boundingRadius,
			Width:  gw.Config.ShipWidth,
			Height: gw.Config.ShipHeight,
			Layer:  component.LayerPlayer,
			Shape:  spatial.ShapeRect,
		},
		&component.NetworkID{ID: netID},
		&component.EntityKind{Type: component.TypeShip},
		&component.ShipControl{
			Thrust:   gw.Config.ShipThrust,
			TurnRate: gw.Config.ShipTurnRate,
			MaxSpeed: gw.Config.MaxSpeed,
		},
		&component.Health{Current: gw.Config.ShipHealth, Max: gw.Config.ShipHealth},
	)

	gw.shipExtrasMapper.Add(entity,
		&component.Shield{Current: gw.Config.ShipShield, Max: gw.Config.ShipShield, RegenRate: gw.Config.ShieldRegenRate, RegenDelay: gw.Config.ShieldRegenDelay},
		&component.Weapon{
			Damage:   gw.Config.WeaponDamage,
			FireRate: gw.Config.WeaponFireRate,
			Speed:    gw.Config.WeaponSpeed,
		},
		&component.Inventory{Resources: savedInv},
		&component.PlayerConn{ConnID: connID},
		&component.PlayerInput{},
	)

	gw.miningMapper.Add(entity, &component.MiningLaser{
		Range: gw.Config.MiningRange,
		Rate:  gw.Config.MiningRate,
	})

	gw.PlayerEntities[connID] = entity
	gw.Log.Log(logger.CatSpawn, "player spawned: conn=%d netID=%d pos=(%.0f,%.0f)", connID, netID, x, y)

	// Send spawn message to client
	sellPrices := make([]float32, 4)
	for i, p := range gw.Config.SellPrices {
		sellPrices[i] = float32(p)
	}
	msg := &gamepb.ServerMessage{
		Msg: &gamepb.ServerMessage_PlayerSpawned{
			PlayerSpawned: &gamepb.PlayerSpawnedMsg{
				YourEntityId: netID,
				WorldWidth:   gw.Config.WorldWidth,
				WorldHeight:  gw.Config.WorldHeight,
				SellPrices:   sellPrices,
			},
		},
	}
	if data, err := proto.Marshal(msg); err == nil {
		gw.ConnMgr.SendReliable(connID, data)
	}
}

// SpawnProjectile creates a new projectile entity.
func (gw *GameWorld) SpawnProjectile(owner ecs.Entity, x, y, angle, speed, damage, lifetime float32) {
	netID := gw.NextNetID()
	vx := float32(math.Cos(float64(angle))) * speed
	vy := float32(math.Sin(float64(angle))) * speed

	entity := gw.projectileMapper.NewEntity(
		&component.Position{X: x, Y: y},
		&component.Velocity{X: vx, Y: vy},
		&component.Rotation{Angle: angle},
		&component.Collider{Radius: 4, Layer: component.LayerProjectile},
		&component.NetworkID{ID: netID},
		&component.EntityKind{Type: component.TypeProjectile},
		&component.Projectile{Damage: damage},
		&component.Lifetime{Remaining: lifetime},
	)

	gw.ownerMapper.Add(entity, &component.Owner{Entity: owner})
}

func (gw *GameWorld) spawnAsteroids() {
	for range gw.Config.AsteroidCount {
		netID := gw.NextNetID()
		radius := gw.Config.AsteroidMinRadius + rand.Float32()*(gw.Config.AsteroidMaxRadius-gw.Config.AsteroidMinRadius)
		x := (rand.Float32()*2 - 1) * gw.Config.WorldWidth
		y := (rand.Float32()*2 - 1) * gw.Config.WorldHeight

		resType := uint8(rand.IntN(4))
		layer := component.LayerTerrain
		if resType == component.ResourceGas {
			layer = 0
		}

		entity := gw.asteroidMapper.NewEntity(
			&component.Position{X: x, Y: y},
			&component.Velocity{},
			&component.Rotation{Angle: rand.Float32() * 2 * math.Pi},
			&component.Collider{Radius: radius, Layer: layer},
			&component.NetworkID{ID: netID},
			&component.EntityKind{Type: component.TypeAsteroid},
		)

		gw.minableMapper.Add(entity, &component.Minable{
			ResourceType: resType,
			Remaining:    radius * 10,
		})
	}

	gw.Log.Log(logger.CatSpawn, "spawned %d asteroids", gw.Config.AsteroidCount)
}

// SpawnStation creates the trade station entity at the origin.
func (gw *GameWorld) SpawnStation() {
	netID := gw.NextNetID()
	entity := gw.stationMapper.NewEntity(
		&component.Position{X: 0, Y: 0},
		&component.Velocity{},
		&component.Rotation{},
		&component.Collider{Radius: gw.Config.StationRadius, Layer: 0},
		&component.NetworkID{ID: netID},
		&component.EntityKind{Type: component.TypeStation},
	)
	gw.stationMarkerMapper.Add(entity, &component.Station{})
	gw.Log.Log(logger.CatSpawn, "station spawned: netID=%d pos=(0,0)", netID)
}

// SpawnLootCrate creates a loot crate entity with the given cargo.
func (gw *GameWorld) SpawnLootCrate(x, y float32, resources [4]float32) {
	netID := gw.NextNetID()
	entity := gw.lootCrateMapper.NewEntity(
		&component.Position{X: x, Y: y},
		&component.Velocity{},
		&component.Rotation{},
		&component.Collider{Radius: gw.Config.LootCrateRadius, Layer: 0},
		&component.NetworkID{ID: netID},
		&component.EntityKind{Type: component.TypeLootCrate},
	)
	gw.lootCrateExtrasMapper.Add(entity,
		&component.Inventory{Resources: resources},
		&component.Lifetime{Remaining: gw.Config.LootCrateLifetime},
		&component.LootCrate{},
	)
	gw.Log.Log(logger.CatSpawn, "loot crate spawned: netID=%d pos=(%.0f,%.0f)", netID, x, y)
}
