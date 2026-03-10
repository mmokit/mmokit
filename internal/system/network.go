package system

import (
	"github.com/mlange-42/ark/ecs"
	"google.golang.org/protobuf/proto"

	gamepb "github.com/zenion/mmoserver/gen/go"
	"github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/internal/game"
	"github.com/zenion/mmoserver/pkg/spatial"
)

// NetworkSystem serializes visible state and broadcasts to each player.
type NetworkSystem struct {
	gw           *game.GameWorld
	playerFilter *ecs.Filter3[component.Position, component.PlayerConn, component.PlayerInput]
	results      []spatial.Entry
	entityStates []*gamepb.EntityState

	// Per-player: set of network IDs that were visible last tick
	lastVisible map[uint32]map[uint32]bool // connID -> set of netIDs
}

func NewNetworkSystem(gw *game.GameWorld) *NetworkSystem {
	return &NetworkSystem{
		gw:           gw,
		results:      make([]spatial.Entry, 0, 256),
		entityStates: make([]*gamepb.EntityState, 0, 256),
		lastVisible:  make(map[uint32]map[uint32]bool),
	}
}

func (s *NetworkSystem) Update(dt float32) {
	gw := s.gw
	if s.playerFilter == nil {
		s.playerFilter = ecs.NewFilter3[component.Position, component.PlayerConn, component.PlayerInput](gw.ECS)
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

			if gw.OwnerMap.HasAll(entry.Entity) {
				owner := gw.OwnerMap.Get(entry.Entity)
				if gw.ECS.Alive(owner.Entity) && gw.NetworkIDMap.HasAll(owner.Entity) {
					ownerID := gw.NetworkIDMap.Get(owner.Entity).ID
					state.OwnerId = &ownerID
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
					state.Resources = inv.Resources[:]
				}
			}

			// Player-specific: pilot name, and FLUX only for the observer's own entity
			if gw.PlayerConnMap.HasAll(entry.Entity) {
				entConnID := gw.PlayerConnMap.Get(entry.Entity).ConnID
				if username, ok := gw.ConnToUsername[entConnID]; ok {
					state.PilotName = username
					if entConnID == conn.ConnID {
						if pdata := gw.PlayerDB.Get(username); pdata != nil {
							state.Flux = float32(pdata.Flux)
						}
					}
				}
			}

			s.entityStates = append(s.entityStates, state)
		}

		// Compute removed IDs: entities the player saw last tick but not this tick
		var removedIDs []uint32
		if prev, ok := s.lastVisible[conn.ConnID]; ok {
			for netID := range prev {
				if !currentVisible[netID] {
					removedIDs = append(removedIDs, netID)
				}
			}
		}

		// Also include globally removed entities (killed, despawned)
		// that the player had in view
		if prev, ok := s.lastVisible[conn.ConnID]; ok {
			for _, netID := range gw.RemovedNetIDs {
				if prev[netID] && !currentVisible[netID] {
					// Avoid duplicates
					found := false
					for _, rid := range removedIDs {
						if rid == netID {
							found = true
							break
						}
					}
					if !found {
						removedIDs = append(removedIDs, netID)
					}
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

		// Build and send world update (unreliable — next tick replaces stale data)
		update := &gamepb.ServerMessage{
			Msg: &gamepb.ServerMessage_WorldUpdate{
				WorldUpdate: &gamepb.WorldUpdateMsg{
					Tick:        gw.Tick,
					AckInputSeq: input.Sequence,
					Entities:    s.entityStates,
					RemovedIds:  removedIDs,
				},
			},
		}

		data, err := proto.Marshal(update)
		if err != nil {
			continue
		}

		gw.ConnMgr.Send(conn.ConnID, data)
	}

	// Clear chat messages after broadcasting to all players
	gw.PendingChat = nil
}
