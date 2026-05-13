package game

import (
	"math"

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
	caster      mmokit.EntityHandle
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
	// Channeling is not part of any kind bundle — prime it so Has/Get
	// inside the tickChannels query iteration doesn't trigger ark's
	// locked-world first-touch registration panic.
	mmokit.Prime[gamecomp.Channeling](s.Stage())
}

func (s *AbilitySystem) Update(dt float32) {
	gw := s.gw

	// Resolve in-flight SustainedBeam channels first: per-tick damage,
	// arc/range validation, channel-end cleanup. Runs before the main
	// dispatch loop so a freshly-started channel (queued via Commands
	// on the previous tick) becomes visible here as soon as it's flushed.
	s.tickChannels(dt)

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

	// --- Projectile weapons (PVE v2) ---
	case item.AbilityTypePlasmaShot:
		s.fireProjectile(casterE, params, gamecomp.ProjectileTypePlasma)
		gw.eng.Log.Log(CatCombatAbility, "ability %s: %d fired projectile dmg=%.0f speed=%.0f",
			params.Name, action.casterNetID, params.Damage, params.ProjectileSpeed)

	case item.AbilityTypeMortarShell:
		s.fireProjectile(casterE, params, gamecomp.ProjectileTypeMortar)
		gw.eng.Log.Log(CatCombatAbility, "ability %s: %d fired mortar dmg=%.0f splash=%.0f",
			params.Name, action.casterNetID, params.Damage, params.SplashRadius)

	// --- Sustained beam channel (PVE v2, Task 21) ---
	case item.AbilityTypeSustainedBeam:
		// Pressing the key while already channeling is a no-op — don't
		// reset the channel and don't refund the press by setting a
		// cooldown. The channel ends on its own when RemainingTime
		// hits zero, the target leaves range/arc, or dies.
		if mmokit.Has[gamecomp.Channeling](casterE) {
			fired = false
			break
		}
		target, ok := activeLockTarget(gw, lock)
		if !ok {
			fired = false
			break
		}
		mmokit.AddComponent(s.Commands(), casterE, gamecomp.Channeling{
			SlotID:        action.slot,
			RemainingTime: params.ChannelDuration,
			NextTickIn:    0, // fire first tick immediately
			TargetNetID:   target.NetID(),
		})
		gw.eng.Log.Log(CatCombatAbility, "ability %s: %d START -> %d duration=%.1fs tickrate=%.1fHz",
			params.Name, action.casterNetID, target.NetID(),
			params.ChannelDuration, params.ChannelTickRate)
		// Cooldown is applied on channel END (in tickChannels), not on
		// press — return false so Update skips the per-slot cooldown
		// assignment.
		fired = false

	// --- Homing missile — requires active target lock (Task 20) ---
	case item.AbilityTypeHomingMissile:
		if _, ok := activeLockTarget(gw, lock); !ok {
			// No active lock — refuse cast. Returning false skips the
			// cooldown assignment in Update, so the ability stays ready.
			gw.eng.Log.Log(CatCombatAbility, "ability %s: %d cancelled (no lock)",
				params.Name, action.casterNetID)
			fired = false
			break
		}
		s.fireProjectile(casterE, params, gamecomp.ProjectileTypeMissile)
		gw.eng.Log.Log(CatCombatAbility, "ability %s: %d fired homing missile dmg=%.0f",
			params.Name, action.casterNetID, params.Damage)

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

// fireProjectile creates a Projectile entity travelling in the caster's
// facing direction (or toward the active target if a lock is present).
// projType is one of gamecomp.ProjectileType* — purely a visual variant
// hint for the client renderer.
//
// Called from the deferred-execution stage of AbilitySystem.Update, so
// the world lock from the query iteration has been released and it is
// safe to spawn entities directly.
func (s *AbilitySystem) fireProjectile(
	caster mmokit.Entity, params *item.AbilityParams, projType uint8,
) {
	gw := s.gw
	casterPos := mmokit.Get[mmokit.Position](caster)
	casterRot := mmokit.Get[mmokit.Rotation](caster)
	if casterPos == nil || casterRot == nil {
		return
	}

	// Default direction: muzzle-forward.
	dirX := float32(math.Cos(float64(casterRot.Angle)))
	dirY := float32(math.Sin(float64(casterRot.Angle)))
	var targetNetID uint32

	// If a lock is active, aim at the target's current position.
	if lock := mmokit.Get[gamecomp.TargetLock](caster); lock != nil {
		if target, ok := activeLockTarget(gw, lock); ok {
			if tpos := mmokit.Get[mmokit.Position](target); tpos != nil {
				dx := tpos.X - casterPos.X
				dy := tpos.Y - casterPos.Y
				norm := float32(math.Sqrt(float64(dx*dx + dy*dy)))
				if norm > 1e-3 {
					dirX, dirY = dx/norm, dy/norm
				}
				if projType == gamecomp.ProjectileTypeMissile {
					targetNetID = target.NetID()
				}
			}
		}
	}

	lifetime := float32(0)
	if params.ProjectileSpeed > 0 {
		lifetime = params.Range / params.ProjectileSpeed
	}

	spec := gamecomp.ProjectileSpec{
		OwnerNetID:   caster.NetID(),
		TargetNetID:  targetNetID,
		Damage:       params.Damage,
		SplashRadius: params.SplashRadius,
		SplashDamage: params.SplashDamage,
		MaxTurnRate:  params.HomingMaxTurnRate,
		Type:         projType,
	}
	gw.SpawnProjectile(
		casterPos.X, casterPos.Y,
		dirX*params.ProjectileSpeed, dirY*params.ProjectileSpeed,
		lifetime, spec,
	)
}

// slotToBeamIndex maps an ability slot to a mining beam index.
// Slots 0-1 (Weapon1 Q/W) → beam 0, slots 2-3 (Weapon2 E/R) → beam 1.
func (s *AbilitySystem) slotToBeamIndex(slot uint8) int {
	if slot <= gamecomp.AbilityW {
		return 0
	}
	return 1
}

// tickChannels resolves all in-flight Channeling components for the
// stage. Each tick we decrement the channel timers, validate the target
// (alive, in range, inside ±BeamHalfArcRad of caster facing), and apply
// per-tick damage at params.ChannelTickRate. On channel end (timeout,
// target loss, target-out-of-arc, or target-out-of-range) the Channeling
// component is removed and the post-channel cooldown is set.
//
// ECS structural mutations (component removal) are queued through
// Commands so they apply after the iteration's world lock releases.
// Per-tick damage is dispatched via gw.Damage which mutates Health /
// Shield component values (locked-world-safe — no structural change).
func (s *AbilitySystem) tickChannels(dt float32) {
	gw := s.gw

	type endCandidate struct {
		caster mmokit.Entity
		slot   uint8
	}
	var ends []endCandidate

	mmokit.ForEach1[gamecomp.Channeling](s.Stage(), func(caster mmokit.Entity, ch *gamecomp.Channeling) {
		if !caster.Alive() {
			ends = append(ends, endCandidate{caster, ch.SlotID})
			return
		}

		ch.RemainingTime -= dt
		ch.NextTickIn -= dt

		// Channel duration expired.
		if ch.RemainingTime <= 0 {
			ends = append(ends, endCandidate{caster, ch.SlotID})
			return
		}

		// Resolve current params from the caster's equipment — the
		// ChannelTickRate / BeamHalfArcRad / Range / Damage values all
		// live there. If the equipment is gone, end the channel.
		equip := mmokit.Get[gamecomp.Equipment](caster)
		if equip == nil {
			ends = append(ends, endCandidate{caster, ch.SlotID})
			return
		}
		params := resolveAbilityParams(equip, ch.SlotID)
		if params == nil || params.Type != item.AbilityTypeSustainedBeam {
			ends = append(ends, endCandidate{caster, ch.SlotID})
			return
		}

		target := mmokit.EntityByNetID(gw.stage, ch.TargetNetID)
		if !target.Alive() {
			ends = append(ends, endCandidate{caster, ch.SlotID})
			return
		}

		casterPos := mmokit.Get[mmokit.Position](caster)
		casterRot := mmokit.Get[mmokit.Rotation](caster)
		tpos := mmokit.Get[mmokit.Position](target)
		if casterPos == nil || casterRot == nil || tpos == nil {
			ends = append(ends, endCandidate{caster, ch.SlotID})
			return
		}

		dx, dy := tpos.X-casterPos.X, tpos.Y-casterPos.Y
		dist := float32(math.Sqrt(float64(dx*dx + dy*dy)))
		if params.Range > 0 && dist > params.Range {
			ends = append(ends, endCandidate{caster, ch.SlotID})
			return
		}

		desired := float32(math.Atan2(float64(dy), float64(dx)))
		if angleDelta(casterRot.Angle, desired) > params.BeamHalfArcRad {
			ends = append(ends, endCandidate{caster, ch.SlotID})
			return
		}

		// Per-tick damage gate.
		if ch.NextTickIn <= 0 && params.ChannelTickRate > 0 {
			gw.Damage(caster, target, params.Damage, 0, ch.SlotID, uint8(params.Type))
			ch.NextTickIn = 1.0 / params.ChannelTickRate
		}
	})

	// End all flagged channels OUTSIDE the iteration so the structural
	// mutations (RemoveComponent) and cooldown writes happen on a
	// non-locked world.
	for _, end := range ends {
		caster := end.caster
		if caster.Alive() {
			if ab := mmokit.Get[gamecomp.AbilitySet](caster); ab != nil {
				if equip := mmokit.Get[gamecomp.Equipment](caster); equip != nil {
					if p := resolveAbilityParams(equip, end.slot); p != nil {
						ab.Cooldowns[end.slot] = p.Cooldown
					}
				}
			}
			gw.eng.Log.Log(CatCombatAbility, "ability SustainedBeam: %d END slot=%d",
				caster.NetID(), end.slot)
		}
		mmokit.RemoveComponent[gamecomp.Channeling](s.Commands(), caster)
	}
}

func (s *AbilitySystem) inRange(caster, target mmokit.EntityHandle, abilityRange float32) bool {
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
