package game

import (
	"maps"

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

	Spatial *mmokit.HashGrid
	// Config is a shared pointer across all GameWorlds in the coordinator —
	// one source of truth. Runtime `config set` mutations propagate to every
	// node immediately because they all see the same struct.
	Config     *GameConfig
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

	// RootCell identifies which root cell this node owns (depth-0 coordinates).
	// Distinct name from the embedded WorldBase.Cell() method, which returns a
	// CellID with depth — this field is kept for game-side convenience that
	// only needs the X/Y of the root cell. Renaming to "RootCell" avoids
	// shadowing WorldBase.Cell(), which would silently break the
	// pkg/universe BoundaryWorld interface check and disable boundary
	// transfers entirely.
	RootCell mmokit.CellCoord

	// OnPostSpawn is called after a player spawns (for topology sends, etc.)
	OnPostSpawn func(connID uint32)

	// SideEffects collects cross-cell side effects during action handling.
	// Any code running during HandleCrossCellAction can emit effects here;
	// the adapter drains them after the action handler returns.
	SideEffects *mmokit.SideEffectCollector

	// sideEffectRegistry dispatches cross-cell action results with side effects.
	sideEffectRegistry *mmokit.SideEffectRegistry
}

// ServerEvents returns the typed server-event registry declared in main.go's
// cfg.Protocol. Used by every site that emits a server event.
func (gw *GameWorld) ServerEvents() *mmokit.ServerEvents {
	return mmokit.ServerEventsOf(gw.Process())
}

// Ensure GameWorld implements mmokit.GameWorld at compile time.
var _ mmokit.GameWorld = (*GameWorld)(nil)

// Ensure GameWorld also satisfies mmokit.BoundaryWorld. A field named `Cell`
// on GameWorld would shadow the embedded WorldBase.Cell() method and
// silently disable all cross-cell entity transfers — this assertion catches
// that class of bug at compile time instead of silently at runtime.
var _ mmokit.BoundaryWorld = (*GameWorld)(nil)

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
			maps.Copy(pdata.Cargo, inv.Items)
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
		// RegenDelay has no equipment modifier — always pulled from config so
		// runtime `config set ShieldRegenDelay` takes effect on existing ships.
		shield.RegenDelay = gw.Config.ShieldRegenDelay
		if shield.Current > shield.Max {
			shield.Current = shield.Max
		}
	}

	// Collider dimensions from config. Re-applied so runtime ShipWidth/Height
	// tweaks propagate to existing ships (cosmetic + hit-box consistency).
	// Note: Health.Current/Max is intentionally NOT re-synced here — mutating
	// HP on a config change is either a heal exploit or a confusing drop.
	if gw.C.Collider.HasAll(entity) {
		col := gw.C.Collider.Get(entity)
		col.Width = gw.Config.ShipWidth
		col.Height = gw.Config.ShipHeight
		col.Radius = boundingRadius(gw.Config.ShipWidth, gw.Config.ShipHeight)
	}

	// Inventory capacity. New cap can be below current cargo mass — that's
	// accepted; the next deposit will be rejected until players clear space.
	if gw.C.Inventory.HasAll(entity) {
		inv := gw.C.Inventory.Get(entity)
		inv.MaxMass = gw.Config.MaxCargo
	}

	// TargetLock tuning — both fields are pure config reads with no
	// equipment modifier today.
	if gw.C.TargetLock.HasAll(entity) {
		tl := gw.C.TargetLock.Get(entity)
		tl.LockTime = gw.Config.LockOnTime
		tl.Range = gw.Config.LockOnRange
	}

	// Movement stats from thruster. All three are re-synced from config
	// each call so that runtime `config set` changes propagate through the
	// game-side `config`-command OnChanged hook (which calls this function
	// on every active ship).
	if gw.C.ShipControl.HasAll(entity) {
		sc := gw.C.ShipControl.Get(entity)
		sc.Thrust = gw.Config.ShipThrust
		sc.MaxSpeed = gw.Config.MaxSpeed
		sc.TurnRate = gw.Config.ShipTurnRate
		sc.TurnAccel = gw.Config.ShipTurnAccel

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
