package mmokit_test

import (
	"testing"

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
