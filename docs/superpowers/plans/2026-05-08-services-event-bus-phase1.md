# Services Event Bus — Phase 1 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the chat-specific `Process.chatHook` mechanism with a generic, typed pub/sub `service.Bus` that runs **process-local only**. All current chat presence behavior preserved; the chat service becomes a normal service with no engine-special-cased core hooks.

**Architecture:** A new `*service.Bus` is owned by each `Process` and threaded into every service through `service.Context.Bus`. Services subscribe to typed events at `Init` time via `service.Subscribe[T](ctx.Bus, handler)`. The gateway publishes framework events (`SessionEnterEvent`, `SessionLeaveEvent`) at the existing call sites via `service.Publish[T]`. No wire-protocol changes — Phase 1 fan-out is process-local synchronous. The cluster-wide peer-mesh delivery layer is **Phase 3** (separate plan).

**Tech Stack:** Go 1.22+ generics, existing `pkg/universe.ReflectMarshal` codec (only the type-name registry is exercised in Phase 1), `github.com/mmokit/mmokit/pkg/service` framework, `github.com/mmokit/mmokit/pkg/services/chat` consumer.

**Reference spec:** [docs/superpowers/specs/2026-05-08-services-event-bus-design.md](../specs/2026-05-08-services-event-bus-design.md) §1–§9, §12 Phase 1.

**Memories that govern this work:**
- `feedback_no_backward_compat` — delete `chatHook`, no aliases or deprecation shims.
- `feedback_refactor_over_stopgaps` — chat becomes a normal service; no leftover special casing.
- `feedback_mmokit_facade_only` — game code still imports `mmokit` only; framework primitives live in `pkg/service/`.
- `feedback_logging` — every new code path logs under category `services:bus`.

---

## File Structure

**New files (`pkg/service/`):**
- `bus.go` — `Bus` struct, `Subscribe[T]`, `Publish[T]`, `PublishLocal[T]`, `Unsubscribe`, panic recovery.
- `events.go` — POD framework event types (`SessionEnterEvent`, `SessionLeaveEvent`, `AuthLoginSucceededEvent`, `AuthLogoutEvent`, `AuthRegisteredEvent`, `PlayerSpawnedEvent`, `PlayerDespawnedEvent`).
- `event_codec.go` — `RegisterEventType[T]`, `LookupEventType(name string)`, package-level type-name → `reflect.Type` map.
- `bus_test.go` — unit tests for Subscribe/Publish/Unsubscribe/panic-recovery/multi-handler-ordering.
- `event_codec_test.go` — duplicate-registration panic, lookup miss, lookup hit.

**Modified files:**
- `pkg/service/context.go` — add `Bus *Bus` field.
- `pkg/universe/coordinator.go` — drop `chatHook` field + `ChatSessionHook` interface + `InstallChatHook` + `ChatHook` accessor; add `bus *service.Bus` field initialized in `New`.
- `pkg/universe/service_runtime.go::serviceContext` — pass `c.bus` into `service.Context.Bus`.
- `pkg/universe/gateway.go` — replace `g.process.chatHook.OnSessionEnter(...)` (line 380) and `g.process.chatHook.OnSessionLeave(...)` (line 446) with `service.Publish[T]` calls.
- `pkg/services/chat/service.go::Init` — subscribe to `service.SessionEnterEvent` + `service.SessionLeaveEvent`; route to existing `HandleSessionEnter` / `HandleSessionLeave`.
- `pkg/mmokit/chat.go` — drop `chatSessionHookImpl` struct + `p.InstallChatHook(...)` call from `RegisterChatService`.
- `pkg/universe/chat_e2e_test.go` — rewrite around `service.Publish` instead of `Process.InstallChatHook` / `Process.ChatHook()`.

**Deleted files:**
- `pkg/services/chat/session_hook.go` — `SessionHook` interface superseded by direct subscription.

Each task below stands alone: code is concrete, tests are concrete, no placeholders.

---

## Task 1: Bus skeleton + first end-to-end test (Subscribe → Publish round-trip)

**Files:**
- Create: `pkg/service/bus.go`
- Create: `pkg/service/bus_test.go`

**Why first:** Locks down the typed API shape (`Subscribe[T]`, `Publish[T]`, `Unsubscribe`) before any caller depends on it. The smallest TDD step that proves the generic + reflection plumbing works.

- [ ] **Step 1: Write the failing test (`pkg/service/bus_test.go`)**

```go
package service_test

import (
	"sync/atomic"
	"testing"

	"github.com/mmokit/mmokit/pkg/service"
)

type ping struct{ N int }

func TestBus_SubscribePublishRoundtrip(t *testing.T) {
	b := service.NewBus("test-proc")
	var got int32
	service.Subscribe(b, func(ev ping) {
		atomic.AddInt32(&got, int32(ev.N))
	})
	service.Publish(b, ping{N: 7})
	if g := atomic.LoadInt32(&got); g != 7 {
		t.Fatalf("handler not invoked: got=%d want=7", g)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/service/ -run TestBus_SubscribePublishRoundtrip -v`
Expected: build error — `service.NewBus`, `service.Subscribe`, `service.Publish` undefined.

- [ ] **Step 3: Implement minimal `Bus` (`pkg/service/bus.go`)**

