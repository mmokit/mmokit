package game

import (
	gamecomp "github.com/zenion/mmokit/internal/component"
	"github.com/zenion/mmokit/pkg/mmokit"
)

// Damage is a typed cross-cell-aware message: deal damage to an entity.
// Routed via mmokit.Send; the registered handler runs on whichever cell
// owns the target, applies shield/health math, and fills the result fields
// (Dealt, Killed) before the call returns (synchronously when same-cell;
// asynchronously after one tick when cross-cell).
//
// This is a game-defined message — the framework provides the routing,
// the game owns the formula.
type Damage struct {
	// Request fields
	Amount      float32
	BonusDamage float32       // applied if target shield is depleted
	Slot        uint8         // ability slot (for visual)
	AbilityType uint8         // ability type enum (for visual)
	Source      mmokit.Entity // attacker (NetID-resolvable across cells)
	Target      mmokit.Entity // receiver (populated by handler — needed by AoI client renderer)

	// Result fields — filled by handler
	Dealt  float32
	Killed bool // target.Health.Current dropped to 0 because of this hit
}

// damageHandler is the canonical damage formula. Runs on the authoritative
// cell for the target. Mutates Health (and Shield, if present), records the
// attacker for kill-attribution, and fills msg.Dealt and msg.Killed.
//
// Auto-broadcast (Plan F Phase 2) handles dest-cell viewer animation: the
// framework pushes Damage onto target.Stage().BroadcastQueue() with target
// + Source as anchors, and NetworkSystem AoI-filters at end-of-tick.
func damageHandler(target mmokit.Entity, msg *Damage) {
	// Populate Target before any logic — the auto-broadcast (Plan F Phase 2)
	// captures the post-handler msg + serializes it for AoI viewers; the
	// client renderer needs the explicit recipient NetID since the receiver
	// is otherwise an implicit anchor on the wire.
	msg.Target = target

	h := mmokit.Get[gamecomp.Health](target)
	if h == nil || h.Current <= 0 {
		return // already dead — drop
	}

	final := msg.Amount
	if msg.BonusDamage > 0 {
		s := mmokit.Get[gamecomp.Shield](target)
		if s != nil && s.Current <= 0 {
			final += msg.BonusDamage
		}
	}

	gw := gameWorldOfEntity(target)
	if gw == nil {
		return
	}
	msg.Dealt = gw.ApplyDamage(target, final, msg.Source.NetID())
	msg.Killed = h.Current <= 0
}

// gameWorldOfEntity returns the *GameWorld bound to the entity's stage.
// Returns nil if the entity has no stage. Panics if GameWorld is not
// registered as cell-local state — that's a programmer error.
func gameWorldOfEntity(e mmokit.Entity) *GameWorld {
	stage := e.Stage()
	if stage == nil {
		return nil
	}
	return mmokit.State[GameWorld](stage)
}

// RegisterDamageVerb wires the damage handler onto every Stage owned by
// the given Process. Call once at startup (typically from GameSetup).
func RegisterDamageVerb(p *mmokit.Process) {
	mmokit.HandleAll(p, damageHandler)
}

// Damage is the game-side helper for damaging another entity. Routes the
// application via target.Send — which handles cross-cell routing transparently
// and auto-broadcasts to AoI viewers on both source and dest cells.
//
// Same-cell: handler runs synchronously; msg.Dealt is populated by the time
// this returns. Cross-cell: handler runs on the target's cell next tick.
//
// Use bonusDmg > 0 for piercing-style abilities that deal extra damage
// when the target's shield is depleted.
func (gw *GameWorld) Damage(caster, target mmokit.Entity, amount, bonusDmg float32, slot, abilityType uint8) {
	if !target.Alive() {
		return
	}

	target.Send(&Damage{
		Amount:      amount,
		BonusDamage: bonusDmg,
		Slot:        slot,
		AbilityType: abilityType,
		Source:      caster,
	})
}

