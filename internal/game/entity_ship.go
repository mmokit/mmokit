package game

import (
	"math"
	"math/rand/v2"
	"time"

	"github.com/mlange-42/ark/ecs"
	"google.golang.org/protobuf/proto"

	gamepb "github.com/zenion/mmoserver/gen/go"
	"github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/internal/item"
	"github.com/zenion/mmoserver/pkg/logger"
	"github.com/zenion/mmoserver/pkg/spatial"
)

type shipMappers struct {
	base   *ecs.Map8[component.Position, component.Velocity, component.Rotation, component.Collider, component.NetworkID, component.EntityKind, component.ShipControl, component.Health]
	extras *ecs.Map4[component.Shield, component.Inventory, component.PlayerConn, component.PlayerInput]
	mining *ecs.Map1[component.MiningLaser]
	combat *ecs.Map4[component.TargetLock, component.AbilitySet, component.StatusEffects, component.MoveTarget]
}

func initShipEntity(gw *GameWorld) {
	gw.shipMappers = &shipMappers{
		base:   ecs.NewMap8[component.Position, component.Velocity, component.Rotation, component.Collider, component.NetworkID, component.EntityKind, component.ShipControl, component.Health](gw.ECS),
		extras: ecs.NewMap4[component.Shield, component.Inventory, component.PlayerConn, component.PlayerInput](gw.ECS),
		mining: ecs.NewMap1[component.MiningLaser](gw.ECS),
		combat: ecs.NewMap4[component.TargetLock, component.AbilitySet, component.StatusEffects, component.MoveTarget](gw.ECS),
	}

	gw.Registry.Register(EntityDef{
		Name:        "ship",
		Description: "player ship",
		EntityType:  component.TypeShip,
		Spawnable:   false,
	})
}

// SpawnPlayer creates a new player ship entity.
// If the player has saved data (from a previous session), restores position, inventory, and flux.
func (gw *GameWorld) SpawnPlayer(connID uint32) {
	netID := gw.NextNetID()
	m := gw.shipMappers

	// Check for saved player data
	var x, y float32
	var savedCargo map[uint32]float32
	username := gw.ConnToUsername[connID]
	pdata := gw.PlayerDB.GetOrCreate(username)
	pdata.LastLogin = time.Now()
	gw.PlayerDB.MarkDirty(username)

	if pdata.HasSave {
		x = pdata.X
		y = pdata.Y
		if len(pdata.Cargo) > 0 {
			savedCargo = make(map[uint32]float32, len(pdata.Cargo))
			for k, v := range pdata.Cargo {
				savedCargo[k] = v
			}
		}
	} else {
		// Random spawn position near station (origin)
		x = (rand.Float32() - 0.5) * 500
		y = (rand.Float32() - 0.5) * 500
	}

	// Bounding radius = half-diagonal of the ship rect
	halfW := gw.Config.ShipWidth / 2
	halfH := gw.Config.ShipHeight / 2
	boundingRadius := float32(math.Sqrt(float64(halfW*halfW + halfH*halfH)))

	entity := m.base.NewEntity(
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

	m.extras.Add(entity,
		&component.Shield{Current: gw.Config.ShipShield, Max: gw.Config.ShipShield, RegenRate: gw.Config.ShieldRegenRate, RegenDelay: gw.Config.ShieldRegenDelay},
		&component.Inventory{Items: savedCargo, MaxMass: gw.Config.MaxCargo},
		&component.PlayerConn{ConnID: connID},
		&component.PlayerInput{},
	)

	m.combat.Add(entity,
		&component.TargetLock{
			LockTime: gw.Config.LockOnTime,
			Range:    gw.Config.LockOnRange,
		},
		&component.AbilitySet{},
		&component.StatusEffects{},
		&component.MoveTarget{},
	)

	m.mining.Add(entity, &component.MiningLaser{
		Range: gw.Config.MiningRange,
		Rate:  gw.Config.MiningRate,
	})

	gw.PlayerEntities[connID] = entity
	gw.Log.Log(logger.CatSpawn, "player spawned: conn=%d netID=%d pos=(%.0f,%.0f)", connID, netID, x, y)

	// Send spawn message to client
	allItems := item.All()
	itemDefs := make([]*gamepb.ItemDefMsg, 0, len(allItems))
	for _, def := range allItems {
		itemDefs = append(itemDefs, &gamepb.ItemDefMsg{
			Id:          def.ID,
			Name:        def.Name,
			MassPerUnit: def.MassPerUnit,
			SellPrice:   float32(def.SellPrice),
		})
	}
	msg := &gamepb.ServerMessage{
		Msg: &gamepb.ServerMessage_PlayerSpawned{
			PlayerSpawned: &gamepb.PlayerSpawnedMsg{
				YourEntityId: netID,
				WorldWidth:   gw.Config.WorldWidth,
				WorldHeight:  gw.Config.WorldHeight,
				ItemDefs:     itemDefs,
			},
		},
	}
	if data, err := proto.Marshal(msg); err == nil {
		gw.ConnMgr.SendReliable(connID, data)
	}
}
