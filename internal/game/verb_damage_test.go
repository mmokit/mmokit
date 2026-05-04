package game

import (
	"testing"

	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// TestDamage_SameCell_AppliesViaSend exercises the gw.Damage entry point
// against a same-cell target. Same-cell dispatch is synchronous: by the
// time gw.Damage returns, the registered handler has run and Health is
// updated. The cross-cell variant is covered by
// pkg/mmokit/integration_damage_test.go (TestIntegration_Damage_CrossCell)
// against an isolated message type — proving the routing path works without
// requiring two-stage GameWorld plumbing here.
func TestDamage_SameCell_AppliesViaSend(t *testing.T) {
	gw, _ := newTestGameWorld()
	// Wire the stage→world link so damageHandler's gameWorldOfEntity helper
	// can resolve the *GameWorld from any Entity bound to gw.Stage.
	gw.Stage.SetGameWorld(gw)

	// Register the handler manually (production goes through GameSetup
	// → RegisterDamageVerb → HandleAll, but this test bypasses Process
	// and works on a single Stage directly).
	mmokit.Handle(gw.Stage, damageHandler)

	target := newTestShip(t, gw, 101, 100, 0)
	caster := newTestShip(t, gw, 202, 100, 0)

	targetE := mmokit.EntityByNetID(gw.Stage, target)
	casterE := mmokit.EntityByNetID(gw.Stage, caster)

	gw.Damage(casterE, targetE, 25, 0, 0, 1)

	// Same-cell: handler ran synchronously. Health should be 75.
	h := mmokit.Get[gamecomp.Health](targetE)
	if h == nil {
		t.Fatal("Health component missing")
	}
	if h.Current != 75 {
		t.Fatalf("Health.Current = %v, want 75", h.Current)
	}
}

// TestDamage_BonusAppliedWhenShieldDown verifies the piercing-style bonus
// damage path: bonus is added when the target's shield is at zero. The
// handler reads the live Shield component on the dest cell.
func TestDamage_BonusAppliedWhenShieldDown(t *testing.T) {
	gw, _ := newTestGameWorld()
	gw.Stage.SetGameWorld(gw)
	mmokit.Handle(gw.Stage, damageHandler)

	target := newTestShip(t, gw, 101, 100, 0) // shield depleted (Current=0)
	caster := newTestShip(t, gw, 202, 100, 0)

	casterE := mmokit.EntityByNetID(gw.Stage, caster)
	targetE := mmokit.EntityByNetID(gw.Stage, target)

	// Base 10 + bonus 15 = 25 since shield is 0.
	gw.Damage(casterE, targetE, 10, 15, 0, 1)

	if got := mmokit.Get[gamecomp.Health](targetE).Current; got != 75 {
		t.Fatalf("Health.Current = %v, want 75 (10 + 15 bonus)", got)
	}
}

// TestDamage_BonusSuppressedWhenShieldUp verifies the symmetric case: when
// shield > 0, the bonus is NOT added. Shield absorbs the base damage.
func TestDamage_BonusSuppressedWhenShieldUp(t *testing.T) {
	gw, _ := newTestGameWorld()
	gw.Stage.SetGameWorld(gw)
	mmokit.Handle(gw.Stage, damageHandler)

	target := newTestShip(t, gw, 101, 100, 50) // shield Current=50
	caster := newTestShip(t, gw, 202, 100, 0)

	casterE := mmokit.EntityByNetID(gw.Stage, caster)
	targetE := mmokit.EntityByNetID(gw.Stage, target)

	// Base 10, bonus 15. Shield up → bonus suppressed; only 10 dmg applied.
	gw.Damage(casterE, targetE, 10, 15, 0, 1)

	// Shield absorbs all 10 — Health stays at 100.
	if got := mmokit.Get[gamecomp.Health](targetE).Current; got != 100 {
		t.Fatalf("Health.Current = %v, want 100 (shield absorbs base; bonus suppressed)", got)
	}
	if got := mmokit.Get[gamecomp.Shield](targetE).Current; got != 40 {
		t.Fatalf("Shield.Current = %v, want 40 (50 - 10 base)", got)
	}
}

// newTestShip spawns a ship-like entity directly via ECS for unit-test
// purposes: NetworkID, Position, Health, and Shield. The netID is
// registered as Live in the stage's NetID index so EntityByNetID
// resolves to the new entity. Returns the netID so callers can build an
// mmokit.Entity handle. shieldCurrent==0 with Max>0 models a depleted
// shield; the helper always adds a Shield component so bonus-damage logic
// can read it.
func newTestShip(t *testing.T, gw *GameWorld, netID uint32, healthMax, shieldCurrent float32) uint32 {
	t.Helper()
	entity := gw.eng.ECS.NewEntity()
	gw.C.NetworkID.Add(entity, &mmokit.NetworkID{ID: netID})
	gw.C.Position.Add(entity, &mmokit.Position{X: 0, Y: 0})
	gw.C.Health.Add(entity, &gamecomp.Health{Current: healthMax, Max: healthMax})
	gw.C.Shield.Add(entity, &gamecomp.Shield{Current: shieldCurrent, Max: 200})
	gw.Stage.RegisterLiveNetID(netID, entity)
	gw.NetIDToEntity[netID] = entity
	return netID
}
