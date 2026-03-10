package game

import (
	"strings"

	"github.com/mlange-42/ark/ecs"

	gamepb "github.com/zenion/gameserver/gen/go"
	"github.com/zenion/gameserver/internal/component"
	"github.com/zenion/gameserver/pkg/engine"
)

// PlayerDeath records a player kill for notification.
type PlayerDeath struct {
	ConnID      uint32
	KillerNetID uint32
}

// PendingLootDrop records cargo to drop as a loot crate.
type PendingLootDrop struct {
	X, Y      float32
	Resources [4]float32
}

// GameWorld holds all game-specific state and embeds the platform Engine.
type GameWorld struct {
	*engine.Engine

	Config GameConfig

	// Mappers for entity creation
	shipMapper        *ecs.Map8[component.Position, component.Velocity, component.Rotation, component.Collider, component.NetworkID, component.EntityKind, component.ShipControl, component.Health]
	shipExtrasMapper  *ecs.Map5[component.Shield, component.Weapon, component.Inventory, component.PlayerConn, component.PlayerInput]
	asteroidMapper    *ecs.Map6[component.Position, component.Velocity, component.Rotation, component.Collider, component.NetworkID, component.EntityKind]
	minableMapper     *ecs.Map1[component.Minable]
	projectileMapper  *ecs.Map8[component.Position, component.Velocity, component.Rotation, component.Collider, component.NetworkID, component.EntityKind, component.Projectile, component.Lifetime]
	ownerMapper       *ecs.Map1[component.Owner]
	miningMapper          *ecs.Map1[component.MiningLaser]
	stationMapper         *ecs.Map6[component.Position, component.Velocity, component.Rotation, component.Collider, component.NetworkID, component.EntityKind]
	stationMarkerMapper   *ecs.Map1[component.Station]
	lootCrateMapper       *ecs.Map6[component.Position, component.Velocity, component.Rotation, component.Collider, component.NetworkID, component.EntityKind]
	lootCrateExtrasMapper *ecs.Map3[component.Inventory, component.Lifetime, component.LootCrate]

	// Mappers for component access
	PositionMap    *ecs.Map1[component.Position]
	VelocityMap    *ecs.Map1[component.Velocity]
	RotationMap    *ecs.Map1[component.Rotation]
	ColliderMap    *ecs.Map1[component.Collider]
	NetworkIDMap   *ecs.Map1[component.NetworkID]
	EntityKindMap  *ecs.Map1[component.EntityKind]
	ShipControlMap *ecs.Map1[component.ShipControl]
	HealthMap      *ecs.Map1[component.Health]
	ShieldMap      *ecs.Map1[component.Shield]
	WeaponMap      *ecs.Map1[component.Weapon]
	ProjectileMap  *ecs.Map1[component.Projectile]
	OwnerMap       *ecs.Map1[component.Owner]
	LifetimeMap    *ecs.Map1[component.Lifetime]
	MinableMap     *ecs.Map1[component.Minable]
	MiningLaserMap *ecs.Map1[component.MiningLaser]
	InventoryMap   *ecs.Map1[component.Inventory]
	PlayerConnMap  *ecs.Map1[component.PlayerConn]
	PlayerInputMap *ecs.Map1[component.PlayerInput]
	StationMap     *ecs.Map1[component.Station]
	LootCrateMap   *ecs.Map1[component.LootCrate]

	// Player deaths pending notification
	PendingDeaths []PlayerDeath

	// Players waiting to respawn (connID set)
	DeadPlayers map[uint32]bool

	// Respawn requests to process this tick
	PendingRespawns []uint32

	// connID -> entity mapping
	PlayerEntities map[uint32]ecs.Entity

	// NetID -> entity mapping (rebuilt each tick by SpatialSystem)
	NetIDToEntity map[uint32]ecs.Entity

	// Persistent player database (keyed by username)
	PlayerDB *PlayerRepo

	// connID -> username mapping for active connections
	ConnToUsername map[uint32]string

	// Connections waiting for login (not yet spawned)
	PendingConnections map[uint32]bool

	// Login requests to process this tick (connID -> username)
	PendingLogins map[uint32]string

	// Pending loot drops to spawn after FlushRemovals
	PendingLootDrops []PendingLootDrop

	// Chat messages to broadcast this tick
	PendingChat []*gamepb.ChatMsg

	// Console reference for dynamic completions
	console *engine.Console
}

// UsernameInUse returns true if the given username is already connected.
func (gw *GameWorld) UsernameInUse(username string) bool {
	for _, u := range gw.ConnToUsername {
		if strings.EqualFold(u, username) {
			return true
		}
	}
	return false
}

// SavePlayerState persists the current entity state to the player database.
func (gw *GameWorld) SavePlayerState(connID uint32, entity ecs.Entity) {
	username, ok := gw.ConnToUsername[connID]
	if !ok {
		return
	}
	pdata := gw.PlayerDB.GetOrCreate(username)
	if gw.PositionMap.HasAll(entity) {
		pos := gw.PositionMap.Get(entity)
		pdata.X = pos.X
		pdata.Y = pos.Y
	}
	if gw.InventoryMap.HasAll(entity) {
		pdata.Resources = gw.InventoryMap.Get(entity).Resources
	}
	pdata.HasSave = true
	gw.PlayerDB.MarkDirty(username)
}

// MarkPlayerDeath records that a player entity was killed.
// The entity will also be marked for removal. Captures inventory for loot drop.
func (gw *GameWorld) MarkPlayerDeath(entity ecs.Entity, killerNetID uint32) {
	if gw.PlayerConnMap.HasAll(entity) {
		connID := gw.PlayerConnMap.Get(entity).ConnID
		gw.PendingDeaths = append(gw.PendingDeaths, PlayerDeath{
			ConnID:      connID,
			KillerNetID: killerNetID,
		})

		// Clear saved state so respawn places them near the station
		if username, ok := gw.ConnToUsername[connID]; ok {
			pdata := gw.PlayerDB.GetOrCreate(username)
			pdata.Resources = [4]float32{} // cargo drops as loot
			pdata.HasSave = false
			gw.PlayerDB.MarkDirty(username)
		}
	}

	// Capture inventory for loot crate drop (only combat deaths, not disconnects)
	if gw.InventoryMap.HasAll(entity) && gw.PositionMap.HasAll(entity) {
		inv := gw.InventoryMap.Get(entity)
		var total float32
		for _, r := range inv.Resources {
			total += r
		}
		if total > 0 {
			pos := gw.PositionMap.Get(entity)
			gw.PendingLootDrops = append(gw.PendingLootDrops, PendingLootDrop{
				X:         pos.X,
				Y:         pos.Y,
				Resources: inv.Resources,
			})
		}
	}

	gw.MarkForRemoval(entity)
}
