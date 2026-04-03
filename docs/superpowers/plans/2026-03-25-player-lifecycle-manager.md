# Player Lifecycle Manager Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a `PlayerManager` to `pkg/engine/` that owns player sessions, validates state transitions, and auto-wires into the tick loop — replacing per-game boilerplate maps.

**Architecture:** A state-machine-based `PlayerManager` lives in `pkg/engine/player_manager.go` with the session struct in `player_session.go`. The Engine creates and owns it. The game loop merges PlayerManager hooks with game hooks. Games register states, transitions, and callbacks — never manipulating player maps directly. `RegisterPendingLogin` moves from the `GameWorld` interface to the `PlayerManager`, called via `node.Engine.Players`.

**Tech Stack:** Go standard library, Ark ECS (`github.com/mlange-42/ark/ecs`), existing `pkg/engine` and `pkg/universe` packages.

**Spec:** `docs/planning/player-lifecycle-manager.md`

---

## File Structure

### New Files

| File | Responsibility |
|------|---------------|
| `pkg/engine/player_session.go` | `PlayerSession` struct, `PlayerState` enum, `SessionID` type, `StateTransition`, `StateCallbacks` |
| `pkg/engine/player_manager.go` | `PlayerManager` struct, all methods (registration, lookup, transition, removal, hook generation) |
| `pkg/engine/player_manager_test.go` | Tests for PlayerManager state machine, transitions, lookups, guards, removal |

### Modified Files

| File | Changes |
|------|---------|
| `pkg/engine/engine.go` | Add `Players *PlayerManager` field, initialize in `New()` |
| `pkg/engine/loop.go` | Merge PlayerManager hooks before game hooks in `NewGameLoop` |
| `pkg/mmokit/mmokit.go` | Re-export `PlayerManager`, `PlayerSession`, `PlayerState`, `SessionID`, `StateTransition`, `StateCallbacks`, built-in states, errors, and `NewPlayerManager` |
| `pkg/universe/world.go` | Remove `RegisterPendingLogin` from `GameWorld` interface |
| `pkg/universe/world_base.go` | Remove no-op `RegisterPendingLogin` |
| `pkg/universe/node.go` | Call `n.Engine.Players.RegisterPendingLogin` instead of `n.World.RegisterPendingLogin` |
| `pkg/universe/coordinator.go` | No changes needed (hook merging stays the same) |
| `pkg/universe/universe_test.go` | Remove `RegisterPendingLogin` from mockWorld, update assertions |
| `internal/game/lifecycle.go` | Rewrite to use `PlayerManager` transitions instead of manual map manipulation |
| `internal/game/players.go` | Delete (replaced by `PlayerManager`) |
| `internal/game/players_test.go` | Delete (tests for deleted `PlayerTracker`) |
| `internal/game/world.go` | Replace `Players *PlayerTracker` with `Players *mmokit.PlayerManager` |
| `internal/game/game.go` | Update `Hooks()` to only return non-player hooks; register PM callbacks, login handler, state callbacks |
| `internal/game/entity_ship.go` | Update `SpawnPlayer` to accept `*mmokit.PlayerSession`, set `s.Entity` |
| `internal/game/transfer.go` | Update `spawnShipFromTransfer` to use session entity assignment |
| `internal/game/transfer_test.go` | Update assertions to use PlayerManager lookups |
| `internal/game/commands.go` | Replace all `gw.Players.Entities/Usernames/Dead` with PM lookups; `kick` uses `pm.Remove()` |
| `internal/game/combat_helpers.go` | Replace `gw.Players.Usernames[connID]` with session username |
| `internal/universe/adapter.go` | Remove `RegisterPendingLogin` method (no longer in interface) |
| `internal/universe/factory.go` | Wire PlayerManager in factory |
| `internal/universe/node_test.go` | Update assertions to use PlayerManager lookups |
| `internal/system/input.go` | Replace `gw.Players.Entities/Usernames/PendingConnections` with PM lookups |
| `internal/system/docking.go` | Replace `gw.Players.Docking/Entities` iteration with PM session state + `session.Data` |
| `internal/system/economy.go` | Replace `gw.Players.Entities/Usernames/Docked` with PM lookups |
| `internal/system/equipment.go` | Replace `gw.Players.Entities` lookup with PM |
| `internal/system/collision.go` | Replace `gw.Players.Entities` lookup with PM |
| `internal/system/network.go` | Replace `gw.Players.Entities/Usernames` with PM iteration |
| `internal/system/sector_boundary.go` | Replace map deletes with `pm.Transition(s, StateTransferring)` + `pm.Remove(s)` |
| `internal/system/nethandler_ship.go` | Replace `gw.Players.Usernames` lookup with PM |
| `examples/slither/world.go` | Remove `Players`, `ConnToName`, `PendingConns` maps; register PM callbacks |
| `examples/slither/system_input.go` | Use `PlayerManager` for player lookups |
| `examples/slither/system_network.go` | Use `PlayerManager` for player iteration |

---

## Task 1: PlayerSession types

**Files:**
- Create: `pkg/engine/player_session.go`

- [ ] **Step 1: Create PlayerSession types**

