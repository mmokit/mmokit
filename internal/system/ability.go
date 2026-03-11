package system

import (
	"math"

	"github.com/mlange-42/ark/ecs"

	gamepb "github.com/zenion/mmoserver/gen/go"
	"github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/internal/game"
	"github.com/zenion/mmoserver/pkg/logger"
)

type abilityAction struct {
	caster      ecs.Entity
	casterNetID uint32
	slot        uint8
}

// AbilitySystem processes ability casts (hitscan damage, buffs, debuffs).
type AbilitySystem struct {
	gw       *game.GameWorld
	filter   *ecs.Filter3[component.PlayerInput, component.TargetLock, component.AbilitySet]
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
		s.filter = ecs.NewFilter3[component.PlayerInput, component.TargetLock, component.AbilitySet](gw.ECS)
	}

	s.deferred = s.deferred[:0]

	// Tick cooldowns and collect ability casts
	query := s.filter.Query()
	for query.Next() {
		input, lock, abilities := query.Get()
		entity := query.Entity()

		// Tick down all cooldowns
		for i := range abilities.Cooldowns {
			if abilities.Cooldowns[i] > 0 {
				abilities.Cooldowns[i] -= dt
			}
		}

		// No abilities pressed this tick
		if input.AbilityCast == 0 {
			continue
		}

		casterNetID := uint32(0)
		if gw.NetworkIDMap.HasAll(entity) {
			casterNetID = gw.NetworkIDMap.Get(entity).ID
		}

		// Process each ability bit
		for slot := uint8(0); slot < component.AbilityCount; slot++ {
			if input.AbilityCast&(1<<slot) == 0 {
				continue
			}

			// Check cooldown
			if abilities.Cooldowns[slot] > 0 {
				continue
			}

			// Targeted abilities (Q/W/E/R) require lock
			if slot <= component.AbilityR {
				if !lock.Locked || !gw.ECS.Alive(lock.TargetEntity) {
					continue
				}

				// Range check
				abilityRange := s.getAbilityRange(slot)
				if !s.inRange(entity, lock.TargetEntity, abilityRange) {
					continue
				}
			}

			// Set cooldown
			abilities.Cooldowns[slot] = gw.Config.AbilityCooldown(slot)

			s.deferred = append(s.deferred, abilityAction{
				caster:      entity,
				casterNetID: casterNetID,
				slot:        slot,
			})
		}

		// Clear ability cast after processing
		input.AbilityCast = 0
	}

	// Execute deferred ability actions
	for _, action := range s.deferred {
		s.executeAbility(action)
	}
}

func (s *AbilitySystem) executeAbility(action abilityAction) {
	gw := s.gw
	entity := action.caster

	if !gw.ECS.Alive(entity) {
		return
	}

	lock := gw.TargetLockMap.Get(entity)

	var targetNetID uint32
	var damageDealt float32

	switch action.slot {
	case component.AbilityQ: // Pulse Laser
		if gw.ECS.Alive(lock.TargetEntity) {
			damageDealt = gw.ApplyDamage(lock.TargetEntity, gw.Config.AbilityQDamage, action.casterNetID)
			targetNetID = lock.TargetNetID
			gw.Log.Log(logger.CatCombat, "ability Q (Pulse Laser): %d -> %d", action.casterNetID, lock.TargetNetID)
		}

	case component.AbilityW: // Railgun
		if gw.ECS.Alive(lock.TargetEntity) {
			damageDealt = gw.ApplyDamage(lock.TargetEntity, gw.Config.AbilityWDamage, action.casterNetID)
			targetNetID = lock.TargetNetID
			gw.Log.Log(logger.CatCombat, "ability W (Railgun): %d -> %d", action.casterNetID, lock.TargetNetID)
		}

	case component.AbilityE: // Ion Burn
		if gw.ECS.Alive(lock.TargetEntity) {
			if gw.StatusEffectsMap.HasAll(lock.TargetEntity) {
				se := gw.StatusEffectsMap.Get(lock.TargetEntity)
				se.Add(component.StatusEffect{
					Type:     component.StatusIonBurn,
					Duration: gw.Config.AbilityEDotDuration,
					Value:    gw.Config.AbilityEDotDPS,
					Source:   entity,
				})
			}
			targetNetID = lock.TargetNetID
			gw.Log.Log(logger.CatCombat, "ability E (Ion Burn): %d -> %d (%.1f dps for %.1fs)",
				action.casterNetID, lock.TargetNetID, gw.Config.AbilityEDotDPS, gw.Config.AbilityEDotDuration)
		}

	case component.AbilityR: // Plasma Torpedo
		if gw.ECS.Alive(lock.TargetEntity) {
			damage := gw.Config.AbilityRDamage
			// Bonus damage if target has no shields
			if gw.ShieldMap.HasAll(lock.TargetEntity) {
				shield := gw.ShieldMap.Get(lock.TargetEntity)
				if shield.Current <= 0 {
					damage += gw.Config.AbilityRBonusDamage
				}
			}
			damageDealt = gw.ApplyDamage(lock.TargetEntity, damage, action.casterNetID)
			targetNetID = lock.TargetNetID
			gw.Log.Log(logger.CatCombat, "ability R (Plasma Torpedo): %d -> %d damage=%.0f",
				action.casterNetID, lock.TargetNetID, damage)
		}

	case component.AbilityD: // Emergency Shields
		// Restore shield
		if gw.ShieldMap.HasAll(entity) {
			shield := gw.ShieldMap.Get(entity)
			shield.Current = min(shield.Current+gw.Config.AbilityDShieldRestore, shield.Max)
		}
		// Apply Fortified buff
		if gw.StatusEffectsMap.HasAll(entity) {
			se := gw.StatusEffectsMap.Get(entity)
			se.Add(component.StatusEffect{
				Type:     component.StatusFortified,
				Duration: gw.Config.AbilityDBuffDuration,
				Value:    gw.Config.AbilityDDmgReduction,
				Source:   entity,
			})
		}
		gw.Log.Log(logger.CatCombat, "ability D (Emergency Shields): %d restored shield, fortified %.1fs",
			action.casterNetID, gw.Config.AbilityDBuffDuration)

	case component.AbilityF: // Afterburner
		if gw.StatusEffectsMap.HasAll(entity) {
			se := gw.StatusEffectsMap.Get(entity)
			se.Add(component.StatusEffect{
				Type:     component.StatusAfterburner,
				Duration: gw.Config.AbilityFDuration,
				Value:    gw.Config.AbilityFSpeedMult,
				Source:   entity,
			})
		}
		gw.Log.Log(logger.CatCombat, "ability F (Afterburner): %d speed x%.1f for %.1fs",
			action.casterNetID, gw.Config.AbilityFSpeedMult, gw.Config.AbilityFDuration)
	}

	// Queue ability event for broadcast to all nearby players
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

func (s *AbilitySystem) getAbilityRange(slot uint8) float32 {
	cfg := &s.gw.Config
	switch slot {
	case component.AbilityQ:
		return cfg.AbilityQRange
	case component.AbilityW:
		return cfg.AbilityWRange
	case component.AbilityE:
		return cfg.AbilityERange
	case component.AbilityR:
		return cfg.AbilityRRange
	default:
		return 0
	}
}

