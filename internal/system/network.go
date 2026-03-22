package system

import (
	"github.com/mlange-42/ark/ecs"

	gamepb "github.com/zenion/mmoserver/gen/go"
	"github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/internal/game"
	"github.com/zenion/mmoserver/internal/netutil"
	"github.com/zenion/mmoserver/pkg/coords"
	"github.com/zenion/mmoserver/pkg/spatial"
)

// lockerInfo tracks the most-progressed entity locking a given target.
type lockerInfo struct {
	netID    uint32
	progress float32
}

// NetworkSystem serializes visible state and broadcasts to each player.
type NetworkSystem struct {
	gw           *game.GameWorld
	playerFilter *ecs.Filter3[component.Position, component.PlayerConn, component.PlayerInput]
	lockFilter   *ecs.Filter2[component.TargetLock, component.NetworkID]
	results      []spatial.Entry
	entityStates []*gamepb.EntityState

	// Handler registry for per-type serialization
	handlers *NetHandlerRegistry

	// Per-player: set of network IDs that were visible last tick
	lastVisible map[uint32]map[uint32]bool // connID -> set of netIDs

	// Per-player last-sent hash for diffing (connID -> netID -> hash)
	lastSentHash map[uint32]map[uint32]uint64

	// Reverse lock map: target entity -> most-progressed locker (rebuilt each tick)
	lockedBy map[ecs.Entity]lockerInfo

	// Reusable hasher (reset per entity)
	hasher SnapshotHasher
}

func NewNetworkSystem(gw *game.GameWorld) *NetworkSystem {
	handlers := NewNetHandlerRegistry()
	handlers.Register(&ShipNetHandler{gw: gw})
	handlers.Register(&NpcNetHandler{})
	handlers.Register(&AsteroidNetHandler{})
	handlers.Register(&LootCrateNetHandler{})
	handlers.Register(&StationNetHandler{})

	return &NetworkSystem{
		gw:           gw,
		handlers:     handlers,
		results:      make([]spatial.Entry, 0, 256),
		entityStates: make([]*gamepb.EntityState, 0, 256),
		lastVisible:  make(map[uint32]map[uint32]bool),
		lastSentHash: make(map[uint32]map[uint32]uint64),
		lockedBy:     make(map[ecs.Entity]lockerInfo),
	}
}

// entityRelativePos computes the position of an entity relative to the player's sector.
func (s *NetworkSystem) entityRelativePos(ctx *NetworkContext, entry spatial.Entry) (float32, float32) {
	var entitySX, entitySY int32
	if s.gw.SectorCoordMap.HasAll(entry.Entity) {
		sec := s.gw.SectorCoordMap.Get(entry.Entity)
		entitySX = sec.SX
		entitySY = sec.SY
	}
	relX := float32(entitySX-ctx.PlayerSX)*coords.SectorSize + entry.X
	relY := float32(entitySY-ctx.PlayerSY)*coords.SectorSize + entry.Y
	return relX, relY
}

// hashEntity hashes the base fields and delegates type-specific hashing to the handler.
func (s *NetworkSystem) hashEntity(ctx *NetworkContext, entry spatial.Entry, entityType uint8) uint64 {
	s.hasher.Reset()

	// Base fields: player-relative position, velocity, rotation
	relX, relY := s.entityRelativePos(ctx, entry)
	s.hasher.Float32(relX)
	s.hasher.Float32(relY)

	gw := ctx.GW
	if gw.VelocityMap.HasAll(entry.Entity) {
		vel := gw.VelocityMap.Get(entry.Entity)
		s.hasher.Float32(vel.X)
		s.hasher.Float32(vel.Y)
	}

	if gw.RotationMap.HasAll(entry.Entity) {
		s.hasher.Float32(gw.RotationMap.Get(entry.Entity).Angle)
	}

	// Locked-by state (base field on all entities)
	if lb, ok := ctx.LockedBy[entry.Entity]; ok {
		s.hasher.Uint32(lb.netID)
		s.hasher.Float32(lb.progress)
	} else {
		s.hasher.Uint32(0)
		s.hasher.Float32(0)
	}

	// Type-specific hash
	if handler := s.handlers.Get(entityType); handler != nil {
		handler.HashSnapshot(&s.hasher, ctx, entry)
	}

	return s.hasher.Sum()
}

// serializeEntity populates base fields and delegates type-specific data to the handler.
func (s *NetworkSystem) serializeEntity(ctx *NetworkContext, entry spatial.Entry, entityType uint8) *gamepb.EntityState {
	gw := ctx.GW
	netID := gw.NetworkIDMap.Get(entry.Entity).ID

	relX, relY := s.entityRelativePos(ctx, entry)
	state := &gamepb.EntityState{
		Id:         netID,
		EntityType: gamepb.EntityType(entityType),
		X:          relX,
		Y:          relY,
		Radius:     entry.Radius,
		Width:      entry.Width,
		Height:     entry.Height,
	}

	if gw.VelocityMap.HasAll(entry.Entity) {
		vel := gw.VelocityMap.Get(entry.Entity)
		state.Vx = vel.X
		state.Vy = vel.Y
	}

	if gw.RotationMap.HasAll(entry.Entity) {
		state.Rotation = gw.RotationMap.Get(entry.Entity).Angle
	}

	// Locked-by state (base field)
	if lb, ok := ctx.LockedBy[entry.Entity]; ok {
		state.LockedById = lb.netID
		state.LockedByProgress = lb.progress
	}

	// Type-specific data
	if handler := s.handlers.Get(entityType); handler != nil {
		handler.Serialize(state, ctx, entry)
	}

	return state
}

