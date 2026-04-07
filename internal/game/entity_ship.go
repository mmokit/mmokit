package game

import (
	"math/rand/v2"
	"time"

	"github.com/mlange-42/ark/ecs"

	enginepb "github.com/zenion/mmoserver/gen/go/enginepb"
	gamepb "github.com/zenion/mmoserver/gen/go/gamepb"
	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/internal/item"
	"github.com/zenion/mmoserver/pkg/coords"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

type shipMappers struct {
	base   *ecs.Map8[mmokit.Position, mmokit.Velocity, mmokit.Rotation, mmokit.Collider, mmokit.NetworkID, mmokit.EntityKind, gamecomp.ShipControl, gamecomp.Health]
	extras *ecs.Map4[gamecomp.Shield, gamecomp.Inventory, mmokit.PlayerConn, gamecomp.PlayerInput]
	mining *ecs.Map1[gamecomp.MiningLaser]
	combat *ecs.Map4[gamecomp.TargetLock, gamecomp.AbilitySet, gamecomp.StatusEffects, mmokit.MoveTarget]
	equip  *ecs.Map1[gamecomp.Equipment]
}

func initShipEntity(gw *GameWorld) {
	m := &shipMappers{
		base:   ecs.NewMap8[mmokit.Position, mmokit.Velocity, mmokit.Rotation, mmokit.Collider, mmokit.NetworkID, mmokit.EntityKind, gamecomp.ShipControl, gamecomp.Health](gw.ECS),
		extras: ecs.NewMap4[gamecomp.Shield, gamecomp.Inventory, mmokit.PlayerConn, gamecomp.PlayerInput](gw.ECS),
		mining: ecs.NewMap1[gamecomp.MiningLaser](gw.ECS),
		combat: ecs.NewMap4[gamecomp.TargetLock, gamecomp.AbilitySet, gamecomp.StatusEffects, mmokit.MoveTarget](gw.ECS),
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
// If s.Entity is already alive, this is a reconnection or cross-node transfer —
// reuse the existing entity instead of creating a new one.
func (gw *GameWorld) SpawnPlayer(s *mmokit.PlayerSession) {
	if s.Entity != (ecs.Entity{}) && gw.ECS.Alive(s.Entity) {
		gw.reconnectPlayer(s)
		return
	}
	connID := s.ConnID
	netID := gw.NextNetID()
	m := gw.Registry.ByType(gamecomp.TypeShip).Mappers.(*shipMappers)

	// Check for saved player data
	var x, y float32
	var cellX, cellY int32
	var savedCargo map[uint32]int32
	username := s.Username
	pdata := gw.PlayerDB.GetOrCreate(username)
	pdata.LastLogin = time.Now()
	gw.PlayerDB.MarkDirty(username)

	if pdata.HasSave {
		x = pdata.X
		y = pdata.Y
		cellX = pdata.CellX
		cellY = pdata.CellY
		if len(pdata.Cargo) > 0 {
			savedCargo = make(map[uint32]int32, len(pdata.Cargo))
			for k, v := range pdata.Cargo {
				savedCargo[k] = v
			}
		}
		// If saved cell differs from this node's cell, offset position so
		// CellBoundarySystem will transfer the entity to the correct node.
		if cellX != gw.Cell.CellX || cellY != gw.Cell.CellY {
			x += float32(cellX-gw.Cell.CellX) * coords.CellSize
			y += float32(cellY-gw.Cell.CellY) * coords.CellSize
			cellX = gw.Cell.CellX
			cellY = gw.Cell.CellY
		}
	} else {
		// Random spawn position near station (center of cell)
		x = coords.CellSize/2 + (rand.Float32()-0.5)*16.7
		y = coords.CellSize/2 + (rand.Float32()-0.5)*16.7
		cellX = gw.Cell.CellX
		cellY = gw.Cell.CellY
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
		&gamecomp.Health{Current: gw.Config.ShipHealth, Max: gw.Config.ShipHealth},
	)

	gw.C.CellCoord.Add(entity, &mmokit.CellCoord{CellX: cellX, CellY: cellY})
	m.extras.Add(entity,
		&gamecomp.Shield{Current: gw.Config.ShipShield, Max: gw.Config.ShipShield, RegenRate: gw.Config.ShieldRegenRate, RegenDelay: gw.Config.ShieldRegenDelay},
		&gamecomp.Inventory{Items: savedCargo, MaxMass: gw.Config.MaxCargo},
		&mmokit.PlayerConn{ConnID: connID},
		&gamecomp.PlayerInput{},
	)

	m.combat.Add(entity,
		&gamecomp.TargetLock{
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

	s.Entity = entity
	gw.Log.Log(CatPlayerSpawn, "player spawned: conn=%d netID=%d pos=(%.0f,%.0f) equip=[w1=%d w2=%d sh=%d th=%d]",
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
	data := mmokit.MakeEvent(uint32(enginepb.ServerEventCode_SE_PLAYER_SPAWNED), &gamepb.PlayerSpawnedMsg{
		YourEntityId: netID,
		ItemDefs:     itemDefs,
		OriginCellX:  cellX,
		OriginCellY:  cellY,
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
	mapFrame := mmokit.MakeEvent(uint32(gamepb.GameServerEventCode_GSE_MAP_DATA), &gamepb.MapDataMsg{
		Stations: mapStations,
	})
	if mapFrame != nil {
		gw.ConnMgr.SendReliable(connID, mapFrame)
	}
	gw.Log.Log(CatWorldMap, "map data sent: conn=%d stations=%d", connID, len(mapStations))

	// Send current debug flags so late-joiners pick up the state
	debugData := mmokit.MakeEvent(uint32(gamepb.GameServerEventCode_GSE_DEBUG_FLAGS), &gamepb.DebugFlagsMsg{
		ShowCellGrid: gw.DebugShowCellGrid,
	})
	if debugData != nil {
		gw.ConnMgr.SendReliable(connID, debugData)
	}

	// Send current currency balances so the client has them immediately
	for curID, bal := range pdata.Currencies {
		curData := mmokit.MakeEvent(uint32(gamepb.GameServerEventCode_GSE_CURRENCY_UPDATE), &gamepb.CurrencyUpdateMsg{
			CurrencyId: curID,
			Balance:    bal,
			Earned:     0,
		})
		if curData != nil {
			gw.ConnMgr.SendReliable(connID, curData)
		}
	}
}

// reconnectPlayer reuses an existing entity for a reconnecting player
// (grace period reconnection). Updates the PlayerConn component with the
// new connID and sends the client the spawn message so it knows its entity ID.
func (gw *GameWorld) reconnectPlayer(s *mmokit.PlayerSession) {
	entity := s.Entity
	connID := s.ConnID

	// Update PlayerConn with new connection ID
	if gw.C.PlayerConn.HasAll(entity) {
		gw.C.PlayerConn.Get(entity).ConnID = connID
	}

	netID := gw.C.NetworkID.Get(entity).ID
	pos := gw.C.Position.Get(entity)
	sec := gw.C.CellCoord.Get(entity)

	gw.Log.Log(CatPlayerSpawn, "player reconnected: conn=%d netID=%d pos=(%.0f,%.0f)", connID, netID, pos.X, pos.Y)

	// Read equipment for spawn message
	var equip gamepb.EquipmentState
	if gw.C.Equipment.HasAll(entity) {
		eq := gw.C.Equipment.Get(entity)
		equip = gamepb.EquipmentState{
			Weapon1:  eq.Weapon1,
			Weapon2:  eq.Weapon2,
			Shield:   eq.Shield,
			Thruster: eq.Thruster,
		}
	}

	// Send same spawn message as fresh login — client needs entity ID
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
	data := mmokit.MakeEvent(uint32(enginepb.ServerEventCode_SE_PLAYER_SPAWNED), &gamepb.PlayerSpawnedMsg{
		YourEntityId: netID,
		ItemDefs:     itemDefs,
		OriginCellX:  sec.CellX,
		OriginCellY:  sec.CellY,
		Equipment:    &equip,
	})
	if data != nil {
		gw.ConnMgr.SendReliable(connID, data)
	}

	// Send map data
	mapStations := gw.CollectStationMapData()
	mapFrame := mmokit.MakeEvent(uint32(gamepb.GameServerEventCode_GSE_MAP_DATA), &gamepb.MapDataMsg{
		Stations: mapStations,
	})
	if mapFrame != nil {
		gw.ConnMgr.SendReliable(connID, mapFrame)
	}

	// Send debug flags
	debugData := mmokit.MakeEvent(uint32(gamepb.GameServerEventCode_GSE_DEBUG_FLAGS), &gamepb.DebugFlagsMsg{
		ShowCellGrid: gw.DebugShowCellGrid,
	})
	if debugData != nil {
		gw.ConnMgr.SendReliable(connID, debugData)
	}

	// Send currency balances
	pdata := gw.PlayerDB.GetOrCreate(s.Username)
	for curID, bal := range pdata.Currencies {
		curData := mmokit.MakeEvent(uint32(gamepb.GameServerEventCode_GSE_CURRENCY_UPDATE), &gamepb.CurrencyUpdateMsg{
			CurrencyId: curID,
			Balance:    bal,
			Earned:     0,
		})
		if curData != nil {
			gw.ConnMgr.SendReliable(connID, curData)
		}
	}
}
