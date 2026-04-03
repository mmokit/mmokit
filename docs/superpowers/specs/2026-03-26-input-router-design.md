# Input Router Design Spec

## Problem

Both mmokit consumers (main space MMO and slither example) independently implement the same input processing pattern: iterate players by state, drain WebSocket messages, unmarshal envelope, switch on message code, unmarshal inner payload, dispatch to handler or enqueue to TickQueue. The main game's `InputSystem` is 337 lines with four duplicated drain-switch blocks (one per player state). Slither's is 91 lines with two blocks. Adding a new message type requires editing a switch statement and manually placing it in the correct state block(s).

## Design Decisions

1. **Standardize on protobuf envelopes.** All mmokit games use `enginepb.ClientEvent` (code + data) as the wire format. Slither migrates from raw binary to protobuf. This eliminates the need for two code paths in the router.

2. **Typed generic handlers (gRPC-inspired).** The router unmarshals the inner protobuf message for you. Handlers receive a typed struct, never raw bytes. Two registration functions: `Handle` (no payload) and `HandleProto[T]` (auto-unmarshal).

3. **State mask per handler.** Each handler declares which player states it's valid for via a bitmask. The router iterates all connected players once (not once per state), checks the bitmask, and dispatches. Invalid messages are silently dropped.

4. **Two-layer filtering.** State-group filters (shared preconditions like "alive and not ghost") run once per player per tick. Per-handler guards (optional) run per-message for handler-specific validation. AND semantics, not middleware chains.

5. **Envelope parsing is injected, not hardcoded.** The router accepts an `EnvelopeParser` function rather than importing `enginepb` directly. This keeps `pkg/engine/` protobuf-free and open-sourceable. The game layer passes a one-liner parser.

6. **Duplicate code registration panics.** Registering two handlers for the same message code panics at startup (fail-fast). This prevents silent overwrites that are hard to debug.

7. **Slither migration is a separate step.** The InputRouter is built and the main game migrated first. Slither's protobuf migration (new proto file, web client rewrite) happens as a follow-up.

## Architecture

### Core Types

```go
// StateMask is a bitmask of PlayerState values.
type StateMask uint32

// States builds a StateMask from one or more PlayerState values.
func States(states ...PlayerState) StateMask

// InputContext is passed to every handler.
// Note: Entity may be zero-value for players without an ECS entity (e.g. StateDead).
// Handlers for such states must not use ctx.Entity.
type InputContext struct {
    Session *PlayerSession // full session (state, entity, username, connID, custom data)
    ConnID  uint32         // shortcut for Session.ConnID
    Entity  ecs.Entity     // shortcut for Session.Entity
}

// EnvelopeParser extracts a message code and inner payload from raw wire bytes.
// The default implementation unmarshals enginepb.ClientEvent.
type EnvelopeParser func(raw []byte) (code uint32, data []byte, err error)

// InputFilter is a predicate used for state-group filters and per-handler guards.
type InputFilter func(ctx *InputContext) bool
```

### InputRouter

```go
type InputRouter struct {
    eng      *Engine
    parse    EnvelopeParser
    handlers map[uint32]*handlerEntry
    filters  map[PlayerState]InputFilter // state-group filters
}

type handlerEntry struct {
    states StateMask
    guard  InputFilter                    // optional per-handler guard
    fn     func(ctx *InputContext, data []byte) // unified internal signature
}
```

### Registration API

```go
// NewInputRouter creates a router wired to the given Engine.
// The EnvelopeParser extracts (code, data) from raw wire bytes.
// For protobuf games, pass engine.ProtoEnvelopeParser (provided as a convenience
// in pkg/mmokit/ which can import enginepb).
func NewInputRouter(eng *Engine, parse EnvelopeParser) *InputRouter

// StateFilter sets a shared precondition for all handlers matching a player state.
// Runs once per player per tick (cached). If it returns false, all messages from
// that player are drained and discarded.
func (r *InputRouter) StateFilter(state PlayerState, fn InputFilter)

// Handle registers a handler for a message code with no auto-unmarshal.
// The data parameter is evt.Data (the inner payload bytes, may be nil).
func (r *InputRouter) Handle(code uint32, states StateMask, fn func(ctx *InputContext, data []byte), opts ...HandlerOption)

// HandleProto registers a typed handler. The router unmarshals T from evt.Data
// before calling fn. If unmarshal fails, the message is silently skipped.
// Package-level function because Go generics cannot be methods.
func HandleProto[T any, P interface{ *T; proto.Message }](
    r *InputRouter, code uint32, states StateMask,
    fn func(ctx *InputContext, msg P), opts ...HandlerOption)

// HandlerOption configures optional per-handler behavior.
type HandlerOption func(*handlerEntry)

// WithGuard sets a per-handler guard predicate. If it returns false, the
// message is skipped but other messages for this player continue processing.
func WithGuard(fn InputFilter) HandlerOption
```

