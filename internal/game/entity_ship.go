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

// ShipBundle is the entity-kind component bundle for player ships. Defines
// every component a ship carries — transferred fields first, local-only
// (added on transfer receive but never serialized) last.
type ShipBundle struct {
	PilotName     *gamecomp.PilotName
	Health        *gamecomp.Health
	Shield        *gamecomp.Shield
	ShipControl   *gamecomp.ShipControl
	Equipment     *gamecomp.Equipment
	Inventory     *gamecomp.Inventory
	TargetLock    *gamecomp.TargetLock
	AbilitySet    *gamecomp.AbilitySet
	StatusEffects *gamecomp.StatusEffects
	MoveTarget    *mmokit.MoveTarget
	LockedBy      *gamecomp.LockedBy
	ActiveMining  *gamecomp.ActiveMining
	PlayerInput   *gamecomp.PlayerInput `mmokit:"local"`
	MiningLaser   *gamecomp.MiningLaser `mmokit:"local"`
}

// SpawnPlayer creates a new player ship entity.
// Restores saved position/inventory/equipment, or applies starter loadout for new/dead players.
// If s.Entity is already alive, this is a reconnection or cross-cell transfer —
// reuse the existing entity instead of creating a new one.
func (gw *GameWorld) SpawnPlayer(s *mmokit.PlayerSession) {
	if s.Entity != (ecs.Entity{}) && gw.Stage.ECSWorld().Alive(s.Entity) {
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
		// Use gateway-resolved spawn (Config.DefaultSpawn for new players),
		// converted from world-space to this cell's local coords. Jitter so
		// stacked first-time logins don't collide.
		loc := s.SpawnLocation
		x = loc.X - float32(gw.RootCell.CellX)*coords.CellSize + (rand.Float32()-0.5)*16.7
		y = loc.Y - float32(gw.RootCell.CellY)*coords.CellSize + (rand.Float32()-0.5)*16.7
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

	handle := gw.SpawnEntity(
		mmokit.Position{X: x, Y: y},
		mmokit.WithEntityKind(gamecomp.TypeShip),
		mmokit.WithCollider(br),
		mmokit.WithRotation(0),  // ShipDynamicsSystem reads Rotation for turn-rate steering
		mmokit.WithComponents(), // auto-adds all registered ship components
	)
	entity := mmokit.EntityFromECS(gw.Stage, handle)

	// Set collider shape details (SpawnEntity only sets radius)
	if col := mmokit.Get[mmokit.Collider](entity); col != nil {
		col.Width = gw.Config.ShipWidth
		col.Height = gw.Config.ShipHeight
		col.Layer = gamecomp.LayerPlayer
		col.Shape = mmokit.ShapeRect
	}

	// Wire player connection
	mmokit.Set(entity, mmokit.PlayerConn{ConnID: connID})

	// Set pilot name for replication
	if pn := mmokit.Get[gamecomp.PilotName](entity); pn != nil {
		pn.Name = username
	}

	// Set non-zero field values on auto-added components
	mmokit.Set(entity, gamecomp.ShipControl{
		Thrust:    gw.Config.ShipThrust,
		TurnRate:  gw.Config.ShipTurnRate,
		TurnAccel: gw.Config.ShipTurnAccel,
		MaxSpeed:  gw.Config.MaxSpeed,
	})
	mmokit.Set(entity, gamecomp.Health{Current: gw.Config.ShipHealth, Max: gw.Config.ShipHealth})
	mmokit.Set(entity, gamecomp.Shield{Current: gw.Config.ShipShield, Max: gw.Config.ShipShield, RegenRate: gw.Config.ShieldRegenRate, RegenDelay: gw.Config.ShieldRegenDelay})
	mmokit.Set(entity, gamecomp.Inventory{Items: savedCargo, MaxMass: gw.Config.MaxCargo})
	mmokit.Set(entity, gamecomp.TargetLock{
		LockTime: gw.Config.LockOnTime,
		Range:    gw.Config.LockOnRange,
	})
	mmokit.Set(entity, equip)

	// Apply equipment passive stats (shield max/regen, thrust/speed)
	gw.ApplyEquipmentStats(entity)

	s.Entity = handle
	netID := entity.NetID()
	sec := mmokit.Get[mmokit.CellCoord](entity)
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
			Category:    uint32(def.Category),
			EquipSlot:   uint32(def.EquipSlot),
		})
	}
	gw.ServerEvents().Send(gw.eng.ConnMgr, connID, uint32(enginepb.ServerEventCode_SE_PLAYER_SPAWNED), &gamepb.PlayerSpawnedMsg{
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

	// Send map data (station positions) to the client
	mapStations := gw.CollectStationMapData()
	gw.ServerEvents().Send(gw.eng.ConnMgr, connID, uint32(gamepb.GameServerEventCode_GSE_MAP_DATA), &gamepb.MapDataMsg{
		Stations: mapStations,
	})
	gw.eng.Log.Log(CatWorldMap, "map data sent: conn=%d stations=%d", connID, len(mapStations))

	// Send current currency balances so the client has them immediately
	for curID, bal := range pdata.Currencies {
		mmokit.SendEvent(gw.Stage, connID, &CurrencyUpdate{
			CurrencyID: curID,
			Balance:    bal,
			Earned:     0,
		})
	}
}

// reconnectPlayer reuses an existing entity for a reconnecting player
// (grace period reconnection). Updates the PlayerConn component with the
// new connID and sends the client the spawn message so it knows its entity ID.
func (gw *GameWorld) reconnectPlayer(s *mmokit.PlayerSession) {
	entity := mmokit.EntityFromECS(gw.Stage, s.Entity)
	connID := s.ConnID

	// Update PlayerConn with new connection ID
	if pc := mmokit.Get[mmokit.PlayerConn](entity); pc != nil {
		pc.ConnID = connID
	}

	netID := entity.NetID()
	pos := mmokit.Get[mmokit.Position](entity)
	sec := mmokit.Get[mmokit.CellCoord](entity)

	gw.eng.Log.Log(CatPlayerSpawn, "player reconnected: conn=%d netID=%d pos=(%.0f,%.0f)", connID, netID, pos.X, pos.Y)

	// Read equipment for spawn message
	var equip gamepb.EquipmentState
	if eq := mmokit.Get[gamecomp.Equipment](entity); eq != nil {
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
			Category:    uint32(def.Category),
			EquipSlot:   uint32(def.EquipSlot),
		})
	}
	gw.ServerEvents().Send(gw.eng.ConnMgr, connID, uint32(enginepb.ServerEventCode_SE_PLAYER_SPAWNED), &gamepb.PlayerSpawnedMsg{
		YourEntityId: netID,
		ItemDefs:     itemDefs,
		OriginCellX:  sec.CellX,
		OriginCellY:  sec.CellY,
		Equipment:    &equip,
	})

	// Send map data
	mapStations := gw.CollectStationMapData()
	gw.ServerEvents().Send(gw.eng.ConnMgr, connID, uint32(gamepb.GameServerEventCode_GSE_MAP_DATA), &gamepb.MapDataMsg{
		Stations: mapStations,
	})

	// Send currency balances
	pdata := gw.PlayerDB.GetOrCreate(s.Username)
	for curID, bal := range pdata.Currencies {
		mmokit.SendEvent(gw.Stage, connID, &CurrencyUpdate{
			CurrencyID: curID,
			Balance:    bal,
			Earned:     0,
		})
	}
}