```go
package engine

import (
	"time"

	"github.com/mlange-42/ark/ecs"
)

// PlayerState represents a player's lifecycle state.
type PlayerState uint8

const (
	StatePending      PlayerState = iota // connected, not yet logged in
	StateActive                          // in-world with an ECS entity
	StateDead                            // awaiting respawn, no entity
	StateTransferring                    // mid-node-transfer
	StateDisconnected                    // grace period, may reconnect
	stateBuiltinEnd                      // marker for custom state registration
)

// builtinStateNames maps built-in states to their names.
var builtinStateNames = map[PlayerState]string{
	StatePending:      "pending",
	StateActive:       "active",
	StateDead:         "dead",
	StateTransferring: "transferring",
	StateDisconnected: "disconnected",
}

// SessionID uniquely identifies a player session.
type SessionID uint64

// PlayerSession tracks a single player's connection and lifecycle state.
type PlayerSession struct {
	ID             SessionID
	ConnID         uint32      // 0 = no active connection
	Username       string
	State          PlayerState
	Entity         ecs.Entity  // zero-value when no entity exists
	Data           any         // game-specific session data
	PriorState     PlayerState // state before disconnect, for reconnect resume
	DisconnectTime time.Time   // when connection was lost

	isTransferLogin bool // true if created via RegisterPendingLogin
}

// StateTransition defines a valid state change with optional guard and action.
type StateTransition struct {
	From   PlayerState
	To     PlayerState
	Guard  func(s *PlayerSession) bool                // optional, can block transition
	Action func(s *PlayerSession, pm *PlayerManager)  // runs during transition (before OnExit)
}

// StateCallbacks are invoked when entering or exiting a state.
type StateCallbacks struct {
	OnEnter func(s *PlayerSession, pm *PlayerManager)
	OnExit  func(s *PlayerSession, pm *PlayerManager)
}
```

- [ ] **Step 2: Verify it compiles**

Run: `cd . && go build ./pkg/engine/...`
Expected: success (no errors)

- [ ] **Step 3: Commit**

```bash
git add pkg/engine/player_session.go
git commit -m "feat(engine): add PlayerSession types for lifecycle manager"
```

---

## Task 2: PlayerManager core

**Files:**
- Create: `pkg/engine/player_manager.go`
- Create: `pkg/engine/player_manager_test.go`

- [ ] **Step 1: Write failing tests for PlayerManager**

Create `pkg/engine/player_manager_test.go`:

```go
package engine

import (
	"errors"
	"testing"
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

	// Pending -> Dead is not a default transition
	err := pm.Transition(s, StateDead)
	if !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("expected ErrInvalidTransition, got %v", err)
	}
}

func TestPlayerManager_GuardBlocks(t *testing.T) {
	pm := NewPlayerManager()
	// Replace the default Pending->Active with one that has a guard
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
	if docked < stateBuiltinEnd {
		t.Errorf("custom state %d should be >= stateBuiltinEnd (%d)", docked, stateBuiltinEnd)
	}

	// Registering same name again should return same state
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

func TestPlayerManager_RegisterPendingLogin(t *testing.T) {
	pm := NewPlayerManager()
	// Simulate a connection first
	s := pm.createSession(99)

	pm.RegisterPendingLogin(99, "transferplayer")

	if s.Username != "transferplayer" {
		t.Errorf("Username = %q, want 'transferplayer'", s.Username)
	}
	if !s.isTransferLogin {
		t.Error("session should be flagged as transfer login")
	}
	if pm.ByUsername("transferplayer") != s {
		t.Error("should be indexed by username")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd . && go test ./pkg/engine/ -run TestPlayerManager -v`
Expected: FAIL — `NewPlayerManager` not defined

- [ ] **Step 3: Implement PlayerManager**

Create `pkg/engine/player_manager.go`:

