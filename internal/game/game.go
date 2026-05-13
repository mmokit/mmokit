package game

import (
	"fmt"
	"time"

	gamecomp "github.com/zenion/mmoserver/internal/component"
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
		stage:         base,
		eng:           eng,
		Spatial:       base.SpatialGrid(),
		Config:        cfg,
		PlayerDB:      playerDB,
		dockingStates: make(map[string]*DockingProgress),
		poiRosters:    make(map[uint32][]uint32),
		autoRespawnAt: make(map[uint32]uint32),
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
		entity := mmokit.EntityFromECS(gw.stage, s.Entity)
		if entity.Alive() {
			if mmokit.Has[mmokit.Ghost](entity) {
				// Transfer ghost — don't remove, let TTL expire.
				// The ghost lingers for visual continuity until the replica arrives.
				s.Entity = mmokit.EntityHandle{}
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
		s.Entity = mmokit.EntityHandle{}
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

	// dieKeepEntity is the StateActive→StateDead action: marks the
	// player entity Dormant (invuln + untargetable + hidden from
	// others' AoI per Dormant semantics) instead of despawning it.
	// Keeping the entity alive gives us three things:
	//   - The typed-input dispatcher (which requires sess.Entity to be
	//     alive) still routes the client's Respawn input to the handler.
	//   - Replication keeps streaming the world to the dead player so
	//     the death overlay isn't a frozen frame.
	//   - PlayerStateOf works inside HandleClient handlers because the
	//     PlayerConn → session lookup chain remains intact.
	// Health is set to 0, velocity zeroed, sess.Entity preserved.
	dieKeepEntity := func(s *mmokit.PlayerSession, pm *mmokit.PlayerManager) {
		entity := mmokit.EntityFromECS(gw.stage, s.Entity)
		if !entity.Alive() {
			return
		}
		if mmokit.Has[mmokit.Ghost](entity) {
			// Transfer ghost — handle as the previous removeFromWorld path
			// did (don't add Dormant; the ghost TTL handles cleanup).
			s.Entity = mmokit.EntityHandle{}
			if gw.PlayerSessions != nil {
				gw.PlayerSessions.Remove(s.ConnID)
			}
			gw.updatePlayerCompletions()
			return
		}
		gw.SavePlayerState(s)
		// Overwrite the saved position with the spawn-cell coords. Without
		// this, a cross-cell respawn (POI death in cell ≠ StationCell)
		// would re-create the entity at the death position in the station
		// cell's local frame, and CellBoundarySystem would immediately
		// transfer it back to the death cell — landing the player at the
		// POI again, where the surviving NPCs kill them on sight.
		pdata := gw.PlayerDB.Bind(s)
		pdata.X = StationLocalX
		pdata.Y = StationLocalY
		pdata.CellX = gw.Config.StationCell.CellX
		pdata.CellY = gw.Config.StationCell.CellY
		gw.PlayerDB.MarkDirtyByUserID(pdata.UserID)
		// Zero velocity + shield NOW (safe — existing component writes,
		// not structural changes).
		if v := mmokit.Get[mmokit.Velocity](entity); v != nil {
			v.X, v.Y = 0, 0
		}
		if sh := mmokit.Get[gamecomp.Shield](entity); sh != nil {
			sh.Current = 0
		}
		// Add the Dormant marker via Commands so it lands at the next
		// system flush boundary (engine.Hooks.AfterSystem). Cannot call
		// mmokit.Set(entity, Dormant{}) directly here — this action runs
		// inside the death observer's locked-world query iteration, and
		// component add is a structural archetype change that would panic
		// against the ark locked-world check. Commands defers the mutation
		// to a safe phase without the queue+drain bookkeeping.
		mmokit.AddComponent(gw.stage.Commands(), entity, mmokit.Dormant{})
	}

	// removeFromWorldDead is the StateDead→StateTransferring action used
	// when processRespawns hands a dead player off to the station cell.
	// Skips SavePlayerState: dieKeepEntity already saved inventory and
	// pinned pdata.{X,Y,CellX,CellY} to spawn coords. Calling Save here
	// would overwrite that pin with the entity's death position (the
	// entity is still parked at the POI under a Dormant marker), causing
	// the dest cell's SpawnPlayer to spawn at the POI again.
	removeFromWorldDead := func(s *mmokit.PlayerSession, pm *mmokit.PlayerManager) {
		entity := mmokit.EntityFromECS(gw.stage, s.Entity)
		if entity.Alive() {
			if mmokit.Has[mmokit.Ghost](entity) {
				s.Entity = mmokit.EntityHandle{}
				if gw.PlayerSessions != nil {
					gw.PlayerSessions.Remove(s.ConnID)
				}
				gw.updatePlayerCompletions()
				return
			}
			gw.Spatial.Deregister(s.Entity)
			mmokit.Despawn(entity)
		}
		s.Entity = mmokit.EntityHandle{}
		if gw.PlayerSessions != nil {
			gw.PlayerSessions.Remove(s.ConnID)
		}
		gw.updatePlayerCompletions()
	}

	// respawnAtSpawnpoint runs on StateDead→StateActive: removes
	// Dormant, repositions to the configured spawn, and refills health
	// and shield. The OnEnter callback in registerPlayerJoin then sees
	// an alive entity and sends the player back into normal gameplay
	// via reconnectPlayer (which broadcasts PlayerSpawned to clients).
	respawnAtSpawnpoint := func(s *mmokit.PlayerSession, pm *mmokit.PlayerManager) {
		entity := mmokit.EntityFromECS(gw.stage, s.Entity)
		if !entity.Alive() {
			return
		}
		// Drop Dormant via Commands so the structural mutation routes
		// through the framework's deferred-flush path (engine.Hooks
		// AfterSystem) instead of poking ark directly.
		mmokit.RemoveComponent[mmokit.Dormant](gw.stage.Commands(), entity)
		if pos := mmokit.Get[mmokit.Position](entity); pos != nil {
			pos.X = StationLocalX
			pos.Y = StationLocalY
		}
		if v := mmokit.Get[mmokit.Velocity](entity); v != nil {
			v.X, v.Y = 0, 0
		}
		if h := mmokit.Get[gamecomp.Health](entity); h != nil {
			h.Current = h.Max
			h.LastDamagedByNetID = 0
		}
		if sh := mmokit.Get[gamecomp.Shield](entity); sh != nil {
			sh.Current = sh.Max
			sh.DamageCooldown = 0
		}
	}

	gw.Players.SetGracePeriod(time.Duration(cfg.DisconnectGracePeriod * float32(time.Second)))
	gw.Players.AddTransitions([]mmokit.StateTransition{
		{From: mmokit.StateActive, To: StateDocking},                                           // entity persists
		{From: mmokit.StateActive, To: StateDead, Action: dieKeepEntity},                       // entity kept (Dormant)
		{From: mmokit.StateActive, To: mmokit.StateTransferring, Action: removeFromWorld},      // entity removed
		{From: mmokit.StateActive, To: mmokit.StateDisconnected, Action: disconnectKeepEntity}, // entity persists for reconnect
		{From: StateDead, To: mmokit.StateActive, Action: respawnAtSpawnpoint},                 // respawn (same cell)
		{From: StateDead, To: mmokit.StateTransferring, Action: removeFromWorldDead},           // respawn (cross-cell handoff)
		{From: StateDead, To: mmokit.StateDisconnected},                                        // disconnect while dead
		{From: mmokit.StateDisconnected, To: StateDead},                                        // reconnect resumes dead state
		{From: mmokit.StateDisconnected, To: StateDocked},                                      // reconnect resumes docked state
		{From: StateDocking, To: StateDocked},
		{From: StateDocking, To: StateDead, Action: dieKeepEntity},
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
			entity := mmokit.EntityFromECS(gw.stage, s.Entity)
			if entity.Alive() {
				gw.SavePlayerState(s)
				gw.Spatial.Deregister(s.Entity)
				mmokit.Despawn(entity)
			}
			s.Entity = mmokit.EntityHandle{}
			gw.updatePlayerCompletions()
		},
	})

	// Auto-respawn: typed-input dispatch drops Respawn frames for dead
	// players (the entity is removed when StateDead is entered, so the
	// dispatcher's "entity must be alive" check rejects the frame). Hold
	// a per-session timer; postTick schedules executeRespawnFor via
	// Commands.Defer when it elapses. Cleaned up on any transition out
	// of StateDead.
	gw.Players.OnState(StateDead, mmokit.StateCallbacks{
		OnEnter: func(s *mmokit.PlayerSession, pm *mmokit.PlayerManager) {
			if s.ConnID == 0 {
				return // entered dead state without a live conn (e.g. via Disconnected→Dead) — nothing to schedule
			}
			delay := uint32(gw.Config.RespawnGraceSec * float32(eng.Config.TickRate))
			if delay == 0 {
				delay = uint32(eng.Config.TickRate) // 1s minimum
			}
			gw.autoRespawnAt[s.ConnID] = gw.eng.Tick + delay
			gw.eng.Log.Log(CatPlayerSpawn, "death: conn=%d auto-respawn in %dt", s.ConnID, delay)
		},
		OnExit: func(s *mmokit.PlayerSession, pm *mmokit.PlayerManager) {
			delete(gw.autoRespawnAt, s.ConnID)
		},
	})

	gw.RootCell = cell
	gw.flushTicks = uint32(gw.Config.PersistFlushInterval * float32(eng.Config.TickRate))
	gw.FullRefreshInterval = uint32(eng.Config.TickRate)

	// Initialize entity kinds (transfer replication + component auto-fill).
	gw.initEntityKinds()

	// Wire per-stage transfer callbacks. Must run after gw is fully built so
	// the closures captured here see the populated GameWorld. Replaces the
	// orphaned GameWorld.Init() that the world.Init() removal in spec
	// 2026-05-08-stage-on-systembase-design left without a caller.
	gw.wireStageCallbacks()

	// Spawn initial content for this cell (skip for split-created worlds —
	// entities arrive via transfer from the parent cell)
	if !fromSplit {
		gw.spawnAsteroids()
		if cell == cfg.StationCell {
			gw.SpawnStation()
		}
		gw.spawnPOIs()
	}

	return gw
}

