package system

import (
	"github.com/mlange-42/ark/ecs"
	"google.golang.org/protobuf/proto"

	gamepb "github.com/zenion/mmoserver/gen/go"
	"github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/internal/game"
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

	// Per-player: set of network IDs that were visible last tick
	lastVisible map[uint32]map[uint32]bool // connID -> set of netIDs

	// Reverse lock map: target entity -> most-progressed locker (rebuilt each tick)
	lockedBy map[ecs.Entity]lockerInfo
}

func NewNetworkSystem(gw *game.GameWorld) *NetworkSystem {
	return &NetworkSystem{
		gw:           gw,
		results:      make([]spatial.Entry, 0, 256),
		entityStates: make([]*gamepb.EntityState, 0, 256),
		lastVisible:  make(map[uint32]map[uint32]bool),
		lockedBy:     make(map[ecs.Entity]lockerInfo),
	}
}

func (s *NetworkSystem) Update(dt float32) {
	gw := s.gw
	if s.playerFilter == nil {
		s.playerFilter = ecs.NewFilter3[component.Position, component.PlayerConn, component.PlayerInput](gw.ECS)
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

	// Clean up tracking for disconnected players
	for connID := range s.lastVisible {
		if _, ok := gw.PlayerEntities[connID]; !ok {
			delete(s.lastVisible, connID)
		}
	}

	query := s.playerFilter.Query()
	for query.Next() {
		pos, conn, input := query.Get()

		// Query entities within AoI
		s.results = s.results[:0]
		s.results = gw.Grid.QueryRadius(pos.X, pos.Y, gw.Config.AoIRadius, s.results)

		// Build entity states and track visible IDs this tick
		s.entityStates = s.entityStates[:0]
		currentVisible := make(map[uint32]bool, len(s.results))

		for _, entry := range s.results {
			if !gw.ECS.Alive(entry.Entity) {
				continue
			}

			netID := gw.NetworkIDMap.Get(entry.Entity).ID
			currentVisible[netID] = true

			state := &gamepb.EntityState{
				Id:     netID,
				X:      entry.X,
				Y:      entry.Y,
				Radius: entry.Radius,
				Width:  entry.Width,
				Height: entry.Height,
			}

			if gw.EntityKindMap.HasAll(entry.Entity) {
				state.EntityType = gamepb.EntityType(gw.EntityKindMap.Get(entry.Entity).Type)
			}

			if gw.VelocityMap.HasAll(entry.Entity) {
				vel := gw.VelocityMap.Get(entry.Entity)
				state.Vx = vel.X
				state.Vy = vel.Y
			}

			if gw.RotationMap.HasAll(entry.Entity) {
				state.Rotation = gw.RotationMap.Get(entry.Entity).Angle
			}

			if gw.HealthMap.HasAll(entry.Entity) {
				h := gw.HealthMap.Get(entry.Entity)
				if h.Max > 0 {
					state.Health = h.Current / h.Max
				}
			}

			if gw.ShieldMap.HasAll(entry.Entity) {
				sh := gw.ShieldMap.Get(entry.Entity)
				if sh.Max > 0 {
					state.Shield = sh.Current / sh.Max
				}
			}

			// Status effects (visible on all entities)
			if gw.StatusEffectsMap.HasAll(entry.Entity) {
				se := gw.StatusEffectsMap.Get(entry.Entity)
				for i := uint8(0); i < se.Count; i++ {
					state.StatusEffects = append(state.StatusEffects, &gamepb.ActiveStatusEffect{
						Type:      gamepb.StatusEffectType(se.Effects[i].Type),
						Remaining: se.Effects[i].Duration,
					})
				}
			}

			// Minable resource info
			if gw.MinableMap.HasAll(entry.Entity) {
				minable := gw.MinableMap.Get(entry.Entity)
				state.ResourceType = gamepb.ResourceType(minable.ResourceType)
				state.ResourceRemaining = minable.Remaining
			}

			// Mining laser state
			if gw.MiningLaserMap.HasAll(entry.Entity) {
				laser := gw.MiningLaserMap.Get(entry.Entity)
				state.MiningActive = laser.Active
				if laser.Active && gw.ECS.Alive(laser.Target) && gw.NetworkIDMap.HasAll(laser.Target) {
					state.MiningTargetId = gw.NetworkIDMap.Get(laser.Target).ID
				}
			}

			// Inventory — send for own player entity and loot crates, not other players
			if gw.InventoryMap.HasAll(entry.Entity) {
				isOwnPlayer := gw.PlayerConnMap.HasAll(entry.Entity) && gw.PlayerConnMap.Get(entry.Entity).ConnID == conn.ConnID
				isLootCrate := gw.LootCrateMap.HasAll(entry.Entity)
				if isOwnPlayer || isLootCrate {
					inv := gw.InventoryMap.Get(entry.Entity)
					for itemID, qty := range inv.Items {
						if qty > 0 {
							state.CargoItems = append(state.CargoItems, &gamepb.InventoryItem{
								ItemId:   itemID,
								Quantity: qty,
							})
						}
					}
					if isOwnPlayer {
						state.CargoMass = inv.TotalMass()
						state.MaxCargoMass = inv.MaxMass
					}
				}
			}

			// Player-specific: pilot name, lock-on, cooldowns (own entity only)
			if gw.PlayerConnMap.HasAll(entry.Entity) {
				entConnID := gw.PlayerConnMap.Get(entry.Entity).ConnID
				if username, ok := gw.ConnToUsername[entConnID]; ok {
					state.PilotName = username
					if entConnID == conn.ConnID {
						// Lock-on state (own entity only)
						if gw.TargetLockMap.HasAll(entry.Entity) {
							lock := gw.TargetLockMap.Get(entry.Entity)
							state.LockProgress = lock.Progress
							state.LockTargetId = lock.TargetNetID
						}

						// Ability cooldowns (own entity only)
						if gw.AbilitySetMap.HasAll(entry.Entity) {
							abilities := gw.AbilitySetMap.Get(entry.Entity)
							for slot := uint32(0); slot < uint32(component.AbilityCount); slot++ {
								cd := abilities.Cooldowns[slot]
								if cd > 0 {
									state.AbilityCooldowns = append(state.AbilityCooldowns, &gamepb.AbilityCooldownState{
										Slot:      slot,
										Remaining: cd,
										Total:     s.getAbilityTotalCooldown(uint8(slot)),
									})
								}
							}
						}
					}
				}
			}

			// Being-locked state (visible on all entities)
			if lb, ok := s.lockedBy[entry.Entity]; ok {
				state.LockedById = lb.netID
				state.LockedByProgress = lb.progress
			}

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

		// Save current visible set for next tick
		s.lastVisible[conn.ConnID] = currentVisible

		// Send chat messages reliably (separate from world update so they survive packet loss)
		if len(gw.PendingChat) > 0 {
			chatMsg := &gamepb.ServerMessage{
				Msg: &gamepb.ServerMessage_WorldUpdate{
					WorldUpdate: &gamepb.WorldUpdateMsg{
						Tick:         gw.Tick,
						ChatMessages: gw.PendingChat,
					},
				},
			}
			if chatData, err := proto.Marshal(chatMsg); err == nil {
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

		// Build and send world update (unreliable — next tick replaces stale data)
		update := &gamepb.ServerMessage{
			Msg: &gamepb.ServerMessage_WorldUpdate{
				WorldUpdate: &gamepb.WorldUpdateMsg{
					Tick:          gw.Tick,
					AckInputSeq:   input.Sequence,
					Entities:      s.entityStates,
					RemovedIds:    removedIDs,
					KilledIds:     killedIDs,
					AbilityEvents: abilityEvents,
				},
			},
		}

		data, err := proto.Marshal(update)
		if err != nil {
			continue
		}

		gw.ConnMgr.Send(conn.ConnID, data)
	}

	// Clear chat messages and ability events after broadcasting to all players
	gw.PendingChat = nil
	gw.PendingAbilityEvents = nil
}

func (s *NetworkSystem) getAbilityTotalCooldown(slot uint8) float32 {
	cfg := &s.gw.Config
	switch slot {
	case component.AbilityQ:
		return cfg.AbilityQCooldown
	case component.AbilityW:
		return cfg.AbilityWCooldown
	case component.AbilityE:
		return cfg.AbilityECooldown
	case component.AbilityR:
		return cfg.AbilityRCooldown
	case component.AbilityD:
		return cfg.AbilityDCooldown
	case component.AbilityF:
		return cfg.AbilityFCooldown
	default:
		return 0
	}
}
