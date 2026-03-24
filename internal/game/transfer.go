package game

import (
	"github.com/mlange-42/ark/ecs"

	enginepb "github.com/zenion/mmoserver/gen/go/enginepb"
	gamepb "github.com/zenion/mmoserver/gen/go/gamepb"
	comp "github.com/zenion/mmoserver/pkg/component"
	gamecomp "github.com/zenion/mmoserver/internal/component"
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
	if gw.C.NetworkID.HasAll(entity) {
		p.NetworkID = gw.C.NetworkID.Get(entity).ID
	}
	if gw.C.EntityKind.HasAll(entity) {
		p.EntityType = gw.C.EntityKind.Get(entity).Type
	}
	if gw.C.Position.HasAll(entity) {
		pos := gw.C.Position.Get(entity)
		p.Position = *pos
	}
	if gw.C.SectorCoord.HasAll(entity) {
		sec := gw.C.SectorCoord.Get(entity)
		p.Sector = *sec
	}
	if gw.C.Velocity.HasAll(entity) {
		vel := gw.C.Velocity.Get(entity)
		p.Velocity = *vel
	}
	if gw.C.Rotation.HasAll(entity) {
		rot := gw.C.Rotation.Get(entity)
		p.Rotation = *rot
	}
	if gw.C.Collider.HasAll(entity) {
		col := gw.C.Collider.Get(entity)
		p.Collider = *col
	}

	// Optional components
	if gw.C.Health.HasAll(entity) {
		h := gw.C.Health.Get(entity)
		hCopy := *h
		p.Health = &hCopy
	}
	if gw.C.Shield.HasAll(entity) {
		s := gw.C.Shield.Get(entity)
		sCopy := *s
		p.Shield = &sCopy
	}
	if gw.C.ShipControl.HasAll(entity) {
		sc := gw.C.ShipControl.Get(entity)
		scCopy := *sc
		p.ShipControl = &scCopy
	}
	if gw.C.Equipment.HasAll(entity) {
		eq := gw.C.Equipment.Get(entity)
		eqCopy := *eq
		p.Equipment = &eqCopy
	}
	if gw.C.MoveTarget.HasAll(entity) {
		mt := gw.C.MoveTarget.Get(entity)
		mtCopy := *mt
		p.MoveTarget = &mtCopy
	}
	if gw.C.AbilitySet.HasAll(entity) {
		ab := gw.C.AbilitySet.Get(entity)
		abCopy := *ab
		p.AbilitySet = &abCopy
	}
	if gw.C.Minable.HasAll(entity) {
		m := gw.C.Minable.Get(entity)
		mCopy := *m
		p.Minable = &mCopy
	}
	if gw.C.Lifetime.HasAll(entity) {
		lt := gw.C.Lifetime.Get(entity)
		ltCopy := *lt
		p.Lifetime = &ltCopy
	}
	if gw.C.StatusEffects.HasAll(entity) {
		se := gw.C.StatusEffects.Get(entity)
		seCopy := *se
		// Clear entity references on status effect sources
		for i := uint8(0); i < seCopy.Count; i++ {
			seCopy.Effects[i].Source = ecs.Entity{}
		}
		p.StatusEffects = &seCopy
	}

	// Deep-copy inventory
	if gw.C.Inventory.HasAll(entity) {
		inv := gw.C.Inventory.Get(entity)
		p.MaxCargo = inv.MaxMass
		if len(inv.Items) > 0 {
			p.CargoItems = make(map[uint32]int32, len(inv.Items))
			for k, v := range inv.Items {
				p.CargoItems[k] = v
			}
		}
	}

	// Player-specific data
	if gw.C.PlayerConn.HasAll(entity) {
		connID := gw.C.PlayerConn.Get(entity).ConnID
		p.ConnID = connID
		p.Username = gw.Players.Usernames[connID]
	}

	return p
}