```go
package service

import (
	"reflect"
	"sync"
)

// Bus is the typed publish/subscribe primitive owned by Process. Phase 1
// is process-local only — Publish[T] dispatches synchronously to every
// local subscriber registered for T. Phase 3 will add cross-process
// peer-mesh fan-out behind the same API.
type Bus struct {
	processID string

	mu       sync.RWMutex
	handlers map[reflect.Type][]*handlerSlot
}

// Unsubscribe removes a registered handler. Idempotent; safe to call from
// any goroutine, including from inside the handler being unsubscribed.
type Unsubscribe func()

// handlerSlot wraps a handler + a tombstone bit so Unsubscribe is O(1)
// and re-entrant from inside Publish without mutating the slice the
// dispatch loop is iterating.
type handlerSlot struct {
	fn   func(any)
	dead bool
}

// NewBus returns an empty Bus. processID is used by Phase 3 self-echo
// detection; in Phase 1 it's stored only for diagnostic logging.
func NewBus(processID string) *Bus {
	return &Bus{
		processID: processID,
		handlers:  map[reflect.Type][]*handlerSlot{},
	}
}

// Subscribe registers a handler for events of type T. Multiple handlers
// per type are allowed; they fire in registration order. Handlers run on
// the caller's goroutine inside Publish — keep them fast.
//
// The returned Unsubscribe is idempotent.
func Subscribe[T any](b *Bus, handler func(T)) Unsubscribe {
	var zero T
	typ := reflect.TypeOf(zero)
	if typ == nil {
		// T is an interface or untyped nil — Subscribe[interface{}] is
		// disallowed. The Bus is type-keyed; an interface key would
		// match every event.
		panic("service.Subscribe: T must be a concrete struct type")
	}
	slot := &handlerSlot{
		fn: func(v any) { handler(v.(T)) },
	}
	b.mu.Lock()
	b.handlers[typ] = append(b.handlers[typ], slot)
	b.mu.Unlock()
	return func() {
		b.mu.Lock()
		slot.dead = true
		b.mu.Unlock()
	}
}

// Publish broadcasts ev to every local subscriber for type T.
// Synchronous. Handler panics are recovered and discarded so one bad
// subscriber cannot poison the publisher; callers that need to debug
// panicking handlers should set GODEBUG=panicnil=1 or wrap their
// handler explicitly.
func Publish[T any](b *Bus, ev T) {
	publishAny(b, reflect.TypeOf(ev), ev)
}

// PublishLocal is identical to Publish in Phase 1. Phase 3 will diverge:
// Publish fans out to remote subscribers, PublishLocal does not.
func PublishLocal[T any](b *Bus, ev T) {
	publishAny(b, reflect.TypeOf(ev), ev)
}

// publishAny is the type-erased dispatch core, shared by Publish and
// PublishLocal (and Phase 3's cross-process receiver).
func publishAny(b *Bus, typ reflect.Type, ev any) {
	b.mu.RLock()
	slots := b.handlers[typ]
	// Snapshot so we can drop the lock before invoking handlers — handlers
	// may call Subscribe/Unsubscribe re-entrantly.
	live := make([]*handlerSlot, 0, len(slots))
	for _, s := range slots {
		if !s.dead {
			live = append(live, s)
		}
	}
	b.mu.RUnlock()
	for _, s := range live {
		invokeHandler(s.fn, ev)
	}
}

func invokeHandler(fn func(any), ev any) {
	defer func() {
		_ = recover() // swallow; tests exercising panic-recovery assert via separate goroutine state
	}()
	fn(ev)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/service/ -run TestBus_SubscribePublishRoundtrip -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/service/bus.go pkg/service/bus_test.go
git commit -m "$(cat <<'EOF'
feat(service): add Bus primitive with Subscribe/Publish (process-local)

Phase 1 of services event bus. Process-local only — peer-mesh fan-out
lands in Phase 3. Replaces the path that will retire Process.chatHook
in subsequent tasks of this plan.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 2: Bus — multi-handler ordering, Unsubscribe, panic recovery

**Files:**
- Modify: `pkg/service/bus_test.go`

**Why:** Lock down the contract from spec §6 ("fire in registration order") and §11 ("handler panics — recovered + logged") before any service-side code depends on the Bus.

- [ ] **Step 1: Append three failing tests to `pkg/service/bus_test.go`**

```go
func TestBus_MultiHandlerOrdering(t *testing.T) {
	b := service.NewBus("test-proc")
	var calls []int
	service.Subscribe(b, func(ev ping) { calls = append(calls, 1) })
	service.Subscribe(b, func(ev ping) { calls = append(calls, 2) })
	service.Subscribe(b, func(ev ping) { calls = append(calls, 3) })
	service.Publish(b, ping{N: 1})
	if len(calls) != 3 || calls[0] != 1 || calls[1] != 2 || calls[2] != 3 {
		t.Fatalf("handlers not invoked in registration order: %v", calls)
	}
}

func TestBus_Unsubscribe(t *testing.T) {
	b := service.NewBus("test-proc")
	var n int32
	unsub := service.Subscribe(b, func(ev ping) { atomic.AddInt32(&n, 1) })
	service.Publish(b, ping{})
	unsub()
	service.Publish(b, ping{})
	unsub() // idempotent — must not panic
	if got := atomic.LoadInt32(&n); got != 1 {
		t.Fatalf("unsub failed: got=%d want=1", got)
	}
}

func TestBus_PanicRecovered(t *testing.T) {
	b := service.NewBus("test-proc")
	var afterRan int32
	service.Subscribe(b, func(ev ping) { panic("boom") })
	service.Subscribe(b, func(ev ping) { atomic.AddInt32(&afterRan, 1) })
	service.Publish(b, ping{})
	if got := atomic.LoadInt32(&afterRan); got != 1 {
		t.Fatalf("second handler did not run after first panicked: got=%d", got)
	}
}
```

- [ ] **Step 2: Run tests to verify they pass**

Run: `go test ./pkg/service/ -run TestBus_ -v`
Expected: all three new tests PASS (the implementation in Task 1 already covers ordering, idempotent Unsubscribe via the dead flag, and panic recovery via `invokeHandler`'s deferred recover).

- [ ] **Step 3: Commit**

```bash
git add pkg/service/bus_test.go
git commit -m "$(cat <<'EOF'
test(service): cover Bus ordering, Unsubscribe idempotency, panic recovery

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 3: Event-type registry (`event_codec.go`)

