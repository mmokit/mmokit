# InputRouter Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace hand-written InputSystem switch statements with a generic, typed InputRouter that dispatches client messages based on message code + player state bitmask, with two-layer filtering.

**Architecture:** `InputRouter` lives in `pkg/engine/`, owns the drain loop, parses envelopes via an injected `EnvelopeParser`, and dispatches to typed handlers registered with `Handle` (raw) or `HandleProto[T]` (auto-unmarshal). State-group filters and per-handler guards provide two-layer filtering. Re-exported through `pkg/mmokit/` facade. Main game migrates from 337-line InputSystem to ~40 lines of handler registrations.

**Tech Stack:** Go 1.23+, Go generics, google.golang.org/protobuf, Ark ECS v0.7.1

**Spec:** `docs/superpowers/specs/2026-03-26-input-router-design.md`

---

## File Structure

### New files

| File | Responsibility |
| --- | --- |
| `pkg/engine/input_router.go` | InputRouter struct, Handle, HandleProto, States, WithGuard, ProcessInput, System interface |
| `pkg/engine/input_router_test.go` | Unit tests for InputRouter dispatch, state filtering, guards, unmarshal |
| `internal/system/input_handlers.go` | Game-specific handler functions extracted from current input.go |

### Modified files

| File | Change |
| --- | --- |
| `pkg/engine/player_manager.go` | Add `ForEachConnected` method |
| `pkg/engine/player_manager_test.go` | Test for `ForEachConnected` |
| `pkg/mmokit/mmokit.go` | Re-export InputRouter types + ProtoEnvelopeParser |
| `internal/universe/factory.go` | Replace `system.NewInputSystem(gw)` with router setup |
| `internal/system/input.go` | Delete entirely |
| `proto/gamepb/game.proto` | Already cleaned up (legacy fields removed) |

---

### Task 1: Add `ForEachConnected` to PlayerManager

**Files:**
- Modify: `pkg/engine/player_manager.go`
- Test: `pkg/engine/player_manager_test.go`

- [ ] **Step 1: Write the failing test**

Add to `pkg/engine/player_manager_test.go`:

```go
func TestForEachConnected(t *testing.T) {
	eng := newTestEngine()
	pm := eng.Players

	// Create sessions in various states
	pending := pm.createSession(1) // StatePending, connID=1
	_ = pending

	active := pm.createSession(2) // will transition to active
	pm.SetLoginHandler(func(s *PlayerSession, pm *PlayerManager) error {
		s.Username = "active"
		return nil
	})
	// Manually transition to active
	active.Username = "active"
	pm.byUsername["active"] = active
	_ = pm.Transition(active, StateActive)

	dead := pm.createSession(3)
	dead.Username = "dead"
	pm.byUsername["dead"] = dead
	_ = pm.Transition(dead, StateActive)
	_ = pm.Transition(dead, StateDead)

	disconnected := pm.createSession(0) // connID=0 means disconnected
	_ = disconnected

	// ForEachConnected should visit active and dead (connID != 0, not pending)
	// but NOT pending (state check) and NOT disconnected (connID == 0)
	var visited []uint32
	pm.ForEachConnected(func(s *PlayerSession) {
		visited = append(visited, s.ConnID)
	})

	if len(visited) != 2 {
		t.Fatalf("expected 2 connected sessions, got %d: %v", len(visited), visited)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd pkg/engine && go test -run TestForEachConnected -v`
Expected: FAIL — `ForEachConnected` not defined

- [ ] **Step 3: Implement ForEachConnected**

Add to `pkg/engine/player_manager.go` after the existing `ForEach` method (~line 139):

```go
// ForEachConnected iterates all sessions that have an active connection
// (connID != 0) and are not in StatePending. Pending sessions are excluded
// because their input is consumed by processLogins().
func (pm *PlayerManager) ForEachConnected(fn func(s *PlayerSession)) {
	for _, s := range pm.byConnID {
		if s.State != StatePending {
			fn(s)
		}
	}
}
```

Note: iterating `byConnID` (not `sessions`) automatically excludes sessions with `connID == 0`.

- [ ] **Step 4: Run test to verify it passes**

Run: `cd pkg/engine && go test -run TestForEachConnected -v`
Expected: PASS

- [ ] **Step 5: Run all existing PlayerManager tests**

Run: `cd pkg/engine && go test -v`
Expected: All PASS

- [ ] **Step 6: Commit**

```bash
git add pkg/engine/player_manager.go pkg/engine/player_manager_test.go
git commit -m "feat(engine): add ForEachConnected to PlayerManager"
```

---

### Task 2: InputRouter Core — Types, Handle, and Dispatch

**Files:**
- Create: `pkg/engine/input_router.go`
- Create: `pkg/engine/input_router_test.go`

- [ ] **Step 1: Write the failing test for basic dispatch**