// finishTransferSpawn adds common components and logging after creating a transferred entity.
func (gw *GameWorld) finishTransferSpawn(entity ecs.Entity, p *TransferPayload, typeName string) {
	gw.C.SectorCoord.Add(entity, &p.Sector)
	gw.C.TransferCooldown.Add(entity, &comp.TransferCooldown{Remaining: 10})
	gw.Log.Log(CatTransfer, "%s spawned from transfer: netID=%d pos=(%.0f,%.0f) sector=(%d,%d)",
		typeName, p.NetworkID, p.Position.X, p.Position.Y, p.Sector.SX, p.Sector.SY)
}

// valOr returns val if non-nil, otherwise returns def.
func valOr[T any](val *T, def T) *T {
	if val != nil {
		return val
	}
	return &def
}

// SpawnFromTransfer creates a new entity from a TransferPayload received from another node.
// For player entities, it also wires up the connection mappings and sends the spawn message.
func (gw *GameWorld) SpawnFromTransfer(p *TransferPayload) ecs.Entity {
	switch p.EntityType {
	case gamecomp.TypeShip:
		return gw.spawnShipFromTransfer(p)
	case gamecomp.TypeAsteroid:
		return gw.spawnAsteroidFromTransfer(p)
	case gamecomp.TypeNPC:
		return gw.spawnNpcFromTransfer(p)
	case gamecomp.TypeLootCrate:
		return gw.spawnLootCrateFromTransfer(p)
	default:
		gw.Log.Log(CatTransfer, "unsupported transfer entity type=%d netID=%d", p.EntityType, p.NetworkID)
		return ecs.Entity{}
	}
}

