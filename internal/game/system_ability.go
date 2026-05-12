package game

import (
	"math"

	"github.com/mlange-42/ark/ecs"

	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/internal/item"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// activeLockTarget returns the active slot's resolved entity if the
// active slot is locked and the target is alive, else (zero, false).
func activeLockTarget(gw *GameWorld, lock *gamecomp.TargetLock) (mmokit.Entity, bool) {
	if lock == nil || lock.ActiveNetID == 0 {
		return mmokit.Entity{}, false
	}
	for _, s := range lock.Slots {
		if s.TargetNetID == lock.ActiveNetID && s.Locked {
			e := mmokit.EntityByNetID(gw.stage, s.TargetNetID)
			if e.Alive() {
				return e, true
			}
			return mmokit.Entity{}, false
		}
	}
	return mmokit.Entity{}, false
}

type abilityBundle struct {
	Input     *gamecomp.PlayerInput
	Lock      *gamecomp.TargetLock
	Abilities *gamecomp.AbilitySet
	Equip     *gamecomp.Equipment
}

type abilityAction struct {
	caster      ecs.Entity
	casterNetID uint32
	slot        uint8
	params      *item.AbilityParams
	abilities   *gamecomp.AbilitySet
}

// AbilitySystem processes ability casts using equipment-driven ability parameters.
type AbilitySystem struct {
	mmokit.SystemBase
	gw       *GameWorld
	entities mmokit.Query[abilityBundle]
	deferred []abilityAction
}

func (s *AbilitySystem) Init() {
	s.gw = mmokit.State[GameWorld](s.Stage())
	s.deferred = make([]abilityAction, 0, 16)
}

func (s *AbilitySystem) Update(dt float32) {
	gw := s.gw

	s.deferred = s.deferred[:0]

	for entity, b := range s.entities.Iter {
		input, lock, abilities, equip := b.Input, b.Lock, b.Abilities, b.Equip

		// Tick down all cooldowns
		for i := range abilities.Cooldowns {
			if abilities.Cooldowns[i] > 0 {
				abilities.Cooldowns[i] -= dt
			}
		}

		if input.AbilityCast == 0 {
			continue
		}

		casterE := mmokit.EntityFromECS(gw.stage, entity)
		casterNetID := casterE.NetID()

		for slot := range uint8(gamecomp.AbilityCount) {
			if input.AbilityCast&(1<<slot) == 0 {
				continue
			}

			// Check cooldown
			if abilities.Cooldowns[slot] > 0 {
				continue
			}

			// Resolve ability params from equipment
			params := resolveAbilityParams(equip, slot)
			if params == nil {
				continue // no equipment in this slot
			}

			// Targeted abilities (slots 0-3 = Q/W/E/R) require lock and range check.
			// Mining beam is a toggle — skip lock check here so deactivation always works;
			// activation validates the target inside executeAbility.
			isMiningToggle := params.Type == item.AbilityTypeMiningBeam
			if slot <= gamecomp.AbilityR && !isMiningToggle {
				target, ok := activeLockTarget(gw, lock)
				if !ok {
					continue
				}
				if params.Range > 0 && !s.inRange(entity, target.Handle(), params.Range) {
					continue
				}
			}

			s.deferred = append(s.deferred, abilityAction{
				caster:      entity,
				casterNetID: casterNetID,
				slot:        slot,
				params:      params,
				abilities:   abilities,
			})
		}

		input.AbilityCast = 0
	}

	for _, action := range s.deferred {
		if s.executeAbility(action) {
			action.abilities.Cooldowns[action.slot] = action.params.Cooldown
		}
	}
}

// resolveAbilityParams looks up the ability parameters for a given slot from the entity's equipment.
func resolveAbilityParams(equip *gamecomp.Equipment, slot uint8) *item.AbilityParams {
	equipSlot, isPrimary := item.AbilitySlotToEquipSlot(slot)

	var itemID uint32
	switch equipSlot {
	case item.SlotWeapon1:
		itemID = equip.Weapon1
	case item.SlotWeapon2:
		itemID = equip.Weapon2
	case item.SlotShield:
		itemID = equip.Shield
	case item.SlotThruster:
		itemID = equip.Thruster
	default:
		return nil
	}

	if itemID == 0 {
		return nil
	}

	def := item.Get(itemID)
	if def == nil || def.Equip == nil {
		return nil
	}

	if isPrimary {
		return &def.Equip.Primary
	}
	if def.Equip.Secondary != nil {
		return def.Equip.Secondary
	}
	return nil
}

func (s *AbilitySystem) executeAbility(action abilityAction) bool {
	gw := s.gw
	entity := action.caster
	casterE := mmokit.EntityFromECS(gw.stage, entity)

	if !casterE.Alive() {
		return false
	}

	lock := mmokit.Get[gamecomp.TargetLock](casterE)
	params := action.params

	fired := true

	switch params.Type {
	// --- Hitscan damage abilities ---
	case item.AbilityTypePulseLaser, item.AbilityTypePulseBarrage,
		item.AbilityTypeRailShot, item.AbilityTypeIonOverload, item.AbilityTypePlasmaBolt:
		target, ok := activeLockTarget(gw, lock)
		if ok {
			caster := mmokit.EntityByNetID(gw.stage, action.casterNetID)
			gw.Damage(caster, target, params.Damage, 0, action.slot, uint8(params.Type))
			gw.eng.Log.Log(CatCombatAbility, "ability %s: %d -> %d dmg=%.0f",
				params.Name, action.casterNetID, target.NetID(), params.Damage)
		}

	// --- Hitscan + bonus vs unshielded ---
	case item.AbilityTypePiercingRound, item.AbilityTypePlasmaTorpedo:
		target, ok := activeLockTarget(gw, lock)
		if ok {
			caster := mmokit.EntityByNetID(gw.stage, action.casterNetID)
			gw.Damage(caster, target, params.Damage, params.BonusDamage, action.slot, uint8(params.Type))
			gw.eng.Log.Log(CatCombatAbility, "ability %s: %d -> %d dmg=%.0f",
				params.Name, action.casterNetID, target.NetID(), params.Damage)
		}

	// --- DoT debuff ---
	case item.AbilityTypeIonBurn:
		target, ok := activeLockTarget(gw, lock)
		if ok {
			caster := mmokit.EntityByNetID(gw.stage, action.casterNetID)
			gw.ApplyStatus(caster, target, gamecomp.StatusIonBurn,
				params.DotDuration, params.DotDPS, action.slot, uint8(params.Type))
			gw.eng.Log.Log(CatCombatAbility, "ability %s: %d -> %d (%.1f dps for %.1fs)",
				params.Name, action.casterNetID, target.NetID(), params.DotDPS, params.DotDuration)
		}

	// --- Shield restore + Fortified buff ---
	case item.AbilityTypeEmergencyShield, item.AbilityTypeHardenedShield:
		regenPerSec := params.ShieldRestore / params.BuffDuration
		gw.ApplyStatus(casterE, casterE, gamecomp.StatusShieldRegen,
			params.BuffDuration, regenPerSec, action.slot, uint8(params.Type))
		gw.ApplyStatus(casterE, casterE, gamecomp.StatusFortified,
			params.BuffDuration, params.DmgReduction, action.slot, uint8(params.Type))
		gw.eng.Log.Log(CatCombatAbility, "ability %s: %d shield regen +%.1f/s for %.1fs",
			params.Name, action.casterNetID, regenPerSec, params.BuffDuration)

	// --- Speed boost ---
	case item.AbilityTypeAfterburner, item.AbilityTypeMicroWarp:
		gw.ApplyStatus(casterE, casterE, gamecomp.StatusAfterburner,
			params.BoostDuration, params.SpeedMult, action.slot, uint8(params.Type))
		gw.eng.Log.Log(CatCombatAbility, "ability %s: %d speed x%.1f for %.1fs",
			params.Name, action.casterNetID, params.SpeedMult, params.BoostDuration)

	// --- Mining beam toggle ---
	case item.AbilityTypeMiningBeam:
		laser := mmokit.Get[gamecomp.MiningLaser](casterE)
		if laser == nil {
			fired = false
			break
		}
		beamIdx := s.slotToBeamIndex(action.slot)

		if laser.Beams[beamIdx].Active {
			// Toggle off
			laser.Beams[beamIdx].Active = false
			gw.eng.Log.Log(CatEconomyMining, "mining beam off: %d beam=%d", action.casterNetID, beamIdx)
		} else {
			// Toggle on — require lock and validate target is minable
			lockTarget, ok := activeLockTarget(gw, lock)
			if !ok || !mmokit.Has[gamecomp.Minable](lockTarget) {
				fired = false
				break
			}
			laser.Beams[beamIdx].Active = true
			laser.Target = lockTarget.Handle()
			gw.eng.Log.Log(CatEconomyMining, "mining beam on: %d beam=%d target=%d",
				action.casterNetID, beamIdx, lockTarget.NetID())
		}
		// Sync replicated ActiveMining immediately so clients see the toggle
		// on the same tick, without waiting for the next MiningSystem pass.
		gw.syncActiveMining(casterE, laser)

		// Press-pulse VFX broadcast (Plan G restoration). The handler is a
		// no-op; the framework auto-broadcast IS the effect.
		casterE.Send(&BeamToggle{
			Caster: casterE,
			Beam:   uint8(beamIdx),
			Active: laser.Beams[beamIdx].Active,
		})

	// --- Extract pulse (mining burst) ---
	case item.AbilityTypeExtractPulse:
		laser := mmokit.Get[gamecomp.MiningLaser](casterE)
		inv := mmokit.Get[gamecomp.Inventory](casterE)
		if laser == nil || inv == nil {
			fired = false
			break
		}
		beamIdx := s.slotToBeamIndex(action.slot)
		beam := &laser.Beams[beamIdx]

		// Require active mining beam
		laserTarget := mmokit.EntityFromECS(gw.stage, laser.Target)
		if !beam.Active || !laserTarget.Alive() {
			fired = false
			break
		}
		minable := mmokit.Get[gamecomp.Minable](laserTarget)
		if minable == nil {
			fired = false
			break
		}
		// Range check
		if !s.inRange(entity, laser.Target, beam.Range) {
			fired = false
			break
		}
		if minable.Remaining <= 0 {
			fired = false
			break
		}
		if inv.RemainingMass() <= 0 {
			fired = false
			break
		}

		amount := params.MiningYield
		if amount > minable.Remaining {
			amount = minable.Remaining
		}
		whole := int32(amount)
		// Extract the last fractional unit so asteroids don't get stuck near zero
		if whole <= 0 && minable.Remaining > 0 && minable.Remaining < 1.0 {
			whole = 1
		}
		if whole <= 0 {
			break
		}
		itemID := minable.ItemID
		added := inv.AddItem(itemID, whole)

		// Resolve the asteroid target as an mmokit.Entity (NetID-based,
		// cell-aware). When local, target.Send dispatches synchronously and
		// the handler decrements Minable.Remaining + marks for removal.
		// When the target is a replica, Send routes the action to the
		// authoritative cell; we still decrement the local replica copy
		// for immediate caster-side visual feedback.
		asteroidNetID := laserTarget.NetID()
		asteroid := mmokit.EntityByNetID(gw.stage, asteroidNetID)
		caster := mmokit.EntityByNetID(gw.stage, action.casterNetID)

		if !asteroid.Local() {
			// Replica: optimistic local decrement; handler runs on the
			// authoritative cell and applies the canonical mutation there.
			minable.Remaining -= float32(added)
		}

		gw.MineExtract(caster, asteroid, uint8(beamIdx), float32(added))

		gw.eng.Log.Log(CatEconomyMining, "extract pulse: %d beam=%d amount=%d remaining=%.1f",
			action.casterNetID, beamIdx, added, minable.Remaining)
	}

	// Self-buffs (EmergencyShield, HardenedShield, Afterburner, MicroWarp)
	// currently don't flow through a typed Send, so the framework
	// auto-broadcast (Plan F Phase 2) does not fire for them. Migrating
	// them to use ApplyStatus(caster, caster, ...) is follow-up work —
	// see Plan F notes. MiningBeam toggle now broadcasts via BeamToggle
	// (Plan G).
	return fired
}

// slotToBeamIndex maps an ability slot to a mining beam index.
// Slots 0-1 (Weapon1 Q/W) → beam 0, slots 2-3 (Weapon2 E/R) → beam 1.
func (s *AbilitySystem) slotToBeamIndex(slot uint8) int {
	if slot <= gamecomp.AbilityW {
		return 0
	}
	return 1
}

func (s *AbilitySystem) inRange(caster, target ecs.Entity, abilityRange float32) bool {
	gw := s.gw
	casterE := mmokit.EntityFromECS(gw.stage, caster)
	targetE := mmokit.EntityFromECS(gw.stage, target)
	casterPos := mmokit.Get[mmokit.Position](casterE)
	targetPos := mmokit.Get[mmokit.Position](targetE)
	if casterPos == nil || targetPos == nil {
		return false
	}
	dx := targetPos.X - casterPos.X
	dy := targetPos.Y - casterPos.Y
	dist := float32(math.Sqrt(float64(dx*dx + dy*dy)))
	return dist <= abilityRange
}
