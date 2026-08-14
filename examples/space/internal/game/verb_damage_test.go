package game

import (
	"testing"

	"github.com/mlange-42/ark/ecs"

	gamecomp "github.com/zenion/mmokit/examples/space/internal/component"
	"github.com/zenion/mmokit/pkg/mmokit"
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
	// can resolve the *GameWorld from any Entity bound to gw.stage.
	gw.stage.SetStateByName("game.GameWorld", gw)

	// Register the handler manually (production goes through GameSetup
	// → RegisterDamageVerb → HandleAll, but this test bypasses Process
	// and works on a single Stage directly).
	mmokit.Handle(gw.stage, damageHandler)

	target := newTestShip(t, gw, 101, 100, 0)
	caster := newTestShip(t, gw, 202, 100, 0)

	targetE := mmokit.EntityByNetID(gw.stage, target)
	casterE := mmokit.EntityByNetID(gw.stage, caster)

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
	gw.stage.SetStateByName("game.GameWorld", gw)
	mmokit.Handle(gw.stage, damageHandler)

	target := newTestShip(t, gw, 101, 100, 0) // shield depleted (Current=0)
	caster := newTestShip(t, gw, 202, 100, 0)

	casterE := mmokit.EntityByNetID(gw.stage, caster)
	targetE := mmokit.EntityByNetID(gw.stage, target)

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
	gw.stage.SetStateByName("game.GameWorld", gw)
	mmokit.Handle(gw.stage, damageHandler)

	target := newTestShip(t, gw, 101, 100, 50) // shield Current=50
	caster := newTestShip(t, gw, 202, 100, 0)

	casterE := mmokit.EntityByNetID(gw.stage, caster)
	targetE := mmokit.EntityByNetID(gw.stage, target)

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
	w := gw.stage.ECSWorld()
	netIDMapper := ecs.NewMap1[mmokit.NetworkID](w)
	handle := w.NewEntity()
	netIDMapper.Add(handle, &mmokit.NetworkID{ID: netID})
	gw.stage.RegisterLiveNetID(netID, handle)
	e := mmokit.EntityByNetID(gw.stage, netID)
	mmokit.Set(e, mmokit.Position{X: 0, Y: 0})
	mmokit.Set(e, gamecomp.Health{Current: healthMax, Max: healthMax})
	mmokit.Set(e, gamecomp.Shield{Current: shieldCurrent, Max: 200})
	return netID
}

// newTestNPC composes on newTestShip and adds an EntityKind component
// (needed by the NPC drop path in killedHandler / handleNPCKilled).
// kindType maps to gamecomp.TypeXxx; pass an arbitrary value when the
// test doesn't depend on a real drop table.
func newTestNPC(t *testing.T, gw *GameWorld, netID uint32, kindType uint8) uint32 {
	t.Helper()
	id := newTestShip(t, gw, netID, 100, 0)
	mmokit.Set(mmokit.EntityByNetID(gw.stage, id), mmokit.EntityKind{Type: kindType})
	return id
}

// newTestPlayerShip composes on newTestShip and adds PlayerConn for the
// kill-credit path. Registers the player session in PlayerDB so the
// killCreditHandler can find it via Players.ByConnID. ConnID equals
// netID — the unit tests just need a stable association.
//
// Production gateway-side login sets s.UserID before any cell hook
// fires. Tests skip the gateway entirely, so we mint a stable
// deterministic UUID (uuid.NewSHA1 on username) and call PlayerDB.Bind
// here to populate the in-memory cache the way real OnEnter would.
// Without that, downstream `playerDB.Get(username)` lookups in tests
// would miss because nothing else creates the cache entry.
func newTestPlayerShip(t *testing.T, gw *GameWorld, netID uint32, username string) uint32 {
	t.Helper()
	connID := netID
	id := newTestShip(t, gw, netID, 100, 0)
	mmokit.Set(mmokit.EntityByNetID(gw.stage, id), mmokit.PlayerConn{ConnID: connID})
	gw.Players.RegisterPlayer(connID, username)
	s := gw.Players.ByConnID(connID)
	if s != nil {
		s.UserID = testUserID(username)
		gw.PlayerDB.Bind(s)
	}
	return id
}
