package game

import (
	"math/rand/v2"
	"time"

	"github.com/mlange-42/ark/ecs"

	gamepb "github.com/zenion/mmoserver/gen/go"
	"github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/internal/item"
	"github.com/zenion/mmoserver/internal/netutil"
	"github.com/zenion/mmoserver/pkg/spatial"
)

type shipMappers struct {
	base   *ecs.Map8[component.Position, component.Velocity, component.Rotation, component.Collider, component.NetworkID, component.EntityKind, component.ShipControl, component.Health]
	extras *ecs.Map4[component.Shield, component.Inventory, component.PlayerConn, component.PlayerInput]
	mining *ecs.Map1[component.MiningLaser]
	combat *ecs.Map4[component.TargetLock, component.AbilitySet, component.StatusEffects, component.MoveTarget]
	equip  *ecs.Map1[component.Equipment]
}

func initShipEntity(gw *GameWorld) {
	gw.shipMappers = &shipMappers{
		base:   ecs.NewMap8[component.Position, component.Velocity, component.Rotation, component.Collider, component.NetworkID, component.EntityKind, component.ShipControl, component.Health](gw.ECS),
		extras: ecs.NewMap4[component.Shield, component.Inventory, component.PlayerConn, component.PlayerInput](gw.ECS),
		mining: ecs.NewMap1[component.MiningLaser](gw.ECS),
		combat: ecs.NewMap4[component.TargetLock, component.AbilitySet, component.StatusEffects, component.MoveTarget](gw.ECS),
		equip:  ecs.NewMap1[component.Equipment](gw.ECS),
	}

	gw.Registry.Register(EntityDef{
		Name:        "ship",
		Description: "player ship",
		EntityType:  component.TypeShip,
		Spawnable:   false,
	})
}

// SpawnPlayer creates a new player ship entity.
// Restores saved position/inventory/equipment, or applies starter loadout for new/dead players.
func (gw *GameWorld) SpawnPlayer(connID uint32) {
	netID := gw.NextNetID()
	m := gw.shipMappers

	// Check for saved player data
	var x, y float32
	var savedCargo map[uint32]int32
	username := gw.ConnToUsername[connID]
	pdata := gw.PlayerDB.GetOrCreate(username)
	pdata.LastLogin = time.Now()
	gw.PlayerDB.MarkDirty(username)

	if pdata.HasSave {
		x = pdata.X
		y = pdata.Y
		if len(pdata.Cargo) > 0 {
			savedCargo = make(map[uint32]int32, len(pdata.Cargo))
			for k, v := range pdata.Cargo {
				savedCargo[k] = v
			}
		}
	} else {
		// Random spawn position near station (origin)
		x = (rand.Float32() - 0.5) * 500
		y = (rand.Float32() - 0.5) * 500
	}

	// Determine equipment: restore saved or assign starter kit
	var equip component.Equipment
	if pdata.HasSave && !pdata.Equipment.IsZero() {
		equip = component.Equipment{
			Weapon1:  pdata.Equipment.Weapon1,
			Weapon2:  pdata.Equipment.Weapon2,
			Shield:   pdata.Equipment.Shield,
			Thruster: pdata.Equipment.Thruster,
		}
	} else {
		equip = component.Equipment{
			Weapon1:  item.StarterWeapon1,
			Weapon2:  item.StarterMiningLaser,
			Shield:   item.StarterShield,
			Thruster: item.StarterThruster,
		}
	}

	boundingRadius := boundingRadius(gw.Config.ShipWidth, gw.Config.ShipHeight)

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

	m.mining.Add(entity, &component.MiningLaser{})

	m.equip.Add(entity, &equip)

	// Apply equipment passive stats (shield max/regen, thrust/speed)
	gw.ApplyEquipmentStats(entity)

	gw.PlayerEntities[connID] = entity
	gw.Log.Log(CatSpawn, "player spawned: conn=%d netID=%d pos=(%.0f,%.0f) equip=[w1=%d w2=%d sh=%d th=%d]",
		connID, netID, x, y, equip.Weapon1, equip.Weapon2, equip.Shield, equip.Thruster)

	// Send spawn message to client
	allItems := item.All()
	itemDefs := make([]*gamepb.ItemDefMsg, 0, len(allItems))
	for _, def := range allItems {
		itemDefs = append(itemDefs, &gamepb.ItemDefMsg{
			Id:          def.ID,
			Name:        def.Name,
			MassPerUnit: def.MassPerUnit,
			SellPrice:   float32(def.SellPrice),
			Category:    uint32(def.Category),
			EquipSlot:   uint32(def.EquipSlot),
			BuyPrice:    float32(def.BuyPrice),
		})
	}
	data := netutil.MakeEvent(uint32(gamepb.ServerEventCode_SE_PLAYER_SPAWNED), &gamepb.PlayerSpawnedMsg{
		YourEntityId: netID,
		WorldWidth:   gw.Config.WorldWidth,
		WorldHeight:  gw.Config.WorldHeight,
		ItemDefs:     itemDefs,
		Equipment: &gamepb.EquipmentState{
			Weapon1:  equip.Weapon1,
			Weapon2:  equip.Weapon2,
			Shield:   equip.Shield,
			Thruster: equip.Thruster,
		},
	})
	if data != nil {
		gw.ConnMgr.SendReliable(connID, data)
	}
}
