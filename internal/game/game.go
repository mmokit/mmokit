package game

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/mlange-42/ark/ecs"

	enginepb "github.com/zenion/mmoserver/gen/go/enginepb"
	gamepb "github.com/zenion/mmoserver/gen/go/gamepb"
	"github.com/zenion/mmoserver/internal/item"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// Package-level custom player states.
//
// Declared as const, not var, so they have correct values at process startup
// when input handlers register their state masks. (Var declarations were
// silently zero-valued during RegisterInputs since gw.Players.RegisterState
// runs per-cell, AFTER GameSetup — which made every handler with a custom
// state in its mask drop messages: e.g. BANK_REQUEST registered with
// States(Active, StateDocked=0) compiled to mask 0b011, rejecting the
// StateDocked bit at dispatch time.)
//
// IDs must match what gw.Players.RegisterState would assign — the registration
// order in NewGameWorld below must keep these in lockstep.
const (
	StateDead    mmokit.PlayerState = mmokit.StateBuiltinEnd + iota
	StateDocking
	StateDocked
)

// NewGameWorld creates a new game world backed by the given Stage.
// The cfg pointer is shared across all GameWorlds in the coordinator so that
// runtime `config set` mutations propagate to every node at once.
func NewGameWorld(base *mmokit.Stage, cfg *GameConfig, playerDB *PlayerRepo, cell mmokit.CellCoord, fromSplit bool) *GameWorld {
	eng := base.Engine()
	item.Init()
	ecsWorld := eng.ECS

	gw := &GameWorld{
		Stage:     base,
		eng:           eng,
		Spatial:       base.SpatialGrid(),
		Config:        cfg,
		Queue:         mmokit.NewTickQueue(),
		NetIDToEntity: make(map[uint32]ecs.Entity),
		PlayerDB:      playerDB,
		dockingStates: make(map[string]*DockingState),
	}
	gw.Players = eng.Players

	// Register custom player state names for state-name display + completion.
	// IDs are deterministic: PlayerManager assigns sequential values from
	// StateBuiltinEnd, matching the const declarations above. Order matters —
	// it must mirror the const block.
	if got := gw.Players.RegisterState("dead"); got != StateDead {
		panic(fmt.Sprintf("StateDead const drift: got %d want %d", got, StateDead))
	}
	if got := gw.Players.RegisterState("docking"); got != StateDocking {
		panic(fmt.Sprintf("StateDocking const drift: got %d want %d", got, StateDocking))
	}
	if got := gw.Players.RegisterState("docked"); got != StateDocked {
		panic(fmt.Sprintf("StateDocked const drift: got %d want %d", got, StateDocked))
	}
	// removeFromWorld saves and removes the player's ECS entity.
	// Used by transitions where the player permanently leaves the world.
	// If the entity has a Ghost component (transfer in progress), skip removal —
	// the ghost lingers for visual continuity until the replica arrives.
	ghostCheck := ecs.NewMap1[mmokit.Ghost](ecsWorld)
	removeFromWorld := func(s *mmokit.PlayerSession, pm *mmokit.PlayerManager) {
		if gw.eng.ECS.Alive(s.Entity) {
			if ghostCheck.HasAll(s.Entity) {
				// Transfer ghost — don't remove, let TTL expire.
				// The ghost lingers for visual continuity until the replica arrives.
				s.Entity = ecs.Entity{}
				if gw.PlayerSessions != nil {
					gw.PlayerSessions.Remove(s.ConnID)
				}
				gw.updatePlayerCompletions()
				return
			}
			gw.SavePlayerState(s)
			gw.Spatial.Deregister(s.Entity)
			gw.eng.ECS.RemoveEntity(s.Entity)
		}
		s.Entity = ecs.Entity{}
		if gw.PlayerSessions != nil {
			gw.PlayerSessions.Remove(s.ConnID)
		}
		gw.updatePlayerCompletions()
	}

	// disconnectKeepEntity preserves the entity during grace period so the
	// player can reconnect to the same ship. Entity cleanup happens in
	// StateDisconnected.OnExit when the grace period expires.
	disconnectKeepEntity := func(s *mmokit.PlayerSession, pm *mmokit.PlayerManager) {
		gw.SavePlayerState(s)
		if gw.PlayerSessions != nil {
			gw.PlayerSessions.Remove(s.ConnID)
		}
	}

	gw.Players.SetGracePeriod(time.Duration(cfg.DisconnectGracePeriod * float32(time.Second)))
	gw.Players.AddTransitions([]mmokit.StateTransition{
		{From: mmokit.StateActive, To: StateDocking},                                           // entity persists
		{From: mmokit.StateActive, To: StateDead, Action: removeFromWorld},                     // entity removed
		{From: mmokit.StateActive, To: mmokit.StateTransferring, Action: removeFromWorld},      // entity removed
		{From: mmokit.StateActive, To: mmokit.StateDisconnected, Action: disconnectKeepEntity}, // entity persists for reconnect
		{From: StateDead, To: mmokit.StateActive},                                              // respawn
		{From: StateDead, To: mmokit.StateDisconnected},                                        // disconnect while dead
		{From: mmokit.StateDisconnected, To: StateDead},                                        // reconnect resumes dead state
		{From: mmokit.StateDisconnected, To: StateDocked},                                      // reconnect resumes docked state
		{From: StateDocking, To: StateDocked},
		{From: StateDocking, To: StateDead, Action: removeFromWorld},
		{From: StateDocking, To: mmokit.StateDisconnected, Action: disconnectKeepEntity},       // disconnect mid-dock keeps the (Dormant) entity
		{From: mmokit.StateDisconnected, To: StateDocking},                                     // reconnect resumes mid-dock
		{From: StateDocked, To: mmokit.StateActive},
		{From: StateDocked, To: mmokit.StateDisconnected, Action: disconnectKeepEntity},
	})

	// StateActive spawn / reconnect hook is installed via coord.OnPlayerJoin
	// in factory.registerPlayerJoin. The Process-level API is canonical;
	// registering directly on gw.Players.OnState(StateActive) here would be
	// silently overwritten by the universe-side callback wired in createNode.

	// When grace period expires (or session is removed while Disconnected),
	// clean up the entity that was kept alive for potential reconnection.
	// On reconnect, ConnID is restored before Transition — preserve the entity.
	gw.Players.OnState(mmokit.StateDisconnected, mmokit.StateCallbacks{
		OnExit: func(s *mmokit.PlayerSession, pm *mmokit.PlayerManager) {
			if s.ConnID != 0 {
				// Reconnecting — keep entity alive for reuse in StateActive.OnEnter
				gw.updatePlayerCompletions()
				return
			}
			// Grace period expired — clean up
			if gw.eng.ECS.Alive(s.Entity) {
				gw.SavePlayerState(s)
				gw.Spatial.Deregister(s.Entity)
				gw.eng.ECS.RemoveEntity(s.Entity)
			}
			s.Entity = ecs.Entity{}
			gw.updatePlayerCompletions()
		},
	})

	gw.RootCell = cell
	gw.flushTicks = uint32(gw.Config.PersistFlushInterval * float32(eng.Config.TickRate))
	gw.FullRefreshInterval = uint32(eng.Config.TickRate)

	// Component mappers (must be created before initEntityKinds which uses them)
	gw.C = NewComponents(ecsWorld)

	// Initialize entity kinds (transfer replication + component auto-fill).
	gw.initEntityKinds()

	// Spawn initial content for this cell (skip for split-created worlds —
	// entities arrive via transfer from the parent cell)
	if !fromSplit {
		gw.spawnAsteroids()
		if cell == cfg.StationCell {
			gw.SpawnStation()
		}
	}

	return gw
}

