package game

import (
	"testing"

	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// TestApplyDamage_DormantTargetTakesNoDamage pins the invariant that a
// Dormant entity (e.g. a docked player parked at a station) is immune to
// damage. TargetLockSystem already breaks locks on Dormant targets, but
// this guard covers any direct ApplyDamage call path that bypasses the
// lock. Without it, the docked player at station center could be killed
// by a stray laser from a passing ship.
func TestApplyDamage_DormantTargetTakesNoDamage(t *testing.T) {
	gw, _ := newTestGameWorld()

	netID := newTestShip(t, gw, 901, 100, 0)
	entity := mmokit.EntityByNetID(gw.Stage, netID)

	// Sanity: damage applies normally to a non-Dormant target.
	dealt := gw.ApplyDamage(entity, 25, 0)
	if dealt != 25 {
		t.Fatalf("non-dormant: ApplyDamage returned %.0f, want 25", dealt)
	}
	if got := mmokit.Get[gamecomp.Health](entity).Current; got != 75 {
		t.Fatalf("non-dormant: health=%.0f, want 75", got)
	}

	// Mark Dormant — subsequent damage must be a no-op.
	mmokit.Set(entity, mmokit.Dormant{})
	dealt = gw.ApplyDamage(entity, 50, 0)
	if dealt != 0 {
		t.Fatalf("dormant: ApplyDamage returned %.0f, want 0", dealt)
	}
	if got := mmokit.Get[gamecomp.Health](entity).Current; got != 75 {
		t.Fatalf("dormant: health=%.0f changed despite immunity, want 75", got)
	}
}
