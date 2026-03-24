package game

import (
	"math/rand/v2"
	"time"

	"github.com/mlange-42/ark/ecs"

	enginepb "github.com/zenion/mmoserver/gen/go/enginepb"
	gamepb "github.com/zenion/mmoserver/gen/go/gamepb"
	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/internal/item"
	"github.com/zenion/mmoserver/internal/netutil"
	"github.com/zenion/mmoserver/pkg/coords"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

type shipMappers struct {
	base   *ecs.Map8[mmokit.Position, mmokit.Velocity, mmokit.Rotation, mmokit.Collider, mmokit.NetworkID, mmokit.EntityKind, gamecomp.ShipControl, mmokit.Health]
	extras *ecs.Map4[mmokit.Shield, gamecomp.Inventory, mmokit.PlayerConn, gamecomp.PlayerInput]
	mining *ecs.Map1[gamecomp.MiningLaser]
	combat *ecs.Map4[mmokit.TargetLock, gamecomp.AbilitySet, gamecomp.StatusEffects, mmokit.MoveTarget]
	equip  *ecs.Map1[gamecomp.Equipment]
}

func initShipEntity(gw *GameWorld) {
	m := &shipMappers{
		base:   ecs.NewMap8[mmokit.Position, mmokit.Velocity, mmokit.Rotation, mmokit.Collider, mmokit.NetworkID, mmokit.EntityKind, gamecomp.ShipControl, mmokit.Health](gw.ECS),
		extras: ecs.NewMap4[mmokit.Shield, gamecomp.Inventory, mmokit.PlayerConn, gamecomp.PlayerInput](gw.ECS),
		mining: ecs.NewMap1[gamecomp.MiningLaser](gw.ECS),
		combat: ecs.NewMap4[mmokit.TargetLock, gamecomp.AbilitySet, gamecomp.StatusEffects, mmokit.MoveTarget](gw.ECS),
		equip:  ecs.NewMap1[gamecomp.Equipment](gw.ECS),
	}

	gw.Registry.Register(mmokit.EntityDef{
		Name:        "ship",
		Description: "player ship",
		EntityType:  gamecomp.TypeShip,
		Spawnable:   false,
		Mappers:     m,
	})
}

// SpawnPlayer creates a new player ship entity.
// Restores saved position/inventory/equipment, or applies starter loadout for new/dead players.
func (gw *GameWorld) SpawnPlayer(connID uint32) {
	netID := gw.NextNetID()
	m := gw.Registry.ByType(gamecomp.TypeShip).Mappers.(*shipMappers)

	// Check for saved player data
	var x, y float32
	var sectorX, sectorY int32
	var savedCargo map[uint32]int32
	username := gw.Players.Usernames[connID]
	pdata := gw.PlayerDB.GetOrCreate(username)
	pdata.LastLogin = time.Now()
	gw.PlayerDB.MarkDirty(username)

	if pdata.HasSave {
		x = pdata.X
		y = pdata.Y
		sectorX = pdata.SectorX
		sectorY = pdata.SectorY
		if len(pdata.Cargo) > 0 {
			savedCargo = make(map[uint32]int32, len(pdata.Cargo))
			for k, v := range pdata.Cargo {
				savedCargo[k] = v
			}
		}
		// If saved sector differs from this node's sector, offset position so
		// SectorBoundarySystem will transfer the entity to the correct node.
		if sectorX != gw.Sector.SX || sectorY != gw.Sector.SY {
			x += float32(sectorX-gw.Sector.SX) * coords.SectorSize
			y += float32(sectorY-gw.Sector.SY) * coords.SectorSize
			sectorX = gw.Sector.SX
			sectorY = gw.Sector.SY
		}
	} else {
		// Random spawn position near station (center of sector)
		x = coords.SectorSize/2 + (rand.Float32()-0.5)*16.7
		y = coords.SectorSize/2 + (rand.Float32()-0.5)*16.7
	}

	// Determine equipment: restore saved or assign starter kit
	var equip gamecomp.Equipment
	if pdata.HasSave && !pdata.Equipment.IsZero() {
		equip = gamecomp.Equipment{
			Weapon1:  pdata.Equipment.Weapon1,
			Weapon2:  pdata.Equipment.Weapon2,
			Shield:   pdata.Equipment.Shield,
			Thruster: pdata.Equipment.Thruster,
		}
	} else {
		equip = gamecomp.Equipment{
			Weapon1:  item.StarterWeapon1,
			Weapon2:  item.StarterMiningLaser,
			Shield:   item.StarterShield,
			Thruster: item.StarterThruster,
		}
	}

	boundingRadius := boundingRadius(gw.Config.ShipWidth, gw.Config.ShipHeight)

	entity := m.base.NewEntity(
		&mmokit.Position{X: x, Y: y},
		&mmokit.Velocity{},
		&mmokit.Rotation{},
		&mmokit.Collider{
			Radius: boundingRadius,
			Width:  gw.Config.ShipWidth,
			Height: gw.Config.ShipHeight,
			Layer:  gamecomp.LayerPlayer,
			Shape:  mmokit.ShapeRect,
		},
		&mmokit.NetworkID{ID: netID},
		&mmokit.EntityKind{Type: gamecomp.TypeShip},
		&gamecomp.ShipControl{
			Thrust:   gw.Config.ShipThrust,
			TurnRate: gw.Config.ShipTurnRate,
			MaxSpeed: gw.Config.MaxSpeed,
		},
		&mmokit.Health{Current: gw.Config.ShipHealth, Max: gw.Config.ShipHealth},
	)

	gw.C.SectorCoord.Add(entity, &mmokit.SectorCoord{SX: sectorX, SY: sectorY})
	m.extras.Add(entity,
		&mmokit.Shield{Current: gw.Config.ShipShield, Max: gw.Config.ShipShield, RegenRate: gw.Config.ShieldRegenRate, RegenDelay: gw.Config.ShieldRegenDelay},
		&gamecomp.Inventory{Items: savedCargo, MaxMass: gw.Config.MaxCargo},
		&mmokit.PlayerConn{ConnID: connID},
		&gamecomp.PlayerInput{},
	)

	m.combat.Add(entity,
		&mmokit.TargetLock{
			LockTime: gw.Config.LockOnTime,
			Range:    gw.Config.LockOnRange,
		},
		&gamecomp.AbilitySet{},
		&gamecomp.StatusEffects{},
		&mmokit.MoveTarget{},
	)

	m.mining.Add(entity, &gamecomp.MiningLaser{})

	m.equip.Add(entity, &equip)

	// Apply equipment passive stats (shield max/regen, thrust/speed)
	gw.ApplyEquipmentStats(entity)

	gw.Players.Entities[connID] = entity
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
	data := netutil.MakeEvent(uint32(enginepb.ServerEventCode_SE_PLAYER_SPAWNED), &gamepb.PlayerSpawnedMsg{
		YourEntityId:  netID,
		ItemDefs:      itemDefs,
		OriginSectorX: sectorX,
		OriginSectorY: sectorY,
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

	// Send map data (station positions) to the client
	mapStations := gw.CollectStationMapData()
	mapFrame := netutil.MakeEvent(uint32(gamepb.GameServerEventCode_GSE_MAP_DATA), &gamepb.MapDataMsg{
		Stations: mapStations,
	})
	if mapFrame != nil {
		gw.ConnMgr.SendReliable(connID, mapFrame)
	}
	gw.Log.Log(CatMap, "map data sent: conn=%d stations=%d", connID, len(mapStations))

	// Send current debug flags so late-joiners pick up the state
	debugData := netutil.MakeEvent(uint32(gamepb.GameServerEventCode_GSE_DEBUG_FLAGS), &gamepb.DebugFlagsMsg{
		ShowSectorGrid: gw.DebugShowSectorGrid,
	})
	if debugData != nil {
		gw.ConnMgr.SendReliable(connID, debugData)
	}

	// Send current currency balances so the client has them immediately
	for curID, bal := range pdata.Currencies {
		curData := netutil.MakeEvent(uint32(gamepb.GameServerEventCode_GSE_CURRENCY_UPDATE), &gamepb.CurrencyUpdateMsg{
			CurrencyId: curID,
			Balance:    bal,
			Earned:     0,
		})
		if curData != nil {
			gw.ConnMgr.SendReliable(connID, curData)
		}
	}
}