```go
package engine

import (
	"errors"
	"fmt"
	"time"
)

var (
	ErrInvalidTransition     = errors.New("invalid state transition")
	ErrTransitionGuardFailed = errors.New("transition guard returned false")
	ErrSessionNil            = errors.New("session is nil")
)

// transitionKey uniquely identifies a From->To pair.
type transitionKey struct {
	From, To PlayerState
}

// PlayerManager owns player sessions and enforces lifecycle state transitions.
type PlayerManager struct {
	sessions    map[SessionID]*PlayerSession
	byConnID    map[uint32]*PlayerSession
	byUsername  map[string]*PlayerSession

	states         map[PlayerState]string         // state -> name
	stateCallbacks map[PlayerState]*StateCallbacks
	transitions    map[transitionKey]*StateTransition

	gracePeriod   time.Duration
	nextSessionID SessionID
	nextState     PlayerState

	onLogin          func(s *PlayerSession, pm *PlayerManager) error
	onLoginRejected  func(connID uint32, reason string)

	eng *Engine
}

// NewPlayerManager creates a PlayerManager with default built-in states and transitions.
func NewPlayerManager() *PlayerManager {
	pm := &PlayerManager{
		sessions:       make(map[SessionID]*PlayerSession),
		byConnID:       make(map[uint32]*PlayerSession),
		byUsername:     make(map[string]*PlayerSession),
		states:         make(map[PlayerState]string),
		stateCallbacks: make(map[PlayerState]*StateCallbacks),
		transitions:    make(map[transitionKey]*StateTransition),
		nextState:      stateBuiltinEnd,
	}

	// Register built-in state names
	for state, name := range builtinStateNames {
		pm.states[state] = name
	}

	// Register default transitions
	defaults := []StateTransition{
		{From: StatePending, To: StateActive},
		{From: StateActive, To: StateDead},
		{From: StateActive, To: StateTransferring},
		{From: StateActive, To: StateDisconnected},
		{From: StateDead, To: StateActive},
		{From: StateDead, To: StateDisconnected},
		{From: StateTransferring, To: StateDisconnected},
		{From: StateDisconnected, To: StateActive},
		{From: StateDisconnected, To: StateDead},
	}
	for i := range defaults {
		pm.transitions[transitionKey{defaults[i].From, defaults[i].To}] = &defaults[i]
	}

	return pm
}

// Engine returns the associated engine (nil if not yet attached).
func (pm *PlayerManager) Engine() *Engine { return pm.eng }

// --- State Registration ---

// RegisterState adds a custom state with the given name. Returns the state ID.
// If a state with the same name already exists, returns the existing ID.
func (pm *PlayerManager) RegisterState(name string) PlayerState {
	for state, n := range pm.states {
		if n == name {
			return state
		}
	}
	state := pm.nextState
	pm.nextState++
	pm.states[state] = name
	return state
}

// StateName returns the display name for a state.
func (pm *PlayerManager) StateName(state PlayerState) string {
	if name, ok := pm.states[state]; ok {
		return name
	}
	return fmt.Sprintf("unknown(%d)", state)
}

// OnState registers enter/exit callbacks for a state.
func (pm *PlayerManager) OnState(state PlayerState, cbs StateCallbacks) {
	pm.stateCallbacks[state] = &cbs
}

// AddTransition adds or replaces a state transition.
func (pm *PlayerManager) AddTransition(t StateTransition) {
	key := transitionKey{t.From, t.To}
	copied := t
	pm.transitions[key] = &copied
}

// AddTransitions adds multiple transitions.
func (pm *PlayerManager) AddTransitions(ts []StateTransition) {
	for _, t := range ts {
		pm.AddTransition(t)
	}
}

// --- Session Lookup ---

// ByConnID returns the session for the given connection ID, or nil.
func (pm *PlayerManager) ByConnID(connID uint32) *PlayerSession {
	return pm.byConnID[connID]
}

// ByUsername returns the session for the given username, or nil.
func (pm *PlayerManager) ByUsername(username string) *PlayerSession {
	return pm.byUsername[username]
}

// BySessionID returns the session for the given session ID, or nil.
func (pm *PlayerManager) BySessionID(id SessionID) *PlayerSession {
	return pm.sessions[id]
}

// --- Iteration ---

// InState returns all sessions currently in the given state.
func (pm *PlayerManager) InState(state PlayerState) []*PlayerSession {
	var result []*PlayerSession
	for _, s := range pm.sessions {
		if s.State == state {
			result = append(result, s)
		}
	}
	return result
}

// ForEach calls fn for every session in the given state.
func (pm *PlayerManager) ForEach(state PlayerState, fn func(s *PlayerSession)) {
	for _, s := range pm.sessions {
		if s.State == state {
			fn(s)
		}
	}
}

// Count returns the number of sessions in the given state.
func (pm *PlayerManager) Count(state PlayerState) int {
	n := 0
	for _, s := range pm.sessions {
		if s.State == state {
			n++
		}
	}
	return n
}

// ConnectedCount returns the number of sessions with an active connection.
func (pm *PlayerManager) ConnectedCount() int {
	return len(pm.byConnID)
}

// --- State Transitions ---

// Transition moves a session to a new state, running guards, actions, and callbacks.
// Order: Guard -> Action -> OnExit(old) -> state update -> OnEnter(new).
func (pm *PlayerManager) Transition(s *PlayerSession, to PlayerState) error {
	if s == nil {
		return ErrSessionNil
	}

	key := transitionKey{s.State, to}
	trans, ok := pm.transitions[key]
	if !ok {
		return fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, pm.StateName(s.State), pm.StateName(to))
	}

	if trans.Guard != nil && !trans.Guard(s) {
		return fmt.Errorf("%w: %s -> %s", ErrTransitionGuardFailed, pm.StateName(s.State), pm.StateName(to))
	}

	// Edge action (runs while old state resources are still available)
	if trans.Action != nil {
		trans.Action(s, pm)
	}

	// OnExit for old state
	if cbs, ok := pm.stateCallbacks[s.State]; ok && cbs.OnExit != nil {
		cbs.OnExit(s, pm)
	}

	oldState := s.State
	s.State = to
	_ = oldState // available if needed for logging

	// OnEnter for new state
	if cbs, ok := pm.stateCallbacks[to]; ok && cbs.OnEnter != nil {
		cbs.OnEnter(s, pm)
	}

	return nil
}

// --- Session Management ---

// createSession allocates a new session for the given connID.
func (pm *PlayerManager) createSession(connID uint32) *PlayerSession {
	pm.nextSessionID++
	s := &PlayerSession{
		ID:     pm.nextSessionID,
		ConnID: connID,
		State:  StatePending,
	}
	pm.sessions[s.ID] = s
	if connID != 0 {
		pm.byConnID[connID] = s
	}
	return s
}

// Remove fully removes a session, running OnExit for the current state.
func (pm *PlayerManager) Remove(s *PlayerSession) {
	if s == nil {
		return
	}

	// OnExit for current state
	if cbs, ok := pm.stateCallbacks[s.State]; ok && cbs.OnExit != nil {
		cbs.OnExit(s, pm)
	}

	delete(pm.sessions, s.ID)
	if s.ConnID != 0 {
		delete(pm.byConnID, s.ConnID)
	}
	if s.Username != "" {
		delete(pm.byUsername, s.Username)
	}
}

// --- Config ---

// SetGracePeriod sets the disconnect grace period. 0 = immediate cleanup (default).
func (pm *PlayerManager) SetGracePeriod(d time.Duration) {
	pm.gracePeriod = d
}

// SetLoginHandler sets the callback that parses login messages and populates session fields.
func (pm *PlayerManager) SetLoginHandler(fn func(s *PlayerSession, pm *PlayerManager) error) {
	pm.onLogin = fn
}

// SetLoginRejectedHandler sets the callback to notify clients when login is rejected.
func (pm *PlayerManager) SetLoginRejectedHandler(fn func(connID uint32, reason string)) {
	pm.onLoginRejected = fn
}

// --- Multi-node ---

// RegisterPendingLogin creates or updates a session for a cross-node transfer.
// The session is flagged so ProcessLogins skips the onLogin handler.
// Must be called from the game loop goroutine.
func (pm *PlayerManager) RegisterPendingLogin(connID uint32, username string) {
	s := pm.byConnID[connID]
	if s == nil {
		// ConnID hasn't connected yet — create a session for it
		s = pm.createSession(connID)
	}
	s.Username = username
	s.isTransferLogin = true
	pm.byUsername[username] = s
}

// --- Hook Generation ---

// hooks returns Hooks that the game loop should call for player lifecycle.
func (pm *PlayerManager) hooks() Hooks {
	return Hooks{
		OnConnect: func(connID uint32) {
			pm.createSession(connID)
		},
		OnDisconnect: func(connID uint32) {
			s := pm.byConnID[connID]
			if s == nil {
				return
			}
			if pm.gracePeriod > 0 && s.State != StatePending {
				s.PriorState = s.State
				s.DisconnectTime = time.Now()
				// Try to transition to Disconnected
				if err := pm.Transition(s, StateDisconnected); err == nil {
					s.ConnID = 0
					delete(pm.byConnID, connID)
					return
				}
			}
			pm.Remove(s)
		},
		ProcessLogins: func() {
			pm.processLogins()
		},
		PostTick: func() {
			pm.expireGracePeriods()
		},
	}
}

// processLogins handles transfer logins and fresh logins for pending sessions.
func (pm *PlayerManager) processLogins() {
	// Collect pending sessions to avoid map mutation during iteration
	var pending []*PlayerSession
	for _, s := range pm.sessions {
		if s.State == StatePending {
			pending = append(pending, s)
		}
	}

	for _, s := range pending {
		// Check session is still valid (could have been removed by a prior iteration)
		if _, ok := pm.sessions[s.ID]; !ok {
			continue
		}

		if s.isTransferLogin {
			// Transfer login: username already set, skip onLogin handler
			s.isTransferLogin = false
			if err := pm.Transition(s, StateActive); err != nil {
				pm.Remove(s)
			}
			continue
		}

		// Fresh login: call game's login handler
		if pm.onLogin == nil {
			continue
		}
		if err := pm.onLogin(s, pm); err != nil {
			if pm.onLoginRejected != nil && s.ConnID != 0 {
				pm.onLoginRejected(s.ConnID, err.Error())
			}
			pm.Remove(s)
			continue
		}

		// Login handler should have set Username. If empty, skip.
		if s.Username == "" {
			continue
		}

		// Check for reconnection (username matches a Disconnected session)
		if existing := pm.byUsername[s.Username]; existing != nil && existing != s && existing.State == StateDisconnected {
			// Reconnect: attach new connID to existing session
			existing.ConnID = s.ConnID
			existing.DisconnectTime = time.Time{}
			pm.byConnID[s.ConnID] = existing

			// Remove the pending session without calling OnExit
			delete(pm.sessions, s.ID)

			// Resume prior state
			if err := pm.Transition(existing, existing.PriorState); err != nil {
				pm.Remove(existing)
			}
			continue
		}

		// Check duplicate username
		if existing := pm.byUsername[s.Username]; existing != nil && existing != s {
			if pm.onLoginRejected != nil && s.ConnID != 0 {
				pm.onLoginRejected(s.ConnID, "Username already connected")
			}
			pm.Remove(s)
			continue
		}

		pm.byUsername[s.Username] = s
		if err := pm.Transition(s, StateActive); err != nil {
			pm.Remove(s)
		}
	}
}

// expireGracePeriods removes disconnected sessions past their TTL.
func (pm *PlayerManager) expireGracePeriods() {
	if pm.gracePeriod <= 0 {
		return
	}
	now := time.Now()
	var expired []*PlayerSession
	for _, s := range pm.sessions {
		if s.State == StateDisconnected && now.Sub(s.DisconnectTime) > pm.gracePeriod {
			expired = append(expired, s)
		}
	}
	for _, s := range expired {
		pm.Remove(s)
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd . && go test ./pkg/engine/ -run TestPlayerManager -v`
Expected: all PASS

