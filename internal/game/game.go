package game

import (
	"fmt"
	"time"

	"github.com/mlange-42/ark/ecs"

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

	gw := &GameWorld{
		Stage:         base,
		eng:           eng,
		Spatial:       base.SpatialGrid(),
		Config:        cfg,
		Queue:         mmokit.NewTickQueue(),
		PlayerDB:      playerDB,
		dockingStates: make(map[string]*DockingProgress),
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
	removeFromWorld := func(s *mmokit.PlayerSession, pm *mmokit.PlayerManager) {
		entity := mmokit.EntityFromECS(gw.Stage, s.Entity)
		if entity.Alive() {
			if mmokit.Has[mmokit.Ghost](entity) {
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
			mmokit.Despawn(entity)
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
			entity := mmokit.EntityFromECS(gw.Stage, s.Entity)
			if entity.Alive() {
				gw.SavePlayerState(s)
				gw.Spatial.Deregister(s.Entity)
				mmokit.Despawn(entity)
			}
			s.Entity = ecs.Entity{}
			gw.updatePlayerCompletions()
		},
	})

	gw.RootCell = cell
	gw.flushTicks = uint32(gw.Config.PersistFlushInterval * float32(eng.Config.TickRate))
	gw.FullRefreshInterval = uint32(eng.Config.TickRate)

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

// UnwrapGameWorld extracts the underlying *GameWorld from a mmokit.GameWorld.
func UnwrapGameWorld(w mmokit.GameWorld) *GameWorld {
	return w.(*GameWorld)
}
