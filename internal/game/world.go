package game

import (
	"strings"

	"github.com/mlange-42/ark/ecs"

	gamepb "github.com/zenion/mmoserver/gen/go"
	"github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/pkg/engine"
)

// PlayerDeath records a player kill for notification.
type PlayerDeath struct {
	ConnID      uint32
	KillerNetID uint32
}

// PendingLootDrop records cargo to drop as a loot crate.
type PendingLootDrop struct {
	X, Y  float32
	Items map[uint32]float32
}

// PendingTransfer records a cargo<->bank transfer request.
type PendingTransfer struct {
	ConnID  uint32
	ItemID  uint32
	Amount  float32
	Deposit bool // true = cargo->bank, false = bank->cargo
}

// PendingBankRequest records a request to view bank contents.
type PendingBankRequest struct {
	ConnID uint32
}

// PendingSellRequest records a request to sell an item from the bank.
type PendingSellRequest struct {
	ConnID uint32
	ItemID uint32
	Amount float32 // 0 = sell all
}

// GameWorld holds all game-specific state and embeds the platform Engine.
type GameWorld struct {
	*engine.Engine

	Config     GameConfig
	flushTicks uint32 // cached: PersistFlushInterval * TickRate

	// Entity registry for tooling and admin commands
	Registry *EntityRegistry

	// Entity creation mappers (initialized by per-entity init functions)
	shipMappers       *shipMappers
	asteroidMappers   *asteroidMappers
	stationMappers *stationMappers
	lootCrateMappers  *lootCrateMappers
	npcMappers        *npcMappers

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
	LifetimeMap    *ecs.Map1[component.Lifetime]
	MinableMap     *ecs.Map1[component.Minable]
	MiningLaserMap *ecs.Map1[component.MiningLaser]
	InventoryMap   *ecs.Map1[component.Inventory]
	PlayerConnMap  *ecs.Map1[component.PlayerConn]
	PlayerInputMap *ecs.Map1[component.PlayerInput]
	StationMap        *ecs.Map1[component.Station]
	LootCrateMap      *ecs.Map1[component.LootCrate]
	TargetLockMap     *ecs.Map1[component.TargetLock]
	AbilitySetMap     *ecs.Map1[component.AbilitySet]
	StatusEffectsMap  *ecs.Map1[component.StatusEffects]
	MoveTargetMap     *ecs.Map1[component.MoveTarget]

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

	// Ability events to broadcast this tick (AoI-filtered by NetworkSystem)
	PendingAbilityEvents []*gamepb.AbilityCastResultMsg

	// Inventory transfer requests to process this tick
	PendingTransfers []PendingTransfer

	// Bank view requests to process this tick
	PendingBankRequests []PendingBankRequest

	// Sell requests (sell bank items for FLUX) to process this tick
	PendingSellRequests []PendingSellRequest

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
		inv := gw.InventoryMap.Get(entity)
		// Deep copy the items map
		if len(inv.Items) > 0 {
			pdata.Cargo = make(map[uint32]float32, len(inv.Items))
			for k, v := range inv.Items {
				pdata.Cargo[k] = v
			}
		} else {
			pdata.Cargo = nil
		}
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
			pdata.Cargo = nil // cargo drops as loot
			pdata.HasSave = false
			gw.PlayerDB.MarkDirty(username)
		}
	}

	// Capture inventory for loot crate drop (only combat deaths, not disconnects)
	if gw.InventoryMap.HasAll(entity) && gw.PositionMap.HasAll(entity) {
		inv := gw.InventoryMap.Get(entity)
		if !inv.IsEmpty() {
			pos := gw.PositionMap.Get(entity)
			items := inv.Clear()
			gw.PendingLootDrops = append(gw.PendingLootDrops, PendingLootDrop{
				X:     pos.X,
				Y:     pos.Y,
				Items: items,
			})
		}
	}

	gw.MarkForRemoval(entity)
}
