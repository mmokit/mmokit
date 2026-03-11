package game

import (
	"strings"

	"github.com/mlange-42/ark/ecs"

	gamepb "github.com/zenion/mmoserver/gen/go"
	"github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/internal/item"
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

// PendingEquipRequest records a request to equip or unequip an item.
type PendingEquipRequest struct {
	ConnID uint32
	ItemID uint32        // 0 = unequip
	Slot   item.EquipSlot
}

// PendingShopBuy records a request to buy an item from the station shop.
type PendingShopBuy struct {
	ConnID uint32
	ItemID uint32
	Qty    uint32
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
	EquipmentMap      *ecs.Map1[component.Equipment]

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

	// Equipment change requests to process this tick
	PendingEquipRequests []PendingEquipRequest

	// Shop buy requests to process this tick
	PendingShopBuys []PendingShopBuy

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
	if gw.EquipmentMap.HasAll(entity) {
		eq := gw.EquipmentMap.Get(entity)
		pdata.Equipment = EquipmentSave{
			Weapon1:  eq.Weapon1,
			Weapon2:  eq.Weapon2,
			Shield:   eq.Shield,
			Thruster: eq.Thruster,
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
			pdata.Cargo = nil                    // cargo drops as loot
			pdata.Equipment = EquipmentSave{}    // equipment drops as loot
			pdata.HasSave = false
			gw.PlayerDB.MarkDirty(username)
		}
	}

	// Capture inventory + equipment for loot crate drop (only combat deaths, not disconnects)
	if gw.PositionMap.HasAll(entity) {
		pos := gw.PositionMap.Get(entity)
		var items map[uint32]float32

		// Collect cargo items
		if gw.InventoryMap.HasAll(entity) {
			inv := gw.InventoryMap.Get(entity)
			if !inv.IsEmpty() {
				items = inv.Clear()
			}
		}

		// Collect equipped items
		if gw.EquipmentMap.HasAll(entity) {
			eq := gw.EquipmentMap.Get(entity)
			for _, eqID := range []uint32{eq.Weapon1, eq.Weapon2, eq.Shield, eq.Thruster} {
				if eqID != 0 {
					if items == nil {
						items = make(map[uint32]float32)
					}
					items[eqID] += 1
				}
			}
			// Clear equipment on the entity
			eq.Weapon1 = 0
			eq.Weapon2 = 0
			eq.Shield = 0
			eq.Thruster = 0
		}

		if len(items) > 0 {
			gw.PendingLootDrops = append(gw.PendingLootDrops, PendingLootDrop{
				X:     pos.X,
				Y:     pos.Y,
				Items: items,
			})
		}
	}

	gw.MarkForRemoval(entity)
}

// ApplyEquipmentStats recalculates shield and movement stats from equipped items.
// Call after any equipment change or at spawn.
func (gw *GameWorld) ApplyEquipmentStats(entity ecs.Entity) {
	if !gw.EquipmentMap.HasAll(entity) {
		return
	}
	eq := gw.EquipmentMap.Get(entity)

	// Shield stats from shield generator
	if gw.ShieldMap.HasAll(entity) {
		shield := gw.ShieldMap.Get(entity)
		baseMax := gw.Config.ShipShield
		baseRegen := gw.Config.ShieldRegenRate

		if def := item.Get(eq.Shield); def != nil && def.Equip != nil {
			shield.Max = baseMax + def.Equip.ShieldMax
			if def.Equip.ShieldRegenRate > 0 {
				shield.RegenRate = def.Equip.ShieldRegenRate
			} else {
				shield.RegenRate = baseRegen
			}
		} else {
			// No shield gen equipped — use base stats
			shield.Max = baseMax
			shield.RegenRate = baseRegen
		}
		if shield.Current > shield.Max {
			shield.Current = shield.Max
		}
	}

	// Movement stats from thruster
	if gw.ShipControlMap.HasAll(entity) {
		sc := gw.ShipControlMap.Get(entity)
		sc.Thrust = gw.Config.ShipThrust
		sc.MaxSpeed = gw.Config.MaxSpeed

		if def := item.Get(eq.Thruster); def != nil && def.Equip != nil {
			sc.Thrust += def.Equip.ThrustBonus
			sc.MaxSpeed += def.Equip.MaxSpeedBonus
		}
	}

	// Mining laser stats from weapon slots
	if gw.MiningLaserMap.HasAll(entity) {
		laser := gw.MiningLaserMap.Get(entity)

		// Weapon1 → beam[0]
		if def := item.Get(eq.Weapon1); def != nil && def.Equip != nil && def.Equip.Primary.Type == item.AbilityTypeMiningBeam {
			laser.Beams[0].Rate = def.Equip.Primary.MiningRate
			laser.Beams[0].Range = def.Equip.Primary.MiningRange
			if def.Equip.Secondary != nil {
				laser.Beams[0].PulseYield = def.Equip.Secondary.MiningYield
			}
		} else {
			laser.Beams[0] = component.MiningBeamState{}
		}

		// Weapon2 → beam[1]
		if def := item.Get(eq.Weapon2); def != nil && def.Equip != nil && def.Equip.Primary.Type == item.AbilityTypeMiningBeam {
			laser.Beams[1].Rate = def.Equip.Primary.MiningRate
			laser.Beams[1].Range = def.Equip.Primary.MiningRange
			if def.Equip.Secondary != nil {
				laser.Beams[1].PulseYield = def.Equip.Secondary.MiningYield
			}
		} else {
			laser.Beams[1] = component.MiningBeamState{}
		}
	}
}

// AbilityCooldownForSlot returns the cooldown duration for a given ability slot,
// reading from the equipped item. Returns 0 if no equipment or no ability.
func (gw *GameWorld) AbilityCooldownForSlot(entity ecs.Entity, slot uint8) float32 {
	if !gw.EquipmentMap.HasAll(entity) {
		return 0
	}
	eq := gw.EquipmentMap.Get(entity)

	equipSlot, isPrimary := item.AbilitySlotToEquipSlot(slot)
	var itemID uint32
	switch equipSlot {
	case item.SlotWeapon1:
		itemID = eq.Weapon1
	case item.SlotWeapon2:
		itemID = eq.Weapon2
	case item.SlotShield:
		itemID = eq.Shield
	case item.SlotThruster:
		itemID = eq.Thruster
	default:
		return 0
	}

	def := item.Get(itemID)
	if def == nil || def.Equip == nil {
		return 0
	}

	if isPrimary {
		return def.Equip.Primary.Cooldown
	}
	if def.Equip.Secondary != nil {
		return def.Equip.Secondary.Cooldown
	}
	return 0
}
