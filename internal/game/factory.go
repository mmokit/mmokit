package game

import (
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
		// Reconnect path: entity preserved across grace period.
		if s.Entity != (mmokit.Entity{}) && gw.eng.ECS.Alive(s.Entity) {
			gw.reconnectPlayer(s)
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
