package system

import (
	"math"

	"github.com/mlange-42/ark/ecs"

	gamepb "github.com/zenion/mmoserver/gen/go"
	"github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/internal/game"
	"github.com/zenion/mmoserver/internal/item"
	"github.com/zenion/mmoserver/pkg/logger"
)

type abilityAction struct {
	caster      ecs.Entity
	casterNetID uint32
	slot        uint8
	params      *item.AbilityParams
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
		s.filter = ecs.NewFilter4[component.PlayerInput, component.TargetLock, component.AbilitySet, component.Equipment](gw.ECS)
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

			// Targeted abilities (slots 0-3 = Q/W/E/R) require lock and range check
			if slot <= component.AbilityR {
				if !lock.Locked || !gw.ECS.Alive(lock.TargetEntity) {
					continue
				}
				if params.Range > 0 && !s.inRange(entity, lock.TargetEntity, params.Range) {
					continue
				}
			}

			// Set cooldown from equipment ability params
			abilities.Cooldowns[slot] = params.Cooldown

			s.deferred = append(s.deferred, abilityAction{
				caster:      entity,
				casterNetID: casterNetID,
				slot:        slot,
				params:      params,
			})
		}

		input.AbilityCast = 0
	}

	for _, action := range s.deferred {
		s.executeAbility(action)
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

func (s *AbilitySystem) executeAbility(action abilityAction) {
	gw := s.gw
	entity := action.caster

	if !gw.ECS.Alive(entity) {
		return
	}

	lock := gw.TargetLockMap.Get(entity)
	params := action.params

	var targetNetID uint32
	var damageDealt float32

	switch params.Type {
	// --- Hitscan damage abilities ---
	case item.AbilityTypePulseLaser, item.AbilityTypePulseBarrage,
		item.AbilityTypeRailShot, item.AbilityTypeIonOverload, item.AbilityTypePlasmaBolt:
		if gw.ECS.Alive(lock.TargetEntity) {
			damageDealt = gw.ApplyDamage(lock.TargetEntity, params.Damage, action.casterNetID)
			targetNetID = lock.TargetNetID
			gw.Log.Log(logger.CatCombat, "ability %s: %d -> %d dmg=%.0f",
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
			gw.Log.Log(logger.CatCombat, "ability %s: %d -> %d dmg=%.0f",
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
			gw.Log.Log(logger.CatCombat, "ability %s: %d -> %d (%.1f dps for %.1fs)",
				params.Name, action.casterNetID, lock.TargetNetID, params.DotDPS, params.DotDuration)
		}

	// --- Shield restore + Fortified buff ---
	case item.AbilityTypeEmergencyShield, item.AbilityTypeHardenedShield:
		if gw.ShieldMap.HasAll(entity) {
			shield := gw.ShieldMap.Get(entity)
			shield.Current = min(shield.Current+params.ShieldRestore, shield.Max)
		}
		if gw.StatusEffectsMap.HasAll(entity) {
			se := gw.StatusEffectsMap.Get(entity)
			se.Add(component.StatusEffect{
				Type:     component.StatusFortified,
				Duration: params.BuffDuration,
				Value:    params.DmgReduction,
				Source:   entity,
			})
		}
		gw.Log.Log(logger.CatCombat, "ability %s: %d restored shield, fortified %.1fs",
			params.Name, action.casterNetID, params.BuffDuration)

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
		gw.Log.Log(logger.CatCombat, "ability %s: %d speed x%.1f for %.1fs",
			params.Name, action.casterNetID, params.SpeedMult, params.BoostDuration)
	}

	gw.PendingAbilityEvents = append(gw.PendingAbilityEvents, &gamepb.AbilityCastResultMsg{
		Slot:        uint32(action.slot),
		Success:     true,
		TargetId:    targetNetID,
		DamageDealt: damageDealt,
		CasterId:    action.casterNetID,
	})
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
