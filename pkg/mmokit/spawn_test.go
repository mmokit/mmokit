package mmokit_test

import (
	"testing"

	"github.com/zenion/mmoserver/pkg/mmokit"
)

type testKindHealth struct{ Current, Max int32 }

func TestSpawn_ReturnsLiveEntity(t *testing.T) {
	stage, _ := newTestStage(t)
	registerTestKind(t, stage)
	e := mmokit.Spawn(stage, testKindID, mmokit.Pos{X: 10, Y: 20})
	if !e.Alive() {
		t.Fatal("Spawn returned dead entity")
	}
	if e.NetID() == 0 {
		t.Fatal("Spawn returned entity with zero NetID")
	}
}

func TestSpawn_AppliesPosition(t *testing.T) {
	stage, _ := newTestStage(t)
	registerTestKind(t, stage)
	e := mmokit.Spawn(stage, testKindID, mmokit.Pos{X: 10, Y: 20})
	x, y := e.Pos()
	if x != 10 || y != 20 {
		t.Fatalf("Pos = (%v, %v), want (10, 20)", x, y)
	}
}

func TestSpawn_AppliesComponentOverrides(t *testing.T) {
	stage, _ := newTestStage(t)
	registerTestKind(t, stage)
	e := mmokit.Spawn(stage, testKindID, mmokit.Pos{}, testKindHealth{Current: 50, Max: 100})
	h := mmokit.Get[testKindHealth](e)
	if h == nil || h.Current != 50 {
		t.Fatalf("Get[testKindHealth] = %+v, want Current=50", h)
	}
}

func TestDespawn_RemovesEntity(t *testing.T) {
	stage, eng := newTestStage(t)
	registerTestKind(t, stage)
	e := mmokit.Spawn(stage, testKindID, mmokit.Pos{})
	if !e.Alive() {
		t.Fatal("precondition")
	}
	mmokit.Despawn(e)
	eng.FlushRemovals()
	if e.Alive() {
		t.Fatal("Despawn should make entity not Alive after FlushRemovals")
	}
}
