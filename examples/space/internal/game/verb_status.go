package game

import (
	gamecomp "github.com/zenion/mmokit/examples/space/internal/component"
	"github.com/zenion/mmokit/pkg/mmokit"
)

// Status is a typed cross-cell-aware status-effect application message.
// Applied to a target via mmokit.Send; the registered handler runs on
// whichever cell owns the target, adds the effect to the target's
// StatusEffects component, and enqueues the cast animation event for
// viewers near the target.
type Status struct {
	EffectType  gamecomp.StatusType
	Duration    float32
	Value       float32
	Slot        uint8         // ability slot (for visual)
	AbilityType uint8         // ability type enum (for visual)
	Source      mmokit.Entity // attacker — used for kill-attribution if a DoT kills
	Target      mmokit.Entity // receiver (populated by handler — needed by AoI client renderer)
}

// statusHandler applies the status effect to the target's StatusEffects
// component. Runs on the authoritative cell.
//
// Auto-broadcast (Plan F Phase 2) handles dest-cell viewer animation: the
// framework pushes Status onto target.Stage().BroadcastQueue() with target
// + Source as anchors, and NetworkSystem AoI-filters at end-of-tick.
func statusHandler(target mmokit.Entity, msg *Status) {
	// Populate Target before any logic so the AoI broadcast carries the
	// receiver NetID for the client cast-animation renderer.
	msg.Target = target

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
}

// RegisterStatusVerb wires statusHandler onto every Stage owned by p.
// Call once at startup (typically from GameSetup).
func RegisterStatusVerb(p *mmokit.Process) {
	mmokit.HandleAll(p, statusHandler)
}

// ApplyStatus is the game-side helper for applying a status effect to
// another entity. Routes via target.Send — same-cell or cross-cell
// transparent — and the framework auto-broadcasts to AoI viewers on both
// source and dest cells.
//
// Identical pattern to gw.Damage / gw.MineExtract.
func (gw *GameWorld) ApplyStatus(caster, target mmokit.Entity, effectType gamecomp.StatusType, duration, value float32, slot, abilityType uint8) {
	if !target.Alive() {
		return
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
