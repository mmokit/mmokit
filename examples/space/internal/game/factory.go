package game

import (
	"github.com/mmokit/mmokit"
	"github.com/mmokit/mmokit/examples/space/internal/world"
)

// NewGameWorldStateFactory returns the factory passed to mmokit.AddState[GameWorld].
// The gameCfg pointer is shared across every GameWorld the coordinator creates so
// that runtime config changes made through the console apply to every cell.
func NewGameWorldStateFactory(
	gameCfg *GameConfig,
	playerDB *PlayerRepo,
	playerSessions *mmokit.PlayerSessions,
	worldRepo world.Repository,
	worldSnap *world.Snapshot,
) func(base *mmokit.Stage) *GameWorld {
	return func(base *mmokit.Stage) *GameWorld {
		cell := base.Cell()

		// Use root cell (depth 0) for CellCoord — entities always keep base-cell coordinates.
		rootCell := cell
		for rootCell.Depth > 0 {
			rootCell = rootCell.Parent()
		}
		gw := NewGameWorld(base, gameCfg, playerDB, mmokit.CellCoord{
			CellX: rootCell.X,
			CellY: rootCell.Y,
		}, base.FromSplit(), worldRepo, worldSnap)
		gw.PlayerSessions = playerSessions
		return gw
	}
}

// GameProtocol registers everything that is part of the client-visible
// contract: entity kinds, typed server events, client inputs, typed ops, and
// the broadcast-eligible verbs.
//
// Call this on EVERY process, whatever its roles. The protocol schema is what
// --dump-schema emits and what a client is validated against, so a process
// that skips it reports a different contract than its peers — and a gateway,
// which is the process clients actually connect to, would report the emptiest
// one of all. It is also what the gateway's own op routing consults:
// processOpFrame asks the local registry whether a request typeID is
// RouteGatewayLocal, so a gateway missing these registrations forwards ops it
// should have handled itself.
//
// Nothing here runs game logic. HandleClient and HandleAll install per-Stage
// handlers through OnStageInit, and a process with no cells has no stages, so
// on a gateway they register the wire types and nothing fires. Entity kinds
// are closures realized per-Stage for the same reason.
func GameProtocol(coord *mmokit.Process) {
	RegisterServerEvents(coord)
	RegisterEntityKinds(coord)
	RegisterInputs(coord)
	// Typed-op handlers — RoutePlayerCell ops dispatched on the player's
	// authoritative cell engine via Process.DispatchCellRoutedOp.
	mmokit.RegisterOp(coord, mmokit.RoutePlayerCell, HandleBankRequest)
	mmokit.RegisterOp(coord, mmokit.RoutePlayerCell, HandleRepairRequest)
	// Verbs are protocol, not behaviour: each is a HandleAll, which is what
	// puts the message type in the broadcast registry and therefore in the
	// schema's broadcast_types section.
	RegisterDamageVerb(coord)
	RegisterMiningVerb(coord)
	RegisterStatusVerb(coord)
	RegisterHealVerb(coord)
	RegisterDeathVerbs(coord)
	RegisterBeamToggleVerb(coord)
}

// GameSystems registers the simulation itself — the tick observers, the
// player-join hook, and every ECS system. None of it is client-visible, and
// all of it needs cells, so it belongs only on a process that hosts them.
//
// System registration order is semantic; preserve it. Network stays last.
func GameSystems(coord *mmokit.Process) {
	// Death observer: fires Killed exactly once per entity per Health drop-to-zero.
	// Runs as the canonical lifecycle path post-Plan E — ApplyDamage no longer
	// dispatches death directly.
	mmokit.OnTickEachAll(coord, deathObserver)
	registerPlayerJoin(coord)
	// Reactive per-player debug-overlay broadcaster — sends SE_DEBUG_INFO
	// (topology + AoI radius) to any active player whose DebugFlags
	// includes the corresponding bit. Replaces the manual sendCellTopology
	// path that used to fire on connect / split / merge.
	coord.AddSystem(mmokit.NewDebugBroadcaster())
	coord.AddSystem(mmokit.NewSystem(&DockingSystem{}))
	coord.AddSystem(mmokit.NewSystem(&ShipDynamicsSystem{}))
	coord.AddSystem(mmokit.NewSystem(&MiningSystem{}))
	coord.AddSystem(mmokit.NewSystem(&AbilitySystem{}))
	coord.AddSystem(mmokit.NewSystem(&ProjectileSystem{})) // after Ability (abilities spawn projectiles)
	coord.AddSystem(mmokit.NewSystem(&StatusEffectSystem{}))
	coord.AddSystem(mmokit.NewSystem(&SupercruiseSystem{}))
	coord.AddSystem(mmokit.NewSystem(&NPCAISystem{}))
	coord.AddSystem(mmokit.NewSystem(&SelectionLOSSystem{}))
	coord.AddSystem(mmokit.NewSystem(&POISystem{}))
	coord.AddSystem(mmokit.NewSystem(&DungeonChamberSystem{}))
	coord.AddSystem(mmokit.NewSystem(&WanderSystem{}))
	coord.AddSystem(mmokit.NewPhysicsSystem())
	coord.AddSystem(mmokit.NewLifetimeSystem())
	// AoESystem runs AFTER LifetimeSystem so it sees Lifetime<=0 on the
	// same tick the engine decrements to zero. The previous order
	// (AoESystem before LifetimeSystem) silently dropped every non-instant
	// AoE: LifetimeSystem would mark the marker for removal as soon as
	// Remaining hit 0, FlushRemovals would despawn it at end-of-tick, and
	// the next tick's AoESystem couldn't find the entity to resolve
	// damage. Instant AoEs (Lifetime=0 spawned by projectile splash) still
	// resolve same-tick — LifetimeSystem decrements 0→-dt (still <=0,
	// queues removal), then AoESystem sees the marker pre-Flush and
	// applies damage.
	coord.AddSystem(mmokit.NewSystem(&AoESystem{}))
	coord.AddSystem(mmokit.NewSpatialSystem())
	coord.AddSystem(mmokit.NewSystem(&CollisionSystem{}))
	coord.AddSystem(mmokit.NewSystem(&ShieldRegenSystem{}))
	coord.AddSystem(mmokit.NewSystem(&NetworkSystem{}))
}

