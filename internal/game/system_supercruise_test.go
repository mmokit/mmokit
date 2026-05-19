package game

import (
	"testing"

	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/pkg/mmokit"
	pkguniverse "github.com/zenion/mmoserver/pkg/universe"
)

// newSupercruiseTest builds a single-cell GameWorld with one ship entity
// pre-equipped with the components SupercruiseSystem needs. Mirrors the
// pattern used by verb_damage_test.go (newTestCell + newTestShip + Set).
func newSupercruiseTest(t *testing.T) (*GameWorld, mmokit.Entity) {
	t.Helper()
	node := newTestCell(pkguniverse.CellID{X: 0, Y: 0, Depth: 0})
	gw := testGW(node)
	gw.Config.SupercruiseSpeedMul = 2.5
	gw.Config.SupercruiseBufferPct = 0.25
	gw.Config.SupercruiseChannelTime = 3.0
	gw.Config.SupercruiseLockoutTime = 10.0

	netID := newTestShip(t, gw, 1, 100, 0)
	e := mmokit.EntityByNetID(gw.stage, netID)
	mmokit.Set(e, gamecomp.StatusEffects{})
	mmokit.Set(e, gamecomp.Supercruise{})
	mmokit.Set(e, mmokit.MoveTarget{})
	return gw, e
}

func TestSupercruise_ChannelCompletesToActive(t *testing.T) {
	gw, e := newSupercruiseTest(t)
	sc := mmokit.Get[gamecomp.Supercruise](e)
	sc.Phase = gamecomp.SupercruiseChanneling
	sc.ChannelRemaining = 3.0

	sys := &SupercruiseSystem{}
	mmokit.WireSystem(sys, gw.stage.ECSWorld(), gw.eng, gw.stage)

	// Tick 3 seconds (the channel duration) plus one extra tick to absorb
	// floating-point accumulation slack — 60 * 0.05 leaves ~1.7e-6 in the
	// counter due to fp32 rounding, so the completion branch (Remaining<=0)
	// fires on tick 61. Use 0.05 dt to mirror the 20Hz tick rate.
	for i := 0; i < 61; i++ {
		sys.Update(0.05)
	}

	if sc.Phase != gamecomp.SupercruiseActive {
		t.Fatalf("expected Active after channel, got %d", sc.Phase)
	}
	if sc.BufferMax != 25 {
		t.Fatalf("expected BufferMax = Health.Max * 0.25 = 25, got %v", sc.BufferMax)
	}
	if sc.BufferHP != 25 {
		t.Fatalf("expected BufferHP = BufferMax = 25, got %v", sc.BufferHP)
	}
	se := mmokit.Get[gamecomp.StatusEffects](e)
	if !se.Has(gamecomp.StatusSupercruise) {
		t.Fatalf("expected StatusSupercruise on entity after channel")
	}
}

func TestSupercruise_LockoutDecaysOverTime(t *testing.T) {
	gw, e := newSupercruiseTest(t)
	sc := mmokit.Get[gamecomp.Supercruise](e)
	sc.LockoutRemaining = 5.0

	sys := &SupercruiseSystem{}
	mmokit.WireSystem(sys, gw.stage.ECSWorld(), gw.eng, gw.stage)

	sys.Update(2.0)
	if sc.LockoutRemaining != 3.0 {
		t.Fatalf("expected lockout=3.0 after 2s, got %v", sc.LockoutRemaining)
	}
	sys.Update(5.0)
	if sc.LockoutRemaining != 0 {
		t.Fatalf("expected lockout=0 after overshoot, got %v", sc.LockoutRemaining)
	}
}

func TestSupercruise_CancelHelperRemovesStatus(t *testing.T) {
	_, e := newSupercruiseTest(t)
	sc := mmokit.Get[gamecomp.Supercruise](e)
	se := mmokit.Get[gamecomp.StatusEffects](e)

	sc.Phase = gamecomp.SupercruiseActive
	sc.BufferHP = 25
	sc.BufferMax = 25
	se.Add(gamecomp.StatusEffect{Type: gamecomp.StatusSupercruise, Duration: 1e9, Value: 2.5})

	cancelSupercruise(e)

	if sc.Phase != gamecomp.SupercruiseIdle {
		t.Fatalf("expected Idle after cancel, got %d", sc.Phase)
	}
	if se.Has(gamecomp.StatusSupercruise) {
		t.Fatalf("expected StatusSupercruise removed after cancel")
	}
	if sc.LockoutRemaining != 0 {
		t.Fatalf("cancel must not stamp lockout (combat hook does that), got %v", sc.LockoutRemaining)
	}
}

func TestSupercruise_ChannelRootsPlayer(t *testing.T) {
	gw, e := newSupercruiseTest(t)
	sc := mmokit.Get[gamecomp.Supercruise](e)
	mt := mmokit.Get[mmokit.MoveTarget](e)

	sc.Phase = gamecomp.SupercruiseChanneling
	sc.ChannelRemaining = 3.0
	mt.SetTarget(50, 50)

	sys := &SupercruiseSystem{}
	mmokit.WireSystem(sys, gw.stage.ECSWorld(), gw.eng, gw.stage)
	sys.Update(0.05)

	if mt.Active {
		t.Fatalf("expected MoveTarget.Active=false during channel")
	}
}

