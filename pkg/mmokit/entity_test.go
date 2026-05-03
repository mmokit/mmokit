package mmokit_test

import (
	"testing"

	"github.com/mlange-42/ark/ecs"
	"github.com/zenion/mmoserver/pkg/component"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

func TestEntity_ZeroValueIsNotAlive(t *testing.T) {
	var e mmokit.Entity
	if e.Alive() {
		t.Fatal("zero-value Entity should not be Alive")
	}
	if e.NetID() != 0 {
		t.Fatalf("zero-value NetID = %d, want 0", e.NetID())
	}
}

func TestEntityByNetID_ResolvesAlive(t *testing.T) {
	stage, _ := newTestStage(t)
	e := spawnTestEntity(t, stage, 42)
	h := mmokit.EntityByNetID(stage, 42)
	if !h.Alive() {
		t.Fatal("EntityByNetID(42).Alive() = false, want true")
	}
	if h.NetID() != 42 {
		t.Fatalf("NetID = %d, want 42", h.NetID())
	}
	_ = e
}

func TestEntityByNetID_UnknownReturnsDead(t *testing.T) {
	stage, _ := newTestStage(t)
	h := mmokit.EntityByNetID(stage, 999)
	if h.Alive() {
		t.Fatal("unknown netID should not be Alive")
	}
}

func TestEntity_PosReturnsPosition(t *testing.T) {
	stage, _ := newTestStage(t)
	h := spawnTestEntity(t, stage, 42)
	posMap := ecs.NewMap1[component.Position](stage.ECSWorld())
	posMap.Get(h).X = 50
	posMap.Get(h).Y = -25
	e := mmokit.EntityByNetID(stage, 42)
	x, y := e.Pos()
	if x != 50 || y != -25 {
		t.Fatalf("Pos = (%v, %v), want (50, -25)", x, y)
	}
}

func TestEntity_PosOnDeadIsZero(t *testing.T) {
	stage, _ := newTestStage(t)
	e := mmokit.EntityByNetID(stage, 999)
	x, y := e.Pos()
	if x != 0 || y != 0 {
		t.Fatalf("dead Pos = (%v, %v), want (0, 0)", x, y)
	}
}

func TestEntity_LocalReportsAuthority(t *testing.T) {
	stage, _ := newTestStage(t)
	spawnTestEntity(t, stage, 42)
	e := mmokit.EntityByNetID(stage, 42)
	if !e.Local() {
		t.Fatal("local Live entity should report Local()==true")
	}
}
