package game

import (
	gamepb "github.com/zenion/mmoserver/gen/go/gamepb"
	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/pkg/mmokit"
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

	// Result fields — filled by handler
	Dealt  float32
	Killed bool // target.Health.Current dropped to 0 because of this hit
}

// damageHandler is the canonical damage formula. Runs on the authoritative
// cell for the target. Mutates Health (and Shield, if present), records the
// attacker for kill-attribution, and fills msg.Dealt and msg.Killed. Also
// enqueues the dest-cell AbilityCastResultMsg so viewers near the target
// see the damage event.
func damageHandler(target mmokit.Entity, msg *Damage) {
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
	msg.Dealt = gw.ApplyDamage(target.Handle(), final, msg.Source.NetID())
	msg.Killed = h.Current <= 0

	// Dest-cell animation enqueue with actual Dealt damage. NetworkSystem
	// afterSend filters by visibility (visible[CasterId] || visible[TargetId])
	// so this reaches viewers near the target on this cell. The caster's cell
	// also enqueues a placeholder via gw.Damage when target is non-local.
	mmokit.Enqueue(gw.Queue, &gamepb.AbilityCastResultMsg{
		Slot:        uint32(msg.Slot),
		Success:     true,
		TargetId:    target.NetID(),
		DamageDealt: msg.Dealt,
		CasterId:    msg.Source.NetID(),
		AbilityType: uint32(msg.AbilityType),
	})
}

// gameWorldOfEntity returns the *GameWorld bound to the entity's stage.
// Returns nil if the entity has no stage or the stage's world is not a
// *GameWorld (test stages without a world).
func gameWorldOfEntity(e mmokit.Entity) *GameWorld {
	stage := e.Stage()
	if stage == nil {
		return nil
	}
	if gw, ok := stage.GameWorld().(*GameWorld); ok {
		return gw
	}
	return nil
}

// RegisterDamageVerb wires the damage handler onto every Stage owned by
// the given Process. Call once at startup (typically from GameSetup).
func RegisterDamageVerb(p *mmokit.Process) {
	mmokit.HandleAll(p, damageHandler)
}

// Damage is the game-side helper for damaging another entity. Handles the
// caller-side animation enqueue (so the caster's client sees the cast fire
// on the same tick as the input) and routes the damage application via
// target.Send — which handles cross-cell routing transparently.
//
// Same-cell: handler runs synchronously; msg.Dealt is populated by the
// time this returns. Cross-cell: handler runs on the target's cell next
// tick; the caster's cell enqueues an AbilityCastResultMsg with placeholder
// Dealt=Amount immediately so the caster's client sees the cast fire.
//
// Use bonusDmg > 0 for piercing-style abilities that deal extra damage
// when the target's shield is depleted.
func (gw *GameWorld) Damage(caster, target mmokit.Entity, amount, bonusDmg float32, slot, abilityType uint8) {
	if !target.Alive() {
		return
	}

	// Source-cell enqueue ONLY when target is on a different cell. Same-cell
	// dispatch will fire the handler synchronously below, which enqueues
	// directly — we must avoid double-enqueue.
	if !target.Local() {
		mmokit.Enqueue(gw.Queue, &gamepb.AbilityCastResultMsg{
			Slot:        uint32(slot),
			Success:     true,
			TargetId:    target.NetID(),
			DamageDealt: amount, // placeholder; corrected by Health replication
			CasterId:    caster.NetID(),
			AbilityType: uint32(abilityType),
		})
	}

	target.Send(&Damage{
		Amount:      amount,
		BonusDamage: bonusDmg,
		Slot:        slot,
		AbilityType: abilityType,
		Source:      caster,
	})
}