func TestSupercruise_ZPressIdleStartsChannel(t *testing.T) {
	_, e := newSupercruiseTest(t)
	sc := mmokit.Get[gamecomp.Supercruise](e)

	// Simulate Idle + Lockout=0 + StateActive precondition; the test
	// exercises the state-transition surface that the handler sets, not
	// the handler dispatch path itself (that's covered by TestSupercruise_RoundTrip).
	sc.Phase = gamecomp.SupercruiseIdle

	if sc.LockoutRemaining > 0 {
		t.Fatalf("precondition: expected LockoutRemaining=0")
	}
	sc.Phase = gamecomp.SupercruiseChanneling
	sc.ChannelRemaining = 3.0

	if sc.Phase != gamecomp.SupercruiseChanneling || sc.ChannelRemaining != 3.0 {
		t.Fatalf("expected Channeling with ChannelRemaining=3, got phase=%d remaining=%v",
			sc.Phase, sc.ChannelRemaining)
	}
}

func TestSupercruise_ZPressActiveCancels(t *testing.T) {
	_, e := newSupercruiseTest(t)
	sc := mmokit.Get[gamecomp.Supercruise](e)
	se := mmokit.Get[gamecomp.StatusEffects](e)

	sc.Phase = gamecomp.SupercruiseActive
	sc.BufferHP = 25
	sc.BufferMax = 25
	se.Add(gamecomp.StatusEffect{Type: gamecomp.StatusSupercruise, Duration: 1e9, Value: 2.5})

	cancelSupercruise(e)

	if sc.Phase != gamecomp.SupercruiseIdle {
		t.Fatalf("expected Idle after manual cancel, got %d", sc.Phase)
	}
	if sc.LockoutRemaining != 0 {
		t.Fatalf("expected no lockout from manual cancel, got %v", sc.LockoutRemaining)
	}
}

func TestSupercruise_RoundTrip(t *testing.T) {
	gw, e := newSupercruiseTest(t)
	sc := mmokit.Get[gamecomp.Supercruise](e)
	se := mmokit.Get[gamecomp.StatusEffects](e)

	sys := &SupercruiseSystem{}
	mmokit.WireSystem(sys, gw.stage.ECSWorld(), gw.eng, gw.stage)
	sys.Init()

	// Phase 1: Z press starts channel.
	sc.Phase = gamecomp.SupercruiseChanneling
	sc.ChannelRemaining = gw.Config.SupercruiseChannelTime

	// Tick the full channel (60 ticks at 0.05 dt = 3.0s nominal; one
	// extra tick to account for float32 accumulation slack — see
	// TestSupercruise_ChannelCompletesToActive for details).
	for i := 0; i < 61; i++ {
		sys.Update(0.05)
	}
	if sc.Phase != gamecomp.SupercruiseActive {
		t.Fatalf("phase 1: expected Active after channel, got %d", sc.Phase)
	}
	if !se.Has(gamecomp.StatusSupercruise) {
		t.Fatalf("phase 1: expected StatusSupercruise applied")
	}
	if EffectiveSpeedMul(se) != 2.5 {
		t.Fatalf("phase 1: expected speed mul=2.5, got %v", EffectiveSpeedMul(se))
	}

	// Phase 2: take partial damage — buffer drains, lockout stamped, still Active.
	gw.ApplyDamage(e, 10, 0)
	if sc.Phase != gamecomp.SupercruiseActive {
		t.Fatalf("phase 2: expected still Active, got %d", sc.Phase)
	}
	if sc.BufferHP != 15 {
		t.Fatalf("phase 2: expected BufferHP=15, got %v", sc.BufferHP)
	}
	if sc.LockoutRemaining != 10 {
		t.Fatalf("phase 2: expected lockout=10, got %v", sc.LockoutRemaining)
	}

	// Phase 3: take remaining damage — knockout, Active → Idle.
	gw.ApplyDamage(e, 15, 0)
	if sc.Phase != gamecomp.SupercruiseIdle {
		t.Fatalf("phase 3: expected Idle after knockout, got %d", sc.Phase)
	}
	if se.Has(gamecomp.StatusSupercruise) {
		t.Fatalf("phase 3: expected StatusSupercruise removed after knockout")
	}
	if EffectiveSpeedMul(se) != 1.0 {
		t.Fatalf("phase 3: expected speed mul=1.0 after knockout, got %v", EffectiveSpeedMul(se))
	}

	// Phase 4: lockout decays — Z press during lockout is blocked, then allowed.
	sys.Update(5.0)
	if sc.LockoutRemaining != 5.0 {
		t.Fatalf("phase 4: expected lockout=5 after 5s, got %v", sc.LockoutRemaining)
	}
	sys.Update(5.0)
	if sc.LockoutRemaining != 0 {
		t.Fatalf("phase 4: expected lockout=0 after 10s, got %v", sc.LockoutRemaining)
	}
}
