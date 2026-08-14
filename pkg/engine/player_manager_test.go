package engine

import (
	"errors"
	"testing"

	"github.com/mmokit/mmokit/pkg/coords"
)

func TestPlayerManager_NewSession(t *testing.T) {
	pm := NewPlayerManager()
	s := pm.createSession(42)
	if s == nil {
		t.Fatal("createSession returned nil")
	}
	if s.ConnID != 42 {
		t.Errorf("ConnID = %d, want 42", s.ConnID)
	}
	if s.State != StatePending {
		t.Errorf("State = %d, want StatePending", s.State)
	}
	if s.ID == 0 {
		t.Error("SessionID should be non-zero")
	}
	if s.StreamGeneration != 1 {
		t.Errorf("StreamGeneration = %d, want 1", s.StreamGeneration)
	}
	got := pm.ByConnID(42)
	if got != s {
		t.Error("ByConnID should return the created session")
	}
}

func TestPlayerManager_Transition(t *testing.T) {
	pm := NewPlayerManager()
	s := pm.createSession(1)
	s.Username = "alice"
	pm.byUsername["alice"] = s

	err := pm.Transition(s, StateActive)
	if err != nil {
		t.Fatalf("Transition to Active failed: %v", err)
	}
	if s.State != StateActive {
		t.Errorf("State = %d, want StateActive", s.State)
	}
}

func TestPlayerManager_InvalidTransition(t *testing.T) {
	pm := NewPlayerManager()
	s := pm.createSession(1)

	// Pending->Transferring is not a default transition
	err := pm.Transition(s, StateTransferring)
	if !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("expected ErrInvalidTransition, got %v", err)
	}
}

func TestPlayerManager_GuardBlocks(t *testing.T) {
	pm := NewPlayerManager()
	pm.AddTransition(StateTransition{
		From:  StatePending,
		To:    StateActive,
		Guard: func(s *PlayerSession) bool { return false },
	})
	s := pm.createSession(1)

	err := pm.Transition(s, StateActive)
	if !errors.Is(err, ErrTransitionGuardFailed) {
		t.Errorf("expected ErrTransitionGuardFailed, got %v", err)
	}
	if s.State != StatePending {
		t.Errorf("State should remain Pending after guard failure")
	}
}

func TestPlayerManager_Callbacks(t *testing.T) {
	pm := NewPlayerManager()
	var log []string

	pm.OnState(StatePending, StateCallbacks{
		OnExit: func(s *PlayerSession, pm *PlayerManager) {
			log = append(log, "exit-pending")
		},
	})
	pm.OnState(StateActive, StateCallbacks{
		OnEnter: func(s *PlayerSession, pm *PlayerManager) {
			log = append(log, "enter-active")
		},
	})
	pm.AddTransition(StateTransition{
		From: StatePending, To: StateActive,
		Action: func(s *PlayerSession, pm *PlayerManager) {
			log = append(log, "edge-action")
		},
	})

	s := pm.createSession(1)
	s.Username = "alice"
	pm.byUsername["alice"] = s
	pm.Transition(s, StateActive)

	want := []string{"edge-action", "exit-pending", "enter-active"}
	if len(log) != len(want) {
		t.Fatalf("log = %v, want %v", log, want)
	}
	for i := range want {
		if log[i] != want[i] {
			t.Errorf("log[%d] = %q, want %q", i, log[i], want[i])
		}
	}
}

func TestPlayerManager_Remove(t *testing.T) {
	pm := NewPlayerManager()
	s := pm.createSession(10)
	s.Username = "bob"
	pm.byUsername["bob"] = s

	var exitCalled bool
	pm.OnState(StatePending, StateCallbacks{
		OnExit: func(s *PlayerSession, pm *PlayerManager) {
			exitCalled = true
		},
	})

	pm.Remove(s)

	if !exitCalled {
		t.Error("OnExit should be called during Remove")
	}
	if pm.ByConnID(10) != nil {
		t.Error("session should be removed from byConnID")
	}
	if pm.ByUsername("bob") != nil {
		t.Error("session should be removed from byUsername")
	}
	if pm.BySessionID(s.ID) != nil {
		t.Error("session should be removed from sessions")
	}
}

func TestPlayerManager_RegisterState(t *testing.T) {
	pm := NewPlayerManager()
	docked := pm.RegisterState("docked")
	if docked < StateBuiltinEnd {
		t.Errorf("custom state %d should be >= StateBuiltinEnd (%d)", docked, StateBuiltinEnd)
	}
	docked2 := pm.RegisterState("docked")
	if docked2 != docked {
		t.Errorf("re-registering 'docked' returned %d, want %d", docked2, docked)
	}
}

