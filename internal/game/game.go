package game

import (
	"log"

	"github.com/mlange-42/ark/ecs"

	"github.com/zenion/mmoserver/internal/item"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// NewGameWorld creates a new game world backed by the given engine.
func NewGameWorld(eng *mmokit.Engine, cfg GameConfig, playerDB *PlayerRepo, grid *mmokit.Grid, sector mmokit.SectorCoord) *GameWorld {
	item.Init()
	ecsWorld := eng.ECS

	gw := &GameWorld{
		Engine:        eng,
		Grid:          grid,
		Config:        cfg,
		Bridge:        mmokit.NoopNodeBridge{},
		Queue:         mmokit.NewTickQueue(),
		Players:       NewPlayerTracker(),
		NetIDToEntity: make(map[uint32]ecs.Entity),
		PlayerDB:      playerDB,
		SideEffects:   &mmokit.SideEffectCollector{},
	}

	gw.Sector = sector
	gw.flushTicks = uint32(gw.Config.PersistFlushInterval * float32(eng.Config.TickRate))
	gw.FullRefreshInterval = uint32(eng.Config.TickRate) // full refresh every 1 second

	// Initialize entity registry and per-entity mappers
	gw.Registry = mmokit.NewEntityRegistry()
	initShipEntity(gw)
	initAsteroidEntity(gw)
	initStationEntity(gw)
	initLootCrateEntity(gw)
	initNpcEntity(gw)

	// Component mappers
	gw.C = NewComponents(ecsWorld)

	// Spawn initial content for this sector
	gw.spawnAsteroids()
	if sector.SX == 0 && sector.SY == 0 {
		gw.SpawnStation()
	}

	return gw
}

// Hooks returns the engine lifecycle hooks wired to this game world.
func (gw *GameWorld) Hooks() mmokit.Hooks {
	return mmokit.Hooks{
		OnConnect:      gw.onConnect,
		OnDisconnect:   gw.onDisconnect,
		ProcessLogins:  gw.processLogins,
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
	for connID, entity := range gw.Players.Entities {
		if gw.ECS.Alive(entity) {
			gw.SavePlayerState(connID, entity)
		}
	}
	gw.PlayerDB.FlushDirty()
	log.Println("shutdown: all player data saved")
}
