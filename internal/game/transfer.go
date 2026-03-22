package game

import (
	"github.com/mlange-42/ark/ecs"

	gamepb "github.com/zenion/mmoserver/gen/go"
	"github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/internal/netutil"
	"github.com/zenion/mmoserver/pkg/spatial"
)

// SerializeEntity reads all components from an entity and produces a TransferPayload.
// Entity references (TargetLock, MiningLaser targets, StatusEffect sources) are cleared.
func (gw *GameWorld) SerializeEntity(entity ecs.Entity) *TransferPayload {
	p := &TransferPayload{
		SourceTick: gw.Tick,
	}

	// Required components
	if gw.NetworkIDMap.HasAll(entity) {
		p.NetworkID = gw.NetworkIDMap.Get(entity).ID
	}
	if gw.EntityKindMap.HasAll(entity) {
		p.EntityType = gw.EntityKindMap.Get(entity).Type
	}
	if gw.PositionMap.HasAll(entity) {
		pos := gw.PositionMap.Get(entity)
		p.Position = *pos
	}
	if gw.SectorCoordMap.HasAll(entity) {
		sec := gw.SectorCoordMap.Get(entity)
		p.Sector = *sec
	}
	if gw.VelocityMap.HasAll(entity) {
		vel := gw.VelocityMap.Get(entity)
		p.Velocity = *vel
	}
	if gw.RotationMap.HasAll(entity) {
		rot := gw.RotationMap.Get(entity)
		p.Rotation = *rot
	}
	if gw.ColliderMap.HasAll(entity) {
		col := gw.ColliderMap.Get(entity)
		p.Collider = *col
	}

	// Optional components
	if gw.HealthMap.HasAll(entity) {
		h := gw.HealthMap.Get(entity)
		hCopy := *h
		p.Health = &hCopy
	}
	if gw.ShieldMap.HasAll(entity) {
		s := gw.ShieldMap.Get(entity)
		sCopy := *s
		p.Shield = &sCopy
	}
	if gw.ShipControlMap.HasAll(entity) {
		sc := gw.ShipControlMap.Get(entity)
		scCopy := *sc
		p.ShipControl = &scCopy
	}
	if gw.EquipmentMap.HasAll(entity) {
		eq := gw.EquipmentMap.Get(entity)
		eqCopy := *eq
		p.Equipment = &eqCopy
	}
	if gw.MoveTargetMap.HasAll(entity) {
		mt := gw.MoveTargetMap.Get(entity)
		mtCopy := *mt
		p.MoveTarget = &mtCopy
	}
	if gw.AbilitySetMap.HasAll(entity) {
		ab := gw.AbilitySetMap.Get(entity)
		abCopy := *ab
		p.AbilitySet = &abCopy
	}
	if gw.MinableMap.HasAll(entity) {
		m := gw.MinableMap.Get(entity)
		mCopy := *m
		p.Minable = &mCopy
	}
	if gw.LifetimeMap.HasAll(entity) {
		lt := gw.LifetimeMap.Get(entity)
		ltCopy := *lt
		p.Lifetime = &ltCopy
	}
	if gw.StatusEffectsMap.HasAll(entity) {
		se := gw.StatusEffectsMap.Get(entity)
		seCopy := *se
		// Clear entity references on status effect sources
		for i := uint8(0); i < seCopy.Count; i++ {
			seCopy.Effects[i].Source = ecs.Entity{}
		}
		p.StatusEffects = &seCopy
	}

	// Deep-copy inventory
	if gw.InventoryMap.HasAll(entity) {
		inv := gw.InventoryMap.Get(entity)
		p.MaxCargo = inv.MaxMass
		if len(inv.Items) > 0 {
			p.CargoItems = make(map[uint32]int32, len(inv.Items))
			for k, v := range inv.Items {
				p.CargoItems[k] = v
			}
		}
	}

	// Player-specific data
	if gw.PlayerConnMap.HasAll(entity) {
		connID := gw.PlayerConnMap.Get(entity).ConnID
		p.ConnID = connID
		p.Username = gw.ConnToUsername[connID]
	}

	return p
}

// SpawnFromTransfer creates a new entity from a TransferPayload received from another node.
// For player entities, it also wires up the connection mappings and sends the spawn message.
func (gw *GameWorld) SpawnFromTransfer(p *TransferPayload) ecs.Entity {
	switch p.EntityType {
	case component.TypeShip:
		return gw.spawnShipFromTransfer(p)
	case component.TypeAsteroid:
		return gw.spawnAsteroidFromTransfer(p)
	default:
		gw.Log.Log(CatTransfer, "unsupported transfer entity type=%d netID=%d", p.EntityType, p.NetworkID)
		return ecs.Entity{}
	}
}

