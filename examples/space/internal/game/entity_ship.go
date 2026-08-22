package game

import (
	"maps"
	"math/rand/v2"
	"time"

	"github.com/mmokit/mmokit"
	gamecomp "github.com/mmokit/mmokit/examples/space/internal/component"
	"github.com/mmokit/mmokit/examples/space/internal/item"
	"github.com/mmokit/mmokit/pkg/spatial"
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
	Selection     *gamecomp.Selection `mmokit:"local"`
	AbilitySet    *gamecomp.AbilitySet
	StatusEffects *gamecomp.StatusEffects
	Supercruise   *gamecomp.Supercruise
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
	if s.Entity != (mmokit.EntityHandle{}) && gw.stage.ECSWorld().Alive(s.Entity) {
		gw.reconnectPlayer(s)
		return
	}
	connID := s.ConnID

	// Check for saved player data
	var x, y float32
	var savedCargo map[uint32]int32
	pdata := gw.PlayerDB.Bind(s)
	pdata.LastLogin = time.Now()
	gw.PlayerDB.MarkDirtyByUserID(pdata.UserID)

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
			x += float32(cellX-gw.RootCell.CellX) * gw.stage.CellSize()
			y += float32(cellY-gw.RootCell.CellY) * gw.stage.CellSize()
		}
	} else {
		// Use gateway-resolved spawn (Process.OnResolveSpawn callback),
		// converted from world-space to this cell's local coords. Jitter so
		// stacked first-time logins don't collide.
		loc := s.SpawnLocation
		x = loc.X - float32(gw.RootCell.CellX)*gw.stage.CellSize() + (rand.Float32()-0.5)*16.7
		y = loc.Y - float32(gw.RootCell.CellY)*gw.stage.CellSize() + (rand.Float32()-0.5)*16.7
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
		// PVE v2: seed new-player cargo with the two new weapon items so
		// players can swap them in from inventory and exercise the new
		// AbilityTypes (PlasmaShot, HomingMissile, SustainedBeam, MortarShell)
		// without any setup. Drop when starter-kit selection lands.
		if savedCargo == nil {
			savedCargo = make(map[uint32]int32, 2)
		}
		savedCargo[item.PlasmaCannon] = 1
		savedCargo[item.BeamMortarBattery] = 1
	}

	br := boundingRadius(gw.Config.ShipWidth, gw.Config.ShipHeight)

	e := gw.stage.Spawn(
		mmokit.Position{X: x, Y: y},
		mmokit.EntityKind{Type: gamecomp.KindShip},
		mmokit.Collider{
			Width:  gw.Config.ShipWidth,
			Height: gw.Config.ShipHeight,
			Layer:  spatial.LayerEntity,
			Shape:  mmokit.ShapeBox,
			Radius: br,
		},
		mmokit.Rotation{},
		mmokit.PlayerConn{ConnID: connID},
		gamecomp.PilotName{Name: s.Username},
		gamecomp.ShipControl{
			Thrust:    gw.Config.ShipThrust,
			TurnRate:  gw.Config.ShipTurnRate,
			TurnAccel: gw.Config.ShipTurnAccel,
			MaxSpeed:  gw.Config.MaxSpeed,
		},
		gamecomp.Health{Current: gw.Config.ShipHealth, Max: gw.Config.ShipHealth},
		gamecomp.Shield{
			Current:    gw.Config.ShipShield,
			Max:        gw.Config.ShipShield,
			RegenRate:  gw.Config.ShieldRegenRate,
			RegenDelay: gw.Config.ShieldRegenDelay,
		},
		gamecomp.Inventory{Items: savedCargo, MaxMass: gw.Config.MaxCargo},
		gamecomp.Selection{EntityNetID: 0},
		gamecomp.Equipment{
			Weapon1:  equip.Weapon1,
			Weapon2:  equip.Weapon2,
			Shield:   equip.Shield,
			Thruster: equip.Thruster,
		},
		gamecomp.AbilitySet{},
		gamecomp.StatusEffects{},
		gamecomp.Supercruise{},
		mmokit.MoveTarget{},
		gamecomp.LockedBy{},
		gamecomp.ActiveMining{},
		gamecomp.PlayerInput{},
		gamecomp.MiningLaser{},
	)

	// Apply equipment passive stats (shield max/regen, thrust/speed).
	gw.ApplyEquipmentStats(e)

	// Fresh spawn: top off shield to its post-equipment Max. Base ShipShield
	// is 0, so the entity was created with Current=0; ApplyEquipmentStats
	// raises Max to the gen's full value but doesn't auto-fill Current
	// (that would also fire on every equipment swap, which we don't want).
	// We DO want a fresh spawn / respawn to start with full shield — both
	// because it matches player expectations and because otherwise the
	// 2s regen delay would leave the player essentially unshielded for
	// several seconds on every login.
	if shield := mmokit.Get[gamecomp.Shield](e); shield != nil {
		shield.Current = shield.Max
	}

	handle := e.Handle()
	s.Entity = handle
	netID := e.NetID()
	sec := mmokit.Get[mmokit.CellCoord](e)
	gw.eng.Log.Log(CatPlayerSpawn, "player spawned: conn=%d netID=%d pos=(%.0f,%.0f) equip=[w1=%d w2=%d sh=%d th=%d]",
		connID, netID, x, y, equip.Weapon1, equip.Weapon2, equip.Shield, equip.Thruster)

	// Send spawn message to client
	allItems := item.All()
	itemDefs := make([]ItemDef, 0, len(allItems))
	for _, def := range allItems {
		itemDefs = append(itemDefs, ItemDef{
			ID:          def.ID,
			Name:        def.Name,
			MassPerUnit: def.MassPerUnit,
			Category:    uint32(def.Category),
			EquipSlot:   uint32(def.EquipSlot),
		})
	}
	mmokit.SendEvent(gw.stage, connID, &PlayerSpawned{
		YourEntityID: netID,
		ItemDefs:     itemDefs,
		OriginCellX:  sec.CellX,
		OriginCellY:  sec.CellY,
		Equipment: EquipmentState{
			Weapon1:  equip.Weapon1,
			Weapon2:  equip.Weapon2,
			Shield:   equip.Shield,
			Thruster: equip.Thruster,
		},
	})

	// Send map data (station positions) to the client
	mapStations := gw.CollectStationMapData()
	mmokit.SendEvent(gw.stage, connID, &MapData{Stations: mapStations})
	gw.eng.Log.Log(CatWorldMap, "map data sent: conn=%d stations=%d", connID, len(mapStations))

	// Send current currency balances so the client has them immediately
	for curID, bal := range pdata.Currencies {
		mmokit.SendEvent(gw.stage, connID, &CurrencyUpdate{
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
	entity := mmokit.EntityFromECS(gw.stage, s.Entity)
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
	var equip EquipmentState
	if eq := mmokit.Get[gamecomp.Equipment](entity); eq != nil {
		equip = EquipmentState{
			Weapon1:  eq.Weapon1,
			Weapon2:  eq.Weapon2,
			Shield:   eq.Shield,
			Thruster: eq.Thruster,
		}
	}

	// Send same spawn message as fresh login — client needs entity ID
	allItems := item.All()
	itemDefs := make([]ItemDef, 0, len(allItems))
	for _, def := range allItems {
		itemDefs = append(itemDefs, ItemDef{
			ID:          def.ID,
			Name:        def.Name,
			MassPerUnit: def.MassPerUnit,
			Category:    uint32(def.Category),
			EquipSlot:   uint32(def.EquipSlot),
		})
	}
	mmokit.SendEvent(gw.stage, connID, &PlayerSpawned{
		YourEntityID: netID,
		ItemDefs:     itemDefs,
		OriginCellX:  sec.CellX,
		OriginCellY:  sec.CellY,
		Equipment:    equip,
	})

	// Send map data
	mapStations := gw.CollectStationMapData()
	mmokit.SendEvent(gw.stage, connID, &MapData{Stations: mapStations})

	// Send currency balances
	pdata := gw.PlayerDB.Bind(s)
	for curID, bal := range pdata.Currencies {
		mmokit.SendEvent(gw.stage, connID, &CurrencyUpdate{
			CurrencyID: curID,
			Balance:    bal,
			Earned:     0,
		})
	}
}
