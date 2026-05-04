package game

import (
	gamepb "github.com/zenion/mmoserver/gen/go/gamepb"
	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// Status is a typed cross-cell-aware status-effect application message.
// Applied to a target via mmokit.Send; the registered handler runs on
// whichever cell owns the target, adds the effect to the target's
// StatusEffects component, and enqueues the cast animation event for
// viewers near the target.
//
// This is the typed replacement for the legacy ActionStatusEffect /
// StatusEffectAction codec.
type Status struct {
	EffectType  gamecomp.StatusType
	Duration    float32
	Value       float32
	Slot        uint8         // ability slot (for visual)
	AbilityType uint8         // ability type enum (for visual)
	Source      mmokit.Entity // attacker — used for kill-attribution if a DoT kills
}

// statusHandler applies the status effect to the target's StatusEffects
// component. Runs on the authoritative cell. Also enqueues the dest-cell
// AbilityCastResultMsg so viewers near the target see the cast animation.
func statusHandler(target mmokit.Entity, msg *Status) {
	se := mmokit.Get[gamecomp.StatusEffects](target)
	if se == nil {
		return
	}

	se.Add(gamecomp.StatusEffect{
		Type:     msg.EffectType,
		Duration: msg.Duration,
		Value:    msg.Value,
		Source:   msg.Source.Handle(),
	})

	gw := gameWorldOfEntity(target)
	if gw == nil {
		return
	}
	gw.eng.Log.Log(CatCombatAbility, "status applied: source=%d -> target=%d type=%d dur=%.1f val=%.1f",
		msg.Source.NetID(), target.NetID(), msg.EffectType, msg.Duration, msg.Value)

	mmokit.Enqueue(gw.Queue, &gamepb.AbilityCastResultMsg{
		Slot:        uint32(msg.Slot),
		Success:     true,
		TargetId:    target.NetID(),
		CasterId:    msg.Source.NetID(),
		AbilityType: uint32(msg.AbilityType),
	})
}

// RegisterStatusVerb wires statusHandler onto every Stage owned by p.
// Call once at startup (typically from GameSetup).
func RegisterStatusVerb(p *mmokit.Process) {
	mmokit.HandleAll(p, statusHandler)
}

// ApplyStatus is the game-side helper for applying a status effect to
// another entity. Routes via target.Send — same-cell or cross-cell
// transparent. Source-cell animation enqueue happens here for non-local
// targets (so the caster's client sees the cast fire on the same tick).
//
// Identical pattern to gw.Damage / gw.MineExtract.
func (gw *GameWorld) ApplyStatus(caster, target mmokit.Entity, effectType gamecomp.StatusType, duration, value float32, slot, abilityType uint8) {
	if !target.Alive() {
		return
	}

	if !target.Local() {
		mmokit.Enqueue(gw.Queue, &gamepb.AbilityCastResultMsg{
			Slot:        uint32(slot),
			Success:     true,
			TargetId:    target.NetID(),
			CasterId:    caster.NetID(),
			AbilityType: uint32(abilityType),
		})
	}

	target.Send(&Status{
		EffectType:  effectType,
		Duration:    duration,
		Value:       value,
		Slot:        slot,
		AbilityType: abilityType,
		Source:      caster,
	})
}