// GameSetup is protocol plus systems — what a process that hosts cells wants.
// main.go calls the two halves separately because a gateway wants only the
// first; tests that build a full single-process world call this.
func GameSetup(coord *mmokit.Process) {
	GameProtocol(coord)
	GameSystems(coord)
}

// registerPlayerJoin installs the spawn / reconnect hook via
// coord.OnPlayerJoin (the canonical Process-level API). The body
// previously lived in NewGameWorld as a gw.Players.OnState(StateActive)
// callback, but that registration was last-writer-wins against the
// universe-side callback installed at createNode time and silently lost
// — see the spec at docs/superpowers/specs/2026-04-28-player-input-api-design.md.
func registerPlayerJoin(coord *mmokit.Process) {
	coord.OnPlayerJoin(func(s *mmokit.PlayerSession, stage *mmokit.Stage) {
		gw := gameWorldFromStage(stage)
		if gw == nil {
			return
		}
		// Reconnect into a state where the entity was deliberately removed
		// (StateDead). The client refreshed mid-death; we don't want to
		// silently respawn them — they should see the death screen until
		// they explicitly press the respawn button. Send the death cue
		// directly without going through SpawnPlayer.
		if s.State == StateDead {
			mmokit.SendEvent(gw.stage, s.ConnID, &PlayerDied{KillerID: 0})
			gw.eng.Log.Log(CatPlayerSpawn, "reconnect-to-dead: conn=%d username=%s", s.ConnID, s.Username)
		} else if s.Entity != (mmokit.EntityHandle{}) && gw.stage.ECSWorld().Alive(s.Entity) {
			// Entity preserved across grace period (Active / Docked / Docking).
			gw.reconnectPlayer(s)
			// State-specific welcome on reconnect into a non-Active state.
			// reconnectPlayer sent SE_PLAYER_SPAWNED which resets the client
			// to "in space"; follow up with the state-specific UI cue so
			// the bank panel / dock animation reopens.
			switch s.State {
			case StateDocked:
				mmokit.SendEvent(gw.stage, s.ConnID, &Docked{})
				gw.eng.Log.Log(CatPlayerDock, "reconnect-to-docked: conn=%d username=%s", s.ConnID, s.Username)
			case StateDocking:
				// Mid-dock disconnect+reconnect: re-send the docking-state
				// progress so the client picks up the tractor-beam animation
				// where it left off.
				if ds := gw.dockingStates[s.Username]; ds != nil {
					progress := 1.0 - ds.Remaining/gw.Config.DockTime
					if progress > 1 {
						progress = 1
					}
					mmokit.SendEvent(gw.stage, s.ConnID, &DockingState{
						Docking:   true,
						Progress:  progress,
						TotalTime: gw.Config.DockTime,
						StationID: ds.StationNetID,
					})
					gw.eng.Log.Log(CatPlayerDock, "reconnect-to-docking: conn=%d username=%s progress=%.2f", s.ConnID, s.Username, progress)
				}
			}
		} else {
			gw.SpawnPlayer(s)
		}
		if gw.OnPostSpawn != nil {
			gw.OnPostSpawn(s.ConnID)
		}
		if gw.PlayerSessions != nil {
			gw.PlayerSessions.Set(s.ConnID, s.Username)
		}
		gw.updatePlayerCompletions()
	})
}

// gameWorldFromStage resolves the cell-local GameWorld for a given stage.
// Returns nil if the stage is nil. Panics if GameWorld was never registered
// via mmokit.AddState[GameWorld] — that's a programmer error, not a runtime
// condition.
func gameWorldFromStage(stage *mmokit.Stage) *GameWorld {
	if stage == nil {
		return nil
	}
	return mmokit.State[GameWorld](stage)
}
