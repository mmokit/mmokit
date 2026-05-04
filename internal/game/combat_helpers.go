package game

import (
	"github.com/mlange-42/ark/ecs"

	"github.com/zenion/mmoserver/internal/component"
)

// ApplyDamage applies hitscan damage to a target entity.
// Handles Fortified damage reduction, shield absorption, and death.
// Returns the actual damage dealt.
func (gw *GameWorld) ApplyDamage(target ecs.Entity, damage float32, attackerNetID uint32) float32 {
	if !gw.eng.ECS.Alive(target) || !gw.C.Health.HasAll(target) {
		return 0
	}
	// Dormant targets (e.g. docked players parked at a station) take no
	// damage. TargetLockSystem already breaks locks on Dormant targets, but
	// belt-and-suspenders here covers any direct call path that bypasses
	// the lock — and pins the invariant in tests.
	if gw.C.Dormant.HasAll(target) {
		return 0
	}

	// Check Fortified buff for damage reduction
	if gw.C.StatusEffects.HasAll(target) {
		se := gw.C.StatusEffects.Get(target)
		if eff := se.Get(component.StatusFortified); eff != nil {
			damage *= (1.0 - eff.Value)
		}
	}

	health := gw.C.Health.Get(target)
	totalDamage := damage
	shieldAbsorbed := float32(0)

	// Shield absorbs damage first
	if gw.C.Shield.HasAll(target) {
		shield := gw.C.Shield.Get(target)
		shield.DamageCooldown = shield.RegenDelay
		if shield.Current > 0 {
			shieldAbsorbed = min(shield.Current, damage)
			shield.Current -= shieldAbsorbed
			damage -= shieldAbsorbed
		}
	}

	health.Current -= damage
	health.LastDamagedByNetID = attackerNetID

	targetNetID := uint32(0)
	if gw.C.NetworkID.HasAll(target) {
		targetNetID = gw.C.NetworkID.Get(target).ID
	}
	gw.eng.Log.Log(CatCombatHit, "hit: attacker=%d -> target=%d damage=%.1f (shield=%.1f) hp=%.1f/%.1f",
		attackerNetID, targetNetID, totalDamage, shieldAbsorbed, health.Current, health.Max)

	return totalDamage
}