- [ ] **Step 5: Commit**

```bash
git add pkg/engine/player_manager.go pkg/engine/player_manager_test.go
git commit -m "feat(engine): implement PlayerManager with state machine, transitions, and hooks"
```

---

## Task 3: Wire PlayerManager into Engine and GameLoop

**Files:**
- Modify: `pkg/engine/engine.go`
- Modify: `pkg/engine/loop.go`

- [ ] **Step 1: Add Players field to Engine**

In `pkg/engine/engine.go`, add `Players *PlayerManager` to the Engine struct and initialize it in `New()`:

```go
// In Engine struct, add after PendingAdminCmds:
Players *PlayerManager

// In New(), add after PendingAdminCmds init:
eng := &Engine{...}
eng.Players = NewPlayerManager()
eng.Players.eng = eng
return eng
```

- [ ] **Step 2: Merge PlayerManager hooks in NewGameLoop**

In `pkg/engine/loop.go`, update `NewGameLoop` to compose PlayerManager hooks before game hooks:

```go
func NewGameLoop(eng *Engine, systems []System, hooks Hooks) *GameLoop {
	// ... existing name extraction and perf setup ...

	// Merge PlayerManager hooks (first) with game hooks (second)
	pmHooks := eng.Players.hooks()
	merged := Hooks{
		OnConnect: func(connID uint32) {
			pmHooks.OnConnect(connID)
			if hooks.OnConnect != nil {
				hooks.OnConnect(connID)
			}
		},
		OnDisconnect: func(connID uint32) {
			pmHooks.OnDisconnect(connID)
			if hooks.OnDisconnect != nil {
				hooks.OnDisconnect(connID)
			}
		},
		ProcessLogins: func() {
			pmHooks.ProcessLogins()
			if hooks.ProcessLogins != nil {
				hooks.ProcessLogins()
			}
		},
		PreFlush:       hooks.PreFlush,
		PostFlush:      hooks.PostFlush,
		ClearTickState: hooks.ClearTickState,
		PostTick: func() {
			if hooks.PostTick != nil {
				hooks.PostTick()
			}
			pmHooks.PostTick()
		},
	}

	return &GameLoop{
		engine:     eng,
		systems:    systems,
		hooks:      merged,
		sysTimings: make([]time.Duration, len(systems)),
	}
}
```