// ApplyDamage applies hitscan damage to a target entity.
// Handles Fortified damage reduction, shield absorption, and death.
// Returns the actual damage dealt.
func (gw *GameWorld) ApplyDamage(target mmokit.Entity, damage float32, attackerNetID uint32) float32 {
	health := mmokit.Get[gamecomp.Health](target)
	if !target.Alive() || health == nil {
		return 0
	}
	// Dormant targets (e.g. docked players parked at a station) take no
	// damage — pins the invariant in tests.
	if mmokit.Has[mmokit.Dormant](target) {
		return 0
	}
	// Leashing NPCs are returning to their POI anchor and are invulnerable
	// during the return. Mirrors the Dormant guard above.
	if mmokit.Has[gamecomp.Leashing](target) {
		return 0
	}

	// Supercruise interaction (Albion-style travel mode).
	// - Any damage to a player in any phase stamps a 10s lockout that
	//   blocks the next Z press once they're back in Idle.
	// - Channeling phase is interrupted: Phase → Idle, ChannelRemaining
	//   cleared.
	// - Active phase: damage drains BufferHP 1:1. While buffer > 0,
	//   Health is untouched (buffer absorbs). When buffer hits 0, the
	//   player is knocked out (Phase → Idle, StatusSupercruise removed),
	//   and the same damage event does NOT spill to Health (per spec:
	//   sequential ApplyDamage calls; this single call is fully absorbed).
	//
	// Attacker-side lockout fires for ANY damage regardless of target phase,
	// so it must run BEFORE the target-side switch (the Active branch returns
	// early when the buffer absorbs the hit).
	if attackerNetID != 0 {
		if att := mmokit.EntityByNetID(gw.stage, attackerNetID); att.Alive() {
			if asc := mmokit.Get[gamecomp.Supercruise](att); asc != nil {
				asc.LockoutRemaining = max(asc.LockoutRemaining, gw.Config.SupercruiseLockoutTime)
			}
		}
	}
	if sc := mmokit.Get[gamecomp.Supercruise](target); sc != nil {
		sc.LockoutRemaining = max(sc.LockoutRemaining, gw.Config.SupercruiseLockoutTime)
		switch sc.Phase {
		case gamecomp.SupercruiseChanneling:
			sc.Phase = gamecomp.SupercruiseIdle
			sc.ChannelRemaining = 0
			gw.eng.Log.Log(CatSupercruise, "channel cancel: netID=%d reason=damage", target.NetID())
		case gamecomp.SupercruiseActive:
			sc.BufferHP -= damage
			gw.eng.Log.Log(CatSupercruise, "buffer drain: netID=%d remaining=%.1f damage=%.1f",
				target.NetID(), sc.BufferHP, damage)
			if sc.BufferHP <= 0 {
				sc.BufferHP = 0
				sc.Phase = gamecomp.SupercruiseIdle
				if se := mmokit.Get[gamecomp.StatusEffects](target); se != nil {
					for i := uint8(0); i < se.Count; i++ {
						if se.Effects[i].Type == gamecomp.StatusSupercruise {
							se.Remove(i)
							break
						}
					}
				}
				gw.eng.Log.Log(CatSupercruise, "knockout: netID=%d", target.NetID())
			}
			// Active buffer absorbs the entire damage event — return early so
			// Health is unaffected (and shield isn't refreshed/depleted by a
			// hit that supercruise ate).
			return damage
		}
	}

	// Check Fortified buff for damage reduction
	if se := mmokit.Get[gamecomp.StatusEffects](target); se != nil {
		if eff := se.Get(gamecomp.StatusFortified); eff != nil {
			damage *= (1.0 - eff.Value)
		}
	}

	totalDamage := damage
	shieldAbsorbed := float32(0)

	// Shield absorbs damage first
	if shield := mmokit.Get[gamecomp.Shield](target); shield != nil {
		shield.DamageCooldown = shield.RegenDelay
		if shield.Current > 0 {
			shieldAbsorbed = min(shield.Current, damage)
			shield.Current -= shieldAbsorbed
			damage -= shieldAbsorbed
		}
	}

	health.Current -= damage
	health.LastDamagedByNetID = attackerNetID

	// Stamp the attacker on NPCAI so the AI tick can decide whether to
	// switch targets (closer-attacker rule). The AI consumes and clears
	// the field at Engage entry — owns the time-stamp side of the contract.
	if ai := mmokit.Get[gamecomp.NPCAI](target); ai != nil {
		if attackerNetID != 0 {
			ai.LastDamageByNetID = attackerNetID
		}
		// PVE v2: track damage during Artillery cast for interrupt threshold.
		if ai.State == AIStateCast {
			ai.CastDamageAccum += totalDamage
		}
	}

	gw.eng.Log.Log(CatCombatHit, "hit: attacker=%d -> target=%d damage=%.1f (shield=%.1f) hp=%.1f/%.1f",
		attackerNetID, target.NetID(), totalDamage, shieldAbsorbed, health.Current, health.Max)

	return totalDamage
}
