package game

import (
	"maps"

	"github.com/mlange-42/ark/ecs"

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

// GameWorld holds all game-specific state for a single cell. It is registered
// per-stage via mmokit.AddState[GameWorld] and looked up via
// mmokit.State[GameWorld](stage). The cell's Stage is held in the unexported
// `stage` field; external packages reach it through the Stage() accessor.
type GameWorld struct {
	stage *mmokit.Stage
	eng   *mmokit.Engine // cached for convenience (avoids gw.Engine().ECS everywhere)

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
	// Convenience over the cell's CellID — game-side code typically only
	// needs the X/Y of the root cell.
	RootCell mmokit.CellCoord

	// OnPostSpawn is called after a player spawns (for topology sends, etc.)
	OnPostSpawn func(connID uint32)

	// poiRosters maps poiNetID → list of currently-alive roster member
	// netIDs. Updated on POI spawn + respawn; mutated by POISystem on
	// NPC death detection.
	poiRosters map[uint32][]uint32

	// autoRespawnAt maps connID → engine tick at which a dead player
	// will be auto-respawned. The client's Respawn input cannot reach
	// the handler (the typed-input dispatcher drops frames when the
	// player entity is dead — there's no session-aware path yet), so
	// the server holds a death-screen timer per dead session and
	// enqueues a PendingRespawn when it elapses.
	autoRespawnAt map[uint32]uint32
}

// GetStage returns the per-cell Stage this GameWorld is wired to. External
// packages (e.g. internal/game/commands) use this in place of the previous
// embedded-pointer access. Named GetStage rather than Stage() to avoid
// shadowing the existing field — game code in the same package reads via
// gw.stage directly.
func (gw *GameWorld) GetStage() *mmokit.Stage { return gw.stage }

// PoiRosters returns the live roster netIDs for a POI. Used by admin
// commands; returns nil if the POI has no tracked roster.
func (gw *GameWorld) PoiRosters(poiNetID uint32) []uint32 {
	return gw.poiRosters[poiNetID]
}

// POICooldownSec is the exported view of poiCooldownSec for cmdsys
// command handlers in internal/game/commands. Returns the configured
// cooldown (seconds) for POIs in this world's root cell — station cell
// uses the tutorial-friendly cooldown, every other cell uses the
// standard one.
func (gw *GameWorld) POICooldownSec() int32 { return poiCooldownSec(gw) }

// Engine returns the engine for this cell.
func (gw *GameWorld) Engine() *mmokit.Engine { return gw.eng }

// MarkForRemoval forwards to the Stage's engine.
func (gw *GameWorld) MarkForRemoval(e ecs.Entity) {
	gw.stage.MarkForRemoval(e)
}

// SavePlayerState persists the current entity state to the player database.
func (gw *GameWorld) SavePlayerState(s *mmokit.PlayerSession) {
	username := s.Username
	if username == "" {
		return
	}
	entity := mmokit.EntityFromECS(gw.stage, s.Entity)
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
	target := mmokit.EntityFromECS(gw.stage, laser.Target)
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

	// TargetLock tuning — Range is a pure config read with no equipment
	// modifier today. LockOnTime is consumed per-slot at LockTarget time;
	// live locks keep their captured value.
	if tl := mmokit.Get[gamecomp.TargetLock](entity); tl != nil {
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