### System Interface

```go
func (r *InputRouter) Name() string      { return "InputRouter" }
func (r *InputRouter) Update(dt float32) { r.ProcessInput() }
```

Games register it as a system: `eng.AddSystem(router)`. It replaces the hand-written InputSystem entirely.

### Dispatch Loop

`ProcessInput()` runs the following logic:

```text
for each connected, non-pending player (via pm.ForEachConnected):
    build InputContext from session
    compute stateBit = 1 << session.State

    // State-group filter (runs once, cached for this player)
    if filter exists for session.State and filter returns false:
        drain and discard all messages (prevent buffer buildup)
        continue to next player

    drain messages from eng.ConnMgr.DrainInput(connID)
    for each raw message:
        parse envelope via r.parse(raw) -> (code, data, err)
        if err: log debug, skip
        look up handler by code
        if no handler or handler.states & stateBit == 0:
            skip (silent drop)
        if handler has guard and guard returns false:
            skip
        call handler.fn(ctx, data)
```

**Important:** `StatePending` sessions are excluded from the iteration. Their messages are consumed by `PlayerManager.processLogins()` which runs earlier in the tick. If the router drained pending sessions, login messages would be lost.

Unmarshal failures (both envelope and inner proto) are logged at debug level via `eng.Log` and the message is skipped. This aids development without impacting production.

### PlayerManager Addition

One new method required on `PlayerManager`:

```go
// ForEachConnected iterates all sessions that have an active connection
// (connID != 0) and are not in StatePending. Pending sessions are excluded
// because their input is consumed by processLogins().
func (pm *PlayerManager) ForEachConnected(fn func(s *PlayerSession))
```

This replaces the current pattern of calling `ForEach` once per state.

## Package Layout

| Type | Package |
| --- | --- |
| `InputRouter`, `InputContext`, `StateMask`, `InputFilter`, `HandlerOption` | `pkg/engine/` |
| `HandleProto[T]`, `WithGuard()`, `States()` | `pkg/engine/` (package-level functions) |
| Re-exports via facade | `pkg/mmokit/` |
| Handler functions | Game code (`internal/system/` or `examples/slither/`) |

The router depends only on `pkg/engine` types and `pkg/net.ConnManager` (via `*Engine`). It has zero protobuf imports — envelope parsing is injected. It has zero knowledge of game-specific protobuf schemas, TickQueue, ECS components, or game entities.

A convenience `ProtoEnvelopeParser` function is provided in `pkg/mmokit/` (which already imports `enginepb`) so games don't have to write their own.

## Migration: Main Game

**Before** (337 lines in `internal/system/input.go`):
- Four `ForEach` blocks (Active, Docking, Dead, Docked)
- Duplicated envelope unmarshal in each block
- Giant switch statements with duplicated handler code across states (e.g. chat, inventory transfer, bank request appear in multiple blocks)
- Bug: `isDocking` check on line 43 is dead code — `ForEach(StateActive)` never yields docking players. The migration naturally fixes this by registering `CE_PLAYER_INPUT` for both `StateActive` and `StateDocking` with a per-handler guard.

**After** (~40 lines of handler registrations + ~15 handler functions):

