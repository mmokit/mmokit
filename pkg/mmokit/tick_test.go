package mmokit_test

import (
	"testing"

	"github.com/zenion/mmoserver/pkg/mmokit"
)

func TestOnWorldTick_FiresOncePerTick(t *testing.T) {
	stage, _ := newTestStage(t)
	var ticks int
	var lastDt float32
	mmokit.OnWorldTick(stage, func(dt float32) {
		ticks++
		lastDt = dt
	})
	runTicks(t, stage, 3)
	if ticks != 3 {
		t.Fatalf("ticks = %d, want 3", ticks)
	}
	if lastDt <= 0 {
		t.Fatalf("dt should be positive, got %v", lastDt)
	}
}

type tickComp struct{ N int }

func TestOnTick_FiresPerEntityWithComponent(t *testing.T) {
	stage, _ := newTestStage(t)
	registerTestKind(t, stage)
	a := stage.Spawn(mmokit.Position{}, mmokit.EntityKind{Type: testKindID})
	b := stage.Spawn(mmokit.Position{}, mmokit.EntityKind{Type: testKindID})
	mmokit.Set(a, tickComp{N: 0})
	mmokit.Set(b, tickComp{N: 0})

	mmokit.OnTick[tickComp](stage, func(e mmokit.Entity, dt float32) {
		c := mmokit.Get[tickComp](e)
		c.N++
	})
	runTicks(t, stage, 5)

	if mmokit.Get[tickComp](a).N != 5 {
		t.Fatalf("a.N = %d, want 5", mmokit.Get[tickComp](a).N)
	}
	if mmokit.Get[tickComp](b).N != 5 {
		t.Fatalf("b.N = %d, want 5", mmokit.Get[tickComp](b).N)
	}
}

type tickAccum struct{ Acc float32 }
type tickRate struct{ Rate float32 }

func TestOnTickEach_BundleAccess(t *testing.T) {
	stage, _ := newTestStage(t)
	registerTestKind(t, stage)
	e := stage.Spawn(mmokit.Position{}, mmokit.EntityKind{Type: testKindID})
	mmokit.Set(e, tickAccum{Acc: 0})
	mmokit.Set(e, tickRate{Rate: 2})

	type bundle struct {
		A *tickAccum
		R *tickRate
	}
	mmokit.OnTickEach(stage, func(e mmokit.Entity, b *bundle, dt float32) {
		b.A.Acc += b.R.Rate * dt
	})
	runTicks(t, stage, 4) // 4 ticks of dt=1/20 = 0.05

	expected := float32(2 * 4 * 0.05) // = 0.4
	got := mmokit.Get[tickAccum](e).Acc
	if got != expected {
		t.Fatalf("Acc = %v, want %v", got, expected)
	}
}