Create `pkg/engine/input_router_test.go` with test helpers and first test:

```go
package engine

import (
	"encoding/binary"
	"fmt"
	"testing"

	"github.com/zenion/mmokit/pkg/net"
)

// --- Test helpers ---

// mockTransport implements net.Transport for testing.
type mockTransport struct {
	inputQueue [][]byte
}

func (m *mockTransport) SendReliable(data []byte)   {}
func (m *mockTransport) SendUnreliable(data []byte)  {}
func (m *mockTransport) DrainInput() [][]byte {
	msgs := m.inputQueue
	m.inputQueue = nil
	return msgs
}
func (m *mockTransport) DrainOpInput() [][]byte { return nil }
func (m *mockTransport) Close()                 {}

// mockTransports tracks mock transports by connID for test injection.
var mockTransports = map[uint32]*mockTransport{}

// newRouterTestEngine creates an Engine with a ConnManager and registers
// a mock transport for the given connIDs.
func newRouterTestEngine(connIDs ...uint32) *Engine {
	eng := newTestEngine()
	for _, id := range connIDs {
		mt := &mockTransport{}
		mockTransports[id] = mt
		eng.ConnMgr.AddTransport(id, mt)
	}
	return eng
}

// injectInput pushes raw bytes into a mock transport's input queue.
func injectInput(connID uint32, data []byte) {
	if mt, ok := mockTransports[connID]; ok {
		mt.inputQueue = append(mt.inputQueue, data)
	}
}

// testEnvelopeParser is a minimal envelope parser for tests.
// Format: 4 bytes little-endian code + rest is data.
func testEnvelopeParser(raw []byte) (uint32, []byte, error) {
	if len(raw) < 4 {
		return 0, nil, fmt.Errorf("too short")
	}
	code := binary.LittleEndian.Uint32(raw[:4])
	return code, raw[4:], nil
}

func makeTestMsg(code uint32, data []byte) []byte {
	buf := make([]byte, 4+len(data))
	binary.LittleEndian.PutUint32(buf[:4], code)
	copy(buf[4:], data)
	return buf
}

// --- Tests ---

func TestInputRouter_BasicDispatch(t *testing.T) {
	mockTransports = map[uint32]*mockTransport{} // reset
	eng := newRouterTestEngine(1)
	router := NewInputRouter(eng, testEnvelopeParser)

	var called bool
	var receivedData []byte
	router.Handle(42, States(StateActive), func(ctx *InputContext, data []byte) {
		called = true
		receivedData = data
	})

	// Create an active player session
	sess := eng.Players.createSession(1)
	sess.Username = "test"
	eng.Players.byUsername["test"] = sess
	_ = eng.Players.Transition(sess, StateActive)

	// Queue a message for connID 1
	injectInput(1, makeTestMsg(42, []byte("hello")))

	router.ProcessInput()

	if !called {
		t.Fatal("handler was not called")
	}
	if string(receivedData) != "hello" {
		t.Fatalf("expected data 'hello', got %q", string(receivedData))
	}
}
```