```go
// In factory.go or a dedicated input_handlers.go
router := mmokit.NewInputRouter(eng, mmokit.ProtoEnvelopeParser)

// State-group filters
router.StateFilter(mmokit.StateActive, func(ctx *mmokit.InputContext) bool {
    return gw.ECS.Alive(ctx.Entity) && !gw.C.Ghost.HasAll(ctx.Entity)
})
router.StateFilter(game.StateDocking, func(ctx *mmokit.InputContext) bool {
    return gw.ECS.Alive(ctx.Entity)
})

// Handlers
allActive := mmokit.States(mmokit.StateActive, game.StateDocking, game.StateDocked)

mmokit.HandleProto[gamepb.PlayerInputMsg](router, CE_PLAYER_INPUT,
    mmokit.States(mmokit.StateActive, game.StateDocking), handlePlayerInput)

router.Handle(GCE_DOCK, mmokit.States(mmokit.StateActive), handleDock)
router.Handle(CE_RESPAWN, mmokit.States(mmokit.StateDead), handleRespawn)
router.Handle(GCE_UNDOCK, mmokit.States(game.StateDocked), handleUndock)

mmokit.HandleProto[enginepb.ChatMsg](router, CE_CHAT, allActive, handleChat)
mmokit.HandleProto[gamepb.InventoryTransferMsg](router, GCE_INVENTORY_TRANSFER, ..., handleTransfer)
// ... etc, one line per message type

eng.AddSystem(router)
```

Each handler function is a small focused function (~5-15 lines) that does one thing.

## Migration: Slither

1. **New proto file:** `proto/slitherpb/slither.proto` with `SlitherInputMsg` (targetAngle, boost, sequence) and `SkinSelectMsg` (skinID, name). Event codes start at 100.
2. **Web client:** Replace `DataView` binary serialization with `@bufbuild/protobuf` using the new slither proto. Wrap messages in `enginepb.ClientEvent` envelope.
3. **Server:** Replace 91-line `system_input.go` with ~10 lines of router registration.

```go
router := mmokit.NewInputRouter(eng, mmokit.ProtoEnvelopeParser)
router.StateFilter(mmokit.StateActive, func(ctx *mmokit.InputContext) bool {
    return w.Alive(ctx.Entity)
})

mmokit.HandleProto[slitherpb.SlitherInputMsg](router, SCE_PLAYER_INPUT,
    mmokit.States(mmokit.StateActive), handleSnakeInput)
router.Handle(SCE_RESPAWN, mmokit.States(mmokit.StateDead), handleRespawn)

eng.AddSystem(router)
```

## Event Code Allocation

- Engine codes (`CE_*`): 0-99, defined in `enginepb`
- Game codes per consumer: 100+, defined in each game's own proto enum
- The router treats codes as `uint32` — no collision because each game binary only registers its own codes

## What the Router Does NOT Own

- Login processing (stays in `PlayerManager.processLogins`)
- Operation/RPC routing (stays in `pkg/ops/Router` on channel 0x01)
- Chat relay to other nodes (game handler calls `gw.Bridge.RelayChatToOtherNodes`)
- ECS component access (handlers access via captured `*GameWorld`)
- TickQueue enqueuing (handlers call `mmokit.Enqueue` directly)

## Testing Strategy

- **Unit tests for InputRouter** (`pkg/engine/input_router_test.go`):
  - Dispatch by code: registered handler fires, unregistered code is dropped
  - State mask filtering: handler only fires for matching player states
  - State-group filter: blocked player's messages are drained but not dispatched
  - Per-handler guard: guard returning false skips message, other messages continue
  - HandleProto unmarshal: valid proto dispatches typed message, invalid proto is skipped
  - Duplicate code registration panics
  - StatePending sessions are excluded from iteration
- **Integration validation**: after migration, the same client messages produce the same TickQueue events and component writes as before. Verify by running main game and slither, connecting via web client.

## Implementation Order

1. `InputRouter` core + `ForEachConnected` on PlayerManager + `States()` + `HandleProto[T]` + unit tests
2. `ProtoEnvelopeParser` in `pkg/mmokit/` + re-exports
3. Main game migration: handler functions + router registration in factory, delete `internal/system/input.go`
4. Slither proto migration (separate step): new proto file, web client protobuf, router registration, delete `system_input.go`

## Impact

- Eliminates ~300 lines from main game, ~80 lines from slither
- New message types become one-liner registrations
- State-based input filtering is declarative, not structural duplication
- Slither standardized on protobuf (enables future cross-language clients)