func TestPlayerManager_ForEach(t *testing.T) {
	pm := NewPlayerManager()
	s1 := pm.createSession(1)
	s2 := pm.createSession(2)
	s1.Username = "a"
	s2.Username = "b"
	pm.byUsername["a"] = s1
	pm.byUsername["b"] = s2

	pm.Transition(s1, StateActive)

	var count int
	pm.ForEach(StateActive, func(s *PlayerSession) { count++ })
	if count != 1 {
		t.Errorf("ForEach(Active) count = %d, want 1", count)
	}

	pm.ForEach(StatePending, func(s *PlayerSession) { count++ })
	if count != 2 {
		t.Errorf("ForEach(Pending) count = %d, want 2 (1 active + 1 pending)", count)
	}
}

func TestPlayerManager_Count(t *testing.T) {
	pm := NewPlayerManager()
	if pm.Count(StatePending) != 0 {
		t.Error("empty manager should have 0 pending")
	}
	pm.createSession(1)
	pm.createSession(2)
	if pm.Count(StatePending) != 2 {
		t.Errorf("Count(Pending) = %d, want 2", pm.Count(StatePending))
	}
}

func TestPlayerManager_ConnectedCount(t *testing.T) {
	pm := NewPlayerManager()
	pm.createSession(1)
	pm.createSession(2)
	if pm.ConnectedCount() != 2 {
		t.Errorf("ConnectedCount = %d, want 2", pm.ConnectedCount())
	}
}

func TestPlayerManager_ByUsername(t *testing.T) {
	pm := NewPlayerManager()
	s := pm.createSession(5)
	s.Username = "charlie"
	pm.byUsername["charlie"] = s

	got := pm.ByUsername("charlie")
	if got != s {
		t.Error("ByUsername should find the session")
	}
	if pm.ByUsername("nobody") != nil {
		t.Error("ByUsername should return nil for unknown")
	}
}

func TestPlayerManager_ForEachConnected(t *testing.T) {
	eng := newTestEngine()
	pm := eng.Players

	// pending with connID — should be excluded
	pending := pm.createSession(1)
	_ = pending

	// active with connID — should be visited
	active := pm.createSession(2)
	active.Username = "alice"
	pm.byUsername["alice"] = active
	if err := pm.Transition(active, StateActive); err != nil {
		t.Fatalf("transition to active: %v", err)
	}

	// custom state with connID — should be visited (not pending, has connID)
	customState := pm.RegisterState("custom")
	pm.AddTransition(StateTransition{From: StateActive, To: customState})
	custom := pm.createSession(3)
	custom.Username = "bob"
	pm.byUsername["bob"] = custom
	if err := pm.Transition(custom, StateActive); err != nil {
		t.Fatalf("transition to active: %v", err)
	}
	if err := pm.Transition(custom, customState); err != nil {
		t.Fatalf("transition to custom: %v", err)
	}

	// disconnected (connID == 0) — should be excluded
	disconn := pm.createSession(4)
	disconn.Username = "carol"
	pm.byUsername["carol"] = disconn
	if err := pm.Transition(disconn, StateActive); err != nil {
		t.Fatalf("transition to active: %v", err)
	}
	// simulate disconnect: remove from byConnID and zero out connID
	delete(pm.byConnID, disconn.ConnID)
	disconn.ConnID = 0
	if err := pm.Transition(disconn, StateDisconnected); err != nil {
		t.Fatalf("transition to disconnected: %v", err)
	}

	var visited []*PlayerSession
	pm.ForEachConnected(func(s *PlayerSession) {
		visited = append(visited, s)
	})

	if len(visited) != 2 {
		t.Fatalf("ForEachConnected visited %d sessions, want 2", len(visited))
	}
	for _, s := range visited {
		if s.State == StatePending {
			t.Error("ForEachConnected should not visit pending sessions")
		}
		if s.ConnID == 0 {
			t.Error("ForEachConnected should not visit sessions with connID == 0")
		}
	}
}

func TestPlayerManager_RegisterTransferSession(t *testing.T) {
	pm := NewPlayerManager()
	s := pm.createSession(99)

	pm.RegisterTransferSession(99, "transferplayer")

	if s.Username != "transferplayer" {
		t.Errorf("Username = %q, want 'transferplayer'", s.Username)
	}
	if !s.isTransfer {
		t.Error("session should be flagged as transfer")
	}
	if pm.ByUsername("transferplayer") != s {
		t.Error("should be indexed by username")
	}
}

func TestPlayerSession_SpawnLocationField(t *testing.T) {
	s := &PlayerSession{SpawnLocation: coords.Location{X: 100, Y: 200, Facing: 1.57, Tag: "bank"}}
	if s.SpawnLocation.X != 100 || s.SpawnLocation.Y != 200 {
		t.Fatalf("SpawnLocation not retained: %+v", s.SpawnLocation)
	}
	if s.SpawnLocation.Facing != 1.57 || s.SpawnLocation.Tag != "bank" {
		t.Fatalf("SpawnLocation facing/tag not retained: %+v", s.SpawnLocation)
	}
}