Note: Check that `ConnManager.AddTransport(connID, transport)` exists. If the method is named differently (e.g., it's done via `HandleWebSocket`), adapt the helper. The key requirement is registering a mock transport so `DrainInput(connID)` returns our injected bytes.

- [ ] **Step 2: Run test to verify it fails**

Run: `cd pkg/engine && go test -run TestInputRouter_BasicDispatch -v`
Expected: FAIL — `NewInputRouter` not defined

- [ ] **Step 3: Implement InputRouter core**

Create `pkg/engine/input_router.go`:

```go
package engine

import (
	"fmt"

	"github.com/mlange-42/ark/ecs"
)

// StateMask is a bitmask of PlayerState values.
type StateMask uint32

// States builds a StateMask from one or more PlayerState values.
func States(states ...PlayerState) StateMask {
	var m StateMask
	for _, s := range states {
		m |= 1 << StateMask(s)
	}
	return m
}

// InputContext is passed to every input handler.
// Entity may be zero-value for players without an ECS entity (e.g. StateDead).
type InputContext struct {
	Session *PlayerSession
	ConnID  uint32
	Entity  ecs.Entity
}

// InputFilter is a predicate used for state-group filters and per-handler guards.
type InputFilter func(ctx *InputContext) bool

// EnvelopeParser extracts a message code and inner payload from raw wire bytes.
type EnvelopeParser func(raw []byte) (code uint32, data []byte, err error)

// HandlerOption configures optional per-handler behavior.
type HandlerOption func(*handlerEntry)

// WithGuard sets a per-handler guard. If it returns false, the message is skipped.
func WithGuard(fn InputFilter) HandlerOption {
	return func(e *handlerEntry) {
		e.guard = fn
	}
}

type handlerEntry struct {
	states StateMask
	guard  InputFilter
	fn     func(ctx *InputContext, data []byte)
}

// InputRouter dispatches client messages to registered handlers based on
// message code and player state. It implements the System interface.
type InputRouter struct {
	eng      *Engine
	parse    EnvelopeParser
	handlers map[uint32]*handlerEntry
	filters  map[PlayerState]InputFilter
}

// NewInputRouter creates an InputRouter wired to the given Engine.
func NewInputRouter(eng *Engine, parse EnvelopeParser) *InputRouter {
	return &InputRouter{
		eng:      eng,
		parse:    parse,
		handlers: make(map[uint32]*handlerEntry),
		filters:  make(map[PlayerState]InputFilter),
	}
}

// Name implements System.
func (r *InputRouter) Name() string { return "InputRouter" }

// Update implements System.
func (r *InputRouter) Update(dt float32) { r.ProcessInput() }

// StateFilter sets a shared precondition for all handlers matching a player state.
// Runs once per player per tick. If it returns false, all messages from that player
// are drained and discarded.
func (r *InputRouter) StateFilter(state PlayerState, fn InputFilter) {
	r.filters[state] = fn
}

// Handle registers a handler for a message code. The data parameter contains
// the inner payload bytes (after envelope parsing). Panics on duplicate code.
func (r *InputRouter) Handle(code uint32, states StateMask, fn func(ctx *InputContext, data []byte), opts ...HandlerOption) {
	if _, exists := r.handlers[code]; exists {
		panic(fmt.Sprintf("InputRouter: duplicate handler for code %d", code))
	}
	entry := &handlerEntry{
		code:   code,
		states: states,
		fn:     fn,
	}
	for _, opt := range opts {
		opt(entry)
	}
	r.handlers[code] = entry
}

// ProcessInput drains and dispatches input for all connected, non-pending players.
func (r *InputRouter) ProcessInput() {
	r.eng.Players.ForEachConnected(func(sess *PlayerSession) {
		stateBit := StateMask(1) << StateMask(sess.State)
		ctx := &InputContext{
			Session: sess,
			ConnID:  sess.ConnID,
			Entity:  sess.Entity,
		}

		// State-group filter (once per player per tick)
		if filter, ok := r.filters[sess.State]; ok && !filter(ctx) {
			// Drain and discard to prevent buffer buildup
			r.eng.ConnMgr.DrainInput(sess.ConnID)
			return
		}

		msgs := r.eng.ConnMgr.DrainInput(sess.ConnID)
		if len(msgs) == 0 {
			return
		}

		for _, raw := range msgs {
			code, data, err := r.parse(raw)
			if err != nil {
				continue
			}

			entry := r.handlers[code]
			if entry == nil || entry.states&stateBit == 0 {
				continue
			}

			if entry.guard != nil && !entry.guard(ctx) {
				continue
			}

			entry.fn(ctx, data)
		}
	})
}
```

- [ ] **Step 4: Fix test — add test input injection helper if needed**

Check if `ConnManager` has a way to inject test input. If not, add a minimal test helper method. The `DrainInput` method returns `[][]byte` — we need to queue bytes for a connID.

Look at `pkg/net/server.go` for `DrainInput` implementation. If it drains from the transport, we may need to use a mock transport or add `InjectTestInput(connID, data)` to ConnManager for testing.

Add to `pkg/net/server.go` (or a `_test.go` file):

```go
// InjectTestInput pushes raw bytes into a connection's input buffer for testing.
// Only available in test builds — add to server_test.go if you prefer build-tag isolation.
func (cm *ConnManager) InjectTestInput(connID uint32, data []byte) {
	cm.mu.RLock()
	t, ok := cm.transports[connID]
	cm.mu.RUnlock()
	if ok {
		t.InjectInput(data)
	}
}
```

This depends on the Transport interface. Check if Transport has a test-friendly way to push data. If not, create a `mockTransport` in the test file that collects input in a buffer and returns it from `DrainInput`.

- [ ] **Step 5: Run test to verify it passes**

Run: `cd pkg/engine && go test -run TestInputRouter_BasicDispatch -v`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add pkg/engine/input_router.go pkg/engine/input_router_test.go
git commit -m "feat(engine): add InputRouter core with Handle and ProcessInput"
```

---

### Task 3: InputRouter Tests — State Filtering, Guards, Duplicates

**Files:**
- Modify: `pkg/engine/input_router_test.go`

- [ ] **Step 1: Write test for state mask filtering**

```go
func TestInputRouter_StateMaskFiltering(t *testing.T) {
	eng := newTestEngine()
	router := NewInputRouter(eng, testEnvelopeParser)

	var called bool
	router.Handle(1, States(StateDead), func(ctx *InputContext, data []byte) {
		called = true
	})

	// Create an active player — handler only accepts StateDead
	sess := eng.Players.createSession(1)
	sess.Username = "test"
	eng.Players.byUsername["test"] = sess
	_ = eng.Players.Transition(sess, StateActive)

	injectInput(1, makeTestMsg(1, nil))
	router.ProcessInput()

	if called {
		t.Fatal("handler should NOT fire for wrong state")
	}
}
```

- [ ] **Step 2: Write test for state-group filter**

```go
func TestInputRouter_StateGroupFilter(t *testing.T) {
	eng := newTestEngine()
	router := NewInputRouter(eng, testEnvelopeParser)

	var called bool
	router.Handle(1, States(StateActive), func(ctx *InputContext, data []byte) {
		called = true
	})

	// Filter blocks all active players
	router.StateFilter(StateActive, func(ctx *InputContext) bool {
		return false
	})

	sess := eng.Players.createSession(1)
	sess.Username = "test"
	eng.Players.byUsername["test"] = sess
	_ = eng.Players.Transition(sess, StateActive)

	injectInput(1, makeTestMsg(1, nil))
	router.ProcessInput()

	if called {
		t.Fatal("handler should NOT fire when state filter blocks")
	}
}
```

- [ ] **Step 3: Write test for per-handler guard**

```go
func TestInputRouter_PerHandlerGuard(t *testing.T) {
	eng := newTestEngine()
	router := NewInputRouter(eng, testEnvelopeParser)

	var guardedCalled, ungardedCalled bool
	router.Handle(1, States(StateActive), func(ctx *InputContext, data []byte) {
		guardedCalled = true
	}, WithGuard(func(ctx *InputContext) bool {
		return false // always block
	}))
	router.Handle(2, States(StateActive), func(ctx *InputContext, data []byte) {
		ungardedCalled = true
	})

	sess := eng.Players.createSession(1)
	sess.Username = "test"
	eng.Players.byUsername["test"] = sess
	_ = eng.Players.Transition(sess, StateActive)

	injectInput(1, makeTestMsg(1, nil))
	injectInput(1, makeTestMsg(2, nil))
	router.ProcessInput()

	if guardedCalled {
		t.Fatal("guarded handler should NOT fire")
	}
	if !ungardedCalled {
		t.Fatal("unguarded handler should fire")
	}
}
```

- [ ] **Step 4: Write test for duplicate registration panic**

```go
func TestInputRouter_DuplicateCodePanics(t *testing.T) {
	eng := newTestEngine()
	router := NewInputRouter(eng, testEnvelopeParser)

	router.Handle(1, States(StateActive), func(ctx *InputContext, data []byte) {})

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on duplicate code registration")
		}
	}()

	router.Handle(1, States(StateDead), func(ctx *InputContext, data []byte) {})
}
```

- [ ] **Step 5: Write test for pending session exclusion**

```go
func TestInputRouter_PendingSessionsExcluded(t *testing.T) {
	eng := newTestEngine()
	router := NewInputRouter(eng, testEnvelopeParser)

	var called bool
	router.Handle(1, States(StatePending), func(ctx *InputContext, data []byte) {
		called = true
	})

	// Session stays in StatePending
	eng.Players.createSession(1)
	injectInput(1, makeTestMsg(1, nil))
	router.ProcessInput()

	if called {
		t.Fatal("pending sessions should be excluded from router dispatch")
	}
}
```

- [ ] **Step 6: Run all tests**

Run: `cd pkg/engine && go test -v`
Expected: All PASS

- [ ] **Step 7: Commit**

```bash
git add pkg/engine/input_router_test.go
git commit -m "test(engine): add InputRouter state filtering, guard, and edge case tests"
```

---

### Task 4: HandleProto Generic Function

**Files:**
- Modify: `pkg/engine/input_router.go`
- Modify: `pkg/engine/input_router_test.go`

- [ ] **Step 1: Write the failing test**

Add to `pkg/engine/input_router_test.go`. Since `pkg/engine` can't import protobuf game types, use a test-local proto-like struct with a custom marshal/unmarshal. The real `HandleProto` uses `proto.Message` — for unit testing, we can create a test-only variant or use a real proto type from `enginepb` (which the test can import).

Actually, since we want `pkg/engine/` to stay protobuf-free, `HandleProto` needs the `proto.Message` interface. The generic constraint is `P interface{ *T; proto.Message }`. This means `input_router.go` needs to import `google.golang.org/protobuf/proto`. This is a lighter dependency than `enginepb` — it's just the proto runtime, not any generated code.

Alternative: make `HandleProto` also use an injected unmarshal function rather than calling `proto.Unmarshal` directly. This keeps `pkg/engine/` fully proto-free:

```go
// HandleFunc registers a typed handler with a custom unmarshal function.
func HandleFunc[T any](r *InputRouter, code uint32, states StateMask,
    unmarshal func([]byte) (T, error),
    fn func(ctx *InputContext, msg T), opts ...HandlerOption)
