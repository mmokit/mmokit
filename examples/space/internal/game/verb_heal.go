package game

import (
	gamecomp "github.com/zenion/mmokit/examples/space/internal/component"
	"github.com/zenion/mmokit/pkg/mmokit"
)

// Heal is a typed cross-cell-aware healing message. Applied to a target
// via mmokit.Send; the registered handler runs on whichever cell owns the
// target, raises Health.Current (capped at Max), and is a no-op on dead
// targets — heals never revive.
//
// Mirrors Damage / Status: same dispatch model, same auto-broadcast
// convention (the framework pushes Heal onto target.Stage().BroadcastQueue()
// with target + Source as anchors so AoI viewers on both cells can play the
// heal animation).
type Heal struct {
	Amount float32
	Source mmokit.Entity // healer (NetID-resolvable across cells)
	Target mmokit.Entity // receiver (populated by handler — needed by AoI client renderer)
}

// healHandler raises the target's Health.Current toward Max. Runs on the
// authoritative cell for the target.
//
// Auto-broadcast handles dest-cell viewer animation: the framework pushes
// Heal onto target.Stage().BroadcastQueue() with target + Source as anchors,
// and NetworkSystem AoI-filters at end-of-tick.
func healHandler(target mmokit.Entity, msg *Heal) {
	// Populate Target before any logic so the AoI broadcast carries the
	// receiver NetID for the client heal-animation renderer (mirrors the
	// damage / status handlers).
	msg.Target = target

	h := mmokit.Get[gamecomp.Health](target)
	if h == nil {
		return
	}
	if h.Current <= 0 {
		// Heals don't revive dead targets — drop.
		return
	}
	prev := h.Current
	h.Current += msg.Amount
	if h.Current > h.Max {
		h.Current = h.Max
	}

	gw := gameWorldOfEntity(target)
	if gw == nil {
		return
	}
	gw.eng.Log.Log(CatCombatAbility, "heal: source=%d -> target=%d amount=%.1f (%.1f -> %.1f)",
		msg.Source.NetID(), target.NetID(), msg.Amount, prev, h.Current)
}

// RegisterHealVerb wires healHandler onto every Stage owned by the given
// Process. Call once at startup (typically from GameSetup).
func RegisterHealVerb(p *mmokit.Process) {
	mmokit.HandleAll(p, healHandler)
}

// Heal is the game-side helper for healing another entity. Routes via
// target.Send — same-cell or cross-cell transparent — and the framework
// auto-broadcasts to AoI viewers on both source and dest cells.
//
// Identical pattern to gw.Damage / gw.ApplyStatus.
func (gw *GameWorld) Heal(caster, target mmokit.Entity, amount float32) {
	if !target.Alive() {
		return
	}
	target.Send(&Heal{
		Amount: amount,
		Source: caster,
	})
}
