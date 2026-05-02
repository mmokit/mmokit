package game

import (
	gamepb "github.com/zenion/mmoserver/gen/go/gamepb"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// WorldFactory returns a world constructor function for use as Config.World.
// The gameCfg pointer is shared across every GameWorld the coordinator creates
// so that runtime config changes made through the console apply to every node.
func WorldFactory(
	gameCfg *GameConfig,
	playerDB *PlayerRepo,
	playerSessions *mmokit.PlayerSessions,
) func(base *mmokit.Stage) mmokit.GameWorld {
	return func(base *mmokit.Stage) mmokit.GameWorld {
		cell := base.Cell()

		// Use root cell (depth 0) for CellCoord — entities always keep base-cell coordinates
		rootCell := cell
		for rootCell.Depth > 0 {
			rootCell = rootCell.Parent()
		}
		gw := NewGameWorld(base, gameCfg, playerDB, mmokit.CellCoord{
			CellX: rootCell.X,
			CellY: rootCell.Y,
		}, base.FromSplit())
		gw.PlayerSessions = playerSessions
		gw.sideEffectRegistry = buildSideEffectRegistry(gw)
		return gw
	}
}

// GameSetup registers game-specific entity kinds, input handlers, the
// player-join hook, and systems on the coordinator.
func GameSetup(coord *mmokit.Process) {
	RegisterEntityKinds(coord)
	RegisterInputs(coord)
	registerPlayerJoin(coord)
	// Reactive per-player debug-overlay broadcaster — sends SE_DEBUG_INFO
	// (topology + AoI radius) to any active player whose DebugFlags
	// includes the corresponding bit. Replaces the manual sendCellTopology
	// path that used to fire on connect / split / merge.
	coord.AddSystem(mmokit.NewDebugBroadcaster())
	coord.AddSystem(mmokit.NewSystem(&DockingSystem{}))
	coord.AddSystem(mmokit.NewSystem(&TargetLockSystem{}))
	coord.AddSystem(mmokit.NewSystem(&ShipDynamicsSystem{}))
	coord.AddSystem(mmokit.NewSystem(&MiningSystem{}))
	coord.AddSystem(mmokit.NewSystem(&EconomySystem{}))
	coord.AddSystem(mmokit.NewSystem(&EquipmentSystem{}))
	coord.AddSystem(mmokit.NewSystem(&AbilitySystem{}))
	coord.AddSystem(mmokit.NewSystem(&StatusEffectSystem{}))
	coord.AddSystem(mmokit.NewSystem(&WanderSystem{}))
	coord.AddSystem(mmokit.NewPhysicsSystem())
	coord.AddSystem(mmokit.NewLifetimeSystem())
	coord.AddSystem(mmokit.NewSpatialSystemWith(func(gw *GameWorld) mmokit.SpatialHooks {
		return mmokit.SpatialHooks{
			PreTick: func() { clear(gw.NetIDToEntity) },
			OnEntity: func(entity mmokit.Entity, _ mmokit.SpatialEntry) {
				gw.NetIDToEntity[gw.C.NetworkID.Get(entity).ID] = entity
			},
		}
	}))
	coord.AddSystem(mmokit.NewSystem(&CollisionSystem{}))
	coord.AddSystem(mmokit.NewSystem(&ShieldRegenSystem{}))
	coord.AddSystem(mmokit.NewSystem(&NetworkSystem{}))
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
			gw.ServerEvents().Send(gw.eng.ConnMgr, s.ConnID,
				uint32(gamepb.GameServerEventCode_GSE_PLAYER_DIED), &gamepb.PlayerDiedMsg{KillerId: 0})
			gw.eng.Log.Log(CatPlayerSpawn, "reconnect-to-dead: conn=%d username=%s", s.ConnID, s.Username)
		} else if s.Entity != (mmokit.Entity{}) && gw.eng.ECS.Alive(s.Entity) {
			// Entity preserved across grace period (Active / Docked / Docking).
			gw.reconnectPlayer(s)
			// State-specific welcome on reconnect into a non-Active state.
			// reconnectPlayer sent SE_PLAYER_SPAWNED which resets the client
			// to "in space"; follow up with the state-specific UI cue so
			// the bank panel / dock animation reopens.
			switch s.State {
			case StateDocked:
				gw.ServerEvents().Send(gw.eng.ConnMgr, s.ConnID,
					uint32(gamepb.GameServerEventCode_GSE_DOCKED), &gamepb.DockedMsg{})
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
					gw.ServerEvents().Send(gw.eng.ConnMgr, s.ConnID,
						uint32(gamepb.GameServerEventCode_GSE_DOCKING_STATE),
						&gamepb.DockingStateMsg{
							Docking:   true,
							Progress:  progress,
							TotalTime: gw.Config.DockTime,
							StationId: ds.StationNetID,
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
// Returns nil if the stage's cell is not in the process's cell map (e.g.
// the cell was torn down between dispatch and resolution).
func gameWorldFromStage(stage *mmokit.Stage) *GameWorld {
	if stage == nil {
		return nil
	}
	cell := stage.Process().CellByID(stage.CellID())
	if cell == nil {
		return nil
	}
	return UnwrapGameWorld(cell.World)
}