- [ ] **Step 3: Verify existing tests still pass**

Run: `cd . && go test ./pkg/engine/ -v`
Expected: all PASS

- [ ] **Step 4: Commit**

```bash
git add pkg/engine/engine.go pkg/engine/loop.go
git commit -m "feat(engine): wire PlayerManager hooks into Engine and GameLoop"
```

---

## Task 4: Re-export through mmokit facade

**Files:**
- Modify: `pkg/mmokit/mmokit.go`

- [ ] **Step 1: Add re-exports**

Add to the Engine section of `pkg/mmokit/mmokit.go`:

```go
// In the Engine types section:
type PlayerManager = engine.PlayerManager
type PlayerSession = engine.PlayerSession
type PlayerState = engine.PlayerState
type SessionID = engine.SessionID
type StateTransition = engine.StateTransition
type StateCallbacks = engine.StateCallbacks

// In the constants section:
var (
	StatePending      = engine.StatePending
	StateActive       = engine.StateActive
	StateDead         = engine.StateDead
	StateTransferring = engine.StateTransferring
	StateDisconnected = engine.StateDisconnected

	ErrInvalidTransition     = engine.ErrInvalidTransition
	ErrTransitionGuardFailed = engine.ErrTransitionGuardFailed
)

// In the constructors section:
var NewPlayerManager = engine.NewPlayerManager
```

- [ ] **Step 2: Verify it compiles**

Run: `cd . && go build ./pkg/mmokit/...`
Expected: success

- [ ] **Step 3: Commit**

```bash
git add pkg/mmokit/mmokit.go
git commit -m "feat(mmokit): re-export PlayerManager types through facade"
```

---

## Task 5: Update GameWorld interface for PlayerManager

**Files:**
- Modify: `pkg/universe/world.go`
- Modify: `pkg/universe/world_base.go`
- Modify: `pkg/universe/coordinator.go`
- Modify: `pkg/universe/universe_test.go` (update mock)

- [ ] **Step 1: Read current universe_test.go to check mockWorld**

Read `pkg/universe/universe_test.go` to see what `mockWorld` implements and what needs updating.

- [ ] **Step 2: Remove RegisterPendingLogin from GameWorld interface**

In `pkg/universe/world.go`, remove the `RegisterPendingLogin(connID uint32, username string)` line from the `GameWorld` interface.

The PlayerManager now owns this via `Engine.Players.RegisterPendingLogin()`.

- [ ] **Step 3: Update WorldBase**

In `pkg/universe/world_base.go`, remove the no-op `RegisterPendingLogin` method.

- [ ] **Step 4: Update Node.processMessage**

In `pkg/universe/node.go`, update the `MsgSpawnTransfer` case to call the engine's PlayerManager:

```go
case MsgSpawnTransfer:
	if msg.Spawn == nil {
		return
	}
	n.Engine.Players.RegisterPendingLogin(msg.Spawn.ConnID, msg.Spawn.Username)
```

- [ ] **Step 5: Update mockWorld in universe_test.go**

Remove the `RegisterPendingLogin` method from `mockWorld`. Update the `TestNodeBridge_SpawnTransfer` test to check `node.Engine.Players.ByUsername("bob")` instead of `mw.logins`.

- [ ] **Step 6: Verify tests pass**

Run: `cd . && go test ./pkg/universe/ -v`
Expected: all PASS

- [ ] **Step 7: Commit**

