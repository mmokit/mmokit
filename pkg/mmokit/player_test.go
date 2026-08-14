package mmokit

import (
	"testing"

	"github.com/mlange-42/ark/ecs"

	"github.com/mmokit/mmokit/pkg/component"
	"github.com/mmokit/mmokit/pkg/engine"
)

func TestPlayer_Identity(t *testing.T) {
	sess := &engine.PlayerSession{
		ID:       42,
		ConnID:   7,
		Username: "alice",
		State:    engine.StateActive,
		Entity:   ecs.Entity{},
	}
	p := newPlayer(nil, nil, sess)

	if got := p.Username(); got != "alice" {
		t.Errorf("Username() = %q, want alice", got)
	}
	if got := p.ConnID(); got != 7 {
		t.Errorf("ConnID() = %d, want 7", got)
	}
	if got := p.State(); got != engine.StateActive {
		t.Errorf("State() = %v, want StateActive", got)
	}
}

func TestPlayer_NetIDZeroWhenNoEntity(t *testing.T) {
	sess := &engine.PlayerSession{Username: "bob", Entity: ecs.Entity{}}
	p := newPlayer(nil, nil, sess)

	if got := p.NetID(); got != 0 {
		t.Errorf("NetID() with no entity = %d, want 0", got)
	}
}

func TestPlayer_GetComponent(t *testing.T) {
	world := ecs.NewWorld()
	eng := &engine.Engine{ECS: world}
	posMap := ecs.NewMap1[component.Position](world)
	e := world.NewEntity()
	posMap.Add(e, &component.Position{X: 10, Y: 20})

	sess := &engine.PlayerSession{Username: "carol", Entity: e, State: engine.StateActive}
	p := newPlayer(nil, eng, sess)

	pos := GetComponent[component.Position](p)
	if pos == nil {
		t.Fatal("GetComponent returned nil for present component")
	}
	if pos.X != 10 || pos.Y != 20 {
		t.Errorf("GetComponent returned wrong values: %+v", pos)
	}

	if !HasComponent[component.Position](p) {
		t.Error("HasComponent returned false for present component")
	}
	if HasComponent[component.Velocity](p) {
		t.Error("HasComponent returned true for absent component")
	}

	vel := GetComponent[component.Velocity](p)
	if vel != nil {
		t.Error("GetComponent returned non-nil for absent component")
	}
}

func TestPlayer_GetComponent_DeadEntity(t *testing.T) {
	world := ecs.NewWorld()
	eng := &engine.Engine{ECS: world}
	sess := &engine.PlayerSession{Username: "dave", Entity: ecs.Entity{}}
	p := newPlayer(nil, eng, sess)

	if got := GetComponent[component.Position](p); got != nil {
		t.Errorf("GetComponent on session with zero entity = %v, want nil", got)
	}
	if HasComponent[component.Position](p) {
		t.Error("HasComponent on session with zero entity should be false")
	}
}
