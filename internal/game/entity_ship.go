package game

import (
	"maps"
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

// SpawnPlayer creates a new player ship entity.
// Restores saved position/inventory/equipment, or applies starter loadout for new/dead players.
// If s.Entity is already alive, this is a reconnection or cross-cell transfer —
// reuse the existing entity instead of creating a new one.
func (gw *GameWorld) SpawnPlayer(s *mmokit.PlayerSession) {
	if s.Entity != (ecs.Entity{}) && gw.eng.ECS.Alive(s.Entity) {
		gw.reconnectPlayer(s)
		return
	}
	connID := s.ConnID

	// Check for saved player data
	var x, y float32
	var savedCargo map[uint32]int32
	username := s.Username
	pdata := gw.PlayerDB.GetOrCreate(username)
	pdata.LastLogin = time.Now()
	gw.PlayerDB.MarkDirty(username)

	if pdata.HasSave {
		x = pdata.X
		y = pdata.Y
		cellX := pdata.CellX
		cellY := pdata.CellY
		if len(pdata.Cargo) > 0 {
			savedCargo = make(map[uint32]int32, len(pdata.Cargo))
			maps.Copy(savedCargo, pdata.Cargo)
		}
		// If saved cell differs from this node's cell, offset position so
		// CellBoundarySystem will transfer the entity to the correct node.
		if cellX != gw.RootCell.CellX || cellY != gw.RootCell.CellY {
			x += float32(cellX-gw.RootCell.CellX) * coords.CellSize
			y += float32(cellY-gw.RootCell.CellY) * coords.CellSize
		}
	} else {
		// Random spawn position near station (center of cell)
		x = coords.CellSize/2 + (rand.Float32()-0.5)*16.7
		y = coords.CellSize/2 + (rand.Float32()-0.5)*16.7
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

	br := boundingRadius(gw.Config.ShipWidth, gw.Config.ShipHeight)

	entity := gw.SpawnEntity(
		mmokit.Position{X: x, Y: y},
		mmokit.WithEntityKind(gamecomp.TypeShip),
		mmokit.WithCollider(br),
		mmokit.WithRotation(0), // ShipDynamicsSystem reads Rotation for turn-rate steering
		mmokit.WithComponents(), // auto-adds all registered ship components
	)

	// Set collider shape details (SpawnEntity only sets radius)
	col := gw.C.Collider.Get(entity)
	col.Width = gw.Config.ShipWidth
	col.Height = gw.Config.ShipHeight
	col.Layer = gamecomp.LayerPlayer
	col.Shape = mmokit.ShapeRect

	// Wire player connection
	gw.C.PlayerConn.Add(entity, &mmokit.PlayerConn{ConnID: connID})

	// Set pilot name for replication
	gw.C.PilotName.Get(entity).Name = username

	// Set non-zero field values on auto-added components
	*gw.C.ShipControl.Get(entity) = gamecomp.ShipControl{
		Thrust:    gw.Config.ShipThrust,
		TurnRate:  gw.Config.ShipTurnRate,
		TurnAccel: gw.Config.ShipTurnAccel,
		MaxSpeed:  gw.Config.MaxSpeed,
	}
	*gw.C.Health.Get(entity) = gamecomp.Health{Current: gw.Config.ShipHealth, Max: gw.Config.ShipHealth}
	*gw.C.Shield.Get(entity) = gamecomp.Shield{Current: gw.Config.ShipShield, Max: gw.Config.ShipShield, RegenRate: gw.Config.ShieldRegenRate, RegenDelay: gw.Config.ShieldRegenDelay}
	*gw.C.Inventory.Get(entity) = gamecomp.Inventory{Items: savedCargo, MaxMass: gw.Config.MaxCargo}
	*gw.C.TargetLock.Get(entity) = gamecomp.TargetLock{
		LockTime: gw.Config.LockOnTime,
		Range:    gw.Config.LockOnRange,
	}
	*gw.C.Equipment.Get(entity) = equip

	// Apply equipment passive stats (shield max/regen, thrust/speed)
	gw.ApplyEquipmentStats(entity)

	s.Entity = entity
	netID := gw.C.NetworkID.Get(entity).ID
	sec := gw.C.CellCoord.Get(entity)
	gw.eng.Log.Log(CatPlayerSpawn, "player spawned: conn=%d netID=%d pos=(%.0f,%.0f) equip=[w1=%d w2=%d sh=%d th=%d]",
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
		OriginCellX:  sec.CellX,
		OriginCellY:  sec.CellY,
		Equipment: &gamepb.EquipmentState{
			Weapon1:  equip.Weapon1,
			Weapon2:  equip.Weapon2,
			Shield:   equip.Shield,
			Thruster: equip.Thruster,
		},
	})
	if data != nil {
		gw.eng.ConnMgr.SendReliable(connID, data)
	}

	// Send map data (station positions) to the client
	mapStations := gw.CollectStationMapData()
	mapFrame := mmokit.MakeEvent(uint32(gamepb.GameServerEventCode_GSE_MAP_DATA), &gamepb.MapDataMsg{
		Stations: mapStations,
	})
	if mapFrame != nil {
		gw.eng.ConnMgr.SendReliable(connID, mapFrame)
	}
	gw.eng.Log.Log(CatWorldMap, "map data sent: conn=%d stations=%d", connID, len(mapStations))

	// Send current currency balances so the client has them immediately
	for curID, bal := range pdata.Currencies {
		curData := mmokit.MakeEvent(uint32(gamepb.GameServerEventCode_GSE_CURRENCY_UPDATE), &gamepb.CurrencyUpdateMsg{
			CurrencyId: curID,
			Balance:    bal,
			Earned:     0,
		})
		if curData != nil {
			gw.eng.ConnMgr.SendReliable(connID, curData)
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

	gw.eng.Log.Log(CatPlayerSpawn, "player reconnected: conn=%d netID=%d pos=(%.0f,%.0f)", connID, netID, pos.X, pos.Y)

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
		gw.eng.ConnMgr.SendReliable(connID, data)
	}

	// Send map data
	mapStations := gw.CollectStationMapData()
	mapFrame := mmokit.MakeEvent(uint32(gamepb.GameServerEventCode_GSE_MAP_DATA), &gamepb.MapDataMsg{
		Stations: mapStations,
	})
	if mapFrame != nil {
		gw.eng.ConnMgr.SendReliable(connID, mapFrame)
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
			gw.eng.ConnMgr.SendReliable(connID, curData)
		}
	}
}