// Hooks returns the engine lifecycle hooks wired to this game world.
// OnConnect and OnDisconnect are handled by PlayerManager; login processing is engine-internal.
func (gw *GameWorld) Hooks() mmokit.Hooks {
	return mmokit.Hooks{
		PreFlush: func() {
			gw.processDockCompletions()
		},
		PostFlush:      gw.postFlush,
		ClearTickState: gw.clearTickState,
		PostTick:       gw.postTick,
	}
}

// Init is called by the Process after all nodes are created and bridges are wired.
// It sets up replication, transfer hooks, and post-spawn callbacks.
func (gw *GameWorld) Init() {
	gw.SetOnTransferReceived(func(entity ecs.Entity, frame *mmokit.TransferFrame) {
		gw.FinishTransferSpawn(entity, frame)
	})

	gw.SetOnPlayerTransferReceived(func(entity ecs.Entity, frame *mmokit.TransferFrame) {
		if s := gw.eng.Players.ByConnID(frame.ConnID); s != nil {
			gw.WireTransferPlayer(entity, s)
		}
		if gw.PlayerSessions != nil {
			gw.PlayerSessions.Set(frame.ConnID, frame.Username)
		}

		// Topology-transparent protocol: no SE_CELL_CHANGE is sent. The
		// destination cell's ReplicationSystem will set the
		// FRAME_FLAG_FRESH_SNAPSHOT bit on its first frame to this conn,
		// causing the client's decoder to reset baselines and repopulate
		// from the frame's Entered list — exactly like Valve Source's
		// cl_fullupdate or Gaffer's "encoded relative to initial state"
		// pattern. Clients never learn about cells, authority transfers,
		// or server boundaries.
		gw.ServerEvents().Send(gw.eng.ConnMgr, frame.ConnID, uint32(gamepb.GameServerEventCode_GSE_MAP_DATA), &gamepb.MapDataMsg{
			Stations: gw.CollectStationMapData(),
		})
		// Topology / debug overlay is pushed reactively by the
		// mmokit.NewDebugBroadcaster system (added in GameSetup) to any
		// player whose DebugFlags carry the topology bit. No explicit
		// per-connect send needed.
	})

	// OnPostSpawn is no longer needed for topology — see comment above.
	gw.OnPostSpawn = nil
}

