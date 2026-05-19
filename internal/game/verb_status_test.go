package game

import (
	"testing"

	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/internal/item"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

func TestApplyStatus_SameCell_AddsEffect(t *testing.T) {
	gw, _ := newTestGameWorld()
	gw.stage.SetStateByName("game.GameWorld", gw)
	mmokit.Handle(gw.stage, statusHandler)

	target := newTestShipWithStatus(t, gw, 101, 100, 0)
	caster := newTestShip(t, gw, 202, 100, 0)

	casterE := mmokit.EntityByNetID(gw.stage, caster)
	targetE := mmokit.EntityByNetID(gw.stage, target)

	gw.ApplyStatus(casterE, targetE, gamecomp.StatusIonBurn, 5.0, 3.0, 0, 1)

	se := mmokit.Get[gamecomp.StatusEffects](targetE)
	if se == nil {
		t.Fatal("StatusEffects component missing on target")
	}
	if !se.Has(gamecomp.StatusIonBurn) {
		t.Fatal("StatusIonBurn not present after ApplyStatus")
	}
	eff := se.Get(gamecomp.StatusIonBurn)
	if eff.Duration != 5.0 || eff.Value != 3.0 {
		t.Fatalf("effect duration=%v value=%v, want 5.0 / 3.0", eff.Duration, eff.Value)
	}
}

// newTestShipWithStatus is the StatusEffects-aware variant of newTestShip
// (in verb_damage_test.go). Adds a StatusEffects component so statusHandler
// has somewhere to write.
func newTestShipWithStatus(t *testing.T, gw *GameWorld, netID uint32, healthMax, shieldCurrent float32) uint32 {
	t.Helper()
	id := newTestShip(t, gw, netID, healthMax, shieldCurrent)
	mmokit.Set(mmokit.EntityByNetID(gw.stage, id), gamecomp.StatusEffects{})
	return id
}

func TestEffectiveSpeedMul_NoEffects(t *testing.T) {
	got := EffectiveSpeedMul(nil)
	if got != 1.0 {
		t.Fatalf("nil StatusEffects = %v, want 1.0", got)
	}
	se := &gamecomp.StatusEffects{}
	if got := EffectiveSpeedMul(se); got != 1.0 {
		t.Fatalf("empty StatusEffects = %v, want 1.0", got)
	}
}

func TestEffectiveSpeedMul_SlowOnly(t *testing.T) {
	se := &gamecomp.StatusEffects{}
	se.Add(gamecomp.StatusEffect{Type: gamecomp.StatusSlow, Duration: 5, Value: 0.5})
	got := EffectiveSpeedMul(se)
	if absf(got-0.5) > 0.001 {
		t.Fatalf("slow mul = %v, want 0.5", got)
	}
}

func TestEffectiveSpeedMul_AfterburnerAndSlowStack(t *testing.T) {
	se := &gamecomp.StatusEffects{}
	se.Add(gamecomp.StatusEffect{Type: gamecomp.StatusAfterburner, Duration: 5, Value: 2.5})
	se.Add(gamecomp.StatusEffect{Type: gamecomp.StatusSlow, Duration: 5, Value: 0.5})
	got := EffectiveSpeedMul(se)
	want := float32(2.5 * 0.5)
	if absf(got-want) > 0.001 {
		t.Fatalf("combined mul = %v, want %v", got, want)
	}
}

// TestStatusSilence_BlocksAbilityCast — a Silenced caster's ability press
// is dropped: no cooldown consumed, no deferred action queued. Mirrors
// the production AbilitySystem.Update pre-dispatch gate.
func TestStatusSilence_BlocksAbilityCast(t *testing.T) {
	gw, _ := newTestGameWorld()
	mmokit.Handle(gw.stage, damageHandler)

	abSys := wireAbilitySystem(t, gw)

	// PlasmaCannon primary = PlasmaShot (skillshot-line). Slot Q.
	player := spawnAbilityTestPlayer(t, gw, 7001, 1, 0, 0,
		item.PlasmaCannon, 0)

	// Attach a StatusEffects component carrying StatusSilence.
	mmokit.Set(player, gamecomp.StatusEffects{})
	se := mmokit.Get[gamecomp.StatusEffects](player)
	se.Add(gamecomp.StatusEffect{Type: gamecomp.StatusSilence, Duration: 5.0, Value: 1})

	ab := mmokit.Get[gamecomp.AbilitySet](player)
	if ab == nil {
		t.Fatal("player missing AbilitySet component")
	}
	cdBefore := ab.Cooldowns[gamecomp.AbilityQ]

	// Queue a cast — production handler sets these bits when the client
	// presses Q. Silence gate must drop the cast before per-slot dispatch.
	setAbilityCast(t, player, gamecomp.AbilityQ, 20.0, 0.0)

	gw.stage.TickOne(abSys, 0.05)

	cdAfter := mmokit.Get[gamecomp.AbilitySet](player).Cooldowns[gamecomp.AbilityQ]
	if absf(cdAfter-cdBefore) > 0.001 {
		t.Fatalf("cooldown changed despite silence: %v -> %v", cdBefore, cdAfter)
	}

	// Silence gate must also have cleared the pending cast bits so the
	// press doesn't fire the moment Silence wears off.
	input := mmokit.Get[gamecomp.PlayerInput](player)
	if input.AbilityCast != 0 {
		t.Fatalf("expected AbilityCast cleared by silence gate, got %v", input.AbilityCast)
	}
}
