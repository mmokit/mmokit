package game

import (
	"github.com/mlange-42/ark/ecs"

	"github.com/zenion/mmoserver/internal/component"
)

// ApplyDamage applies hitscan damage to a target entity.
// Handles Fortified damage reduction, shield absorption, and death.
// Returns the actual damage dealt.
func (gw *GameWorld) ApplyDamage(target ecs.Entity, damage float32, attackerNetID uint32) float32 {
	if !gw.ECS.Alive(target) || !gw.C.Health.HasAll(target) {
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

	targetNetID := uint32(0)
	if gw.C.NetworkID.HasAll(target) {
		targetNetID = gw.C.NetworkID.Get(target).ID
	}
	gw.Log.Log(CatCombat, "hit: attacker=%d -> target=%d damage=%.1f (shield=%.1f) hp=%.1f/%.1f",
		attackerNetID, targetNetID, totalDamage, shieldAbsorbed, health.Current, health.Max)

	if health.Current <= 0 {
		gw.Log.Log(CatKill, "killed: target=%d by attacker=%d", targetNetID, attackerNetID)
		if gw.C.PlayerConn.HasAll(target) {
			gw.MarkPlayerDeath(target, attackerNetID)
		} else {
			gw.MarkNPCDeath(target, attackerNetID)
		}
	}

	return totalDamage
}

// MarkNPCDeath handles NPC death: rolls drop table, queues loot crate, and removes entity.
func (gw *GameWorld) MarkNPCDeath(entity ecs.Entity, attackerNetID uint32) {
	if gw.C.Position.HasAll(entity) && gw.C.EntityKind.HasAll(entity) {
		pos := gw.C.Position.Get(entity)
		kind := gw.C.EntityKind.Get(entity)

		if table, ok := NPCDropTables[kind.Type]; ok {
			items := RollDrops(table)
			if len(items) > 0 {
				Enqueue(gw.Queue, PendingLootDrop{
					X:     pos.X,
					Y:     pos.Y,
					Items: items,
				})
				gw.Log.Log(CatLoot, "npc drop: attacker=%d pos=(%.0f,%.0f) items=%v",
					attackerNetID, pos.X, pos.Y, items)
			}
		}
	}

	gw.MarkForRemoval(entity)
}
