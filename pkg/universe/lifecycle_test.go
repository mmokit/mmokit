package universe

import (
	"testing"

	"github.com/zenion/mmoserver/pkg/engine"
)

// driveToActive creates a pending session via RegisterPlayer and immediately
// transitions it to StateActive. This bypasses the game-loop tick cycle while
// still exercising all OnState callbacks.
func driveToActive(t *testing.T, pm *engine.PlayerManager, connID uint32, username string) *engine.PlayerSession {
	t.Helper()
	pm.RegisterPlayer(connID, username)
	s := pm.ByConnID(connID)
	if s == nil {
		t.Fatalf("RegisterPlayer: session not found for connID %d", connID)
	}
	if err := pm.Transition(s, engine.StateActive); err != nil {
		t.Fatalf("Transition to StateActive: %v", err)
	}
	return s
}

func TestOnPlayerJoin_FiresOnStateActive(t *testing.T) {
	cfg := Config{
		CellsX: 1, CellsY: 1, CellSize: 1000, TickRate: 20, AoIRadius: 100,
		Headless: true,
	}
	p := New(cfg)

	var calls int
	p.OnPlayerJoin(func(s *engine.PlayerSession, _ *WorldBase) {
		calls++
		if s.Username != "alice" {
			t.Errorf("expected alice, got %q", s.Username)
		}
	})

	p.Build()
	t.Cleanup(func() { p.Shutdown() })

	cell := p.Cells["cell_0_0"]
	if cell == nil {
		t.Fatal("expected cell cell_0_0")
	}
	driveToActive(t, cell.Engine.Players, 1, "alice")

	if calls != 1 {
		t.Errorf("OnPlayerJoin called %d times, want 1", calls)
	}
}

func TestOnPlayerLeave_FiresOnStateActiveExit(t *testing.T) {
	cfg := Config{
		CellsX: 1, CellsY: 1, CellSize: 1000, TickRate: 20, AoIRadius: 100,
		Headless: true,
	}
	p := New(cfg)

	var leaveCalls int
	p.OnPlayerLeave(func(s *engine.PlayerSession, _ *WorldBase) {
		leaveCalls++
		if s.Username != "bob" {
			t.Errorf("expected bob, got %q", s.Username)
		}
	})

	p.Build()
	t.Cleanup(func() { p.Shutdown() })

	cell := p.Cells["cell_0_0"]
	if cell == nil {
		t.Fatal("expected cell cell_0_0")
	}
	pm := cell.Engine.Players

	s := driveToActive(t, pm, 2, "bob")

	// Exit StateActive via disconnect transition.
	if err := pm.Transition(s, engine.StateDisconnected); err != nil {
		t.Fatalf("Transition to StateDisconnected: %v", err)
	}

	if leaveCalls != 1 {
		t.Errorf("OnPlayerLeave called %d times, want 1", leaveCalls)
	}
}

func TestOnPlayerJoin_MultipleHooks(t *testing.T) {
	cfg := Config{
		CellsX: 1, CellsY: 1, CellSize: 1000, TickRate: 20, AoIRadius: 100,
		Headless: true,
	}
	p := New(cfg)

	var order []int
	p.OnPlayerJoin(func(s *engine.PlayerSession, _ *WorldBase) { order = append(order, 1) })
	p.OnPlayerJoin(func(s *engine.PlayerSession, _ *WorldBase) { order = append(order, 2) })
	p.OnPlayerJoin(func(s *engine.PlayerSession, _ *WorldBase) { order = append(order, 3) })

	p.Build()
	t.Cleanup(func() { p.Shutdown() })

	cell := p.Cells["cell_0_0"]
	if cell == nil {
		t.Fatal("expected cell cell_0_0")
	}
	driveToActive(t, cell.Engine.Players, 3, "charlie")

	if len(order) != 3 || order[0] != 1 || order[1] != 2 || order[2] != 3 {
		t.Errorf("hook order = %v, want [1 2 3]", order)
	}
}
