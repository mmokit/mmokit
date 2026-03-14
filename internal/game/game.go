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
func NewGameWorld(eng *engine.Engine, cfg GameConfig, playerDB *PlayerRepo, grid *spatial.Grid) *GameWorld {
	item.Init()
	ecsWorld := eng.ECS

	gw := &GameWorld{
		Engine:             eng,
		Grid:               grid,
		Config:             cfg,
		PlayerEntities:     make(map[uint32]ecs.Entity),
		NetIDToEntity:      make(map[uint32]ecs.Entity),
		DeadPlayers:        make(map[uint32]bool),
		DockedPlayers:      make(map[uint32]bool),
		DockingPlayers:     make(map[uint32]*DockingState),
		PlayerDB:           playerDB,
		ConnToUsername:     make(map[uint32]string),
		PendingConnections: make(map[uint32]bool),
		PendingLogins:      make(map[uint32]string),
		PendingRespawns:    make([]uint32, 0, 8),
	}

	gw.flushTicks = uint32(gw.Config.PersistFlushInterval * float32(eng.Config.TickRate))
	gw.FullRefreshInterval = uint32(eng.Config.TickRate) // full refresh every 1 second

	// Initialize entity registry and per-entity mappers
	gw.Registry = NewEntityRegistry()
	initShipEntity(gw)
	initAsteroidEntity(gw)
	initStationEntity(gw)
	initLootCrateEntity(gw)
	initNpcEntity(gw)

	// Single-component mappers for access
	gw.PositionMap = ecs.NewMap1[component.Position](ecsWorld)
	gw.VelocityMap = ecs.NewMap1[component.Velocity](ecsWorld)
	gw.RotationMap = ecs.NewMap1[component.Rotation](ecsWorld)
	gw.ColliderMap = ecs.NewMap1[component.Collider](ecsWorld)
	gw.NetworkIDMap = ecs.NewMap1[component.NetworkID](ecsWorld)
	gw.EntityKindMap = ecs.NewMap1[component.EntityKind](ecsWorld)
	gw.ShipControlMap = ecs.NewMap1[component.ShipControl](ecsWorld)
	gw.HealthMap = ecs.NewMap1[component.Health](ecsWorld)
	gw.ShieldMap = ecs.NewMap1[component.Shield](ecsWorld)
	gw.LifetimeMap = ecs.NewMap1[component.Lifetime](ecsWorld)
	gw.MinableMap = ecs.NewMap1[component.Minable](ecsWorld)
	gw.MiningLaserMap = ecs.NewMap1[component.MiningLaser](ecsWorld)
	gw.InventoryMap = ecs.NewMap1[component.Inventory](ecsWorld)
	gw.PlayerConnMap = ecs.NewMap1[component.PlayerConn](ecsWorld)
	gw.PlayerInputMap = ecs.NewMap1[component.PlayerInput](ecsWorld)
	gw.StationMap = ecs.NewMap1[component.Station](ecsWorld)
	gw.LootCrateMap = ecs.NewMap1[component.LootCrate](ecsWorld)
	gw.TargetLockMap = ecs.NewMap1[component.TargetLock](ecsWorld)
	gw.AbilitySetMap = ecs.NewMap1[component.AbilitySet](ecsWorld)
	gw.StatusEffectsMap = ecs.NewMap1[component.StatusEffects](ecsWorld)
	gw.MoveTargetMap = ecs.NewMap1[component.MoveTarget](ecsWorld)
	gw.EquipmentMap = ecs.NewMap1[component.Equipment](ecsWorld)

	// Spawn initial asteroids
	gw.spawnAsteroids()

	// Spawn trade station at origin
	gw.SpawnStation()

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

// postTick runs after each tick — flushes dirty player data periodically.
func (gw *GameWorld) postTick() {
	if gw.flushTicks > 0 && gw.Tick%gw.flushTicks == 0 {
		gw.PlayerDB.FlushDirty()
	}
}

// Shutdown saves all connected players and flushes dirty data.
// Call after the game loop has stopped.
func (gw *GameWorld) Shutdown() {
	for connID, entity := range gw.PlayerEntities {
		if gw.ECS.Alive(entity) {
			gw.SavePlayerState(connID, entity)
		}
	}
	gw.PlayerDB.FlushDirty()
	log.Println("shutdown: all player data saved")
}