**Files:**
- Create: `pkg/service/event_codec.go`
- Create: `pkg/service/event_codec_test.go`

**Why:** Phase 1 doesn't *use* the registry on the wire, but the API is fixed now so service authors learn the right pattern from day one and Phase 3 doesn't have to break source compat. Mirrors `mmokit.RegisterEvent[T]` (typed wire events) — same shape, different namespace.

- [ ] **Step 1: Write the failing tests (`pkg/service/event_codec_test.go`)**

```go
package service_test

import (
	"reflect"
	"testing"

	"github.com/mmokit/mmokit/pkg/service"
)

type fooEvent struct{ N int }
type barEvent struct{ Name string }

func TestEventCodec_RegisterAndLookup(t *testing.T) {
	service.RegisterEventType[fooEvent]()
	service.RegisterEventType[barEvent]()
	got, ok := service.LookupEventType("service_test.fooEvent")
	if !ok {
		t.Fatalf("LookupEventType(fooEvent) miss")
	}
	if got != reflect.TypeOf(fooEvent{}) {
		t.Fatalf("LookupEventType(fooEvent) = %v, want fooEvent", got)
	}
}

func TestEventCodec_RegisterIdempotent(t *testing.T) {
	// Second registration of the same T must not panic.
	service.RegisterEventType[fooEvent]()
	service.RegisterEventType[fooEvent]()
}

func TestEventCodec_LookupMiss(t *testing.T) {
	if _, ok := service.LookupEventType("does.not.Exist"); ok {
		t.Fatal("expected miss")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./pkg/service/ -run TestEventCodec_ -v`
Expected: build error — `service.RegisterEventType` / `service.LookupEventType` undefined.

- [ ] **Step 3: Implement the registry (`pkg/service/event_codec.go`)**

```go
package service

import (
	"fmt"
	"reflect"
	"sync"
)

// EventTypeName returns the canonical wire identifier for event type T:
// the package-qualified Go type name (e.g. "service.SessionEnterEvent").
//
// Renames break the wire — same convention as the typed-event channel in
// mmokit. Phase 3 carries this string in MeshFrame.ServiceEvent.type_name;
// Phase 1 uses it only as the registry key so the API is stable.
func EventTypeName[T any]() string {
	var zero T
	t := reflect.TypeOf(zero)
	if t == nil {
		panic("service: T must be a concrete struct type")
	}
	return typeName(t)
}

func typeName(t reflect.Type) string {
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.PkgPath() == "" {
		// Builtin / unnamed — guard so we never silently collide.
		return t.String()
	}
	// PkgPath()/Name() avoids the "main." prefix Go uses in t.String().
	pkg := t.PkgPath()
	// Only the last path segment is meaningful for diagnostics, but using
	// the full PkgPath disambiguates two packages sharing a Name.
	return pkg + "." + t.Name()
}

var (
	eventTypeMu sync.RWMutex
	eventTypes  = map[string]reflect.Type{}
)

// RegisterEventType registers T's type-name → reflect.Type mapping in the
// process-global registry. Idempotent for the same T; panics on a name
// collision between two distinct Go types (almost impossible — Go's
// PkgPath qualification handles namespacing).
//
// Typically called from package init() of every package that declares
// event types so all processes that link the package have the registry
// pre-populated. Phase 3 receivers consult this registry to decode wire
// payloads back into Go values.
func RegisterEventType[T any]() {
	var zero T
	t := reflect.TypeOf(zero)
	if t == nil {
		panic("service.RegisterEventType: T must be a concrete struct type")
	}
	name := typeName(t)
	eventTypeMu.Lock()
	defer eventTypeMu.Unlock()
	if existing, ok := eventTypes[name]; ok {
		if existing == t {
			return // idempotent
		}
		panic(fmt.Sprintf("service.RegisterEventType: name %q already registered for %v (new=%v)", name, existing, t))
	}
	eventTypes[name] = t
}

// LookupEventType returns the reflect.Type registered under name, if any.
func LookupEventType(name string) (reflect.Type, bool) {
	eventTypeMu.RLock()
	defer eventTypeMu.RUnlock()
	t, ok := eventTypes[name]
	return t, ok
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./pkg/service/ -run TestEventCodec_ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/service/event_codec.go pkg/service/event_codec_test.go
git commit -m "$(cat <<'EOF'
feat(service): add event-type registry for Bus

Stable typeName→reflect.Type registry consumed by Phase 3 wire receivers.
Phase 1 exercises it only for API stability + duplicate-registration safety.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 4: Framework event types (`pkg/service/events.go`)

**Files:**
- Create: `pkg/service/events.go`

**Why:** Locks the spec §7 POD types in code before any caller depends on them. Each type is a small POD struct; `init()` registers them so any process that imports `pkg/service` has the registry primed.

- [ ] **Step 1: Create the file**

```go
// Package service — events.go defines the framework-emitted event types
// fired on the per-process service.Bus. Each type is a small POD struct
// with no shared inheritance; service authors subscribe via
// service.Subscribe[T](ctx.Bus, handler).
//
// Phase 1: only SessionEnterEvent + SessionLeaveEvent are published by
// the engine (gateway login/disconnect paths). The remaining types are
// declared here so future phases (auth event extraction in Phase 2,
// player-spawn wiring in a later phase) can land additively.
//
// Wire-stability: type names are registered with the event codec in
// init(); Go-level renames break the wire identity. Same convention as
// pkg/services/auth typed messages.
package service