```

Then `pkg/mmokit/` provides `HandleProto[T]` as a convenience that wraps `HandleFunc` with `proto.Unmarshal`. This is the cleanest separation.

Write the test using `HandleFunc` directly:

```go
type testMsg struct {
	Value string
}

func TestInputRouter_HandleFunc(t *testing.T) {
	eng := newTestEngine()
	router := NewInputRouter(eng, testEnvelopeParser)

	var received *testMsg
	HandleFunc(router, 10, States(StateActive),
		func(data []byte) (testMsg, error) {
			return testMsg{Value: string(data)}, nil
		},
		func(ctx *InputContext, msg testMsg) {
			received = &msg
		},
	)

	sess := eng.Players.createSession(1)
	sess.Username = "test"
	eng.Players.byUsername["test"] = sess
	_ = eng.Players.Transition(sess, StateActive)

	injectInput(1, makeTestMsg(10, []byte("world")))
	router.ProcessInput()

	if received == nil || received.Value != "world" {
		t.Fatalf("expected msg with Value='world', got %v", received)
	}
}

func TestInputRouter_HandleFunc_UnmarshalError(t *testing.T) {
	eng := newTestEngine()
	router := NewInputRouter(eng, testEnvelopeParser)

	var called bool
	HandleFunc(router, 10, States(StateActive),
		func(data []byte) (testMsg, error) {
			return testMsg{}, fmt.Errorf("bad data")
		},
		func(ctx *InputContext, msg testMsg) {
			called = true
		},
	)

	sess := eng.Players.createSession(1)
	sess.Username = "test"
	eng.Players.byUsername["test"] = sess
	_ = eng.Players.Transition(sess, StateActive)

	injectInput(1, makeTestMsg(10, []byte("garbage")))
	router.ProcessInput()

	if called {
		t.Fatal("handler should NOT fire on unmarshal error")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd pkg/engine && go test -run TestInputRouter_HandleFunc -v`
Expected: FAIL — `HandleFunc` not defined

- [ ] **Step 3: Implement HandleFunc**

Add to `pkg/engine/input_router.go`:

```go
// HandleFunc registers a typed handler with a custom unmarshal function.
// The router calls unmarshal(data) and passes the result to fn.
// If unmarshal returns an error, the message is silently skipped.
// Panics on duplicate code registration.
func HandleFunc[T any](r *InputRouter, code uint32, states StateMask,
	unmarshal func([]byte) (T, error),
	fn func(ctx *InputContext, msg T), opts ...HandlerOption) {

	r.Handle(code, states, func(ctx *InputContext, data []byte) {
		msg, err := unmarshal(data)
		if err != nil {
			return
		}
		fn(ctx, msg)
	}, opts...)
}
```

- [ ] **Step 4: Run tests**

Run: `cd pkg/engine && go test -v`
Expected: All PASS

- [ ] **Step 5: Commit**

```bash
git add pkg/engine/input_router.go pkg/engine/input_router_test.go
git commit -m "feat(engine): add HandleFunc generic typed handler registration"
```

---

### Task 5: mmokit Facade — Re-exports and ProtoEnvelopeParser + HandleProto

**Files:**
- Modify: `pkg/mmokit/mmokit.go`

- [ ] **Step 1: Add re-exports and ProtoEnvelopeParser**

Add to `pkg/mmokit/mmokit.go` in the appropriate sections:

In the Engine type aliases section:

```go
type InputRouter = engine.InputRouter
type InputContext = engine.InputContext
type StateMask = engine.StateMask
type InputFilter = engine.InputFilter
type EnvelopeParser = engine.EnvelopeParser
type HandlerOption = engine.HandlerOption
```

In the constructors/functions section:

```go
var (
	NewInputRouter = engine.NewInputRouter
	States         = engine.States
	WithGuard      = engine.WithGuard
)
```

Add the `ProtoEnvelopeParser` and `HandleProto` — these live in mmokit because they import `enginepb`:

```go
// ProtoEnvelopeParser unmarshals an enginepb.ClientEvent envelope.
func ProtoEnvelopeParser(raw []byte) (uint32, []byte, error) {
	var evt enginepb.ClientEvent
	if err := proto.Unmarshal(raw, &evt); err != nil {
		return 0, nil, err
	}
	return evt.Code, evt.Data, nil
}

// HandleProto registers a typed protobuf handler on an InputRouter.
// The router unmarshals T from the envelope's data field before calling fn.
func HandleProto[T any, P interface{ *T; proto.Message }](
	r *engine.InputRouter, code uint32, states engine.StateMask,
	fn func(ctx *engine.InputContext, msg P), opts ...engine.HandlerOption) {

	engine.HandleFunc(r, code, states,
		func(data []byte) (P, error) {
			var msg P = new(T)
			if err := proto.Unmarshal(data, msg); err != nil {
				return nil, err
			}
			return msg, nil
		},
		fn, opts...)
}
```

Add imports for `enginepb` and `proto`:

```go
import (
	enginepb "github.com/zenion/mmokit/gen/go/enginepb"
	"google.golang.org/protobuf/proto"
)
```

- [ ] **Step 2: Verify build**

Run: `make build`
Expected: Success

- [ ] **Step 3: Commit**

```bash
git add pkg/mmokit/mmokit.go
git commit -m "feat(mmokit): re-export InputRouter types and add ProtoEnvelopeParser + HandleProto"
```

---

### Task 6: Main Game Migration — Extract Handler Functions

**Files:**
- Create: `internal/system/input_handlers.go`
- Reference: `internal/system/input.go` (will be deleted in Task 7)

This task extracts each switch case from the current `input.go` into a standalone handler function. Each handler receives `*mmokit.InputContext` and (for typed handlers) the unmarshaled proto message.

- [ ] **Step 1: Create input_handlers.go with all handler functions**

Create `internal/system/input_handlers.go`. Each function is extracted from the corresponding switch case in `input.go`. The handlers capture `*game.GameWorld` via closure when registered (see Task 7).

```go
package system

import (
	"strings"

	"google.golang.org/protobuf/proto"

	enginepb "github.com/zenion/mmokit/gen/go/enginepb"
	gamepb "github.com/zenion/mmokit/gen/go/gamepb"
	"github.com/zenion/mmokit/internal/game"
	"github.com/zenion/mmokit/internal/item"
	"github.com/zenion/mmokit/pkg/mmokit"
)

func handlePlayerInput(gw *game.GameWorld) func(ctx *mmokit.InputContext, msg *gamepb.PlayerInputMsg) {
	return func(ctx *mmokit.InputContext, msg *gamepb.PlayerInputMsg) {
		input := gw.C.PlayerInput.Get(ctx.Entity)
		input.Sequence = msg.Sequence

		// Suppress movement/ability input while docking
		if ctx.Session.State == game.StateDocking {
			return
		}

		input.JettisonItemID = msg.Jettison
		input.AbilityCast = msg.AbilityCast
		input.LockTargetNetID = msg.LockTargetId

		if msg.MoveActive && gw.C.MoveTarget.HasAll(ctx.Entity) {
			mt := gw.C.MoveTarget.Get(ctx.Entity)
			mt.X = msg.MoveX
			mt.Y = msg.MoveY
			if gw.C.SectorCoord.HasAll(ctx.Entity) {
				sec := gw.C.SectorCoord.Get(ctx.Entity)
				mt.SX = sec.SX
				mt.SY = sec.SY
			}
			mt.Active = true
		}

		netID := gw.C.NetworkID.Get(ctx.Entity).ID
		gw.Log.Log(game.CatInput, "player=%d abilities=0x%x lock=%d seq=%d",
			netID, input.AbilityCast, input.LockTargetNetID, input.Sequence)
	}
}

func handleChat(gw *game.GameWorld) func(ctx *mmokit.InputContext, msg *enginepb.ChatMsg) {
	return func(ctx *mmokit.InputContext, msg *enginepb.ChatMsg) {
		text := strings.TrimSpace(msg.Text)
		if len(text) == 0 || len(text) > 200 {
			return
		}
		username := ctx.Session.Username
		mmokit.Enqueue(gw.Queue, &enginepb.ChatMsg{
			Username: username,
			Text:     text,
		})
		gw.Log.Log(game.CatChat, "<%s> %s", username, text)
		gw.Bridge.RelayChatToOtherNodes(username, text)
	}
}

func handleDock(gw *game.GameWorld) func(ctx *mmokit.InputContext, data []byte) {
	return func(ctx *mmokit.InputContext, data []byte) {
		mmokit.Enqueue(gw.Queue, game.PendingDockRequest{ConnID: ctx.ConnID})
	}
}

func handleUndock(gw *game.GameWorld) func(ctx *mmokit.InputContext, data []byte) {
	return func(ctx *mmokit.InputContext, data []byte) {
		mmokit.Enqueue(gw.Queue, game.PendingUndockRequest{ConnID: ctx.ConnID})
	}
}

func handleRespawn(gw *game.GameWorld) func(ctx *mmokit.InputContext, data []byte) {
	return func(ctx *mmokit.InputContext, data []byte) {
		gw.Log.Log(game.CatSpawn, "respawn requested: conn=%d", ctx.ConnID)
		mmokit.Enqueue(gw.Queue, game.PendingRespawn{ConnID: ctx.ConnID})
	}
}

func handleInventoryTransfer(gw *game.GameWorld) func(ctx *mmokit.InputContext, msg *gamepb.InventoryTransferMsg) {
	return func(ctx *mmokit.InputContext, msg *gamepb.InventoryTransferMsg) {
		mmokit.Enqueue(gw.Queue, game.PendingTransfer{
			ConnID:  ctx.ConnID,
			ItemID:  msg.ItemId,
			Amount:  msg.Quantity,
			Deposit: msg.Deposit,
		})
	}
}

func handleBankRequest(gw *game.GameWorld) func(ctx *mmokit.InputContext, data []byte) {
	return func(ctx *mmokit.InputContext, data []byte) {
		mmokit.Enqueue(gw.Queue, game.PendingBankRequest{ConnID: ctx.ConnID})
	}
}

func handleSellBankItem(gw *game.GameWorld) func(ctx *mmokit.InputContext, msg *gamepb.SellBankItemMsg) {
	return func(ctx *mmokit.InputContext, msg *gamepb.SellBankItemMsg) {
		mmokit.Enqueue(gw.Queue, game.PendingSellRequest{
			ConnID: ctx.ConnID,
			ItemID: msg.ItemId,
			Amount: msg.Quantity,
		})
	}
}

func handleEquip(gw *game.GameWorld) func(ctx *mmokit.InputContext, msg *gamepb.EquipRequestMsg) {
	return func(ctx *mmokit.InputContext, msg *gamepb.EquipRequestMsg) {
		mmokit.Enqueue(gw.Queue, game.PendingEquipRequest{
			ConnID: ctx.ConnID,
			ItemID: msg.ItemId,
			Slot:   item.EquipSlot(msg.Slot),
		})
	}
}

func handleShopBuy(gw *game.GameWorld) func(ctx *mmokit.InputContext, msg *gamepb.ShopBuyMsg) {
	return func(ctx *mmokit.InputContext, msg *gamepb.ShopBuyMsg) {
		mmokit.Enqueue(gw.Queue, game.PendingShopBuy{
			ConnID: ctx.ConnID,
			ItemID: msg.ItemId,
			Qty:    msg.Quantity,
		})
	}
}

func handleLootItem(gw *game.GameWorld) func(ctx *mmokit.InputContext, msg *gamepb.LootItemMsg) {
	return func(ctx *mmokit.InputContext, msg *gamepb.LootItemMsg) {
		mmokit.Enqueue(gw.Queue, game.PendingLootItem{
			ConnID:     ctx.ConnID,
			CrateNetID: msg.CrateNetId,
			ItemID:     msg.ItemId,
		})
	}
}

func handleLootAll(gw *game.GameWorld) func(ctx *mmokit.InputContext, msg *gamepb.LootAllMsg) {
	return func(ctx *mmokit.InputContext, msg *gamepb.LootAllMsg) {
		mmokit.Enqueue(gw.Queue, game.PendingLootAll{
			ConnID:     ctx.ConnID,
			CrateNetID: msg.CrateNetId,
		})
	}
}

// RegisterInputHandlers creates an InputRouter and registers all game-specific
// input handlers. Returns the router (which implements mmokit.System).
func RegisterInputHandlers(eng *mmokit.Engine, gw *game.GameWorld) *mmokit.InputRouter {
	router := mmokit.NewInputRouter(eng, mmokit.ProtoEnvelopeParser)

	// State-group filters
	router.StateFilter(mmokit.StateActive, func(ctx *mmokit.InputContext) bool {
		return gw.ECS.Alive(ctx.Entity) && !gw.C.Ghost.HasAll(ctx.Entity)
	})
	router.StateFilter(game.StateDocking, func(ctx *mmokit.InputContext) bool {
		return gw.ECS.Alive(ctx.Entity)
	})

	// Active + docking states for movement input
	movementStates := mmokit.States(mmokit.StateActive, game.StateDocking)
	// States that accept trading/economy messages
	tradingStates := mmokit.States(mmokit.StateActive, game.StateDocked)
	// All "alive-ish" states that accept chat
	chatStates := mmokit.States(mmokit.StateActive, game.StateDocking, game.StateDocked)

	// Player input (movement, abilities)
	mmokit.HandleProto[gamepb.PlayerInputMsg](router,
		uint32(enginepb.ClientEventCode_CE_PLAYER_INPUT), movementStates,
		handlePlayerInput(gw))

	// Chat
	mmokit.HandleProto[enginepb.ChatMsg](router,
		uint32(enginepb.ClientEventCode_CE_CHAT), chatStates,
		handleChat(gw))

	// Docking
	router.Handle(uint32(gamepb.GameClientEventCode_GCE_DOCK),
		mmokit.States(mmokit.StateActive), handleDock(gw))
	router.Handle(uint32(gamepb.GameClientEventCode_GCE_UNDOCK),
		mmokit.States(game.StateDocked), handleUndock(gw))

	// Respawn
	router.Handle(uint32(enginepb.ClientEventCode_CE_RESPAWN),
		mmokit.States(mmokit.StateDead), handleRespawn(gw))

	// Trading / economy
	mmokit.HandleProto[gamepb.InventoryTransferMsg](router,
		uint32(gamepb.GameClientEventCode_GCE_INVENTORY_TRANSFER), tradingStates,
		handleInventoryTransfer(gw))
	router.Handle(uint32(gamepb.GameClientEventCode_GCE_BANK_REQUEST),
		tradingStates, handleBankRequest(gw))
	mmokit.HandleProto[gamepb.SellBankItemMsg](router,
		uint32(gamepb.GameClientEventCode_GCE_SELL_BANK_ITEM), tradingStates,
		handleSellBankItem(gw))
	mmokit.HandleProto[gamepb.EquipRequestMsg](router,
		uint32(gamepb.GameClientEventCode_GCE_EQUIP), tradingStates,
		handleEquip(gw))
	mmokit.HandleProto[gamepb.ShopBuyMsg](router,
		uint32(gamepb.GameClientEventCode_GCE_SHOP_BUY), tradingStates,
		handleShopBuy(gw))

	// Looting (active only — docked players cannot loot crates)
	lootStates := mmokit.States(mmokit.StateActive)
	mmokit.HandleProto[gamepb.LootItemMsg](router,
		uint32(gamepb.GameClientEventCode_GCE_LOOT_ITEM), lootStates,
		handleLootItem(gw))
	mmokit.HandleProto[gamepb.LootAllMsg](router,
		uint32(gamepb.GameClientEventCode_GCE_LOOT_ALL), lootStates,
		handleLootAll(gw))

	return router
}
```

- [ ] **Step 2: Verify the file compiles**

Run: `go build ./internal/system/`
Expected: Success (input.go and input_handlers.go coexist temporarily)

- [ ] **Step 3: Commit**

```bash
git add internal/system/input_handlers.go
git commit -m "feat(game): extract input handler functions for InputRouter migration"
```

---

### Task 7: Wire InputRouter into Factory, Delete Old InputSystem

**Files:**
- Modify: `internal/universe/factory.go`
- Delete: `internal/system/input.go`

- [ ] **Step 1: Update factory.go to use InputRouter**

Replace `system.NewInputSystem(gw)` with `system.RegisterInputHandlers(eng, gw)`:

In `internal/universe/factory.go`, change:

```go
systems := []mmokit.System{
    system.NewInputSystem(gw),
```

to:

```go
systems := []mmokit.System{
    system.RegisterInputHandlers(eng, gw),
```

Note: `eng` is already available as `base.Engine()` on line 16. Pass it to `RegisterInputHandlers`.

- [ ] **Step 2: Delete internal/system/input.go**

```bash
rm internal/system/input.go
```

- [ ] **Step 3: Remove unused imports from input_handlers.go**

After deleting `input.go`, check if `input_handlers.go` has any missing or unused imports. The `proto` import was used by `input.go` — `input_handlers.go` may not need it directly since `HandleProto` in mmokit handles unmarshaling. Clean up.

- [ ] **Step 4: Verify build**

Run: `make build`
Expected: Success

- [ ] **Step 5: Run full project tests**

Run: `go test ./...`
Expected: All pass

- [ ] **Step 6: Verify slither still builds**

Run: `cd examples/slither && go build ./...`
Expected: Success (slither doesn't depend on internal/system/input.go)

- [ ] **Step 7: Commit**

```bash
git add internal/universe/factory.go internal/system/input_handlers.go
git rm internal/system/input.go
git commit -m "refactor(game): replace InputSystem with InputRouter

Migrates from 337-line hand-written switch-based InputSystem to
declarative handler registration via InputRouter. Each message type
is now a one-liner registration with state mask and typed handler."
```

---

### Task 8: Manual Integration Test

**Files:** None (testing only)

- [ ] **Step 1: Start the server**

Run: `make run`
Expected: Server starts, no panics

- [ ] **Step 2: Connect via web client**

Open `http://localhost:8080` in a browser. Log in with a username.

Verify:
- Login works (player spawns)
- Movement works (click to move)
- Chat works (type a message)
- Abilities work (press Q/W/E/R)

- [ ] **Step 3: Test docking (if a station is nearby)**

Fly to a station and dock. Verify:
- Docking animation plays
- While docked: chat works, trading works, movement is suppressed
- Undock works

- [ ] **Step 4: Test death and respawn**

Get killed by an NPC or use a console command. Verify:
- Death notification received
- Respawn button works
- Player re-enters world

- [ ] **Step 5: Stop server**

Ctrl+C. No panics or errors on shutdown.
