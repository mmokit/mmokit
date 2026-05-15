package game

import (
	"math"

	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/internal/item"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

type abilityBundle struct {
	Input     *gamecomp.PlayerInput
	Abilities *gamecomp.AbilitySet
	Equip     *gamecomp.Equipment
}

type abilityAction struct {
	caster      mmokit.EntityHandle
	casterNetID uint32
	slot        uint8
	params      *item.AbilityParams
	abilities   *gamecomp.AbilitySet
	aimX        float32 // PVE v3: cursor world coords from CastAbility wire msg
	aimY        float32
}

// AbilitySystem processes ability casts using equipment-driven ability parameters.
type AbilitySystem struct {
	mmokit.SystemBase
	gw               *GameWorld
	entities         mmokit.Query[abilityBundle]
	deferred         []abilityAction
	nearbyChannel    []mmokit.SpatialEntry // reusable scratch for tickChannels hitscan
	cursorPickNearby []mmokit.SpatialEntry // reusable scratch for dispatchCursorPick
}

func (s *AbilitySystem) Init() {
	s.gw = mmokit.State[GameWorld](s.Stage())
	s.deferred = make([]abilityAction, 0, 16)
	s.nearbyChannel = make([]mmokit.SpatialEntry, 0, 32)
	s.cursorPickNearby = make([]mmokit.SpatialEntry, 0, 32)
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
		input, abilities, equip := b.Input, b.Abilities, b.Equip

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
			if slot <= gamecomp.AbilityR && !isMiningToggle && params.Mode == item.TargetingLockOn {
				sel := mmokit.Get[gamecomp.Selection](casterE)
				if sel == nil || sel.EntityNetID == 0 {
					continue
				}
				target := mmokit.EntityByNetID(gw.stage, sel.EntityNetID)
				if !target.Alive() {
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
				aimX:        input.LastCastAimX,
				aimY:        input.LastCastAimY,
			})
		}

		input.AbilityCast = 0
		input.LastCastAimX = 0
		input.LastCastAimY = 0
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
	casterE := mmokit.EntityFromECS(gw.stage, action.caster)
	if !casterE.Alive() {
		return false
	}
	params := action.params

	switch params.Mode {
	case item.TargetingSkillshotLine:
		return s.dispatchSkillshotLine(action, casterE)
	case item.TargetingSkillshotGround:
		return s.dispatchSkillshotGround(action, casterE)
	case item.TargetingSkillshotChannel:
		return s.dispatchSkillshotChannel(action, casterE)
	case item.TargetingCursorPick:
		return s.dispatchCursorPick(action, casterE)
	}

	// TargetingSelf falls through to the existing per-Type dispatch —
	// these abilities have bespoke handlers (shield buffs, thruster
	// effects, mining beam toggle) that are too varied for a generic
	// dispatch. TargetingLockOn is now an alias for selection-based
	// hitscan in the lock-on gate above; the dispatchByType lock-on
	// hitscan cases were retired with the TargetLock tear-out.
	return s.dispatchByType(action, casterE)
}

