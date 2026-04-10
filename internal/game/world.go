package game

import (
	"github.com/mlange-42/ark/ecs"

	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/internal/item"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// PlayerDeath records a player kill for notification.
type PlayerDeath struct {
	ConnID      uint32
	KillerNetID uint32
}

// PendingLootDrop records cargo to drop as a loot crate.
type PendingLootDrop struct {
	X, Y  float32
	Items map[uint32]int32
}

// PendingTransfer records a cargo<->bank transfer request.
type PendingTransfer struct {
	ConnID  uint32
	ItemID  uint32
	Amount  int32
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
	Amount int32 // 0 = sell all
}

// PendingEquipRequest records a request to equip or unequip an item.
type PendingEquipRequest struct {
	ConnID uint32
	ItemID uint32 // 0 = unequip
	Slot   item.EquipSlot
}

// PendingShopBuy records a request to buy an item from the station shop.
type PendingShopBuy struct {
	ConnID uint32
	ItemID uint32
	Qty    uint32
}

// PendingDockRequest records a request to begin docking at a station.
type PendingDockRequest struct {
	ConnID uint32
}

// PendingUndockRequest records a request to undock from a station.
type PendingUndockRequest struct {
	ConnID uint32
}

// PendingLootItem records a request to loot a single item from a crate.
type PendingLootItem struct {
	ConnID     uint32
	CrateNetID uint32
	ItemID     uint32
}

// PendingLootAll records a request to loot all items from a crate.
type PendingLootAll struct {
	ConnID     uint32
	CrateNetID uint32
}

// PendingRespawn records a respawn request.
type PendingRespawn struct {
	ConnID uint32
}

// DockingState tracks a player's in-progress docking sequence.
type DockingState struct {
	Remaining    float32 // seconds left
	StationX     float32 // target station position
	StationY     float32
	StationNetID uint32 // for client VFX
}

// GameWorld holds all game-specific state and embeds WorldBase for multi-node support.
type GameWorld struct {
	*mmokit.WorldBase
	eng *mmokit.Engine // cached for convenience (avoids gw.Engine().ECS everywhere)

	Spatial    *mmokit.HashGrid
	Config     GameConfig
	flushTicks uint32 // cached: PersistFlushInterval * TickRate

	// Ticks between forced full-state sends (safety net for diffing bugs)
	FullRefreshInterval uint32

	// Entity registry for tooling and admin commands
	Registry *mmokit.EntityRegistry

	// C holds all single-component mappers and the replica batch mapper.
	C *Components

	// Queue holds all per-tick pending work (replaces individual Pending* slices).
	Queue *mmokit.TickQueue

	// Players tracks all player-connection state via the engine's PlayerManager.
	Players *mmokit.PlayerManager

	// NetID -> entity mapping (rebuilt each tick by SpatialSystem)
	NetIDToEntity map[uint32]ecs.Entity

	// Persistent player database (keyed by username)
	PlayerDB *PlayerRepo

	// Console reference for dynamic completions
	console *mmokit.Console

	// PlayerSessions for the operation router (thread-safe, set from game loop)
	PlayerSessions *mmokit.PlayerSessions

	// Cell identifies which cell this node owns (root-cell coordinates).
	Cell mmokit.CellCoord

	// OnPostSpawn is called after a player spawns (for topology sends, etc.)
	OnPostSpawn func(connID uint32)

	// SideEffects collects cross-node side effects during action handling.
	// Any code running during HandleCrossNodeAction can emit effects here;
	// the adapter drains them after the action handler returns.
	SideEffects *mmokit.SideEffectCollector

	// sideEffectRegistry dispatches cross-node action results with side effects.
	sideEffectRegistry *mmokit.SideEffectRegistry
}

// Ensure GameWorld implements mmokit.GameWorld at compile time.
var _ mmokit.GameWorld = (*GameWorld)(nil)

// SavePlayerState persists the current entity state to the player database.
func (gw *GameWorld) SavePlayerState(s *mmokit.PlayerSession) {
	username := s.Username
	if username == "" {
		return
	}
	entity := s.Entity
	if entity == (ecs.Entity{}) || !gw.eng.ECS.Alive(entity) {
		return
	}
	pdata := gw.PlayerDB.GetOrCreate(username)
	if gw.C.Position.HasAll(entity) {
		pos := gw.C.Position.Get(entity)
		pdata.X = pos.X
		pdata.Y = pos.Y
	}
	if gw.C.CellCoord.HasAll(entity) {
		sec := gw.C.CellCoord.Get(entity)
		pdata.CellX = sec.CellX
		pdata.CellY = sec.CellY
	}
	if gw.C.Inventory.HasAll(entity) {
		inv := gw.C.Inventory.Get(entity)
		// Deep copy the items map
		if len(inv.Items) > 0 {
			pdata.Cargo = make(map[uint32]int32, len(inv.Items))
			for k, v := range inv.Items {
				pdata.Cargo[k] = v
			}
		} else {
			pdata.Cargo = nil
		}
	}
	if gw.C.Equipment.HasAll(entity) {
		eq := gw.C.Equipment.Get(entity)
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
	if gw.C.PlayerConn.HasAll(entity) {
		connID := gw.C.PlayerConn.Get(entity).ConnID
		mmokit.Enqueue(gw.Queue, PlayerDeath{
			ConnID:      connID,
			KillerNetID: killerNetID,
		})

		// Clear saved state so respawn places them near the station
		if s := gw.Players.ByConnID(connID); s != nil && s.Username != "" {
			pdata := gw.PlayerDB.GetOrCreate(s.Username)
			pdata.Cargo = nil                 // cargo drops as loot
			pdata.Equipment = EquipmentSave{} // equipment drops as loot
			pdata.HasSave = false
			gw.PlayerDB.MarkDirty(s.Username)
		}
	}

	// Capture inventory + equipment for loot crate drop (only combat deaths, not disconnects)
	if gw.C.Position.HasAll(entity) {
		pos := gw.C.Position.Get(entity)
		var items map[uint32]int32

		// Collect cargo items
		if gw.C.Inventory.HasAll(entity) {
			inv := gw.C.Inventory.Get(entity)
			if !inv.IsEmpty() {
				items = inv.Clear()
			}
		}

		// Collect equipped items
		if gw.C.Equipment.HasAll(entity) {
			eq := gw.C.Equipment.Get(entity)
			for _, eqID := range []uint32{eq.Weapon1, eq.Weapon2, eq.Shield, eq.Thruster} {
				if eqID != 0 {
					if items == nil {
						items = make(map[uint32]int32)
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
			mmokit.Enqueue(gw.Queue, PendingLootDrop{
				X:     pos.X,
				Y:     pos.Y,
				Items: items,
			})
		}
	}

	gw.MarkForRemoval(entity)
}

// syncActiveMining updates the replicated ActiveMining component from the
// authoritative MiningLaser state on the same entity. Call whenever beam
// activation or target may have changed so clients see the toggle immediately.
// Logs on state transitions only.
func (gw *GameWorld) syncActiveMining(entity ecs.Entity, laser *gamecomp.MiningLaser) {
	if !gw.C.ActiveMining.HasAll(entity) {
		return
	}
	active := gw.C.ActiveMining.Get(entity)
	newBeam0 := laser.Beams[0].Active
	newBeam1 := laser.Beams[1].Active
	var newTarget uint32
	if (newBeam0 || newBeam1) && gw.eng.ECS.Alive(laser.Target) && gw.C.NetworkID.HasAll(laser.Target) {
		newTarget = gw.C.NetworkID.Get(laser.Target).ID
	}
	if active.Beam0Active != newBeam0 || active.Beam1Active != newBeam1 || active.MiningTargetNetID != newTarget {
		gw.eng.Log.Log(CatEconomyMining, "active-mining sync: player=%d beams=[%v,%v] target=%d",
			gw.C.NetworkID.Get(entity).ID, newBeam0, newBeam1, newTarget)
	}
	active.Beam0Active = newBeam0
	active.Beam1Active = newBeam1
	active.MiningTargetNetID = newTarget
}

// ApplyEquipmentStats recalculates shield and movement stats from equipped items.
// Call after any equipment change or at spawn.
func (gw *GameWorld) ApplyEquipmentStats(entity ecs.Entity) {
	if !gw.C.Equipment.HasAll(entity) {
		return
	}
	eq := gw.C.Equipment.Get(entity)

	// Shield stats from shield generator
	if gw.C.Shield.HasAll(entity) {
		shield := gw.C.Shield.Get(entity)
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
	if gw.C.ShipControl.HasAll(entity) {
		sc := gw.C.ShipControl.Get(entity)
		sc.Thrust = gw.Config.ShipThrust
		sc.MaxSpeed = gw.Config.MaxSpeed

		if def := item.Get(eq.Thruster); def != nil && def.Equip != nil {
			sc.Thrust += def.Equip.ThrustBonus
			sc.MaxSpeed += def.Equip.MaxSpeedBonus
		}
	}

	// Mining laser stats from weapon slots
	if gw.C.MiningLaser.HasAll(entity) {
		laser := gw.C.MiningLaser.Get(entity)

		// Weapon1 → beam[0]
		if def := item.Get(eq.Weapon1); def != nil && def.Equip != nil && def.Equip.Primary.Type == item.AbilityTypeMiningBeam {
			laser.Beams[0].Rate = def.Equip.Primary.MiningRate
			laser.Beams[0].Range = def.Equip.Primary.MiningRange
			if def.Equip.Secondary != nil {
				laser.Beams[0].PulseYield = def.Equip.Secondary.MiningYield
			}
		} else {
			laser.Beams[0] = gamecomp.MiningBeamState{}
		}

		// Weapon2 → beam[1]
		if def := item.Get(eq.Weapon2); def != nil && def.Equip != nil && def.Equip.Primary.Type == item.AbilityTypeMiningBeam {
			laser.Beams[1].Rate = def.Equip.Primary.MiningRate
			laser.Beams[1].Range = def.Equip.Primary.MiningRange
			if def.Equip.Secondary != nil {
				laser.Beams[1].PulseYield = def.Equip.Secondary.MiningYield
			}
		} else {
			laser.Beams[1] = gamecomp.MiningBeamState{}
		}
	}
}

// AbilityCooldownForSlot returns the cooldown duration for a given ability slot,
// reading from the equipped item. Returns 0 if no equipment or no ability.
func (gw *GameWorld) AbilityCooldownForSlot(entity ecs.Entity, slot uint8) float32 {
	if !gw.C.Equipment.HasAll(entity) {
		return 0
	}
	eq := gw.C.Equipment.Get(entity)

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