// spawnShipFromTransfer creates a player ship from transfer data.
func (gw *GameWorld) spawnShipFromTransfer(p *TransferPayload) ecs.Entity {
	m := gw.shipMappers
	netID := gw.NextNetID()

	boundingRadius := boundingRadius(gw.Config.ShipWidth, gw.Config.ShipHeight)

	// Use transferred collider but ensure bounding radius is correct
	collider := p.Collider
	collider.Radius = boundingRadius
	collider.Width = gw.Config.ShipWidth
	collider.Height = gw.Config.ShipHeight
	collider.Layer = component.LayerPlayer
	collider.Shape = spatial.ShapeRect

	health := &component.Health{Current: gw.Config.ShipHealth, Max: gw.Config.ShipHealth}
	if p.Health != nil {
		health = p.Health
	}

	shipCtrl := &component.ShipControl{
		Thrust:   gw.Config.ShipThrust,
		TurnRate: gw.Config.ShipTurnRate,
		MaxSpeed: gw.Config.MaxSpeed,
	}
	if p.ShipControl != nil {
		shipCtrl = p.ShipControl
	}

	entity := m.base.NewEntity(
		&p.Position,
		&p.Velocity,
		&p.Rotation,
		&collider,
		&component.NetworkID{ID: netID},
		&component.EntityKind{Type: component.TypeShip},
		shipCtrl,
		health,
	)

	gw.SectorCoordMap.Add(entity, &p.Sector)

	// Shield
	shield := &component.Shield{Current: gw.Config.ShipShield, Max: gw.Config.ShipShield, RegenRate: gw.Config.ShieldRegenRate, RegenDelay: gw.Config.ShieldRegenDelay}
	if p.Shield != nil {
		shield = p.Shield
	}

	// Inventory
	inv := &component.Inventory{Items: p.CargoItems, MaxMass: p.MaxCargo}
	if inv.MaxMass == 0 {
		inv.MaxMass = gw.Config.MaxCargo
	}

	m.extras.Add(entity,
		shield,
		inv,
		&component.PlayerConn{ConnID: p.ConnID},
		&component.PlayerInput{},
	)

	// Combat components
	tl := &component.TargetLock{
		LockTime: gw.Config.LockOnTime,
		Range:    gw.Config.LockOnRange,
		Locked:   false,
		Progress: 0,
	}
	ab := &component.AbilitySet{}
	if p.AbilitySet != nil {
		ab = p.AbilitySet
	}
	se := &component.StatusEffects{}
	if p.StatusEffects != nil {
		se = p.StatusEffects
	}
	mt := &component.MoveTarget{}
	if p.MoveTarget != nil {
		mt = p.MoveTarget
	}

	m.combat.Add(entity, tl, ab, se, mt)

	// Mining laser — beams all inactive after transfer
	ml := &component.MiningLaser{}
	m.mining.Add(entity, ml)

	// Equipment
	equip := &component.Equipment{}
	if p.Equipment != nil {
		equip = p.Equipment
	}
	m.equip.Add(entity, equip)

	// Apply equipment passive stats
	gw.ApplyEquipmentStats(entity)

	// Add transfer cooldown to prevent immediate re-transfer
	gw.TransferCooldownMap.Add(entity, &component.TransferCooldown{Remaining: 10})

	// Wire up player connection mappings
	if p.ConnID != 0 {
		gw.PlayerEntities[p.ConnID] = entity
		gw.ConnToUsername[p.ConnID] = p.Username

		gw.Log.Log(CatTransfer, "ship spawned from transfer: conn=%d username=%s netID=%d (was %d) pos=(%.0f,%.0f) sector=(%d,%d)",
			p.ConnID, p.Username, netID, p.NetworkID, p.Position.X, p.Position.Y, p.Sector.SX, p.Sector.SY)

		// Send spawn message to client with new netID
		data := netutil.MakeEvent(uint32(gamepb.ServerEventCode_SE_PLAYER_SPAWNED), &gamepb.PlayerSpawnedMsg{
			YourEntityId:  netID,
			OriginSectorX: p.Sector.SX,
			OriginSectorY: p.Sector.SY,
			Equipment: &gamepb.EquipmentState{
				Weapon1:  equip.Weapon1,
				Weapon2:  equip.Weapon2,
				Shield:   equip.Shield,
				Thruster: equip.Thruster,
			},
		})
		if data != nil {
			gw.ConnMgr.SendReliable(p.ConnID, data)
		}

		// Send sector change notification
		secFrame := netutil.MakeEvent(uint32(gamepb.ServerEventCode_SE_SECTOR_CHANGE), &gamepb.SectorChangeMsg{
			SectorX: p.Sector.SX,
			SectorY: p.Sector.SY,
		})
		if secFrame != nil {
			gw.ConnMgr.SendReliable(p.ConnID, secFrame)
		}

		// Send map data
		mapFrame := netutil.MakeEvent(uint32(gamepb.ServerEventCode_SE_MAP_DATA), &gamepb.MapDataMsg{
			Stations: gw.CollectStationMapData(),
		})
		if mapFrame != nil {
			gw.ConnMgr.SendReliable(p.ConnID, mapFrame)
		}
	}

	return entity
}

// spawnAsteroidFromTransfer creates an asteroid entity from transfer data.
func (gw *GameWorld) spawnAsteroidFromTransfer(p *TransferPayload) ecs.Entity {
	m := gw.asteroidMappers
	netID := gw.NextNetID()

	entity := m.base.NewEntity(
		&p.Position,
		&p.Velocity,
		&p.Rotation,
		&p.Collider,
		&component.NetworkID{ID: netID},
		&component.EntityKind{Type: component.TypeAsteroid},
	)

	gw.SectorCoordMap.Add(entity, &p.Sector)
	if p.Minable != nil {
		m.minable.Add(entity, p.Minable)
	}

	// Add transfer cooldown
	gw.TransferCooldownMap.Add(entity, &component.TransferCooldown{Remaining: 10})

	gw.Log.Log(CatTransfer, "asteroid spawned from transfer: netID=%d (was %d) pos=(%.0f,%.0f) sector=(%d,%d)",
		netID, p.NetworkID, p.Position.X, p.Position.Y, p.Sector.SX, p.Sector.SY)

	return entity
}

