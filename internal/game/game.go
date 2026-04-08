package game

import (
	"log"
	"time"

	"github.com/mlange-42/ark/ecs"

	"github.com/zenion/mmoserver/internal/item"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// Package-level custom player states.
var (
	StateDead    mmokit.PlayerState
	StateDocking mmokit.PlayerState
	StateDocked  mmokit.PlayerState
)

// NewGameWorld creates a new game world backed by the given engine.
func NewGameWorld(eng *mmokit.Engine, cfg GameConfig, playerDB *PlayerRepo, grid *mmokit.HashGrid, cell mmokit.CellCoord, fromSplit bool) *GameWorld {
	item.Init()
	ecsWorld := eng.ECS

	gw := &GameWorld{
		Engine:        eng,
		Spatial:       grid,
		Config:        cfg,
		Bridge:        mmokit.NoopNodeBridge{},
		Queue:         mmokit.NewTickQueue(),
		NetIDToEntity: make(map[uint32]ecs.Entity),
		PlayerDB:      playerDB,
		SideEffects:   &mmokit.SideEffectCollector{},
	}
	gw.Players = eng.Players

	// Register custom player states
	StateDead = gw.Players.RegisterState("dead")
	StateDocking = gw.Players.RegisterState("docking")
	StateDocked = gw.Players.RegisterState("docked")
	// removeFromWorld saves and removes the player's ECS entity.
	// Used by transitions where the player permanently leaves the world.
	// If the entity has a Ghost component (transfer in progress), skip removal —
	// the ghost lingers for visual continuity until the replica arrives.
	ghostCheck := ecs.NewMap1[mmokit.Ghost](ecsWorld)
	removeFromWorld := func(s *mmokit.PlayerSession, pm *mmokit.PlayerManager) {
		if gw.ECS.Alive(s.Entity) {
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
			gw.ECS.RemoveEntity(s.Entity)
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
		{From: StateDocking, To: StateDocked},
		{From: StateDocking, To: StateDead, Action: removeFromWorld},
		{From: StateDocked, To: mmokit.StateActive},
		{From: StateDocked, To: mmokit.StateDisconnected, Action: disconnectKeepEntity},
	})

	// Register state callbacks
	gw.Players.OnState(mmokit.StateActive, mmokit.StateCallbacks{
		OnEnter: func(s *mmokit.PlayerSession, pm *mmokit.PlayerManager) {
			// Reconnect: entity still alive from grace period — just re-wire, don't respawn
			if s.Entity != (ecs.Entity{}) && gw.ECS.Alive(s.Entity) {
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
		},
	})

	// When grace period expires (or session is removed while Disconnected),
	// clean up the entity that was kept alive for potential reconnection.
	gw.Players.OnState(mmokit.StateDisconnected, mmokit.StateCallbacks{
		OnExit: func(s *mmokit.PlayerSession, pm *mmokit.PlayerManager) {
			if gw.ECS.Alive(s.Entity) {
				gw.SavePlayerState(s)
				gw.Spatial.Deregister(s.Entity)
				gw.ECS.RemoveEntity(s.Entity)
			}
			s.Entity = ecs.Entity{}
			gw.updatePlayerCompletions()
		},
	})

	gw.Cell = cell
	gw.flushTicks = uint32(gw.Config.PersistFlushInterval * float32(eng.Config.TickRate))
	gw.FullRefreshInterval = uint32(eng.Config.TickRate)

	// Initialize entity registry and per-entity mappers
	gw.Registry = mmokit.NewEntityRegistry()
	initShipEntity(gw)
	initAsteroidEntity(gw)
	initStationEntity(gw)
	initLootCrateEntity(gw)
	initNpcEntity(gw)

	// Component mappers
	gw.C = NewComponents(ecsWorld)

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
// OnConnect, OnDisconnect, and ProcessLogins are handled by PlayerManager.
func (gw *GameWorld) Hooks() mmokit.Hooks {
	return mmokit.Hooks{
		PreFlush: func() {
			gw.processDeaths()
			gw.processDockCompletions()
		},
		PostFlush:      gw.postFlush,
		ClearTickState: gw.clearTickState,
		PostTick:       gw.postTick,
	}
}

// postTick runs after each tick — periodic saves.
// Bridge.PostSystems() is called by the Coordinator's merged hooks.
func (gw *GameWorld) postTick() {
	if gw.flushTicks > 0 && gw.Tick%gw.flushTicks == 0 {
		gw.PlayerDB.FlushDirty()
	}
}

// Shutdown saves all connected players and flushes dirty data.
// Call after the game loop has stopped.
func (gw *GameWorld) Shutdown() {
	gw.Players.ForEach(mmokit.StateActive, func(s *mmokit.PlayerSession) {
		if gw.ECS.Alive(s.Entity) {
			gw.SavePlayerState(s)
		}
	})
	gw.PlayerDB.FlushDirty()
	log.Println("shutdown: all player data saved")
}
