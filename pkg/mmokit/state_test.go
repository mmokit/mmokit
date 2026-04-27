package mmokit

import (
	"testing"

	"github.com/zenion/mmoserver/pkg/universe"
)

type stateTestMarket struct{ Orders int }
type stateTestAI struct{ Bots int }

func TestAddState_RoundTrip(t *testing.T) {
	mmo := New(Config{
		CellsX: 1, CellsY: 1, CellSize: 1000, TickRate: 20, AoIRadius: 100,
		Headless: true,
	})
	AddState(mmo, func(*universe.WorldBase) *stateTestMarket {
		return &stateTestMarket{Orders: 5}
	})
	AddState(mmo, func(*universe.WorldBase) *stateTestAI {
		return &stateTestAI{Bots: 10}
	})
	mmo.Build()
	t.Cleanup(mmo.Shutdown)

	var stage *universe.WorldBase
	for _, c := range mmo.Cells {
		stage = c.Base
		break
	}
	if stage == nil {
		t.Fatal("no cells")
	}

	market := State[stateTestMarket](stage)
	if market.Orders != 5 {
		t.Errorf("expected 5 orders, got %d", market.Orders)
	}
	ai := State[stateTestAI](stage)
	if ai.Bots != 10 {
		t.Errorf("expected 10 bots, got %d", ai.Bots)
	}
}

func TestState_PanicsOnUnregistered(t *testing.T) {
	mmo := New(Config{
		CellsX: 1, CellsY: 1, CellSize: 1000, TickRate: 20, AoIRadius: 100,
		Headless: true,
	})
	mmo.Build()
	t.Cleanup(mmo.Shutdown)

	var stage *universe.WorldBase
	for _, c := range mmo.Cells {
		stage = c.Base
		break
	}
	if stage == nil {
		t.Fatal("no cells")
	}

	defer func() {
		if recover() == nil {
			t.Fatal("expected panic for unregistered state type")
		}
	}()
	_ = State[stateTestMarket](stage)
}

func TestAddState_PerStageInstance(t *testing.T) {
	// Verify each cell gets its OWN instance — factory runs once per cell.
	mmo := New(Config{
		CellsX: 2, CellsY: 1, CellSize: 1000, TickRate: 20, AoIRadius: 100,
		Headless: true,
	})
	var calls int
	AddState(mmo, func(*universe.WorldBase) *stateTestMarket {
		calls++
		return &stateTestMarket{Orders: calls}
	})
	mmo.Build()
	t.Cleanup(mmo.Shutdown)

	if calls != 2 {
		t.Errorf("expected factory to fire once per cell (2 cells), got %d calls", calls)
	}

	seen := make(map[*stateTestMarket]bool)
	for _, c := range mmo.Cells {
		m := State[stateTestMarket](c.Base)
		if seen[m] {
			t.Error("two cells share the same state instance")
		}
		seen[m] = true
	}
}
