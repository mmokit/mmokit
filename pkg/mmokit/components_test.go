package mmokit_test

import (
	"testing"

	"github.com/mmokit/mmokit/pkg/component"
	"github.com/mmokit/mmokit/pkg/mmokit"
)

func TestGet_ReturnsComponent(t *testing.T) {
	stage, _ := newTestStage(t)
	spawnTestEntity(t, stage, 42)
	e := mmokit.EntityByNetID(stage, 42)
	pos := mmokit.Get[component.Position](e)
	if pos == nil {
		t.Fatal("Get[Position] returned nil for entity with Position")
	}
}

func TestGet_NilForMissingComponent(t *testing.T) {
	stage, _ := newTestStage(t)
	spawnTestEntity(t, stage, 42)
	e := mmokit.EntityByNetID(stage, 42)
	type Custom struct{ X int }
	c := mmokit.Get[Custom](e)
	if c != nil {
		t.Fatal("Get[Custom] should return nil — entity has no Custom component")
	}
}

func TestGet_NilForDeadEntity(t *testing.T) {
	stage, _ := newTestStage(t)
	e := mmokit.EntityByNetID(stage, 999)
	if mmokit.Get[component.Position](e) != nil {
		t.Fatal("Get on dead entity should return nil")
	}
}

func TestHas(t *testing.T) {
	stage, _ := newTestStage(t)
	spawnTestEntity(t, stage, 42)
	e := mmokit.EntityByNetID(stage, 42)
	if !mmokit.Has[component.Position](e) {
		t.Fatal("Has[Position] should be true")
	}
	type Foo struct{}
	if mmokit.Has[Foo](e) {
		t.Fatal("Has[Foo] should be false")
	}
}

func TestSet_AddsAndOverwrites(t *testing.T) {
	stage, _ := newTestStage(t)
	spawnTestEntity(t, stage, 42)
	e := mmokit.EntityByNetID(stage, 42)
	type HP struct{ Current int }
	mmokit.Set(e, HP{Current: 100})
	h := mmokit.Get[HP](e)
	if h == nil || h.Current != 100 {
		t.Fatalf("after Set(HP=100), Get returned %+v", h)
	}
	mmokit.Set(e, HP{Current: 50})
	if mmokit.Get[HP](e).Current != 50 {
		t.Fatal("Set should overwrite existing component")
	}
}
