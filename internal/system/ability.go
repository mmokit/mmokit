package system

import (
	"math"

	"github.com/mlange-42/ark/ecs"

	gamepb "github.com/zenion/mmoserver/gen/go"
	"github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/internal/game"
	"github.com/zenion/mmoserver/internal/item"
)

type abilityAction struct {
	caster      ecs.Entity
	casterNetID uint32
	slot        uint8
	params      *item.AbilityParams
	abilities   *component.AbilitySet
}

// AbilitySystem processes ability casts using equipment-driven ability parameters.
type AbilitySystem struct {
	gw       *game.GameWorld
	filter   *ecs.Filter4[component.PlayerInput, component.TargetLock, component.AbilitySet, component.Equipment]
	deferred []abilityAction
}

func NewAbilitySystem(gw *game.GameWorld) *AbilitySystem {
	return &AbilitySystem{
		gw:       gw,
		deferred: make([]abilityAction, 0, 16),
	}
}

func (s *AbilitySystem) Update(dt float32) {
	gw := s.gw
	if s.filter == nil {
		s.filter = ecs.NewFilter4[component.PlayerInput, component.TargetLock, component.AbilitySet, component.Equipment](gw.ECS).Without(ecs.C[component.Ghost](), ecs.C[component.Replica]())
	}

	s.deferred = s.deferred[:0]

	query := s.filter.Query()
	for query.Next() {
		input, lock, abilities, equip := query.Get()
		entity := query.Entity()

		// Tick down all cooldowns
		for i := range abilities.Cooldowns {
			if abilities.Cooldowns[i] > 0 {
				abilities.Cooldowns[i] -= dt
			}
		}

		if input.AbilityCast == 0 {
			continue
		}

		casterNetID := uint32(0)
		if gw.NetworkIDMap.HasAll(entity) {
			casterNetID = gw.NetworkIDMap.Get(entity).ID
		}

		for slot := uint8(0); slot < component.AbilityCount; slot++ {
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
			if slot <= component.AbilityR && !isMiningToggle {
				if !lock.Locked || !gw.ECS.Alive(lock.TargetEntity) {
					continue
				}
				if params.Range > 0 && !s.inRange(entity, lock.TargetEntity, params.Range) {
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
func resolveAbilityParams(equip *component.Equipment, slot uint8) *item.AbilityParams {
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

	if !gw.ECS.Alive(entity) {
		return false
	}

	lock := gw.TargetLockMap.Get(entity)
	params := action.params

	var targetNetID uint32
	var damageDealt float32
	fired := true

	switch params.Type {
	// --- Hitscan damage abilities ---
	case item.AbilityTypePulseLaser, item.AbilityTypePulseBarrage,
		item.AbilityTypeRailShot, item.AbilityTypeIonOverload, item.AbilityTypePlasmaBolt:
		if gw.ECS.Alive(lock.TargetEntity) {
			damageDealt = gw.ApplyDamage(lock.TargetEntity, params.Damage, action.casterNetID)
			targetNetID = lock.TargetNetID
			gw.Log.Log(game.CatCombat, "ability %s: %d -> %d dmg=%.0f",
				params.Name, action.casterNetID, lock.TargetNetID, params.Damage)
		}

	// --- Hitscan + bonus vs unshielded ---
	case item.AbilityTypePiercingRound, item.AbilityTypePlasmaTorpedo:
		if gw.ECS.Alive(lock.TargetEntity) {
			damage := params.Damage
			if gw.ShieldMap.HasAll(lock.TargetEntity) {
				shield := gw.ShieldMap.Get(lock.TargetEntity)
				if shield.Current <= 0 {
					damage += params.BonusDamage
				}
			}
			damageDealt = gw.ApplyDamage(lock.TargetEntity, damage, action.casterNetID)
			targetNetID = lock.TargetNetID
			gw.Log.Log(game.CatCombat, "ability %s: %d -> %d dmg=%.0f",
				params.Name, action.casterNetID, lock.TargetNetID, damage)
		}

	// --- DoT debuff ---
	case item.AbilityTypeIonBurn:
		if gw.ECS.Alive(lock.TargetEntity) {
			if gw.StatusEffectsMap.HasAll(lock.TargetEntity) {
				se := gw.StatusEffectsMap.Get(lock.TargetEntity)
				se.Add(component.StatusEffect{
					Type:     component.StatusIonBurn,
					Duration: params.DotDuration,
					Value:    params.DotDPS,
					Source:   entity,
				})
			}
			targetNetID = lock.TargetNetID
			gw.Log.Log(game.CatCombat, "ability %s: %d -> %d (%.1f dps for %.1fs)",
				params.Name, action.casterNetID, lock.TargetNetID, params.DotDPS, params.DotDuration)
		}

	// --- Shield restore + Fortified buff ---
	case item.AbilityTypeEmergencyShield, item.AbilityTypeHardenedShield:
		if gw.StatusEffectsMap.HasAll(entity) {
			se := gw.StatusEffectsMap.Get(entity)
			regenPerSec := params.ShieldRestore / params.BuffDuration
			se.Add(component.StatusEffect{
				Type:     component.StatusShieldRegen,
				Duration: params.BuffDuration,
				Value:    regenPerSec,
				Source:   entity,
			})
			se.Add(component.StatusEffect{
				Type:     component.StatusFortified,
				Duration: params.BuffDuration,
				Value:    params.DmgReduction,
				Source:   entity,
			})
		}
		gw.Log.Log(game.CatCombat, "ability %s: %d shield regen +%.1f/s for %.1fs",
			params.Name, action.casterNetID, params.ShieldRestore/params.BuffDuration, params.BuffDuration)

	// --- Speed boost ---
	case item.AbilityTypeAfterburner, item.AbilityTypeMicroWarp:
		if gw.StatusEffectsMap.HasAll(entity) {
			se := gw.StatusEffectsMap.Get(entity)
			se.Add(component.StatusEffect{
				Type:     component.StatusAfterburner,
				Duration: params.BoostDuration,
				Value:    params.SpeedMult,
				Source:   entity,
			})
		}
		gw.Log.Log(game.CatCombat, "ability %s: %d speed x%.1f for %.1fs",
			params.Name, action.casterNetID, params.SpeedMult, params.BoostDuration)

	// --- Mining beam toggle ---
	case item.AbilityTypeMiningBeam:
		if !gw.MiningLaserMap.HasAll(entity) {
			fired = false
			break
		}
		laser := gw.MiningLaserMap.Get(entity)
		beamIdx := s.slotToBeamIndex(action.slot)

		if laser.Beams[beamIdx].Active {
			// Toggle off
			laser.Beams[beamIdx].Active = false
			gw.Log.Log(game.CatMining, "mining beam off: %d beam=%d", action.casterNetID, beamIdx)
		} else {
			// Toggle on — require lock and validate target is minable
			if !lock.Locked || !gw.ECS.Alive(lock.TargetEntity) || !gw.MinableMap.HasAll(lock.TargetEntity) {
				fired = false
				break
			}
			laser.Beams[beamIdx].Active = true
			laser.Target = lock.TargetEntity
			gw.Log.Log(game.CatMining, "mining beam on: %d beam=%d target=%d",
				action.casterNetID, beamIdx, lock.TargetNetID)
		}

	// --- Extract pulse (mining burst) ---
	case item.AbilityTypeExtractPulse:
		if !gw.MiningLaserMap.HasAll(entity) || !gw.InventoryMap.HasAll(entity) {
			fired = false
			break
		}
		laser := gw.MiningLaserMap.Get(entity)
		beamIdx := s.slotToBeamIndex(action.slot)
		beam := &laser.Beams[beamIdx]

		// Require active mining beam
		if !beam.Active || !gw.ECS.Alive(laser.Target) {
			fired = false
			break
		}
		if !gw.MinableMap.HasAll(laser.Target) {
			fired = false
			break
		}
		// Range check
		if !s.inRange(entity, laser.Target, beam.Range) {
			fired = false
			break
		}
		minable := gw.MinableMap.Get(laser.Target)
		if minable.Remaining <= 0 {
			fired = false
			break
		}
		inv := gw.InventoryMap.Get(entity)
		if inv.RemainingMass() <= 0 {
			fired = false
			break
		}

		amount := params.MiningYield
		if amount > minable.Remaining {
			amount = minable.Remaining
		}
		whole := int32(amount)
		if whole <= 0 {
			break
		}
		itemID := item.ResourceItemID(minable.ResourceType)
		added := inv.AddItem(itemID, whole)
		minable.Remaining -= float32(added)

		gw.Log.Log(game.CatMining, "extract pulse: %d beam=%d amount=%d remaining=%.1f",
			action.casterNetID, beamIdx, added, minable.Remaining)

		if minable.Remaining <= 0 {
			gw.MarkForRemoval(laser.Target)
			gw.Log.Log(game.CatMining, "asteroid depleted by extract pulse")
		}
	}

	if fired {
		gw.PendingAbilityEvents = append(gw.PendingAbilityEvents, &gamepb.AbilityCastResultMsg{
			Slot:        uint32(action.slot),
			Success:     true,
			TargetId:    targetNetID,
			DamageDealt: damageDealt,
			CasterId:    action.casterNetID,
			AbilityType: uint32(params.Type),
		})
	}
	return fired
}

// slotToBeamIndex maps an ability slot to a mining beam index.
// Slots 0-1 (Weapon1 Q/W) → beam 0, slots 2-3 (Weapon2 E/R) → beam 1.
func (s *AbilitySystem) slotToBeamIndex(slot uint8) int {
	if slot <= component.AbilityW {
		return 0
	}
	return 1
}

func (s *AbilitySystem) inRange(caster, target ecs.Entity, abilityRange float32) bool {
	gw := s.gw
	if !gw.PositionMap.HasAll(caster) || !gw.PositionMap.HasAll(target) {
		return false
	}
	casterPos := gw.PositionMap.Get(caster)
	targetPos := gw.PositionMap.Get(target)
	dx := targetPos.X - casterPos.X
	dy := targetPos.Y - casterPos.Y
	dist := float32(math.Sqrt(float64(dx*dx + dy*dy)))
	return dist <= abilityRange
}
