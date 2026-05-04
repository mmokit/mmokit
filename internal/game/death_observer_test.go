package game

import (
	"testing"

	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// TestDeathObserver_FiresOnceWhenHealthZero verifies the observer fires
// Killed exactly once when Health.Current crosses to zero, and doesn't
// re-fire on subsequent ticks (DeathFired idempotence).
func TestDeathObserver_FiresOnceWhenHealthZero(t *testing.T) {
	gw, _ := newTestGameWorld()
	gw.Stage.SetGameWorld(gw)

	var killedFires int
	mmokit.Handle(gw.Stage, func(target mmokit.Entity, msg *Killed) {
		killedFires++
	})
	mmokit.OnTickEach[deathObserverBundle](gw.Stage, deathObserver)

	target := newTestShip(t, gw, 101, 100, 0)
	targetE := mmokit.EntityByNetID(gw.Stage, target)
	h := mmokit.Get[gamecomp.Health](targetE)
	if h == nil {
		t.Fatal("Health missing on test ship")
	}
	h.Current = 0
	h.LastDamagedByNetID = 0 // unattributed death

	// Drive 3 ticks; Killed should fire on tick 1 and not refire.
	runTickCallbacks(t, gw.Stage, 3)

	if killedFires != 1 {
		t.Fatalf("Killed fired %d times, want exactly 1", killedFires)
	}
	if !h.DeathFired {
		t.Fatal("Health.DeathFired not set after observer fired")
	}
}

// TestDeathObserver_DoesNotRefireWhenDeathFiredTrue covers the observer
// idempotence path — DeathFired prevents a double-fire even if Health.Current
// is still <=0 from a previous tick (or a transferred mid-death entity).
func TestDeathObserver_DoesNotRefireWhenDeathFiredTrue(t *testing.T) {
	gw, _ := newTestGameWorld()
	gw.Stage.SetGameWorld(gw)

	var killedFires int
	mmokit.Handle(gw.Stage, func(target mmokit.Entity, msg *Killed) { killedFires++ })
	mmokit.OnTickEach[deathObserverBundle](gw.Stage, deathObserver)

	target := newTestShip(t, gw, 101, 100, 0)
	targetE := mmokit.EntityByNetID(gw.Stage, target)
	h := mmokit.Get[gamecomp.Health](targetE)
	h.Current = 0
	h.DeathFired = true // simulate post-transfer state

	runTickCallbacks(t, gw.Stage, 3)

	if killedFires != 0 {
		t.Fatalf("Killed fired %d times when DeathFired=true; expected 0", killedFires)
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
