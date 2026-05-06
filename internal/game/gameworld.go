package game

import (
	"maps"

	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/internal/item"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

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

// (PendingBankRequest deleted — Plan 2 Phase 3 migrated BankRequest to
// a typed-op served synchronously on the cell loop via
// Process.DispatchCellRoutedOp.)

// PendingEquipRequest records a request to equip or unequip an item.
// TargetBank only matters while docked: when true, equip pulls from bank
// and the swapped-out item returns to bank; unequip deposits to bank
// instead of cargo.
type PendingEquipRequest struct {
	ConnID     uint32
	ItemID     uint32 // 0 = unequip
	Slot       item.EquipSlot
	TargetBank bool
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

// DockingProgress tracks a player's in-progress docking sequence.
type DockingProgress struct {
	Remaining    float32 // seconds left
	StationX     float32 // target station position
	StationY     float32
	StationNetID uint32 // for client VFX
}

// GameWorld holds all game-specific state and embeds Stage for multi-node support.
type GameWorld struct {
	*mmokit.Stage
	eng *mmokit.Engine // cached for convenience (avoids gw.Engine().ECS everywhere)

	Spatial *mmokit.HashGrid
	// Config is a shared pointer across all GameWorlds in the coordinator —
	// one source of truth. Runtime `config set` mutations propagate to every
	// node immediately because they all see the same struct.
	Config     *GameConfig
	flushTicks uint32 // cached: PersistFlushInterval * TickRate

	// Ticks between forced full-state sends (safety net for diffing bugs)
	FullRefreshInterval uint32

	// Queue holds all per-tick pending work (replaces individual Pending* slices).
	Queue *mmokit.TickQueue

	// Players tracks all player-connection state via the engine's PlayerManager.
	Players *mmokit.PlayerManager

	// dockingStates tracks in-flight DockingProgress keyed by Username so the
	// timer survives a mid-dock reconnect (ConnID changes on reconnect, the
	// session's PriorState is restored to StateDocking, and we need to find
	// the same in-flight DockingProgress the new ConnID inherits). Cleared
	// when docking completes or the entity is gone.
	dockingStates map[string]*DockingProgress

	// Persistent player database (keyed by username)
	PlayerDB *PlayerRepo

	// Console reference for dynamic completions
	console *mmokit.Console

	// PlayerSessions for the operation router (thread-safe, set from game loop)
	PlayerSessions *mmokit.PlayerSessions

	// RootCell identifies which root cell this node owns (depth-0 coordinates).
	// Distinct name from the embedded Stage.Cell() method, which returns a
	// CellID with depth — this field is kept for game-side convenience that
	// only needs the X/Y of the root cell. Renaming to "RootCell" avoids
	// shadowing Stage.Cell(), which would silently break the
	// pkg/universe BoundaryWorld interface check and disable boundary
	// transfers entirely.
	RootCell mmokit.CellCoord

	// OnPostSpawn is called after a player spawns (for topology sends, etc.)
	OnPostSpawn func(connID uint32)
}

// ServerEvents returns the typed server-event registry declared in main.go's
// cfg.Protocol. Used by every site that emits a server event.
func (gw *GameWorld) ServerEvents() *mmokit.ServerEvents {
	return mmokit.ServerEventsOf(gw.Process())
}

// Ensure GameWorld implements mmokit.GameWorld at compile time.
var _ mmokit.GameWorld = (*GameWorld)(nil)

// Ensure GameWorld also satisfies mmokit.BoundaryWorld. A field named `Cell`
// on GameWorld would shadow the embedded Stage.Cell() method and
// silently disable all cross-cell entity transfers — this assertion catches
// that class of bug at compile time instead of silently at runtime.
var _ mmokit.BoundaryWorld = (*GameWorld)(nil)

// SavePlayerState persists the current entity state to the player database.
func (gw *GameWorld) SavePlayerState(s *mmokit.PlayerSession) {
	username := s.Username
	if username == "" {
		return
	}
	entity := mmokit.EntityFromECS(gw.Stage, s.Entity)
	if !entity.Alive() {
		return
	}
	pdata := gw.PlayerDB.GetOrCreate(username)
	if pos := mmokit.Get[mmokit.Position](entity); pos != nil {
		pdata.X = pos.X
		pdata.Y = pos.Y
	}
	if sec := mmokit.Get[mmokit.CellCoord](entity); sec != nil {
		pdata.CellX = sec.CellX
		pdata.CellY = sec.CellY
	}
	if inv := mmokit.Get[gamecomp.Inventory](entity); inv != nil {
		// Deep copy the items map
		if len(inv.Items) > 0 {
			pdata.Cargo = make(map[uint32]int32, len(inv.Items))
			maps.Copy(pdata.Cargo, inv.Items)
		} else {
			pdata.Cargo = nil
		}
	}
	if eq := mmokit.Get[gamecomp.Equipment](entity); eq != nil {
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

// syncActiveMining updates the replicated ActiveMining component from the
// authoritative MiningLaser state on the same entity. Call whenever beam
// activation or target may have changed so clients see the toggle immediately.
// Logs on state transitions only.
func (gw *GameWorld) syncActiveMining(entity mmokit.Entity, laser *gamecomp.MiningLaser) {
	active := mmokit.Get[gamecomp.ActiveMining](entity)
	if active == nil {
		return
	}
	newBeam0 := laser.Beams[0].Active
	newBeam1 := laser.Beams[1].Active
	var newTarget uint32
	target := mmokit.EntityFromECS(gw.Stage, laser.Target)
	if (newBeam0 || newBeam1) && target.Alive() {
		newTarget = target.NetID()
	}
	if active.Beam0Active != newBeam0 || active.Beam1Active != newBeam1 || active.MiningTargetNetID != newTarget {
		gw.eng.Log.Log(CatEconomyMining, "active-mining sync: player=%d beams=[%v,%v] target=%d",
			entity.NetID(), newBeam0, newBeam1, newTarget)
	}
	active.Beam0Active = newBeam0
	active.Beam1Active = newBeam1
	active.MiningTargetNetID = newTarget
}

// ApplyEquipmentStats recalculates shield and movement stats from equipped items.
// Call after any equipment change or at spawn.
func (gw *GameWorld) ApplyEquipmentStats(entity mmokit.Entity) {
	eq := mmokit.Get[gamecomp.Equipment](entity)
	if eq == nil {
		return
	}

	// Shield stats from shield generator
	if shield := mmokit.Get[gamecomp.Shield](entity); shield != nil {
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
	if col := mmokit.Get[mmokit.Collider](entity); col != nil {
		col.Width = gw.Config.ShipWidth
		col.Height = gw.Config.ShipHeight
		col.Radius = boundingRadius(gw.Config.ShipWidth, gw.Config.ShipHeight)
	}

	// Inventory capacity. New cap can be below current cargo mass — that's
	// accepted; the next deposit will be rejected until players clear space.
	if inv := mmokit.Get[gamecomp.Inventory](entity); inv != nil {
		inv.MaxMass = gw.Config.MaxCargo
	}

	// TargetLock tuning — both fields are pure config reads with no
	// equipment modifier today.
	if tl := mmokit.Get[gamecomp.TargetLock](entity); tl != nil {
		tl.LockTime = gw.Config.LockOnTime
		tl.Range = gw.Config.LockOnRange
	}

	// Movement stats from thruster. All three are re-synced from config
	// each call so that runtime `config set` changes propagate through the
	// game-side `config`-command OnChanged hook (which calls this function
	// on every active ship).
	if sc := mmokit.Get[gamecomp.ShipControl](entity); sc != nil {
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
	if laser := mmokit.Get[gamecomp.MiningLaser](entity); laser != nil {
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
func (gw *GameWorld) AbilityCooldownForSlot(entity mmokit.Entity, slot uint8) float32 {
	eq := mmokit.Get[gamecomp.Equipment](entity)
	if eq == nil {
		return 0
	}

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
