package game

import (
	"math"

	"github.com/mlange-42/ark/ecs"

	gamepb "github.com/zenion/mmoserver/gen/go/gamepb"
	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/internal/item"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

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
	mmokit.SystemBase[*GameWorld]
	entities mmokit.Query[abilityBundle]
	deferred []abilityAction
}

func (s *AbilitySystem) Init() {
	s.deferred = make([]abilityAction, 0, 16)
}

func (s *AbilitySystem) Update(dt float32) {
	gw := s.World()

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

		casterNetID := uint32(0)
		if gw.C.NetworkID.HasAll(entity) {
			casterNetID = gw.C.NetworkID.Get(entity).ID
		}

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
				if !lock.Locked || !gw.eng.ECS.Alive(lock.TargetEntity) {
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
	gw := s.World()
	entity := action.caster

	if !gw.eng.ECS.Alive(entity) {
		return false
	}

	lock := gw.C.TargetLock.Get(entity)
	params := action.params

	var targetNetID uint32
	var damageDealt float32
	fired := true
	sentCrossNode := false

	switch params.Type {
	// --- Hitscan damage abilities ---
	case item.AbilityTypePulseLaser, item.AbilityTypePulseBarrage,
		item.AbilityTypeRailShot, item.AbilityTypeIonOverload, item.AbilityTypePlasmaBolt:
		if gw.eng.ECS.Alive(lock.TargetEntity) {
			if s.isReplica(lock.TargetEntity) {
				s.sendCrossNodeDamage(action.casterNetID, lock.TargetEntity, params.Damage, action.slot, uint8(params.Type))
				sentCrossNode = true
			} else {
				damageDealt = gw.ApplyDamage(lock.TargetEntity, params.Damage, action.casterNetID)
			}
			targetNetID = lock.TargetNetID
			gw.eng.Log.Log(CatCombatAbility, "ability %s: %d -> %d dmg=%.0f",
				params.Name, action.casterNetID, lock.TargetNetID, params.Damage)
		}

	// --- Hitscan + bonus vs unshielded ---
	case item.AbilityTypePiercingRound, item.AbilityTypePlasmaTorpedo:
		if gw.eng.ECS.Alive(lock.TargetEntity) {
			if s.isReplica(lock.TargetEntity) {
				// Send base + bonus; authoritative node decides based on its own shield state
				s.sendCrossNodeDamageWithBonus(action.casterNetID, lock.TargetEntity, params.Damage, params.BonusDamage, action.slot, uint8(params.Type))
				sentCrossNode = true
			} else {
				damage := params.Damage
				if gw.C.Shield.HasAll(lock.TargetEntity) {
					shield := gw.C.Shield.Get(lock.TargetEntity)
					if shield.Current <= 0 {
						damage += params.BonusDamage
					}
				}
				damageDealt = gw.ApplyDamage(lock.TargetEntity, damage, action.casterNetID)
			}
			targetNetID = lock.TargetNetID
			gw.eng.Log.Log(CatCombatAbility, "ability %s: %d -> %d dmg=%.0f",
				params.Name, action.casterNetID, lock.TargetNetID, params.Damage)
		}

	// --- DoT debuff ---
	case item.AbilityTypeIonBurn:
		if gw.eng.ECS.Alive(lock.TargetEntity) {
			if s.isReplica(lock.TargetEntity) {
				s.sendCrossNodeStatusEffect(action.casterNetID, lock.TargetEntity,
					uint8(gamecomp.StatusIonBurn), params.DotDuration, params.DotDPS)
				sentCrossNode = true
			} else if gw.C.StatusEffects.HasAll(lock.TargetEntity) {
				se := gw.C.StatusEffects.Get(lock.TargetEntity)
				se.Add(gamecomp.StatusEffect{
					Type:     gamecomp.StatusIonBurn,
					Duration: params.DotDuration,
					Value:    params.DotDPS,
					Source:   entity,
				})
			}
			targetNetID = lock.TargetNetID
			gw.eng.Log.Log(CatCombatAbility, "ability %s: %d -> %d (%.1f dps for %.1fs)",
				params.Name, action.casterNetID, lock.TargetNetID, params.DotDPS, params.DotDuration)
		}

	// --- Shield restore + Fortified buff ---
	case item.AbilityTypeEmergencyShield, item.AbilityTypeHardenedShield:
		if gw.C.StatusEffects.HasAll(entity) {
			se := gw.C.StatusEffects.Get(entity)
			regenPerSec := params.ShieldRestore / params.BuffDuration
			se.Add(gamecomp.StatusEffect{
				Type:     gamecomp.StatusShieldRegen,
				Duration: params.BuffDuration,
				Value:    regenPerSec,
				Source:   entity,
			})
			se.Add(gamecomp.StatusEffect{
				Type:     gamecomp.StatusFortified,
				Duration: params.BuffDuration,
				Value:    params.DmgReduction,
				Source:   entity,
			})
		}
		gw.eng.Log.Log(CatCombatAbility, "ability %s: %d shield regen +%.1f/s for %.1fs",
			params.Name, action.casterNetID, params.ShieldRestore/params.BuffDuration, params.BuffDuration)

	// --- Speed boost ---
	case item.AbilityTypeAfterburner, item.AbilityTypeMicroWarp:
		if gw.C.StatusEffects.HasAll(entity) {
			se := gw.C.StatusEffects.Get(entity)
			se.Add(gamecomp.StatusEffect{
				Type:     gamecomp.StatusAfterburner,
				Duration: params.BoostDuration,
				Value:    params.SpeedMult,
				Source:   entity,
			})
		}
		gw.eng.Log.Log(CatCombatAbility, "ability %s: %d speed x%.1f for %.1fs",
			params.Name, action.casterNetID, params.SpeedMult, params.BoostDuration)

	// --- Mining beam toggle ---
	case item.AbilityTypeMiningBeam:
		if !gw.C.MiningLaser.HasAll(entity) {
			fired = false
			break
		}
		laser := gw.C.MiningLaser.Get(entity)
		beamIdx := s.slotToBeamIndex(action.slot)

		if laser.Beams[beamIdx].Active {
			// Toggle off
			laser.Beams[beamIdx].Active = false
			gw.eng.Log.Log(CatEconomyMining, "mining beam off: %d beam=%d", action.casterNetID, beamIdx)
		} else {
			// Toggle on — require lock and validate target is minable
			if !lock.Locked || !gw.eng.ECS.Alive(lock.TargetEntity) || !gw.C.Minable.HasAll(lock.TargetEntity) {
				fired = false
				break
			}
			laser.Beams[beamIdx].Active = true
			laser.Target = lock.TargetEntity
			gw.eng.Log.Log(CatEconomyMining, "mining beam on: %d beam=%d target=%d",
				action.casterNetID, beamIdx, lock.TargetNetID)
		}
		// Sync replicated ActiveMining immediately so clients see the toggle
		// on the same tick, without waiting for the next MiningSystem pass.
		gw.syncActiveMining(entity, laser)

	// --- Extract pulse (mining burst) ---
	case item.AbilityTypeExtractPulse:
		if !gw.C.MiningLaser.HasAll(entity) || !gw.C.Inventory.HasAll(entity) {
			fired = false
			break
		}
		laser := gw.C.MiningLaser.Get(entity)
		beamIdx := s.slotToBeamIndex(action.slot)
		beam := &laser.Beams[beamIdx]

		// Require active mining beam
		if !beam.Active || !gw.eng.ECS.Alive(laser.Target) {
			fired = false
			break
		}
		if !gw.C.Minable.HasAll(laser.Target) {
			fired = false
			break
		}
		// Range check
		if !s.inRange(entity, laser.Target, beam.Range) {
			fired = false
			break
		}
		minable := gw.C.Minable.Get(laser.Target)
		if minable.Remaining <= 0 {
			fired = false
			break
		}
		inv := gw.C.Inventory.Get(entity)
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

		if s.isReplica(laser.Target) {
			// Cross-cell mining: send action to authoritative node
			s.sendCrossNodeMining(action.casterNetID, laser.Target, float32(added))
			// Update local replica for immediate visual feedback
			minable.Remaining -= float32(added)
			sentCrossNode = true
			gw.eng.Log.Log(CatEconomyMining, "extract pulse cross-cell: %d beam=%d amount=%d remaining=%.1f",
				action.casterNetID, beamIdx, added, minable.Remaining)
		} else {
			minable.Remaining -= float32(added)
			gw.eng.Log.Log(CatEconomyMining, "extract pulse: %d beam=%d amount=%d remaining=%.1f",
				action.casterNetID, beamIdx, added, minable.Remaining)

			if minable.Remaining <= 0 {
				gw.MarkForRemoval(laser.Target)
				gw.eng.Log.Log(CatEconomyMining, "asteroid depleted by extract pulse")
			}
		}
	}

	if fired && !sentCrossNode {
		mmokit.Enqueue(gw.Queue, &gamepb.AbilityCastResultMsg{
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
	if slot <= gamecomp.AbilityW {
		return 0
	}
	return 1
}

func (s *AbilitySystem) isReplica(entity ecs.Entity) bool {
	return s.World().C.Replica.HasAll(entity)
}

func (s *AbilitySystem) sendCrossNodeDamage(casterNetID uint32, target ecs.Entity, damage float32, slot uint8, abilityType uint8) {
	s.sendCrossNodeDamageWithBonus(casterNetID, target, damage, 0, slot, abilityType)
}

func (s *AbilitySystem) sendCrossNodeDamageWithBonus(casterNetID uint32, target ecs.Entity, damage, bonusDamage float32, slot uint8, abilityType uint8) {
	gw := s.World()
	rep := gw.C.Replica.Get(target)
	gw.Bridge().SendAction(rep.SourceCellID, &mmokit.CrossCellAction{
		Type:         ActionDamage,
		TargetNetID:  rep.SourceNetID,
		SourceNetID:  casterNetID,
		SourceCellID: gw.CellID(),
		Payload:      MarshalDamageAction(&DamageAction{Damage: damage, BonusDamage: bonusDamage, Slot: slot, AbilityType: abilityType}),
	})
}

func (s *AbilitySystem) sendCrossNodeStatusEffect(casterNetID uint32, target ecs.Entity, effectType uint8, duration, value float32) {
	gw := s.World()
	rep := gw.C.Replica.Get(target)
	gw.Bridge().SendAction(rep.SourceCellID, &mmokit.CrossCellAction{
		Type:         ActionStatusEffect,
		TargetNetID:  rep.SourceNetID,
		SourceNetID:  casterNetID,
		SourceCellID: gw.CellID(),
		Payload:      MarshalStatusEffectAction(&StatusEffectAction{EffectType: effectType, Duration: duration, Value: value}),
	})
}

func (s *AbilitySystem) sendCrossNodeMining(casterNetID uint32, target ecs.Entity, amount float32) {
	gw := s.World()
	rep := gw.C.Replica.Get(target)
	gw.Bridge().SendAction(rep.SourceCellID, &mmokit.CrossCellAction{
		Type:         ActionMining,
		TargetNetID:  rep.SourceNetID,
		SourceNetID:  casterNetID,
		SourceCellID: gw.CellID(),
		Payload:      MarshalMiningAction(&MiningAction{Amount: amount}),
	})
}

func (s *AbilitySystem) inRange(caster, target ecs.Entity, abilityRange float32) bool {
	gw := s.World()
	if !gw.C.Position.HasAll(caster) || !gw.C.Position.HasAll(target) {
		return false
	}
	casterPos := gw.C.Position.Get(caster)
	targetPos := gw.C.Position.Get(target)
	dx := targetPos.X - casterPos.X
	dy := targetPos.Y - casterPos.Y
	dist := float32(math.Sqrt(float64(dx*dx + dy*dy)))
	return dist <= abilityRange
}