// sendOwnState builds and sends PlayerOwnStateMsg to the owning player each tick.
func (s *NetworkSystem) sendOwnState(ctx *NetworkContext, connID uint32, entity ecs.Entity) {
	gw := ctx.GW

	msg := &gamepb.PlayerOwnStateMsg{}

	// Lock-on state
	if gw.TargetLockMap.HasAll(entity) {
		lock := gw.TargetLockMap.Get(entity)
		msg.LockProgress = lock.Progress
		msg.LockTargetId = lock.TargetNetID
	}

	// Ability cooldowns
	if gw.AbilitySetMap.HasAll(entity) {
		abilities := gw.AbilitySetMap.Get(entity)
		for slot := uint32(0); slot < uint32(component.AbilityCount); slot++ {
			cd := abilities.Cooldowns[slot]
			if cd > 0 {
				msg.AbilityCooldowns = append(msg.AbilityCooldowns, &gamepb.AbilityCooldownState{
					Slot:      slot,
					Remaining: cd,
					Total:     gw.AbilityCooldownForSlot(entity, uint8(slot)),
				})
			}
		}
	}

	// Equipment state
	if gw.EquipmentMap.HasAll(entity) {
		eq := gw.EquipmentMap.Get(entity)
		msg.Equipment = &gamepb.EquipmentState{
			Weapon1:  eq.Weapon1,
			Weapon2:  eq.Weapon2,
			Shield:   eq.Shield,
			Thruster: eq.Thruster,
		}
	}

	// Cargo inventory
	if gw.InventoryMap.HasAll(entity) {
		inv := gw.InventoryMap.Get(entity)
		for itemID, qty := range inv.Items {
			if qty > 0 {
				msg.CargoItems = append(msg.CargoItems, &gamepb.InventoryItem{
					ItemId:   itemID,
					Quantity: qty,
				})
			}
		}
		msg.CargoMass = inv.TotalMass()
		msg.MaxCargoMass = inv.MaxMass
	}

	// Being-locked state from reverse map
	if lb, ok := ctx.LockedBy[entity]; ok {
		msg.BeingLockedById = lb.netID
		msg.BeingLockedByProgress = lb.progress
	}

	data := netutil.MakeEvent(uint32(gamepb.ServerEventCode_SE_PLAYER_OWN_STATE), msg)
	if data != nil {
		gw.ConnMgr.Send(connID, data)
	}
}