```bash
git add pkg/universe/world.go pkg/universe/world_base.go pkg/universe/node.go pkg/universe/universe_test.go
git commit -m "refactor(universe): remove RegisterPendingLogin from GameWorld, use PlayerManager"
```

---

## Task 6: Migrate main game core to PlayerManager

**Files:**
- Modify: `internal/game/world.go`
- Modify: `internal/game/game.go`
- Modify: `internal/game/lifecycle.go`
- Modify: `internal/game/entity_ship.go`
- Delete: `internal/game/players.go`
- Delete: `internal/game/players_test.go`
- Modify: `internal/universe/adapter.go`

This task sets up the core PM integration. Task 6b migrates all callers.

- [ ] **Step 1: Read all callers of PlayerTracker**

Search for all references to `gw.Players.` across `internal/` to establish full scope:

Run: `grep -rn "gw\.Players\." internal/ --include="*.go" | wc -l`
Run: `grep -rn "gw\.Players\." internal/ --include="*.go" | cut -d: -f1 | sort -u`

- [ ] **Step 2: Update GameWorld struct**

In `internal/game/world.go`, change the field type from `*PlayerTracker` to `*mmokit.PlayerManager`:

```go
Players *mmokit.PlayerManager
```

In `internal/game/game.go` `NewGameWorld`, replace `Players: NewPlayerTracker()` with:
```go
gw.Players = eng.Players
```

- [ ] **Step 3: Define custom states and register transitions**

Add package-level state variables in `internal/game/game.go`:

```go
var (
	StateDocking mmokit.PlayerState
	StateDocked  mmokit.PlayerState
)
```

After `gw.Players = eng.Players` in `NewGameWorld`:

```go
StateDocking = gw.Players.RegisterState("docking")
StateDocked = gw.Players.RegisterState("docked")

gw.Players.AddTransitions([]mmokit.StateTransition{
	{From: mmokit.StateActive, To: StateDocking},
	{From: StateDocking, To: StateDocked},
	{From: StateDocked, To: mmokit.StateActive},
	{From: StateDocking, To: mmokit.StateDead},
	{From: StateDocked, To: mmokit.StateDisconnected},
})
```

- [ ] **Step 4: Set up login handler**

```go
gw.Players.SetLoginHandler(func(s *mmokit.PlayerSession, pm *mmokit.PlayerManager) error {
	msgs := pm.Engine().ConnMgr.DrainInput(s.ConnID)
	for _, data := range msgs {
		var evt enginepb.ClientEvent
		if err := proto.Unmarshal(data, &evt); err != nil {
			continue
		}
		if enginepb.ClientEventCode(evt.Code) == enginepb.ClientEventCode_CE_LOGIN {
			var login enginepb.LoginMsg
			if err := proto.Unmarshal(evt.Data, &login); err != nil {
				continue
			}
			username := strings.ToLower(login.Username)
			if username == "" {
				continue
			}
			s.Username = username
			return nil
		}
	}
	return nil // no login message yet — username stays empty, PM will skip
})

gw.Players.SetLoginRejectedHandler(func(connID uint32, reason string) {
	rejectData := netutil.MakeEvent(uint32(enginepb.ServerEventCode_SE_LOGIN_REJECTED), &enginepb.LoginRejectedMsg{
		Reason: reason,
	})
	if rejectData != nil {
		gw.ConnMgr.SendReliable(connID, rejectData)
	}
})
```

- [ ] **Step 5: Register state callbacks**

```go
gw.Players.OnState(mmokit.StateActive, mmokit.StateCallbacks{
	OnEnter: func(s *mmokit.PlayerSession, pm *mmokit.PlayerManager) {
		gw.SpawnPlayer(s)
		if gw.PlayerSessions != nil {
			gw.PlayerSessions.Set(s.ConnID, s.Username)
		}
		gw.updatePlayerCompletions()
	},
	OnExit: func(s *mmokit.PlayerSession, pm *mmokit.PlayerManager) {
		if s.Entity != (ecs.Entity{}) && gw.ECS.Alive(s.Entity) {
			gw.SavePlayerState(s)
			gw.ECS.RemoveEntity(s.Entity)
			s.Entity = ecs.Entity{}
		}
		if gw.PlayerSessions != nil {
			gw.PlayerSessions.Remove(s.ConnID)
		}
		gw.updatePlayerCompletions()
	},
})
```

- [ ] **Step 6: Update SpawnPlayer signature**

In `internal/game/entity_ship.go`, change `SpawnPlayer(connID uint32)` to `SpawnPlayer(s *mmokit.PlayerSession)`:
- Replace `gw.Players.Usernames[connID]` with `s.Username`
- Replace `gw.Players.Entities[connID] = entity` with `s.Entity = entity`
- Replace all internal `connID` with `s.ConnID`

- [ ] **Step 7: Update SavePlayerState signature**

In `internal/game/world.go`, change `SavePlayerState(connID uint32, entity ecs.Entity)` to `SavePlayerState(s *mmokit.PlayerSession)`:
- Replace `username, ok := gw.Players.Usernames[connID]` with `username := s.Username`
- Use `s.Entity` instead of the entity parameter
- Update callers: `gw.Shutdown()` in `game.go` and `lifecycle.go`

- [ ] **Step 8: Rewrite lifecycle.go**

Remove `onConnect`, `onDisconnect`, `processLogins` (handled by PM). Update remaining functions:

- `processDeaths`: `gw.Players.Transition(session, mmokit.StateDead)` instead of map manipulation
- `processDockCompletions`: `gw.Players.Transition(session, StateDocked)`. Store `DockingState` in `session.Data` when entering StateDocking.
- `processUndocks`: `gw.Players.Transition(session, mmokit.StateActive)`
- `processRespawns`: `gw.Players.Transition(session, mmokit.StateActive)`. For cross-node: `gw.Players.Transition(session, mmokit.StateTransferring)` then `gw.Players.Remove(session)`.
- `updatePlayerCompletions`: Use `gw.Players.ForEach` to collect usernames
- `Shutdown()`: Use `gw.Players.ForEach(mmokit.StateActive, ...)` to save all players

- [ ] **Step 9: Update Hooks()**

```go
func (gw *GameWorld) Hooks() mmokit.Hooks {
	return mmokit.Hooks{
		PreFlush: func() {
			gw.processDeaths()
			gw.processDockCompletions()
		},
		PostFlush:      gw.postFlush,
		ClearTickState: gw.clearTickState,
		PostTick:       gw.postTick,
	}
}
```

- [ ] **Step 10: Delete players.go and players_test.go**

```bash
rm internal/game/players.go internal/game/players_test.go
```

- [ ] **Step 11: Update adapter.go**

Remove the `RegisterPendingLogin` method from `gameWorldAdapter`.

- [ ] **Step 12: Commit (will not compile yet — callers in systems still use old API)**

```bash
git add -A
git commit -m "refactor(game): set up PlayerManager core - states, callbacks, login handler"
```

---

## Task 6b: Migrate all callers to PlayerManager

**Files (all references to old PlayerTracker patterns):**
- Modify: `internal/game/commands.go` — ~17 references. `resolvePlayer()`, `broadcastDebugFlags()`, `kick` command, `players` command, `kill` command
- Modify: `internal/game/combat_helpers.go` — 2 references. Username lookups for logging
- Modify: `internal/game/transfer.go` — 3 references. `spawnShipFromTransfer` sets `gw.Players.Entities/Usernames`
- Modify: `internal/game/transfer_test.go` — 2 references. Assertions on PlayerTracker maps
- Modify: `internal/system/input.go` — 6 references. `gw.Players.Entities`, `gw.Players.Usernames`, `gw.Players.PendingConnections`
- Modify: `internal/system/docking.go` — 6 references. `gw.Players.Docking` iteration, `gw.Players.Entities`
- Modify: `internal/system/economy.go` — 14 references. `gw.Players.Entities`, `gw.Players.Usernames`, `gw.Players.Docked`
- Modify: `internal/system/equipment.go` — 1 reference. `gw.Players.Entities`
- Modify: `internal/system/collision.go` — 1 reference. `gw.Players.Entities`
- Modify: `internal/system/network.go` — 2 references. `gw.Players.Entities`, `gw.Players.Usernames`
- Modify: `internal/system/sector_boundary.go` — 4 references. Map deletes during transfer
- Modify: `internal/system/nethandler_ship.go` — 2 references. `gw.Players.Usernames`
- Modify: `internal/universe/node_test.go` — 2 references. Assertions on old PlayerTracker

**Replacement patterns:**

| Old pattern | New pattern |
|-------------|------------|
| `gw.Players.Entities[connID]` | `s := gw.Players.ByConnID(connID); s.Entity` |
| `_, ok := gw.Players.Entities[connID]` | `s := gw.Players.ByConnID(connID); s != nil && s.State == mmokit.StateActive` |
| `gw.Players.Usernames[connID]` | `s := gw.Players.ByConnID(connID); s.Username` |
| `gw.Players.Dead[connID]` | `s := gw.Players.ByConnID(connID); s.State == mmokit.StateDead` |
| `gw.Players.Docked[connID]` | `s := gw.Players.ByConnID(connID); s.State == StateDocked` |
| `gw.Players.Docking[connID]` | `s.Data.(*DockingState)` when `s.State == StateDocking` |
| `for connID, entity := range gw.Players.Entities` | `gw.Players.ForEach(mmokit.StateActive, func(s) { connID, entity := s.ConnID, s.Entity; ... })` |
| `for connID := range gw.Players.Usernames` | `gw.Players.ForEach(...)` across relevant states |
| `delete(gw.Players.Entities, connID)` + `delete(gw.Players.Usernames, connID)` | `gw.Players.Transition(s, StateTransferring)` + `gw.Players.Remove(s)` |
| `gw.Players.Entities[connID] = entity` (transfer.go) | `s.Entity = entity` |
| `gw.Players.UsernameInUse(username)` | `gw.Players.ByUsername(username) != nil` |

**Special cases:**

