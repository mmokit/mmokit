package game

import (
	"testing"

	gamecomp "github.com/zenion/mmokit/internal/component"
	"github.com/zenion/mmokit/pkg/mmokit"
	pkguniverse "github.com/zenion/mmokit/pkg/universe"
)

// TestDeathObserver_FiresOnceWhenHealthZero verifies the observer fires
// Killed exactly once when Health.Current crosses to zero, and doesn't
// re-fire on subsequent ticks (DeathFired idempotence).
func TestDeathObserver_FiresOnceWhenHealthZero(t *testing.T) {
	gw, _ := newTestGameWorld()
	gw.stage.SetStateByName("game.GameWorld", gw)

	var killedFires int
	mmokit.Handle(gw.stage, func(target mmokit.Entity, msg *Killed) {
		killedFires++
	})
	mmokit.OnTickEach(gw.stage, deathObserver)

	target := newTestShip(t, gw, 101, 100, 0)
	targetE := mmokit.EntityByNetID(gw.stage, target)
	h := mmokit.Get[gamecomp.Health](targetE)
	if h == nil {
		t.Fatal("Health missing on test ship")
	}
	h.Current = 0
	h.LastDamagedByNetID = 0 // unattributed death

	// Drive 3 ticks; Killed should fire on tick 1 and not refire.
	runTickCallbacks(t, gw.stage, 3)

	if killedFires != 1 {
		t.Fatalf("Killed fired %d times, want exactly 1", killedFires)
	}
	if !h.DeathFired {
		t.Fatal("Health.DeathFired not set after observer fired")
	}
}

// TestHealth_DeathFiredAndKillerSurviveTransferCodec pins that the cell-to-cell
// transfer codec serializes the new Health fields. Without this, an entity
// that crosses a cell boundary mid-death would have its observer re-fire on
// the destination cell, double-spawning loot or double-crediting kills.
//
// The full transfer + observer integration test (entity crosses cells then
// is observed for non-firing) requires two-stage GameWorld scaffolding that
// doesn't exist in this package; the codec roundtrip is the load-bearing
// claim — Health.DeathFired and LastDamagedByNetID must survive the wire.
func TestHealth_DeathFiredAndKillerSurviveTransferCodec(t *testing.T) {
	in := gamecomp.Health{Current: 0, Max: 100, LastDamagedByNetID: 42, DeathFired: true}
	data, err := pkguniverse.ReflectMarshal(&in)
	if err != nil {
		t.Fatalf("ReflectMarshal: %v", err)
	}
	var out gamecomp.Health
	if err := pkguniverse.ReflectUnmarshal(data, &out); err != nil {
		t.Fatalf("ReflectUnmarshal: %v", err)
	}
	if out != in {
		t.Fatalf("Health roundtrip: got %+v, want %+v", out, in)
	}
}

// runTickCallbacks drives the registered tick callbacks N times manually.
// Mirrors pkg/mmokit/testutil_test.go::runTicks; defined locally because the
// mmokit-side helper is package-private to mmokit_test.
func runTickCallbacks(t *testing.T, stage *mmokit.Stage, n int) {
	t.Helper()
	const dt = float32(1.0 / 20.0)
	for range n {
		for _, fn := range stage.TickCallbacks() {
			fn(dt)
		}
	}
}