func (s *NetworkSystem) Update(dt float32) {
	gw := s.gw
	if s.playerFilter == nil {
		s.playerFilter = ecs.NewFilter3[component.Position, component.PlayerConn, component.PlayerInput](gw.ECS).Without(ecs.C[component.Ghost]())
	}
	if s.lockFilter == nil {
		s.lockFilter = ecs.NewFilter2[component.TargetLock, component.NetworkID](gw.ECS)
	}

	// Build reverse lock map: for each entity being locked, track the most-progressed locker
	clear(s.lockedBy)
	lockQuery := s.lockFilter.Query()
	for lockQuery.Next() {
		lock, netID := lockQuery.Get()
		if lock.TargetNetID == 0 || lock.Progress <= 0 {
			continue
		}
		if !gw.ECS.Alive(lock.TargetEntity) {
			continue
		}
		if existing, ok := s.lockedBy[lock.TargetEntity]; !ok || lock.Progress > existing.progress {
			s.lockedBy[lock.TargetEntity] = lockerInfo{netID: netID.ID, progress: lock.Progress}
		}
	}

	// Build per-tick network context
	ctx := &NetworkContext{
		GW:       gw,
		LockedBy: s.lockedBy,
	}

	isFullRefresh := gw.FullRefreshInterval > 0 && gw.Tick%gw.FullRefreshInterval == 0

	// Clean up tracking for disconnected players
	for connID := range s.lastVisible {
		if _, ok := gw.PlayerEntities[connID]; !ok {
			delete(s.lastVisible, connID)
			delete(s.lastSentHash, connID)
		}
	}

	query := s.playerFilter.Query()
	for query.Next() {
		pos, conn, input := query.Get()

		// Set player's sector for relative position computation
		playerEntity := query.Entity()
		if gw.SectorCoordMap.HasAll(playerEntity) {
			sec := gw.SectorCoordMap.Get(playerEntity)
			ctx.PlayerSX = sec.SX
			ctx.PlayerSY = sec.SY
		} else {
			ctx.PlayerSX = 0
			ctx.PlayerSY = 0
		}

		// Query entities within AoI
		s.results = s.results[:0]
		s.results = gw.Grid.QueryRadius(pos.X, pos.Y, gw.Config.AoIRadius, s.results)

		// Build entity states and track visible IDs this tick
		s.entityStates = s.entityStates[:0]
		currentVisible := make(map[uint32]bool, len(s.results))

		var sentCount, skippedCount int

		for _, entry := range s.results {
			if !gw.ECS.Alive(entry.Entity) {
				continue
			}

			netID := gw.NetworkIDMap.Get(entry.Entity).ID
			currentVisible[netID] = true

			// Get entity type
			var entityType uint8
			if gw.EntityKindMap.HasAll(entry.Entity) {
				entityType = gw.EntityKindMap.Get(entry.Entity).Type
			}

			// Determine if this entity is new to the player's AoI
			isNewToPlayer := true
			if prev, ok := s.lastVisible[conn.ConnID]; ok && prev[netID] {
				isNewToPlayer = false
			}

			// Hash entity and compare to last-sent hash
			hash := s.hashEntity(ctx, entry, entityType)

			if !isNewToPlayer && !isFullRefresh {
				if playerHashes, ok := s.lastSentHash[conn.ConnID]; ok {
					if lastHash, ok := playerHashes[netID]; ok && lastHash == hash {
						skippedCount++
						continue // Nothing changed — skip proto serialization
					}
				}
			}

			// Update stored hash
			if s.lastSentHash[conn.ConnID] == nil {
				s.lastSentHash[conn.ConnID] = make(map[uint32]uint64)
			}
			s.lastSentHash[conn.ConnID][netID] = hash

			sentCount++

			state := s.serializeEntity(ctx, entry, entityType)
			s.entityStates = append(s.entityStates, state)
		}

		// Compute removed IDs (AoI exits) and killed IDs (actual deaths/despawns)
		var removedIDs []uint32
		var killedIDs []uint32

		if prev, ok := s.lastVisible[conn.ConnID]; ok {
			// Build set of globally killed net IDs for fast lookup
			killedSet := make(map[uint32]bool, len(gw.RemovedNetIDs))
			for _, netID := range gw.RemovedNetIDs {
				killedSet[netID] = true
			}

			for netID := range prev {
				if currentVisible[netID] {
					continue
				}
				if killedSet[netID] {
					killedIDs = append(killedIDs, netID)
				} else {
					removedIDs = append(removedIDs, netID)
				}
			}
		}

		// Clean up hashes for entities leaving AoI
		if playerHashes, ok := s.lastSentHash[conn.ConnID]; ok {
			for _, netID := range removedIDs {
				delete(playerHashes, netID)
			}
			for _, netID := range killedIDs {
				delete(playerHashes, netID)
			}
		}

		// Save current visible set for next tick
		s.lastVisible[conn.ConnID] = currentVisible

		// Send chat messages reliably (separate from world update so they survive packet loss)
		if len(gw.PendingChat) > 0 {
			chatData := netutil.MakeEvent(uint32(gamepb.ServerEventCode_SE_WORLD_UPDATE), &gamepb.WorldUpdateMsg{
				Tick:         gw.Tick,
				ChatMessages: gw.PendingChat,
			})
			if chatData != nil {
				gw.ConnMgr.SendReliable(conn.ConnID, chatData)
			}
		}

		// Filter ability events by AoI — include if caster or target is visible
		var abilityEvents []*gamepb.AbilityCastResultMsg
		for _, evt := range gw.PendingAbilityEvents {
			if currentVisible[evt.CasterId] || currentVisible[evt.TargetId] {
				abilityEvents = append(abilityEvents, evt)
			}
		}

		gw.Log.Log(game.CatNetwork, "tick=%d player=%d aoi=%d sent=%d skipped=%d",
			gw.Tick, conn.ConnID, len(currentVisible), sentCount, skippedCount)

		// Build and send world update (unreliable — next tick replaces stale data)
		data := netutil.MakeEvent(uint32(gamepb.ServerEventCode_SE_WORLD_UPDATE), &gamepb.WorldUpdateMsg{
			Tick:          gw.Tick,
			AckInputSeq:  input.Sequence,
			Entities:      s.entityStates,
			RemovedIds:    removedIDs,
			KilledIds:     killedIDs,
			AbilityEvents: abilityEvents,
		})
		if data == nil {
			continue
		}

		gw.ConnMgr.Send(conn.ConnID, data)

		// Send own-entity state to this player
		if entity, ok := gw.PlayerEntities[conn.ConnID]; ok && gw.ECS.Alive(entity) {
			s.sendOwnState(ctx, conn.ConnID, entity)
		}
	}

	// Send chat messages to docked players (they have no entity in the AoI loop)
	if len(gw.PendingChat) > 0 {
		for connID := range gw.DockedPlayers {
			chatData := netutil.MakeEvent(uint32(gamepb.ServerEventCode_SE_WORLD_UPDATE), &gamepb.WorldUpdateMsg{
				Tick:         gw.Tick,
				ChatMessages: gw.PendingChat,
			})
			if chatData != nil {
				gw.ConnMgr.SendReliable(connID, chatData)
			}
		}
	}

	// Clear chat messages and ability events after broadcasting to all players
	gw.PendingChat = nil
	gw.PendingAbilityEvents = nil
}