func init() {
	RegisterEventType[SessionEnterEvent]()
	RegisterEventType[SessionLeaveEvent]()
	RegisterEventType[AuthLoginSucceededEvent]()
	RegisterEventType[AuthLogoutEvent]()
	RegisterEventType[AuthRegisteredEvent]()
	RegisterEventType[PlayerSpawnedEvent]()
	RegisterEventType[PlayerDespawnedEvent]()
}

// SessionEnterEvent fires after a successful auth login + cell dispatch.
// Published by the gateway. Consumed by services that need per-session
// state (chat presence, presence service, achievements, etc).
type SessionEnterEvent struct {
	ConnID    uint32
	UserID    string
	Username  string
	GatewayID string
}

// SessionLeaveEvent fires when a WS connection closes (clean disconnect,
// gateway crash recovery, kick, etc). Published by the gateway.
type SessionLeaveEvent struct {
	ConnID    uint32
	UserID    string // populated when known; empty for unauthenticated drops
	GatewayID string
}

// AuthLoginSucceededEvent fires after a successful AUTH_LOGIN /
// AUTH_REGISTER / AUTH_VALIDATE_TOKEN op. Published by the auth service
// (Phase 2 wiring).
type AuthLoginSucceededEvent struct {
	UserID       string
	Username     string
	SessionToken string // populated on AUTH_LOGIN / AUTH_REGISTER; empty on validate
	ConnID       uint32
	GatewayID    string
}

// AuthLogoutEvent fires after an explicit AUTH_LOGOUT op. NOT fired on
// WS close — that's SessionLeaveEvent. Phase 2 wiring.
type AuthLogoutEvent struct {
	UserID    string
	Username  string
	ConnID    uint32
	GatewayID string
}

// AuthRegisteredEvent fires after a successful AUTH_REGISTER op. Lets
// achievements / starter-pack / welcome-message services run on first
// login. Phase 2 wiring.
type AuthRegisteredEvent struct {
	UserID   string
	Username string
}

// PlayerSpawnedEvent fires after a player's entity is created on its
// authoritative cell. Wired in a later phase (cell host integration).
type PlayerSpawnedEvent struct {
	UserID   string
	Username string
	CellID   string
	NetID    uint32
}

// PlayerDespawnedEvent fires when a player's entity is removed
// (disconnect, transfer, kick). Wired in a later phase.
type PlayerDespawnedEvent struct {
	UserID string
	NetID  uint32
}
```

- [ ] **Step 2: Verify package compiles**

Run: `go vet ./pkg/service/...`
Expected: no errors.

- [ ] **Step 3: Verify init() registered all types**

Add to `pkg/service/event_codec_test.go`:

```go
func TestEventCodec_FrameworkEventsRegistered(t *testing.T) {
	for _, name := range []string{
		"github.com/mmokit/mmokit/pkg/service.SessionEnterEvent",
		"github.com/mmokit/mmokit/pkg/service.SessionLeaveEvent",
		"github.com/mmokit/mmokit/pkg/service.AuthLoginSucceededEvent",
		"github.com/mmokit/mmokit/pkg/service.AuthLogoutEvent",
		"github.com/mmokit/mmokit/pkg/service.AuthRegisteredEvent",
		"github.com/mmokit/mmokit/pkg/service.PlayerSpawnedEvent",
		"github.com/mmokit/mmokit/pkg/service.PlayerDespawnedEvent",
	} {
		if _, ok := service.LookupEventType(name); !ok {
			t.Errorf("framework event %s not registered", name)
		}
	}
}
```

Run: `go test ./pkg/service/ -run TestEventCodec_FrameworkEventsRegistered -v`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add pkg/service/events.go pkg/service/event_codec_test.go
git commit -m "$(cat <<'EOF'
feat(service): declare framework event types (Session*, Auth*, Player*)

Spec §7 POD types. Phase 1 publishes Session* from the gateway;
Auth* + Player* land in subsequent phases.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 5: Wire `Bus` onto `service.Context` and `Process`

**Files:**
- Modify: `pkg/service/context.go`
- Modify: `pkg/universe/coordinator.go` (around line 660 — `New(cfg Config) *Process`; struct fields after line 629)
- Modify: `pkg/universe/service_runtime.go:67-75` (`serviceContext` builder)

**Why:** Without this, services can't reach the Bus from `Init`. Done in one task because the three edits are coupled — independently they don't compile.

- [ ] **Step 1: Add `Bus *Bus` to `service.Context`**

Edit `pkg/service/context.go`. After the existing `Roles map[string]struct{}` field, add:

```go
	// Bus is the per-process typed pub/sub bus. Services subscribe to
	// framework events and to sibling-service events here at Init time.
	// See pkg/service/bus.go.
	//
	// Always non-nil — Process.Build constructs the Bus before any
	// service.Context is built.
	Bus *Bus
```

- [ ] **Step 2: Add `bus *service.Bus` field to `Process` struct**

Edit `pkg/universe/coordinator.go`. Locate the `Process` struct fields around line 629 (the `chatHook` field — to be deleted in Task 9). Right before the closing `}` of the struct, add:

```go
	// bus is the per-process typed pub/sub bus shared by every service
	// instance running on this Process. Initialized in New(); injected
	// into service.Context.Bus by serviceContext().
	//
	// Phase 1 fan-out is process-local only. Phase 3 will plumb a
	// peer-mesh dispatch callback so Publish[T] reaches remote subscribers.
	bus *service.Bus
