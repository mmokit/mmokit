package mmokit

import (
	"testing"

	ecs "github.com/mlange-42/ark/ecs"
	"github.com/zenion/mmoserver/pkg/universe"
)

type initTestNameComp struct{ Name string }
type initTestHealthComp struct{ HP float32 }
type initTestBundle struct {
	Name   *initTestNameComp
	Health *initTestHealthComp
}

func TestInit_PopulatesBundleAfterSpawn(t *testing.T) {
	mmo := New(Config{
		CellsX: 1, CellsY: 1, CellSize: 1000, TickRate: 20, AoIRadius: 100,
		Headless: true,
	})
	RegisterKind[initTestBundle](mmo, 50, "InitTest", EngineBindingsConfig{})
	mmo.Build()
	t.Cleanup(func() { mmo.Shutdown() })

	var stage *universe.WorldBase
	for _, c := range mmo.Cells {
		stage = c.Base
		break
	}
	if stage == nil {
		t.Fatal("no cells")
	}

	var captured *initTestBundle
	e := stage.SpawnEntity(Position{X: 1, Y: 2},
		WithEntityKind(50),
		Init(func(b *initTestBundle) {
			b.Name.Name = "alice"
			b.Health.HP = 42
			captured = b
		}),
	)
	if captured == nil {
		t.Fatal("Init callback never fired")
	}

	// Verify the values landed on the actual entity (not just on a stale bundle).
	nameMap := ecs.NewMap1[initTestNameComp](stage.ECSWorld())
	if nameMap.Get(e).Name != "alice" {
		t.Errorf("expected name 'alice', got %q", nameMap.Get(e).Name)
	}
	healthMap := ecs.NewMap1[initTestHealthComp](stage.ECSWorld())
	if healthMap.Get(e).HP != 42 {
		t.Errorf("expected HP 42, got %v", healthMap.Get(e).HP)
	}
}