- **`commands.go` kick command**: Currently does `delete()` from 3 maps + `ECS.RemoveEntity`. Replace with `gw.Players.Remove(session)` which calls `OnExit(Active)` → saves + removes entity.
- **`docking.go`**: The `DockingSystem` iterates `gw.Players.Docking` and decrements `Remaining`. Change to `gw.Players.ForEach(StateDocking, func(s) { ds := s.Data.(*DockingState); ds.Remaining -= dt; ... })`.
- **`sector_boundary.go`**: During cross-node transfer at lines 177-178, does `delete(Entities)`, `delete(Usernames)`. Replace with `gw.Players.Transition(s, mmokit.StateTransferring)` then `gw.Players.Remove(s)`.
- **`transfer.go` spawnShipFromTransfer**: Sets `gw.Players.Entities[connID] = entity` and `gw.Players.Usernames[connID] = username`. Replace with session lookup + `s.Entity = entity` (username already set by PM's `RegisterPendingLogin`).

- [ ] **Step 1: Update internal/system/ files**

Read each file, apply the replacement patterns above. Files to update in order:
1. `internal/system/input.go`
2. `internal/system/docking.go`
3. `internal/system/economy.go`
4. `internal/system/equipment.go`
5. `internal/system/collision.go`
6. `internal/system/network.go`
7. `internal/system/sector_boundary.go`
8. `internal/system/nethandler_ship.go`

- [ ] **Step 2: Update internal/game/ files**

1. `internal/game/commands.go` — especially the kick command and resolvePlayer()
2. `internal/game/combat_helpers.go`
3. `internal/game/transfer.go`

- [ ] **Step 3: Update test files**

1. `internal/game/transfer_test.go` — assertions use PM lookups
2. `internal/universe/node_test.go` — assertions use PM lookups

- [ ] **Step 4: Verify compilation**

Run: `cd . && go build ./...`
Expected: success

- [ ] **Step 5: Run all tests**

Run: `cd . && go test ./...`
Expected: all PASS

- [ ] **Step 6: Commit**

```bash
git add -A
git commit -m "refactor(game): migrate all PlayerTracker callers to PlayerManager"
```

---

## Task 7: Migrate slither example to PlayerManager

**Files:**
- Modify: `examples/slither/world.go`
- Modify: `examples/slither/system_input.go`
- Modify: `examples/slither/system_network.go`

- [ ] **Step 1: Read slither network system**

Read `examples/slither/system_network.go` to understand all player map references.

- [ ] **Step 2: Update SlitherWorld struct**

Remove `Players`, `ConnToName`, `PendingConns` fields. They're replaced by `Engine().Players`.

- [ ] **Step 3: Register PlayerManager callbacks in NewSlitherWorld**

```go
pm := base.Engine().Players
pm.SetLoginHandler(func(s *mmokit.PlayerSession, pm *mmokit.PlayerManager) error {
	// Login is handled by InputSystem which enqueues PendingLogin
	// The PM's login handler just needs to check the queue
	for _, login := range mmokit.Peek[PendingLogin](gw.Queue) {
		if login.ConnID == s.ConnID {
			s.Username = login.Name
			s.Data = &SlitherSessionData{SkinID: login.SkinID}
			return nil
		}
	}
	return nil // no login message yet
})

pm.OnState(mmokit.StateActive, mmokit.StateCallbacks{
	OnEnter: func(s *mmokit.PlayerSession, pm *mmokit.PlayerManager) {
		data, _ := s.Data.(*SlitherSessionData)
		skinID := uint8(0)
		if data != nil {
			skinID = data.SkinID
		}
		s.Entity = gw.SpawnPlayerSnake(s.ConnID, s.Username, skinID)
	},
	OnExit: func(s *mmokit.PlayerSession, pm *mmokit.PlayerManager) {
		if s.Entity != (ecs.Entity{}) {
			gw.MarkForRemoval(s.Entity)
			s.Entity = ecs.Entity{}
		}
	},
})
```

Define `SlitherSessionData`:
```go
type SlitherSessionData struct {
	SkinID uint8
}
```

- [ ] **Step 4: Update Hooks()**

Remove `OnConnect`, `OnDisconnect`, `ProcessLogins`. Keep only `ClearTickState`:

```go
func (gw *SlitherWorld) Hooks() engine.Hooks {
	return engine.Hooks{
		ClearTickState: func() {
			gw.PendingKills = gw.PendingKills[:0]
			gw.KillFeed = gw.KillFeed[:0]
		},
	}
}
```

- [ ] **Step 5: Update InputSystem**

Replace `gw.Players` map iteration with `gw.Engine().Players.ForEach(mmokit.StateActive, ...)`.
Replace `gw.PendingConns` with `gw.Engine().Players.ForEach(mmokit.StatePending, ...)`.
Replace `gw.ConnToName[connID]` with session username lookup.

- [ ] **Step 6: Update NetworkSystem**

Replace `gw.Players` map references with PlayerManager lookups.

- [ ] **Step 7: Update SpawnFromTransfer**

Replace `gw.Players[frame.ConnID] = entity` with session entity assignment:
```go
if frame.ConnID != 0 {
	if s := gw.Engine().Players.ByConnID(frame.ConnID); s != nil {
		s.Entity = entity
	}
}
```

- [ ] **Step 8: Verify slither compiles and runs**

Run: `cd examples/slither && go build .`
Expected: success

- [ ] **Step 9: Commit**

```bash
git add -A
git commit -m "refactor(slither): migrate from manual player maps to PlayerManager"
```

---

## Task 8: Full build verification

**Files:** none (verification only)

- [ ] **Step 1: Build everything**

Run: `cd . && go build ./...`
Expected: success

- [ ] **Step 2: Run all tests**

Run: `cd . && go test ./...`
Expected: all PASS

- [ ] **Step 3: Build slither example**

Run: `cd examples/slither && go build .`
Expected: success

- [ ] **Step 4: Final commit if any fixups needed**

Only if previous steps required changes.