// postTick runs after each tick — periodic saves.
// Bridge.PostSystems() is called by the Process's merged hooks.
//
// Snapshots every active player's live ECS state (position, cell, cargo,
// equipment) into the PlayerRepo on each flush tick so an ungraceful crash
// loses at most PersistFlushInterval seconds of gameplay. Without this,
// SavePlayerState is only called on state transitions (disconnect, death,
// dock, transfer, shutdown), so normal gameplay leaves positions stale in
// the DB until the next transition. StateDocked sessions have no live
// entity and their inventory/currency mutations already MarkDirty directly,
// so they piggyback on FlushDirty without needing iteration here.
func (gw *GameWorld) postTick() {
	if gw.flushTicks > 0 && gw.eng.Tick%gw.flushTicks == 0 {
		gw.Players.ForEach(mmokit.StateActive, func(s *mmokit.PlayerSession) {
			if gw.eng.ECS.Alive(s.Entity) {
				gw.SavePlayerState(s)
			}
		})
		n, err := gw.PlayerDB.FlushDirty(context.Background())
		if err != nil {
			gw.eng.Log.Log(CatPersistFlush, "flush error: %v", err)
		}
		if n > 0 {
			gw.eng.Log.Log(CatPersistFlush, "flushed %d dirty players", n)
		}
	}
}

// Shutdown saves all connected players and flushes dirty data.
// Call after the game loop has stopped.
func (gw *GameWorld) Shutdown() {
	gw.Players.ForEach(mmokit.StateActive, func(s *mmokit.PlayerSession) {
		if gw.eng.ECS.Alive(s.Entity) {
			gw.SavePlayerState(s)
		}
	})
	n, err := gw.PlayerDB.FlushDirty(context.Background())
	if err != nil {
		log.Printf("shutdown: flush error: %v", err)
	}
	log.Printf("shutdown: saved %d players", n)
}

// DispatchChat handles a chat message relayed from another node.
func (gw *GameWorld) DispatchChat(username, text string) {
	gw.eng.Log.Log(CatPlayerChat, "inbox: relayed chat <%s> %s", username, text)
	mmokit.Enqueue(gw.Queue, &enginepb.ChatMsg{
		Username: username,
		Text:     text,
	})
}

// UnwrapGameWorld extracts the underlying *GameWorld from a mmokit.GameWorld.
func UnwrapGameWorld(w mmokit.GameWorld) *GameWorld {
	return w.(*GameWorld)
}