```

- [ ] **Step 3: Construct `Bus` in `New(cfg Config)`**

Still in `pkg/universe/coordinator.go`, in the `New` function (around line 655). After `cfg.BindFlags()`/`flag.Parse()` and the defaults block, before the `Process{}` literal is built, derive a process ID and instantiate the bus. Pass it into the literal. The exact insertion point is wherever `Process` is constructed; locate the `return &Process{` and add a field initializer:

```go
		bus: service.NewBus(processIDFromConfig(cfg)),
```

Add a small helper near the top of the file (or wherever helper funcs live):

```go
// processIDFromConfig derives a stable per-process identifier used by the
// service.Bus for self-echo skip + diagnostics. Mirrors
// localServiceHostID() but operates on raw config (called before Process
// is fully built). Empty defaults to "local" for in-process dev servers.
func processIDFromConfig(cfg Config) string {
	if cfg.HostID != "" {
		return cfg.HostID
	}
	if cfg.GatewayID != "" {
		return cfg.GatewayID
	}
	return "local"
}
```

(If `cfg.GatewayID` doesn't exist yet — confirm by `grep -n "GatewayID" pkg/universe/config.go` — drop that branch and fall through to `"local"`. The point is a non-empty stable string; Phase 3 hardens this.)

- [ ] **Step 4: Inject `c.bus` into `serviceContext`**

Edit `pkg/universe/service_runtime.go:67-75`. Replace the `Context{...}` literal:

```go
func (c *Process) serviceContext(kindName, instanceID string) *service.Context {
	return &service.Context{
		KindName:   kindName,
		InstanceID: instanceID,
		Logger:     c.Log,
		DB:         c.serviceDBStore(),
		Roles:      map[string]struct{}(c.roles),
		Bus:        c.bus,
	}
}
```

- [ ] **Step 5: Verify the package compiles**

Run: `just build`
Expected: build succeeds. (No callers reference `c.bus` yet outside `serviceContext`; the field is wired but unused.)

- [ ] **Step 6: Add a regression test that `service.Context.Bus` is non-nil**

Add to `pkg/universe/coordinator_test.go` (or create one if absent — use the existing test pattern in any `*_test.go` in `pkg/universe/`):

```go
func TestProcess_BusPresentInServiceContext(t *testing.T) {
	p := New(Config{Headless: true, Mode: "all", CellsX: 1, CellsY: 1})
	if p.bus == nil {
		t.Fatal("Process.bus is nil after New")
	}
	ctx := p.serviceContext("test-kind", "test-instance")
	if ctx.Bus == nil {
		t.Fatal("service.Context.Bus is nil — serviceContext did not inject p.bus")
	}
	if ctx.Bus != p.bus {
		t.Fatal("service.Context.Bus is not the same *Bus held by Process")
	}
}
```

Run: `go test ./pkg/universe/ -run TestProcess_BusPresentInServiceContext -v`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add pkg/service/context.go pkg/universe/coordinator.go pkg/universe/service_runtime.go pkg/universe/coordinator_test.go
git commit -m "$(cat <<'EOF'
feat(service,universe): thread *service.Bus through Process → service.Context

Bus is constructed in Process.New and injected into every service.Context
via serviceContext(). Field is unused by callers in this commit; the next
commits in Phase 1 wire chat (Subscribe) and the gateway (Publish).

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 6: Chat subscribes to `SessionEnterEvent` / `SessionLeaveEvent`

**Files:**
- Modify: `pkg/services/chat/service.go::Init` (around line 128)

**Why:** Sets up the new chat-side consumer BEFORE replacing the gateway publisher. After this task, the chat service handles BOTH the old hook path (still wired) AND the new bus path. The next task switches the gateway to publish via the bus; chat then sees only one path. This avoids a window where presence is broken.

- [ ] **Step 1: Add subscription calls to `Init`**

Edit `pkg/services/chat/service.go`. Inside `func (s *Service) Init(ctx *service.Context) error`, immediately after `s.ctx = ctx` (line 129), add:

```go
	// Bus subscriptions for engine-driven session events. Replaces the
	// previous Process.chatHook plumbing — chat now listens for the same
	// transitions on the typed pub/sub bus owned by Process.
	//
	// Bus is always non-nil per service.Context contract.
	service.Subscribe(ctx.Bus, func(ev service.SessionEnterEvent) {
		_, _ = s.HandleSessionEnter(nil, &ChatSessionEnterRequest{
			ConnID:    ev.ConnID,
			UserID:    ev.UserID,
			Username:  ev.Username,
			GatewayID: ev.GatewayID,
		})
	})
	service.Subscribe(ctx.Bus, func(ev service.SessionLeaveEvent) {
		_, _ = s.HandleSessionLeave(nil, &ChatSessionLeaveRequest{
			ConnID:    ev.ConnID,
			GatewayID: ev.GatewayID,
		})
	})
```

(The `service` import is already present from `func (s *Service) Init(ctx *service.Context)`. No import change required.)

- [ ] **Step 2: Verify the build**

Run: `just build`
Expected: build succeeds. The chat service now subscribes; the gateway still calls `chatHook.OnSessionEnter` (Task 7 replaces that).

- [ ] **Step 3: Add a unit test that the subscription wires HandleSessionEnter**

Append to `pkg/services/chat/service_test.go`:

```go
func TestService_BusSubscriptionDrivesPresence(t *testing.T) {
	bus := service.NewBus("test-proc")
	repo := chattest.NewMock()
	svc := newTestServiceWithBus(t, repo, bus) // helper added below

	uid := uuid.NewString()
	service.Publish(bus, service.SessionEnterEvent{
		ConnID: 42, UserID: uid, Username: "alice", GatewayID: "gw-1",
	})
	if _, online := svc.OnlineConnIDForUser(uuid.MustParse(uid)); !online {
		t.Fatal("alice not online after SessionEnterEvent")
	}
	service.Publish(bus, service.SessionLeaveEvent{
		ConnID: 42, GatewayID: "gw-1",
	})
	if _, online := svc.OnlineConnIDForUser(uuid.MustParse(uid)); online {
		t.Fatal("alice still online after SessionLeaveEvent")
	}
}
```

Then add a `newTestServiceWithBus(t, repo, bus)` helper to `pkg/services/chat/testing.go` modeled on the existing test-fixture function. The helper builds a `service.Context{Bus: bus, ...}` and calls `Init` on it. Reference: existing test fixtures in the same file (search for `newTestService` or whatever helper the existing tests use, e.g. `handlers_test.go:60`).

- [ ] **Step 4: Run the test**

Run: `go test ./pkg/services/chat/ -run TestService_BusSubscriptionDrivesPresence -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/services/chat/service.go pkg/services/chat/service_test.go pkg/services/chat/testing.go
git commit -m "$(cat <<'EOF'
feat(chat): subscribe to service.Session{Enter,Leave}Event via Bus

Chat now consumes presence transitions from the typed pub/sub bus.
Old Process.chatHook path is still wired in this commit; the gateway
switch + scaffolding deletion follow in subsequent commits.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 7: Gateway publishes `SessionEnterEvent` / `SessionLeaveEvent`

**Files:**
- Modify: `pkg/universe/gateway.go:374-381` (`onAuthSuccess` chatHook call)
- Modify: `pkg/universe/gateway.go:441-447` (`handleDisconnect` chatHook call)

**Why:** Replaces the chatHook publish call with a typed-bus publish. Chat subscribes (Task 6) so behavior is preserved. This change must compile + test green BEFORE the next task deletes the chat-hook scaffolding.

- [ ] **Step 1: Replace the `OnSessionEnter` call**

In `pkg/universe/gateway.go`, locate the block at line 374-381:

```go
	// Drive chat presence/subscription bookkeeping if the chat service
	// is registered. Use g.process (set in both embedded and standalone
	// build paths) rather than g.coord (nil for standalone gateways).
	// On a standalone gateway,service process the chat service is local;
	// on a coord+gateway colocated process the same path applies.
	if g.process != nil && g.process.chatHook != nil {
		g.process.chatHook.OnSessionEnter(connID, userID.String(), username, g.id)
	}
```

Replace with:

```go
	// Publish the session-enter event on the per-process bus. Any service
	// (chat presence, future presence service, achievements) subscribed
	// at Init time receives the event synchronously. No-op if Bus is nil
	// (defensive — Bus is always constructed in Process.New, but standalone
	// fixture tests may bypass that path).
	if g.process != nil && g.process.bus != nil {
		service.Publish(g.process.bus, service.SessionEnterEvent{
			ConnID:    connID,
			UserID:    userID.String(),
			Username:  username,
			GatewayID: g.id,
		})
	}
```

Add the import to the file's import block (gateway.go has many imports — add `"github.com/mmokit/mmokit/pkg/service"` alphabetically).

- [ ] **Step 2: Replace the `OnSessionLeave` call**

In `pkg/universe/gateway.go`, locate the block at line 441-447:

```go
	// Drop chat presence/subscription state if the chat service is
	// registered. Idempotent — chat tolerates unknown connIDs. Use
	// g.process (set in both embedded and standalone paths) so this
	// fires on standalone gateway,service processes too.
	if g.process != nil && g.process.chatHook != nil {
		g.process.chatHook.OnSessionLeave(connID, g.id)
	}
```

Replace with:

```go
	// Publish the session-leave event on the per-process bus. Subscribers
	// (chat presence, future presence service, etc) drop per-conn state.
	// Idempotent on the chat-side handler.
	if g.process != nil && g.process.bus != nil {
		userIDStr := ""
		if sess.userID != uuid.Nil {
			userIDStr = sess.userID.String()
		}
		service.Publish(g.process.bus, service.SessionLeaveEvent{
			ConnID:    connID,
			UserID:    userIDStr,
			GatewayID: g.id,
		})
	}
```

(Verify `sess.userID` exists by `grep -n "userID" pkg/universe/gateway.go` — the gateway session struct does carry a UUID field. If the field name differs, use the actual one.)

- [ ] **Step 3: Verify the build**

Run: `just build`
Expected: build succeeds. The chat-hook plumbing is still present (`Process.chatHook` still set by `RegisterChatService`) but the publish path is now bus-driven. Both paths fire — chat-side handler is idempotent so this is safe.

- [ ] **Step 4: Run the chat e2e test as a regression check**

Run: `go test ./pkg/universe/ -run TestChat -v`
Expected: existing tests PASS. (`TestChat_InstallChatHook_Storage` still passes because we haven't deleted `InstallChatHook` yet — Task 9 does that. `TestChat_PresencePopulatedAfterLogin` passes via either the old hook OR the new bus path; both fire.)

- [ ] **Step 5: Commit**

```bash
git add pkg/universe/gateway.go
git commit -m "$(cat <<'EOF'
feat(universe): gateway publishes Session{Enter,Leave}Event on Bus

Replaces the direct g.process.chatHook.OnSession* calls. Chat-hook
plumbing remains in place this commit (deleted in the next one) so the
test suite stays green during the transition.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 8: Update `chat_e2e_test.go` to assert via the Bus

**Files:**
- Modify: `pkg/universe/chat_e2e_test.go`

**Why:** The existing e2e test asserts presence via `Process.ChatHook()` / `InstallChatHook` — both go away in Task 9. Migrate the test now so Task 9's deletion compiles cleanly.

- [ ] **Step 1: Read the existing test**

Run: `cat pkg/universe/chat_e2e_test.go` to see the full file shape. Three test functions exercise the hook-install storage + the e2e gateway→chat presence path. The presence-via-bus version replaces the install-hook calls with bus-publish calls.

- [ ] **Step 2: Rewrite `TestChat_InstallChatHook_Storage` as `TestChat_BusInjectedIntoContext`**

Replace the test function body. The new test asserts (a) `p.bus` is non-nil after `New`, (b) a service registered on the process sees `ctx.Bus == p.bus`. Sample skeleton:

```go
func TestChat_BusInjectedIntoContext(t *testing.T) {
	p := New(Config{Headless: true, Mode: "all", CellsX: 1, CellsY: 1})
	if p.bus == nil {
		t.Fatal("Process.bus is nil after New")
	}
	// (full e2e with the chat service is exercised by the next test;
	//  this one just locks down the bus-injection contract)
}
```

- [ ] **Step 3: Rewrite the presence-driving test to use `service.Publish`**

The existing test (around line 165) calls `p.ChatHook().OnSessionEnter(42, uid, "alice", "gw-1")`. Replace with:

```go
	service.Publish(p.bus, service.SessionEnterEvent{
		ConnID: 42, UserID: uid, Username: "alice", GatewayID: "gw-1",
	})
```

And the matching `OnSessionLeave` call at line ~180 with:

```go
	service.Publish(p.bus, service.SessionLeaveEvent{
		ConnID: 42, GatewayID: "gw-1",
	})
```

Drop the `fakeChatHook` struct (lines 21-46) and the `chatHookAdapter` struct (line 187+) — both become dead code with no callers.

Add the `service` import:

```go
import "github.com/mmokit/mmokit/pkg/service"
```

- [ ] **Step 4: Run the rewritten tests**

Run: `go test ./pkg/universe/ -run TestChat_ -v`
Expected: PASS. (Bus is wired via Task 5; chat subscribes via Task 6; gateway publishes via Task 7; the test exercises the chat-side subscriber by publishing to the bus directly.)

- [ ] **Step 5: Commit**

```bash
git add pkg/universe/chat_e2e_test.go
git commit -m "$(cat <<'EOF'
test(universe): chat presence asserted via service.Bus, not Process.ChatHook

Migrates chat_e2e_test.go off the soon-to-be-deleted InstallChatHook /
ChatHook accessors. Presence is exercised by publishing
service.SessionEnterEvent on Process.bus.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 9: Delete the chat-hook scaffolding

**Files:**
- Delete: `pkg/services/chat/session_hook.go`
- Modify: `pkg/universe/coordinator.go` (delete `chatHook` field at ~line 629; delete `ChatSessionHook` interface at lines 632-645; delete `InstallChatHook` at lines 1207-1218; delete `ChatHook` accessor at line 1220-1224)
- Modify: `pkg/mmokit/chat.go` (delete `chatSessionHookImpl` struct at lines 206-236; delete `p.InstallChatHook(...)` call at line 150)

**Why:** Greenfield refactor per `feedback_no_backward_compat`. Chat is now a normal service — every special engine hook is gone.

- [ ] **Step 1: Delete `pkg/services/chat/session_hook.go`**

```bash
git rm pkg/services/chat/session_hook.go
```

- [ ] **Step 2: Delete `chatSessionHookImpl` and `InstallChatHook` call in `pkg/mmokit/chat.go`**

Edit `pkg/mmokit/chat.go`. Delete lines 147-151 (the `// Install the gateway session hook ...` comment block AND the `p.InstallChatHook(&chatSessionHookImpl{getSvc: getSvc})` call). Then delete the `chatSessionHookImpl` struct + its two methods at lines 206-236 (entire `// chatSessionHookImpl satisfies pkguniverse.ChatSessionHook ...` block through the closing `}` of `OnSessionLeave`).

Verify nothing else in `pkg/mmokit/` references `chatSessionHookImpl` or `InstallChatHook`:

Run: `grep -rn "chatSessionHookImpl\|InstallChatHook\|ChatSessionHook\|ChatHook\b" pkg/mmokit/ pkg/services/chat/`
Expected output: empty.

- [ ] **Step 3: Delete `chatHook` field, `ChatSessionHook` interface, `InstallChatHook` and `ChatHook` accessors in `pkg/universe/coordinator.go`**

Edit `pkg/universe/coordinator.go`:

1. Delete the `chatHook ChatSessionHook` field (around line 629) AND its preceding doc comment (lines 621-628).
2. Delete the entire `ChatSessionHook` interface declaration (lines 632-645).
3. Delete `InstallChatHook` (lines 1207-1218) and `ChatHook` accessor (lines 1220-1224).

Verify no remaining references:

Run: `grep -rn "chatHook\|ChatSessionHook\|InstallChatHook\|ChatHook\b" pkg/universe/`
Expected output: empty.

- [ ] **Step 4: Verify the full project compiles**

Run: `go vet ./...`
Expected: no errors.

Run: `just build`
Expected: build succeeds.

- [ ] **Step 5: Run the full test suite for the affected packages**

Run: `go test ./pkg/service/... ./pkg/services/chat/... ./pkg/universe/...`
Expected: all PASS.

- [ ] **Step 6: Commit**

```bash
git add -A
git commit -m "$(cat <<'EOF'
refactor(universe,chat,mmokit): delete Process.chatHook scaffolding

Chat is now a normal service consuming Session{Enter,Leave}Event from
the typed service.Bus. No engine-special-cased hooks remain. Deleted:
- pkg/services/chat/session_hook.go
- pkg/universe/coordinator.go: ChatSessionHook iface, chatHook field,
  InstallChatHook + ChatHook accessors
- pkg/mmokit/chat.go: chatSessionHookImpl + RegisterChatService's
  InstallChatHook call

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 10: Logger category + structured publish logging

**Files:**
- Modify: `pkg/universe/coordinator.go` (where `c.bus` is constructed in `New`)

**Why:** `feedback_logging` requires every new server-side path to log under a category. Phase 1's only meaningful event source is the gateway publish; we register the category centrally so Phase 3 can extend without re-registering.

- [ ] **Step 1: Register the category at Process construction**

In `pkg/universe/coordinator.go::New`, after `c.bus = service.NewBus(...)` (or wherever the Process literal is finalized — find it via the `bus:` initializer added in Task 5), add:

```go
	c.Log.RegisterCategories("services:bus")
```

- [ ] **Step 2: Add a small helper that logs publishes (optional but recommended)**

In `pkg/universe/gateway.go`, after each `service.Publish(...)` call added in Task 7, add a one-liner under category `services:bus`:

```go
		g.process.Log.Log("services:bus", "publish SessionEnterEvent conn=%d user=%s gw=%s",
			connID, username, g.id)
```

(Mirror for SessionLeaveEvent.)

- [ ] **Step 3: Verify the build + run tests**

Run: `go vet ./... && go test ./pkg/service/... ./pkg/services/chat/... ./pkg/universe/...`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add pkg/universe/coordinator.go pkg/universe/gateway.go
git commit -m "$(cat <<'EOF'
chore(universe): register services:bus log category + log publishes

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 11: End-to-end smoke test (4node-basic + chat)

**Files:**
- No code changes. Manual verification.

**Why:** The chat e2e unit test in Task 8 covers the hot path, but the live process — `examples/4node-basic` with chat enabled, if it's wired — is the canonical smoke. Confirms no runtime regression.

- [ ] **Step 1: Check whether `examples/4node-basic` registers chat**

Run: `grep -n "RegisterChat" examples/4node-basic/main.go`

If the example doesn't register chat: skip this task — the regression test in Task 6 + chat_e2e_test.go in Task 8 are sufficient. Note that and move to Task 12.

If it does: continue.

- [ ] **Step 2: Run `just dev` and exercise the demo**

```bash
just dev
```

Open `http://localhost:8080`, log in as two clients, watch one client's chat panel hydrate; send a message between them.

- [ ] **Step 3: Verify the publish log fires**

In the server console, look for `services:bus` entries:

```
[services:bus] publish SessionEnterEvent conn=1 user=... gw=...
```

Two entries (one per client login).

- [ ] **Step 4: Disconnect a client; verify SessionLeaveEvent log fires**

```
[services:bus] publish SessionLeaveEvent conn=1 ...
```

If observed: chat presence path is fully bus-driven end to end. Stop the dev server.

- [ ] **Step 5: No commit (verification-only task)**

---

## Task 12: Plan complete — verify spec coverage

**Files:**
- No code changes.

- [ ] **Step 1: Re-read spec §12 Phase 1**

Confirm every bullet maps to a completed task:

| Spec bullet | Task |
|---|---|
| New `pkg/service/bus.go` | Tasks 1+2 |
| New `pkg/service/events.go` | Task 4 |
| New `pkg/service/event_codec.go` | Task 3 |
| Modified `pkg/service/service.go` (Bus field on Context) | Task 5 (placed in `context.go` — same package) |
| Modified `pkg/universe/coordinator.go` — own bus, thread into serviceContext | Task 5 |
| Modified `pkg/universe/gateway.go` — Publish replaces chatHook calls | Task 7 |
| Modified `pkg/services/chat/service.go::Init` — Subscribe replaces install-from-facade | Task 6 |
| Modified `pkg/mmokit/chat.go` — drop chatSessionHookImpl + InstallChatHook call | Task 9 |
| Deleted: `Process.ChatSessionHook`, `chatHook`, `InstallChatHook`, `ChatHook`, `chat/session_hook.go`, `chatSessionHookImpl` | Task 9 |

- [ ] **Step 2: Confirm all tests pass**

Run: `go test ./...`
Expected: all PASS.

- [ ] **Step 3: Final lint**

Run: `go vet ./... && just build`
Expected: clean.

- [ ] **Step 4: Update memory with what landed**

Save a `project_services_event_bus_phase1.md` memory describing what shipped and pointing at Phase 3 as the architectural follow-up.

- [ ] **Step 5: Final commit (if any drift) and merge**

If `git status` is clean, Phase 1 is done. Per `user_solo_developer`, merge directly to `main` (no PR).

---

## Out of scope for this plan

- **Phase 2 (auth event extraction)** — separate plan or land as a follow-up after Phase 1 is on `main`. Auth's response-interception is request/response-shaped (§14.1) and may need a sibling primitive.
- **Phase 3 (cluster-wide peer-mesh delivery)** — separate plan ([2026-05-08-services-event-bus-phase3.md](2026-05-08-services-event-bus-phase3.md)). Requires proto changes + MeshData participation by service-host processes.
- **Phase 4 (docs)** — minor; trail Phases 1+2+3.
