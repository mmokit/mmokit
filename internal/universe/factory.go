package universe

import (
	enginepb "github.com/zenion/mmoserver/gen/go/enginepb"
	gamepb "github.com/zenion/mmoserver/gen/go/gamepb"
	"github.com/zenion/mmoserver/internal/game"
	"github.com/zenion/mmoserver/internal/system"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// GameSetup configures the coordinator with game-specific world factory and systems.
func GameSetup(
	coord *mmokit.Coordinator,
	gameCfg game.GameConfig,
	playerDB *game.PlayerRepo,
	playerSessions *mmokit.PlayerSessions,
) {
	coord.SetWorldFactory(func(base *mmokit.WorldBase) mmokit.GameWorld {
		eng := base.Engine()
		cell := base.Cell()
		id := base.NodeID()

		gw := game.NewGameWorld(eng, gameCfg, playerDB, base.SpatialGrid(), mmokit.CellCoord{
			CellX: cell.CellX,
			CellY: cell.CellY,
		})
		gw.NodeID = id
		gw.PlayerSessions = playerSessions

		replRegistry := buildReplicationRegistry(gw)
		base.SetReplicationRegistry(replRegistry)

		// Hook: called after any entity is spawned from a transfer
		base.SetOnTransferReceived(func(entity mmokit.Entity, frame *mmokit.TransferFrame) {
			gw.FinishTransferSpawn(entity, frame)
		})

		// Hook: called after a player entity is spawned from a transfer
		base.SetOnPlayerTransferReceived(func(entity mmokit.Entity, frame *mmokit.TransferFrame) {
			if s := base.Engine().Players.ByConnID(frame.ConnID); s != nil {
				gw.WireTransferPlayer(entity, s)
			}
			if gw.PlayerSessions != nil {
				gw.PlayerSessions.Set(frame.ConnID, frame.Username)
			}

			secFrame := mmokit.MakeEvent(uint32(enginepb.ServerEventCode_SE_CELL_CHANGE), &enginepb.CellChangeMsg{
				CellX: frame.CellX,
				CellY: frame.CellY,
			})
			if secFrame != nil {
				gw.ConnMgr.SendReliable(frame.ConnID, secFrame)
			}
			mapFrame := mmokit.MakeEvent(uint32(gamepb.GameServerEventCode_GSE_MAP_DATA), &gamepb.MapDataMsg{
				Stations: gw.CollectStationMapData(),
			})
			if mapFrame != nil {
				gw.ConnMgr.SendReliable(frame.ConnID, mapFrame)
			}
		})

		seRegistry := buildSideEffectRegistry(gw)
		return newGameWorldAdapter(base, gw, seRegistry)
	})

	// Register systems in the same order as before
	coord.AddSystem("Input", mmokit.NewInputSystem(func(router *mmokit.InputRouter, a *gameWorldAdapter) {
		system.SetupInputHandlers(router, a.GW())
	}))
	coord.AddSystem("Docking", func() mmokit.System { return &system.DockingSystem{} })
	coord.AddSystem("TargetLock", func() mmokit.System { return &system.TargetLockSystem{} })
	coord.AddSystem("ShipControl", func() mmokit.System { return &system.ShipControlSystem{} })
	coord.AddSystem("Mining", func() mmokit.System { return &system.MiningSystem{} })
	coord.AddSystem("Economy", func() mmokit.System { return &system.EconomySystem{} })
	coord.AddSystem("Equipment", func() mmokit.System { return &system.EquipmentSystem{} })
	coord.AddSystem("Ability", func() mmokit.System { return &system.AbilitySystem{} })
	coord.AddSystem("StatusEffect", func() mmokit.System { return &system.StatusEffectSystem{} })
	coord.AddSystem("Wander", func() mmokit.System { return &system.WanderSystem{} })
	coord.AddSystem("Physics", func() mmokit.System { return &system.PhysicsSystem{} })
	coord.AddSystem("DeadReckoning", func() mmokit.System { return &mmokit.ReplicaDeadReckoningSystem{} })
	coord.AddSystem("Lifetime", func() mmokit.System { return &system.LifetimeSystem{} })
	coord.AddSystem("Spatial", mmokit.NewSpatialSystemWith(func(adapter *gameWorldAdapter) mmokit.SpatialHooks {
		gw := adapter.GW()
		return mmokit.SpatialHooks{
			PreTick:  func() { clear(gw.NetIDToEntity) },
			OnEntity: func(entity mmokit.Entity, _ mmokit.SpatialEntry) {
				gw.NetIDToEntity[gw.C.NetworkID.Get(entity).ID] = entity
			},
		}
	}))
	coord.AddSystem("Collision", func() mmokit.System { return &system.CollisionSystem{} })
	coord.AddSystem("ShieldRegen", func() mmokit.System { return &system.ShieldRegenSystem{} })
	coord.AddSystem("Network", func() mmokit.System { return &system.NetworkSystem{} })
}
