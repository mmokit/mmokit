package game

import (
	"log"

	"github.com/mlange-42/ark/ecs"

	"github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/internal/item"
	"github.com/zenion/mmoserver/pkg/engine"
	"github.com/zenion/mmoserver/pkg/spatial"
)

// NewGameWorld creates a new game world backed by the given engine.
func NewGameWorld(eng *engine.Engine, cfg GameConfig, playerDB *PlayerRepo, grid *spatial.Grid, sector component.SectorCoord) *GameWorld {
	item.Init()
	ecsWorld := eng.ECS

	gw := &GameWorld{
		Engine:             eng,
		Grid:               grid,
		Config:             cfg,
		Bridge:        NoopNodeBridge{},
		Queue:         NewTickQueue(),
		Players:       NewPlayerTracker(),
		NetIDToEntity: make(map[uint32]ecs.Entity),
		PlayerDB:      playerDB,
	}

	gw.Sector = sector
	gw.flushTicks = uint32(gw.Config.PersistFlushInterval * float32(eng.Config.TickRate))
	gw.FullRefreshInterval = uint32(eng.Config.TickRate) // full refresh every 1 second

	// Initialize entity registry and per-entity mappers
	gw.Registry = NewEntityRegistry()
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
func (gw *GameWorld) Hooks() engine.Hooks {
	return engine.Hooks{
		OnConnect:      gw.onConnect,
		OnDisconnect:   gw.onDisconnect,
		ProcessLogins:  gw.processLogins,
		PreFlush: func() {
			gw.processDeaths()
			gw.processDockCompletions()
		},
		GetNetID:       gw.getNetID,
		PostFlush:      gw.postFlush,
		ClearTickState: gw.clearTickState,
		PostTick:       gw.postTick,
	}
}

// postTick runs after each tick — replica replication/expiration and periodic saves.
func (gw *GameWorld) postTick() {
	gw.Bridge.PostSystems()
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