// spawnShipFromTransfer creates a player ship from transfer data.
func (gw *GameWorld) spawnShipFromTransfer(p *TransferPayload) ecs.Entity {
	m := gw.Registry.ByType(gamecomp.TypeShip).Mappers.(*shipMappers)
	br := boundingRadius(gw.Config.ShipWidth, gw.Config.ShipHeight)
	collider := p.Collider
	collider.Radius = br
	collider.Width = gw.Config.ShipWidth
	collider.Height = gw.Config.ShipHeight
	collider.Layer = gamecomp.LayerPlayer
	collider.Shape = spatial.ShapeRect

	entity := m.base.NewEntity(
		&p.Position, &p.Velocity, &p.Rotation, &collider,
		&comp.NetworkID{ID: p.NetworkID},
		&comp.EntityKind{Type: gamecomp.TypeShip},
		valOr(p.ShipControl, gamecomp.ShipControl{Thrust: gw.Config.ShipThrust, TurnRate: gw.Config.ShipTurnRate, MaxSpeed: gw.Config.MaxSpeed}),
		valOr(p.Health, comp.Health{Current: gw.Config.ShipHealth, Max: gw.Config.ShipHealth}),
	)
	gw.finishTransferSpawn(entity, p, "ship")

	inv := &gamecomp.Inventory{Items: p.CargoItems, MaxMass: p.MaxCargo}
	if inv.MaxMass == 0 {
		inv.MaxMass = gw.Config.MaxCargo
	}
	m.extras.Add(entity,
		valOr(p.Shield, comp.Shield{Current: gw.Config.ShipShield, Max: gw.Config.ShipShield, RegenRate: gw.Config.ShieldRegenRate, RegenDelay: gw.Config.ShieldRegenDelay}),
		inv,
		&comp.PlayerConn{ConnID: p.ConnID},
		&gamecomp.PlayerInput{},
	)

	m.combat.Add(entity,
		&comp.TargetLock{LockTime: gw.Config.LockOnTime, Range: gw.Config.LockOnRange},
		valOr(p.AbilitySet, gamecomp.AbilitySet{}),
		valOr(p.StatusEffects, gamecomp.StatusEffects{}),
		valOr(p.MoveTarget, comp.MoveTarget{}),
	)
	m.mining.Add(entity, &gamecomp.MiningLaser{})
	m.equip.Add(entity, valOr(p.Equipment, gamecomp.Equipment{}))
	gw.ApplyEquipmentStats(entity)

	// Wire up player connection mappings
	if p.ConnID != 0 {
		gw.Players.Entities[p.ConnID] = entity
		gw.Players.Usernames[p.ConnID] = p.Username
		gw.Log.Log(CatTransfer, "ship transfer wired: conn=%d username=%s", p.ConnID, p.Username)

		// Send sector change — NOT SE_PLAYER_SPAWNED (which would clear client entities).
		// The entity keeps the same NetworkID so the client tracks it seamlessly.
		secFrame := netutil.MakeEvent(uint32(enginepb.ServerEventCode_SE_SECTOR_CHANGE), &enginepb.SectorChangeMsg{
			SectorX: p.Sector.SX,
			SectorY: p.Sector.SY,
		})
		if secFrame != nil {
			gw.ConnMgr.SendReliable(p.ConnID, secFrame)
		}

		// Send map data for the new sector
		mapFrame := netutil.MakeEvent(uint32(gamepb.GameServerEventCode_GSE_MAP_DATA), &gamepb.MapDataMsg{
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
	m := gw.Registry.ByType(gamecomp.TypeAsteroid).Mappers.(*asteroidMappers)
	entity := m.base.NewEntity(
		&p.Position, &p.Velocity, &p.Rotation, &p.Collider,
		&comp.NetworkID{ID: p.NetworkID},
		&comp.EntityKind{Type: gamecomp.TypeAsteroid},
	)
	gw.finishTransferSpawn(entity, p, "asteroid")
	if p.Minable != nil {
		m.minable.Add(entity, p.Minable)
	}
	return entity
}

// spawnNpcFromTransfer creates an NPC entity from transfer data.
func (gw *GameWorld) spawnNpcFromTransfer(p *TransferPayload) ecs.Entity {
	m := gw.Registry.ByType(gamecomp.TypeNPC).Mappers.(*npcMappers)
	br := boundingRadius(gw.Config.NpcWidth, gw.Config.NpcHeight)
	collider := p.Collider
	collider.Radius = br
	collider.Width = gw.Config.NpcWidth
	collider.Height = gw.Config.NpcHeight
	collider.Layer = gamecomp.LayerPlayer
	collider.Shape = spatial.ShapeRect

	entity := m.base.NewEntity(
		&p.Position, &p.Velocity, &p.Rotation, &collider,
		&comp.NetworkID{ID: p.NetworkID},
		&comp.EntityKind{Type: gamecomp.TypeNPC},
	)
	gw.finishTransferSpawn(entity, p, "npc")

	m.combat.Add(entity,
		valOr(p.Health, comp.Health{Current: gw.Config.NpcHealth, Max: gw.Config.NpcHealth}),
		valOr(p.Shield, comp.Shield{Current: gw.Config.NpcShield, Max: gw.Config.NpcShield, RegenRate: gw.Config.NpcShieldRegenRate, RegenDelay: gw.Config.NpcShieldRegenDelay}),
		valOr(p.StatusEffects, gamecomp.StatusEffects{}),
	)
	return entity
}

// spawnLootCrateFromTransfer creates a loot crate entity from transfer data.
func (gw *GameWorld) spawnLootCrateFromTransfer(p *TransferPayload) ecs.Entity {
	m := gw.Registry.ByType(gamecomp.TypeLootCrate).Mappers.(*lootCrateMappers)
	entity := m.base.NewEntity(
		&p.Position, &p.Velocity, &p.Rotation,
		&comp.Collider{Radius: gw.Config.LootCrateRadius, Layer: 0},
		&comp.NetworkID{ID: p.NetworkID},
		&comp.EntityKind{Type: gamecomp.TypeLootCrate},
	)
	gw.finishTransferSpawn(entity, p, "loot crate")

	items := p.CargoItems
	if items == nil {
		items = make(map[uint32]int32)
	}
	m.extras.Add(entity,
		&gamecomp.Inventory{Items: items, MaxMass: p.MaxCargo},
		valOr(p.Lifetime, comp.Lifetime{Remaining: gw.Config.LootCrateLifetime}),
		&gamecomp.LootCrate{},
	)
	return entity
}

