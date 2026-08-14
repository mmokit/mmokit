package game

import (
	gamecomp "github.com/mmokit/mmokit/examples/space/internal/component"
	"github.com/mmokit/mmokit/pkg/mmokit"
)

// MineExtract is a typed cross-cell-aware mining-extract message. Sent
// from the caster's cell to the asteroid's authoritative cell; the
// handler subtracts from Minable, marks the asteroid for removal if
// depleted, and fills the result fields (Extracted, Depleted).
//
// Caster-side optimistic inventory add and ammunition tracking happen on
// the caster's cell BEFORE Send (matches existing behavior and gives
// instant cargo-mass feedback). msg.Extracted is the actual amount the
// asteroid had — same as the request unless the asteroid is nearly
// depleted.
type MineExtract struct {
	// Request fields
	Caster       mmokit.Entity
	Asteroid     mmokit.Entity // receiver (populated by handler — needed by AoI client renderer)
	Beam         uint8
	RequestedAmt float32 // what caster asked for

	// Result fields — filled by handler
	Extracted float32 // actual amount removed from asteroid
	Depleted  bool    // asteroid hit zero and is being removed
}

// mineExtractHandler runs on the asteroid's authoritative cell. Mutates
// Minable.Remaining, fills Extracted/Depleted, and marks the asteroid for
// removal when depleted.
func mineExtractHandler(target mmokit.Entity, msg *MineExtract) {
	// Populate Asteroid before any logic so the AoI broadcast carries the
	// receiver NetID for the client mining-beam renderer.
	msg.Asteroid = target

	minable := mmokit.Get[gamecomp.Minable](target)
	if minable == nil || minable.Remaining <= 0 {
		return
	}

	extracted := msg.RequestedAmt
	if extracted > minable.Remaining {
		extracted = minable.Remaining
	}
	minable.Remaining -= extracted
	msg.Extracted = extracted
	msg.Depleted = minable.Remaining <= 0

	if msg.Depleted {
		gw := gameWorldOfEntity(target)
		if gw != nil {
			gw.MarkForRemoval(target.Handle())
		}
	}
}

// RegisterMiningVerb wires the mineExtractHandler onto every Stage owned
// by p. Call once at startup (typically from GameSetup).
func RegisterMiningVerb(p *mmokit.Process) {
	mmokit.HandleAll(p, mineExtractHandler)
}

// MineExtract is the game-side helper for mining-extract pulses. Routes
// to the asteroid's authoritative cell via target.Send. The caller
// (executeAbility) is expected to have already done the optimistic
// caster-side inventory add and the local-replica .Remaining decrement
// before reaching here — this helper only routes the authoritative
// asteroid mutation. (Cleaning up the dual-update is a follow-up.)
func (gw *GameWorld) MineExtract(caster, asteroid mmokit.Entity, beam uint8, requested float32) {
	if !asteroid.Alive() {
		return
	}
	asteroid.Send(&MineExtract{
		Caster:       caster,
		Beam:         beam,
		RequestedAmt: requested,
	})
}
