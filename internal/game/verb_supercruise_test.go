package game

import (
	"testing"

	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

func TestSupercruise_DamageDuringChannelCancelsAndLocksOut(t *testing.T) {
	gw, e := newSupercruiseTest(t)
	sc := mmokit.Get[gamecomp.Supercruise](e)

	sc.Phase = gamecomp.SupercruiseChanneling
	sc.ChannelRemaining = 2.0

	gw.ApplyDamage(e, 5, 0)

	if sc.Phase != gamecomp.SupercruiseIdle {
		t.Fatalf("expected Idle after damage during channel, got %d", sc.Phase)
	}
	if sc.LockoutRemaining != 10 {
		t.Fatalf("expected lockout=10s after damage during channel, got %v", sc.LockoutRemaining)
	}
}

func TestSupercruise_DamageDuringActiveDrainsBuffer(t *testing.T) {
	gw, e := newSupercruiseTest(t)
	sc := mmokit.Get[gamecomp.Supercruise](e)
	h := mmokit.Get[gamecomp.Health](e)

	sc.Phase = gamecomp.SupercruiseActive
	sc.BufferMax = 25
	sc.BufferHP = 25

	gw.ApplyDamage(e, 10, 0)

	if sc.Phase != gamecomp.SupercruiseActive {
		t.Fatalf("expected still Active after partial drain, got %d", sc.Phase)
	}
	if sc.BufferHP != 15 {
		t.Fatalf("expected BufferHP=15 (25-10), got %v", sc.BufferHP)
	}
	if h.Current != 100 {
		t.Fatalf("expected Health unchanged while buffer absorbs, got %v", h.Current)
	}
	if sc.LockoutRemaining != 10 {
		t.Fatalf("expected lockout=10s after damage in Active, got %v", sc.LockoutRemaining)
	}
}

func TestSupercruise_BufferDrainKnockout(t *testing.T) {
	gw, e := newSupercruiseTest(t)
	sc := mmokit.Get[gamecomp.Supercruise](e)
	h := mmokit.Get[gamecomp.Health](e)
	se := mmokit.Get[gamecomp.StatusEffects](e)

	sc.Phase = gamecomp.SupercruiseActive
	sc.BufferMax = 25
	sc.BufferHP = 25
	se.Add(gamecomp.StatusEffect{Type: gamecomp.StatusSupercruise, Duration: 1e9, Value: 2.5})

	// Apply damage equal to buffer — should knock out, no Health loss.
	gw.ApplyDamage(e, 25, 0)

	if sc.Phase != gamecomp.SupercruiseIdle {
		t.Fatalf("expected Idle after buffer drain, got %d", sc.Phase)
	}
	if sc.BufferHP != 0 {
		t.Fatalf("expected BufferHP=0, got %v", sc.BufferHP)
	}
	if se.Has(gamecomp.StatusSupercruise) {
		t.Fatalf("expected StatusSupercruise removed after knockout")
	}
	if h.Current != 100 {
		t.Fatalf("expected Health unchanged on exact buffer drain, got %v", h.Current)
	}
	if sc.LockoutRemaining != 10 {
		t.Fatalf("expected lockout=10s after knockout, got %v", sc.LockoutRemaining)
	}
}

func TestSupercruise_DamageInIdleStartsLockout(t *testing.T) {
	gw, e := newSupercruiseTest(t)
	sc := mmokit.Get[gamecomp.Supercruise](e)

	sc.Phase = gamecomp.SupercruiseIdle

	gw.ApplyDamage(e, 5, 0)

	if sc.Phase != gamecomp.SupercruiseIdle {
		t.Fatalf("expected still Idle, got %d", sc.Phase)
	}
	if sc.LockoutRemaining != 10 {
		t.Fatalf("expected lockout=10s after damage in Idle, got %v", sc.LockoutRemaining)
	}
}

func TestSupercruise_AttackerLockoutOnDealtDamage(t *testing.T) {
	gw, victim := newSupercruiseTest(t)

	// Spawn a second ship as the attacker (raw entity creation to avoid
	// the kinded ShipBundle invariant check; mirrors newTestShip's pattern).
	attackerNetID := uint32(2)
	newTestShip(t, gw, attackerNetID, 100, 0)
	attacker := mmokit.EntityByNetID(gw.stage, attackerNetID)
	mmokit.Set(attacker, gamecomp.StatusEffects{})
	mmokit.Set(attacker, gamecomp.Supercruise{})

	attackerSC := mmokit.Get[gamecomp.Supercruise](attacker)

	gw.ApplyDamage(victim, 5, attackerNetID)

	if attackerSC.LockoutRemaining != 10 {
		t.Fatalf("expected attacker lockout=10s, got %v", attackerSC.LockoutRemaining)
	}
}