// dispatchByType handles TargetingSelf abilities (and the mining-beam
// toggle) via a per-AbilityType switch. Skillshot + CursorPick + LockOn
// modes are routed by executeAbility before reaching this function.
func (s *AbilitySystem) dispatchByType(action abilityAction, casterE mmokit.Entity) bool {
	gw := s.gw
	entity := action.caster
	params := action.params

	fired := true

	switch params.Type {
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
			// Toggle on — require selection and validate target is minable
			sel := mmokit.Get[gamecomp.Selection](casterE)
			if sel == nil || sel.EntityNetID == 0 {
				fired = false
				break
			}
			selTarget := mmokit.EntityByNetID(gw.stage, sel.EntityNetID)
			if !selTarget.Alive() || !mmokit.Has[gamecomp.Minable](selTarget) {
				fired = false
				break
			}
			laser.Beams[beamIdx].Active = true
			laser.Target = selTarget.Handle()
			gw.eng.Log.Log(CatEconomyMining, "mining beam on: %d beam=%d target=%d",
				action.casterNetID, beamIdx, selTarget.NetID())
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

// dispatchSkillshotLine handles TargetingSkillshotLine abilities — a
// projectile fired in the direction from caster toward the cursor
// position carried on the cast action (aimX/aimY). Pierce count and
// projectile visual type vary per AbilityType; PulseBarrage fans 3
// sub-projectiles in a ±10° cone.
func (s *AbilitySystem) dispatchSkillshotLine(action abilityAction, casterE mmokit.Entity) bool {
	gw := s.gw
	params := action.params

	casterPos := mmokit.Get[mmokit.Position](casterE)
	if casterPos == nil {
		return false
	}
	dx := action.aimX - casterPos.X
	dy := action.aimY - casterPos.Y

	// Choose pierce count + visual variant per AbilityType. Each weapon
	// family gets its own ProjectileType so pulse/rail/ion/plasma read
	// distinctly on the client (color, size, trail).
	var pierceCount uint8
	var projType uint8 = gamecomp.ProjectileTypePlasma
	switch params.Type {
	case item.AbilityTypePulseLaser, item.AbilityTypePulseBarrage:
		projType = gamecomp.ProjectileTypePulse
	case item.AbilityTypeRailShot:
		pierceCount = 2
		projType = gamecomp.ProjectileTypeRail
	case item.AbilityTypePiercingRound:
		pierceCount = 99
		projType = gamecomp.ProjectileTypeRail
	case item.AbilityTypeIonOverload:
		projType = gamecomp.ProjectileTypeIon
	case item.AbilityTypePlasmaBolt, item.AbilityTypePlasmaShot:
		projType = gamecomp.ProjectileTypePlasma
	}

	if params.Type == item.AbilityTypePulseBarrage {
		// 3 sub-projectiles in a ±10° cone (~0.1745 rad).
		s.fireProjectile(casterE, params, projType, pierceCount, dx, dy)
		spread := float32(0.1745)
		cos, sin := float32(math.Cos(float64(spread))), float32(math.Sin(float64(spread)))
		dxL := cos*dx - sin*dy
		dyL := sin*dx + cos*dy
		dxR := cos*dx + sin*dy
		dyR := -sin*dx + cos*dy
		s.fireProjectile(casterE, params, projType, pierceCount, dxL, dyL)
		s.fireProjectile(casterE, params, projType, pierceCount, dxR, dyR)
	} else {
		s.fireProjectile(casterE, params, projType, pierceCount, dx, dy)
	}

	gw.eng.Log.Log(CatCombatAbility, "ability %s: %d skillshot fired aim=(%.0f,%.0f) pierce=%d",
		params.Name, action.casterNetID, action.aimX, action.aimY, pierceCount)
	return true
}

// dispatchSkillshotGround handles TargetingSkillshotGround abilities — an
// AoE marker dropped at the cursor position (aimX/aimY), clamped to the
// ability's Range. The marker lives for GroundCastDelay seconds before
// AoESystem resolves it as an explosion.
func (s *AbilitySystem) dispatchSkillshotGround(action abilityAction, casterE mmokit.Entity) bool {
	gw := s.gw
	params := action.params

	casterPos := mmokit.Get[mmokit.Position](casterE)
	if casterPos == nil {
		return false
	}

	// Clamp cursor to ability range.
	dx := action.aimX - casterPos.X
	dy := action.aimY - casterPos.Y
	dist := float32(math.Sqrt(float64(dx*dx + dy*dy)))
	aimX, aimY := action.aimX, action.aimY
	if dist > params.Range && dist > 0 {
		scale := params.Range / dist
		aimX = casterPos.X + dx*scale
		aimY = casterPos.Y + dy*scale
	}

	// GroundCastDelay: time the marker lives before exploding.
	delay := params.GroundCastDelay
	if delay <= 0 {
		delay = 0.6 // fallback default
	}

	px, py := aimX, aimY
	radius := params.SplashRadius
	damage := params.SplashDamage
	ownerNetID := casterE.NetID()
	mask := factionMaskFromOwner(gw, ownerNetID)
	abilityType := uint8(params.Type)

	s.Commands().Defer(func() {
		gw.SpawnAoEMarker(px, py, delay, radius, damage, ownerNetID, mask, abilityType)
	})

	gw.eng.Log.Log(CatCombatAbility, "ability %s: %d ground-cast at (%.0f,%.0f) r=%.0f delay=%.1fs",
		params.Name, action.casterNetID, px, py, radius, delay)
	return true
}

// dispatchSkillshotChannel handles TargetingSkillshotChannel abilities —
// a beam that tracks cursor direction (aimX/aimY) for the channel duration.
// Pressing the key while already channeling is a no-op (don't restart);
// the channel ends on timeout, caster death, or facing too far off-aim
// (see tickChannels). Returns false so Update skips the per-press
// cooldown assignment — cooldown is set on channel END in tickChannels.
func (s *AbilitySystem) dispatchSkillshotChannel(action abilityAction, casterE mmokit.Entity) bool {
	gw := s.gw
	params := action.params

	// Already channeling? No-op.
	if mmokit.Has[gamecomp.Channeling](casterE) {
		return false
	}

	mmokit.AddComponent(s.Commands(), casterE, gamecomp.Channeling{
		SlotID:        action.slot,
		RemainingTime: params.ChannelDuration,
		NextTickIn:    0, // fire first tick immediately
		AimX:          action.aimX,
		AimY:          action.aimY,
	})

	gw.eng.Log.Log(CatCombatAbility, "ability %s: %d channel START aim=(%.0f,%.0f) duration=%.1fs",
		params.Name, action.casterNetID, action.aimX, action.aimY, params.ChannelDuration)

	// Returning false keeps the cooldown at 0 — channel end (in tickChannels) sets it.
	return false
}

// dispatchCursorPick runs the per-ability effect on whichever enemy is
// nearest the cursor at fire time, within params.Range of the caster.
// No selection required. If nothing is under the cursor (within 5u of
// the aim point), returns false so the caller refunds the cooldown.
func (s *AbilitySystem) dispatchCursorPick(action abilityAction, casterE mmokit.Entity) bool {
	gw := s.gw
	params := action.params

	casterPos := mmokit.Get[mmokit.Position](casterE)
	if casterPos == nil {
		return false
	}
	ownerNetID := casterE.NetID()

	// Search radius around cursor: small, so the player is forced to
	// hover roughly over the target. 5u is generous enough to forgive
	// a slightly-off click, tight enough that empty-space clicks miss.
	const pickRadius float32 = 5.0
	s.cursorPickNearby = gw.Spatial.QueryRadius(
		action.aimX, action.aimY, pickRadius,
		s.cursorPickNearby[:0],
	)

	var best mmokit.Entity
	bestDist2 := float32(pickRadius * pickRadius)
	for _, entry := range s.cursorPickNearby {
		cand := mmokit.EntityFromECS(gw.stage, entry.Entity)
		if !cand.Alive() || cand.NetID() == ownerNetID {
			continue
		}
		// Damage-eligible: NPC enemies for now (no PvP).
		if !mmokit.Has[gamecomp.NPCAI](cand) {
			continue
		}
		cpos := mmokit.Get[mmokit.Position](cand)
		if cpos == nil {
			continue
		}
		// Range from caster (not from cursor) gates the ability.
		rx, ry := cpos.X-casterPos.X, cpos.Y-casterPos.Y
		if params.Range > 0 && rx*rx+ry*ry > params.Range*params.Range {
			continue
		}
		// Pick the one closest to the cursor.
		cx, cy := cpos.X-action.aimX, cpos.Y-action.aimY
		d2 := cx*cx + cy*cy
		if d2 < bestDist2 {
			bestDist2 = d2
			best = cand
		}
	}
	if !best.Alive() {
		gw.eng.Log.Log(CatCombatAbility, "ability %s: %d cursor-pick MISS aim=(%.0f,%.0f)",
			params.Name, action.casterNetID, action.aimX, action.aimY)
		return false // refund cooldown
	}

	targetNetID := best.NetID()
	gw.eng.Log.Log(CatCombatAbility, "ability %s: %d cursor-pick HIT target=%d",
		params.Name, action.casterNetID, targetNetID)

	// Per-ability effect — runs against the cursor-picked `best` target.
	switch params.Type {
	case item.AbilityTypeHomingMissile:
		bpos := mmokit.Get[mmokit.Position](best)
		if bpos == nil {
			return false
		}
		dx, dy := bpos.X-casterPos.X, bpos.Y-casterPos.Y
		s.fireProjectileToward(casterE, params, gamecomp.ProjectileTypeMissile, targetNetID, dx, dy)
		return true
	case item.AbilityTypePlasmaTorpedo:
		bpos := mmokit.Get[mmokit.Position](best)
		if bpos == nil {
			return false
		}
		dx, dy := bpos.X-casterPos.X, bpos.Y-casterPos.Y
		s.fireProjectileToward(casterE, params, gamecomp.ProjectileTypePlasma, targetNetID, dx, dy)
		return true
	case item.AbilityTypeIonBurn:
		// Apply DoT to the picked target. Replicates the old LockOn
		// IonBurn path verbatim (no hitscan damage — DoT only), just with
		// `best` as the target.
		gw.ApplyStatus(casterE, best, gamecomp.StatusIonBurn,
			params.DotDuration, params.DotDPS, action.slot, uint8(params.Type))
		return true
	}
	return false
}

// fireProjectileToward — same as fireProjectile but stamps an explicit
// TargetNetID so the projectile homes/tracks the cursor-pick target.
// fireProjectile (used by dispatchSkillshotLine) always fires straight;
// cursor-pick supplies the homing target directly.
func (s *AbilitySystem) fireProjectileToward(
	caster mmokit.Entity, params *item.AbilityParams,
	projType uint8, targetNetID uint32, dirX, dirY float32,
) {
	gw := s.gw
	casterPos := mmokit.Get[mmokit.Position](caster)
	if casterPos == nil {
		return
	}
	norm := float32(math.Sqrt(float64(dirX*dirX + dirY*dirY)))
	if norm < 1e-3 {
		return
	}
	dirX, dirY = dirX/norm, dirY/norm
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

// fireProjectile creates a Projectile entity travelling in the supplied
// (dirX, dirY) direction. The caller owns the targeting decision —
// dispatchSkillshotLine builds dx/dy from cursor aim.
//
// dirX/dirY do not need to be pre-normalized; this function normalizes
// them. If the vector is degenerate (e.g. cursor on top of ship), the
// caster's facing rotation is used as a fallback.
//
// projType is one of gamecomp.ProjectileType* — purely a visual variant
// hint for the client renderer.
//
// Called from the deferred-execution stage of AbilitySystem.Update, so
// the world lock from the query iteration has been released and it is
// safe to spawn entities directly.
func (s *AbilitySystem) fireProjectile(
	caster mmokit.Entity, params *item.AbilityParams, projType uint8,
	pierceCount uint8, dirX, dirY float32,
) {
	gw := s.gw
	casterPos := mmokit.Get[mmokit.Position](caster)
	if casterPos == nil {
		return
	}

	// Normalize the supplied aim vector; on degeneracy fall back to caster
	// facing so we never spawn a stationary projectile.
	norm := float32(math.Sqrt(float64(dirX*dirX + dirY*dirY)))
	if norm < 1e-3 {
		if rot := mmokit.Get[mmokit.Rotation](caster); rot != nil {
			dirX = float32(math.Cos(float64(rot.Angle)))
			dirY = float32(math.Sin(float64(rot.Angle)))
		} else {
			return // can't fire without a direction
		}
	} else {
		dirX, dirY = dirX/norm, dirY/norm
	}

	// fireProjectile is now only called from dispatchSkillshotLine — a
	// straight-line shot with no homing. HomingMissile / PlasmaTorpedo
	// (cursor-pick) take the fireProjectileToward path and stamp
	// TargetNetID explicitly; here we always fire un-targeted.
	lifetime := float32(0)
	if params.ProjectileSpeed > 0 {
		lifetime = params.Range / params.ProjectileSpeed
	}

	spec := gamecomp.ProjectileSpec{
		OwnerNetID:   caster.NetID(),
		Damage:       params.Damage,
		SplashRadius: params.SplashRadius,
		SplashDamage: params.SplashDamage,
		MaxTurnRate:  params.HomingMaxTurnRate,
		Type:         projType,
		PierceCount:  pierceCount,
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
// stage. Each tick we decrement the channel timers, compute the aim
// direction from the player's cursor coords (AimX/AimY — updated by
// the ChannelAim wire handler), validate the caster's facing is within
// ±BeamHalfArcRad of the aim direction, then hitscan along the beam
// line to find the closest valid victim within params.Range. On
// channel end (timeout, caster death, facing too far off-aim) the
// Channeling component is removed and the post-channel cooldown is set.
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

	mmokit.ForEach1(s.Stage(), func(caster mmokit.Entity, ch *gamecomp.Channeling) {
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

		casterPos := mmokit.Get[mmokit.Position](caster)
		casterRot := mmokit.Get[mmokit.Rotation](caster)
		if casterPos == nil || casterRot == nil {
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

		// Aim direction from cursor coords carried on the Channeling
		// component (ChannelAim wire handler updates this each tick).
		dx := ch.AimX - casterPos.X
		dy := ch.AimY - casterPos.Y
		aimNorm := float32(math.Sqrt(float64(dx*dx + dy*dy)))
		if aimNorm < 1e-3 {
			// Cursor on top of ship → no clear aim; skip tick but don't end.
			return
		}
		aimAngle := float32(math.Atan2(float64(dy), float64(dx)))

		// Player can be rotated separately from cursor — drop the
		// channel if the player's facing is way off the aim direction.
		// BeamHalfArcRad is the allowed slack between facing and aim.
		if angleDelta(casterRot.Angle, aimAngle) > params.BeamHalfArcRad {
			ends = append(ends, endCandidate{caster, ch.SlotID})
			return
		}

		// Per-tick damage gate.
		if ch.NextTickIn > 0 || params.ChannelTickRate <= 0 {
			return
		}

		// Hitscan along the aim line — find the closest non-owner entity
		// within params.Range and ±4u of the beam line.
		s.nearbyChannel = gw.Spatial.QueryRadius(
			casterPos.X+dx/aimNorm*params.Range*0.5, // midpoint of the beam line
			casterPos.Y+dy/aimNorm*params.Range*0.5,
			params.Range*0.5+8, // search radius covering the full beam length
			s.nearbyChannel[:0],
		)

		ownerNetID := caster.NetID()
		var victim mmokit.Entity
		victimDist := params.Range + 1
		for _, entry := range s.nearbyChannel {
			e := mmokit.EntityFromECS(gw.stage, entry.Entity)
			if !e.Alive() || e.NetID() == ownerNetID {
				continue
			}
			if !mmokit.Has[gamecomp.NPCAI](e) && !mmokit.Has[mmokit.PlayerConn](e) {
				continue
			}
			epos := mmokit.Get[mmokit.Position](e)
			if epos == nil {
				continue
			}
			rx, ry := epos.X-casterPos.X, epos.Y-casterPos.Y
			parallel := rx*dx/aimNorm + ry*dy/aimNorm
			if parallel < 0 || parallel > params.Range {
				continue
			}
			// perpendicular distance = |r · n| where n is unit normal
			nx, ny := -dy/aimNorm, dx/aimNorm
			perp := rx*nx + ry*ny
			if perp < 0 {
				perp = -perp
			}
			if perp > 4 {
				continue
			}
			if parallel < victimDist {
				victimDist = parallel
				victim = e
			}
		}

		if victim.Alive() {
			gw.Damage(caster, victim, params.Damage, 0, ch.SlotID, uint8(params.Type))
			gw.eng.Log.Log(CatCombatHit, "channel: %d -> %d dmg=%.0f",
				ownerNetID, victim.NetID(), params.Damage)
		}
		ch.NextTickIn = 1.0 / params.ChannelTickRate
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
