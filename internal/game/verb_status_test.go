package game

import (
	"testing"

	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

func TestApplyStatus_SameCell_AddsEffect(t *testing.T) {
	gw, _ := newTestGameWorld()
	gw.Stage.SetGameWorld(gw)
	mmokit.Handle(gw.Stage, statusHandler)

	target := newTestShipWithStatus(t, gw, 101, 100, 0)
	caster := newTestShip(t, gw, 202, 100, 0)

	casterE := mmokit.EntityByNetID(gw.Stage, caster)
	targetE := mmokit.EntityByNetID(gw.Stage, target)

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
	entity := gw.NetIDToEntity[id]
	gw.C.StatusEffects.Add(entity, &gamecomp.StatusEffects{})
	return id
}
