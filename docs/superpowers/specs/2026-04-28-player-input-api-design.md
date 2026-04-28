# Player Input API Redesign — Design

**Status:** Draft for review
**Author:** Josh Stout (with Claude)
**Date:** 2026-04-28
**Related memories:** `feedback_no_backward_compat`, `feedback_refactor_over_stopgaps`, `feedback_mmokit_facade_only`, `feedback_logging`, `project_opensource_ready`

## 1. Summary

The current input API leaks ECS internals into game code. To register a single click-to-move handler, a developer in `examples/4node-basic/main.go` writes:

```go
mmo.AddSystem(mmokit.NewInputSystem(func(router *mmokit.InputRouter, gw *mmokit.Stage) {
    moveTargetMap := ecs.NewMap1[mmokit.MoveTarget](gw.ECSWorld())
    mmokit.Handle(router, basicpb.ClientEventCode_BCE_MOVE_TARGET,
        mmokit.States(mmokit.StateActive),
        func(ctx *mmokit.InputContext, msg *basicpb.MoveTargetMsg) {
            if !moveTargetMap.HasAll(ctx.Entity) {
                return
            }
            mmokit.SetMoveTarget(moveTargetMap.Get(ctx.Entity), msg.TargetX, msg.TargetY)
        })
}))
```

That ten-line snippet exposes raw `ecs.NewMap1[T]`, `ecs.Entity` (via `ctx.Entity`), `ECSWorld()`, and forces every handler to hand-roll a presence check. It also splits one logical fact ("`BCE_MOVE_TARGET` carries a `basicpb.MoveTargetMsg`") into two declarations: `RegisterClientEvent[T]` for schema export and `Handle(router, code, …, fn)` for runtime dispatch.

This spec redesigns the input API around a single fluent registration call. Game code declares wire → handler bindings without touching ECS. The framework owns dispatch, component caching, schema export, presence gating, and threading. The same example becomes:

```go
mmokit.OnInputWith[basicpb.MoveTargetMsg, MoveDeps](mmo, BCE_MOVE_TARGET).
    Active().
    Do(func(p *mmokit.Player, msg *basicpb.MoveTargetMsg, c *MoveDeps) {
        c.MT.SetTarget(msg.TargetX, msg.TargetY)
    })
```

Five lines, zero ECS exposure, zero parallel registrations.

The mental model follows the established Unity Input System philosophy: wire format and game logic are decoupled by an explicit binding layer, with strong typing in the registration. Go generics replace what Unity does with codegen.

## 2. Goals & non-goals

### Goals

- **Eliminate ECS leakage** from input registration sites. Game handlers never see `ecs.Entity`, `ecs.World`, or `ecs.Map*`.
- **One source of truth** per input. Wire code, proto type, state filter, and behavior live in one fluent call.
- **Two natural patterns inside handler bodies**: mutate a component (continuous state) or call a service (discrete actions). Framework picks neither — the handler is just a function.
- **Auto schema export** from generic type parameters; no parallel `RegisterClientEvent[T]` for routed events.
- **Dedicated input tick phase**, not a system the user adds. Hard ordering guarantee that all input for tick *N* is visible to all systems in tick *N*.
- **Unify `MoveTarget`** as a single component used by both wire-driven players and engine-driven bots.
- **No backward-compat layer.** Old API (`NewInputSystem`, `InputRouter`, `Handle`, `InputContext`, `States(...)`) is deleted in the same change. Per `feedback_no_backward_compat`.

### Non-goals

- **Client-side prediction / reconciliation.** Removed in `2026-04-25-remove-client-prediction-design.md`; outside this scope.
- **Input rate limiting / anti-cheat / replay protection.** Discussed under Open Questions but not implemented in v1.
- **Per-message authority validation** (e.g. "did this client really see that NPC before clicking on it?"). That is a game-layer concern, accessed via `.Guard` if shipped.
- **Generated client-side typed input methods.** The existing `cmd/sdkgen/` already auto-generates client SDKs; this spec keeps that pipeline working but does not change its output shape.
- **Migrating the space game's marketplace operations** (`pkg/ops/`). Operations are RPCs over channel `0x01`, not input events on channel `0x00`; out of scope.

## 3. Architecture

The redesign replaces the public `InputRouter` system with a private `inputDispatcher` owned by each `Stage` (cell), driven by a process-level binding registry.

```text
┌──────────────────────────┐
│  Process (mmo)           │
│                          │
│  inputBindings[]         │ ← OnInput / OnInputWith populate this at startup
│   { code, msgType,       │
│     stateMask, deps,     │
│     decode, handler }    │
└──────────────┬───────────┘
               │  (replayed per cell at Build time)
               ▼
┌──────────────────────────┐
│  Stage (cell)            │
│                          │
│  inputDispatcher         │
│   - per-component        │ ← lazy ecs.Map1[T] cache, one per deps field
│     map cache            │
│   - per-tick drain       │
└──────────────┬───────────┘
               │  (drained in tick phase 5; see §6)
               ▼
       handler(p *Player, msg, c *Deps)
```

**Five new pieces:**

1. **`mmokit.OnInput[Msg]` / `mmokit.OnInputWith[Msg, Deps]`** — top-level generic functions returning a fluent builder. Two functions because Go has no method-level generics.
2. **`mmokit.Player`** — opaque session wrapper. Replaces `*InputContext` everywhere it was surfaced to game code. Hides `ecs.Entity`.
3. **`inputBinding`** — internal record stored on the `Process`, replayed on every cell at `Build()` time.
4. **`inputDispatcher`** — per-cell engine-internal type that owns the per-component map cache and the per-tick drain. Replaces `engine.InputRouter` as a public type.
5. **`Tick.dispatchInput`** — new dedicated tick phase between `processPendingSessions` and `Run all systems`.

## 4. Package layout & file changes

```text
pkg/mmokit/
  input.go                  // NEW: OnInput, OnInputWith, builder, Player wrapper
  input_test.go             // NEW: registration, dispatch, deps resolution tests

pkg/engine/
  input_dispatcher.go       // NEW: replaces input_router.go
  input_router.go           // DELETED
  input_router_test.go      // DELETED (tests rewritten in input_dispatcher_test.go)
  loop.go                   // MODIFIED: add dispatchInput phase
  player_manager.go         // MODIFIED: Player session retains a *PlayerView

pkg/component/
  core.go                   // MODIFIED: MoveTarget gains Sequence field;
                            // helper SetTarget method moved off package func
  click_to_move.go          // MODIFIED: drop standalone SetMoveTarget func

examples/4node-basic/
  main.go                   // MODIFIED: 10-line input chunk → 5-line OnInputWith
  player.go                 // (no change required — bundle already lists MoveTarget)

internal/game/
  input_handlers.go         // MODIFIED: SetupInputHandlers → RegisterInputs;
                            // every Handle/router.Handle → OnInput/OnInputWith
  factory.go                // MODIFIED: drop AddSystem(NewInputSystem(...))

CLAUDE.md                   // MODIFIED: stale "tick phases" list updated to use
                            // engine-level hook names (no fabricated death/loot
                            // phases); add note about new dispatchInput phase
```

`pkg/mmokit/mmokit.go` loses `NewInputSystem`, `inputSystem[W]`, and `RegisterClientEvent` for routed events (the latter stays for bypass events like login/ping).

## 5. Public API

### 5.1 The two registration functions

```go
// OnInput registers a handler that does not need component injection.
func OnInput[Msg any, P interface { *Msg; proto.Message }, C engine.EventCode](
    mmo *Process, code C) *InputBuilder[Msg, struct{}]

// OnInputWith registers a handler with declarative component injection.
func OnInputWith[Msg any, P interface { *Msg; proto.Message }, Deps any, C engine.EventCode](
    mmo *Process, code C) *InputBuilder[Msg, Deps]
```

The `*Msg` + `proto.Message` constraint pattern matches existing `mmokit.RegisterClientEvent[T, P, C]` exactly — `P` is the implicit pointer-to-message type and is inferred from the call site. `C` accepts any proto enum int32 / uint32. Game code never types `P` or `C` explicitly; only `Msg` (and `Deps` for the With variant) are spelled at the call site.

### 5.2 Builder methods

`InputBuilder[Msg, Deps]` exposes a small fluent surface:

```go
.States(states ...PlayerState) *InputBuilder[Msg, Deps]    // restrict to listed states
.Active() *InputBuilder[Msg, Deps]                          // shorthand for States(StateActive)
.Guard(fn func(*Player) bool) *InputBuilder[Msg, Deps]      // optional per-input gate
.Do(handler func(...)) error                                // terminator; returns error on dup code
```

The handler signature is determined by the function used:

```go
// from OnInput:
.Do(func(p *Player, msg *Msg))

// from OnInputWith:
.Do(func(p *Player, msg *Msg, c *Deps))
```

The terminator returns an error (always nil today; reserved for richer validation). Game code typically discards the error since registration runs at startup; the engine logs duplicate-code panics at registration time, matching the old `InputRouter.Handle` panic-on-dup contract.

### 5.3 Open question on `.Guard`

Section 13 covers this. v1 ships with `.Guard` defined but unused; if no consumer materializes after one milestone we delete it.

### 5.4 The `*Player` type

`mmokit.Player` is the friendly facade game code talks to. It is an immutable view over a `*PlayerSession` plus the player's authoritative entity. Construction and lifetime are owned by the engine.

```go
// Identity
func (p *Player) Username() string
func (p *Player) ConnID()   uint32
func (p *Player) NetID()    uint32              // visible network identifier
func (p *Player) State()    PlayerState

// Communication
func (p *Player) Send(code uint32, msg proto.Message)
func (p *Player) Disconnect(reason string)
func (p *Player) TransitionState(to PlayerState) error

// Engine handles (rare, escape hatches)
func (p *Player) Stage()  *Stage
func (p *Player) Engine() *Engine

// Component access (package-level for Go's type inference)
func GetComponent[T any](p *Player) *T          // nil if absent
func HasComponent[T any](p *Player) bool
```

There is no `Entity()` accessor and no `*ecs.World` exposure. The `Stage` and `Engine` escape hatches funnel through the same surface systems already use — no second-class API.

`Player` instances are reused across handler invocations within a session's lifetime. They are pool-allocated at session activation and dropped at disconnect, matching the existing `PlayerSession` lifecycle.

### 5.5 Deps struct conventions

Deps structs follow the same shape and tag conventions as `mmokit.Query[T]` ([pkg/query/query.go](pkg/query/query.go)):

```go
type EquipDeps struct {
    Inv *Inventory                          // required: handler skipped if absent
    Eq  *Equipment                          // required
    Ab  *AbilitySet  `ecs:"optional"`       // optional: c.Ab is nil if absent, handler runs
}
```

**Rules enforced at registration** (mirror `pkg/query/buildFields[T]`):

- Every exported field must be a pointer to a registered ECS component struct type.
- Required fields (no tag) — handler is silently skipped if any required component is absent on the player's entity. A debug log line is emitted under category `input` so the absence is grep-able.
- Optional fields (`ecs:"optional"`) — pointer is nil if absent; handler runs.
- Non-pointer or non-component-typed fields cause registration to panic at startup, matching the `query.Query` panic.
- Reflection cost is paid once per registration; per-call dispatch uses cached field offsets and `ecs.UnsafeMap` accessors — no per-call reflection.

The same `ecs:"optional"` tag is reused intentionally so deps structs and query bundles are interchangeable mental models.

### 5.6 Schema export

The `[Msg]` generic parameter captures the proto type. At registration, the engine extracts `proto.MessageName(*new(Msg))` and registers the binding in the same `ClientEvents` registry that `RegisterClientEvent[T]` uses. Schema export is automatic — no parallel call.

The `Protocol.ClientEvents(...)` callback shrinks. Today every routed event must be declared there; after this change, only events that *bypass* the input dispatcher are declared:

```go
// Before:
Protocol: mmokit.NewProtocol("basic").
    ClientEvents(func(e *mmokit.ClientEvents) {
        mmokit.RegisterClientEvent[basicpb.LoginMsg](e, basicpb.ClientEventCode_BCE_LOGIN)
        mmokit.RegisterClientEvent[basicpb.MoveTargetMsg](e, basicpb.ClientEventCode_BCE_MOVE_TARGET)
        // ... one line per input ...
    }),

// After:
Protocol: mmokit.NewProtocol("basic").
    ClientEvents(func(e *mmokit.ClientEvents) {
        // Only LOGIN — bypasses dispatcher (handled by LoginHandler on the gateway).
        mmokit.RegisterClientEvent[basicpb.LoginMsg](e, basicpb.ClientEventCode_BCE_LOGIN)
    }),
```

`OnInput` registrations contribute to the same exported schema; the SDK generator sees both sources and emits a unified typed client.

## 6. Engine internals

### 6.1 `inputBinding` (process-level)

```go
type inputBinding struct {
    code      uint32
    msgType   reflect.Type           // *Msg
    protoName string                 // for schema export
    stateMask StateMask
    guard     func(*Player) bool     // nil if not set
    deps      *depsLayout            // nil for OnInput (no deps)
    handler   func(*Player, []byte)  // pre-bound thunk; decodes Msg + resolves deps + calls user fn
}
```

The `Process` stores `[]*inputBinding`. Bindings are append-only; no removal API. Duplicate codes panic at registration time.

### 6.2 `depsLayout`

Built once per registration via reflection on the `Deps` struct, paralleling `pkg/query/buildFields[T]`:

```go
type depsLayout struct {
    structType reflect.Type
    fields     []depsField
}

type depsField struct {
    name     string         // for diagnostics
    compID   ecs.ID         // Ark-registered component ID
    offset   uintptr        // field offset in the deps struct
    optional bool           // ecs:"optional"
}
```

The dispatcher uses `unsafe.Pointer` + `offset` math to populate fields without allocating a fresh deps struct per call. A pooled `Deps` value is reused per binding — safe because handlers run sequentially on the loop goroutine.

### 6.3 `inputDispatcher` (per-cell)

```go
type inputDispatcher struct {
    cell     *Cell
    bindings []*cellBinding         // mirror of process bindings, with cell-local accessors
}

type cellBinding struct {
    *inputBinding
    accessors  []ecs.UnsafeMap      // one per deps field; built once per cell at first dispatch.
                                    // nil for OnInput bindings (no deps).
    pooledDeps unsafe.Pointer       // pre-allocated deps struct (sizeof inputBinding.deps.structType),
                                    // reused across calls; nil for OnInput bindings.
}
```

`ecs.UnsafeMap` is Ark's reflection-friendly accessor (a `compID` plus a method to fetch a `*T` for an entity as `unsafe.Pointer`); `pkg/query/` already uses it via `buildFilter`. Accessors are built lazily on the first message that touches the binding because they are bound to a specific `*ecs.World` and the cell's world isn't constructed at registration time.

### 6.4 Per-tick drain (conceptual pseudocode)

```go
func (d *inputDispatcher) Tick() {
    d.cell.Engine.Players.ForEachConnected(func(sess *PlayerSession) {
        if sess.Entity == (ecs.Entity{}) || !d.cell.Engine.ECS.Alive(sess.Entity) {
            d.cell.Engine.ConnMgr.DrainInput(sess.ConnID)
            return
        }
        msgs := d.cell.Engine.ConnMgr.DrainInput(sess.ConnID)
        if len(msgs) == 0 {
            return
        }
        p := d.cell.playerView(sess)   // pooled *Player

        for _, raw := range msgs {
            code, data, err := parseEnvelope(raw)
            if err != nil {
                d.cell.Engine.Log.Log("input", "envelope parse error: conn=%d err=%v", sess.ConnID, err)
                continue
            }
            cb := d.lookup(code)
            if cb == nil {
                continue
            }
            stateBit := StateMask(1) << StateMask(sess.State)
            if cb.stateMask&stateBit == 0 {
                continue
            }
            if cb.guard != nil && !cb.guard(p) {
                continue
            }
            cb.invoke(p, data)
        }
    })
}
```

`cb.invoke` resolves deps fields against the player's entity, decodes the proto, and calls the user handler. Required-field absence is the only silent skip; everything else logs.

### 6.5 Tick order

Final tick phase order in `pkg/engine/loop.go`:

```text
1. ClearTickState hook
2. processEvents               (connect/disconnect)
3. processAdminCmds            (drain RunOnLoop queue)
4. processPendingSessions      (login state machine)
5. dispatchInput               ← NEW: drain wire input → run handlers
6. Run all systems
7. PreFlush hook
8. FlushRemovals
9. PostFlush hook
10. PostTick hook
```

The new `dispatchInput` phase is engine-internal — game code does not add it as a system. It runs after pending-session processing (so newly-active players can receive input) and before any game system (so `MoveTarget` writes are visible to `ClickToMoveSystem`).

`pkg/engine/loop.go` is the single edit site; no other file is aware of phase ordering.

### 6.6 Threading

All handlers run on the cell's loop goroutine. Same rules as systems — no spawning goroutines that mutate ECS, no blocking I/O. Off-loop callers needing ECS access continue to use `engine.RunOnLoop`.

### 6.7 Per-cell consistency on splits & merges

When `Stage.FromSplit()` or merge produces a new cell, the new cell's `Build()` calls `process.applyInputBindings(stage)`. The binding registry on `Process` is the source of truth; cells never own their own bindings. Splits and merges work without per-cell setup callbacks.

## 7. Migration

### 7.1 What gets deleted

| Symbol | Location | Replacement |
|---|---|---|
| `mmokit.NewInputSystem[W]` | `pkg/mmokit/mmokit.go` | gone — framework owns input phase |
| `mmokit.inputSystem[W]` | `pkg/mmokit/mmokit.go` | gone |
| `engine.InputRouter` (public type) | `pkg/engine/input_router.go` | replaced by engine-internal `inputDispatcher` |
| `engine.InputContext` | `pkg/engine/input_router.go` | replaced by `*mmokit.Player` |
| `engine.Handle[C, T]` (package fn) | `pkg/engine/input_router.go` | replaced by `OnInput` / `OnInputWith` |
| `engine.States(...)` (helper) | `pkg/engine/input_router.go` | becomes engine-internal; games use `.States(...)` builder |
| `engine.WithGuard`, `engine.WithProtoName` | `pkg/engine/input_router.go` | replaced by builder methods |
| `mmokit.SetMoveTarget` (package fn) | `pkg/system/click_to_move.go` | becomes `MoveTarget.SetTarget(x, y)` method |
| `mmokit.RegisterClientEvent[T]` for routed codes | `pkg/mmokit/client_events.go` | auto-derived from `OnInput[Msg]` (function stays for bypass events) |

### 7.2 4node-basic migration (mechanical)

In `examples/4node-basic/main.go`, the `mmo.AddSystem(mmokit.NewInputSystem(...))` block (lines 85–95) is replaced by:

```go
mmokit.OnInputWith[basicpb.MoveTargetMsg, MoveDeps](mmo, BCE_MOVE_TARGET).
    Active().
    Do(func(p *mmokit.Player, msg *basicpb.MoveTargetMsg, c *MoveDeps) {
        c.MT.SetTarget(msg.TargetX, msg.TargetY)
    })
```

A new file `examples/4node-basic/player.go` (or a section of `main.go`) declares:

```go
type MoveDeps struct {
    MT *mmokit.MoveTarget
}
```

The `Protocol.ClientEvents(...)` callback drops the `MoveTargetMsg` registration (it had to be present today even though `Handle()` registered it once already — schema export hack). Only `BCE_LOGIN` remains.

### 7.3 Space game migration (`internal/game/input_handlers.go`)

Today's file has 13 handlers. The migration is mechanical: each `mmokit.Handle(router, code, states, fn)` becomes one `OnInput[Msg]` or `OnInputWith[Msg, Deps]` call. Two handler bodies need closer attention:

**`handlePlayerInput`** — currently writes multiple fields on a `PlayerInput` component (sequence, jettison, ability cast, lock target, optional move). It becomes:

```go
type PlayerInputDeps struct {
    Input      *gamecomp.PlayerInput
    MoveTarget *mmokit.MoveTarget   `ecs:"optional"`
}

mmokit.OnInputWith[gamepb.PlayerInputMsg, PlayerInputDeps](mmo, CE_PLAYER_INPUT).
    States(StateActive, StateDocking).
    Do(func(p *mmokit.Player, msg *gamepb.PlayerInputMsg, c *PlayerInputDeps) {
        if p.State() == StateDocking {
            c.Input.Sequence = msg.Sequence
            return
        }
        prevAbility := c.Input.AbilityCast
        prevLock := c.Input.LockTargetNetID

        c.Input.Sequence = msg.Sequence
        c.Input.JettisonItemID = msg.Jettison
        c.Input.AbilityCast = msg.AbilityCast
        c.Input.LockTargetNetID = msg.LockTargetId

        if msg.MoveActive && c.MoveTarget != nil {
            c.MoveTarget.SetTarget(msg.MoveX, msg.MoveY)
        }
        if c.Input.AbilityCast != prevAbility || c.Input.LockTargetNetID != prevLock {
            p.Engine().Log.Log(CatPlayerInput, "...")
        }
    })
```

**`handleChat`** — currently calls `gw.Bridge().RelayChatToOtherCells(...)`. The `gw` reference comes from a closure captured by `SetupInputHandlers(router, gw)`. The migration replaces the file-level setup function with `RegisterInputs(mmo *mmokit.Process, gw *GameWorld)` that captures `gw` the same way:

```go
mmokit.OnInput[enginepb.ChatMsg](mmo, CE_CHAT).
    States(StateActive, StateDocking, StateDocked).
    Do(func(p *mmokit.Player, msg *enginepb.ChatMsg) {
        text := strings.TrimSpace(msg.Text)
        if len(text) == 0 || len(text) > 200 {
            return
        }
        gw.Chat.Broadcast(p.Username(), text)
        gw.Bridge().RelayChatToOtherCells(p.Username(), text)
    })
```

The `Pending*` enqueue handlers (dock, undock, respawn, transfer, bank, sell, equip, shop, loot) all collapse into the same shape — `OnInput[Msg]` (no deps) → call into the existing service queue:

```go
mmokit.OnInput[gamepb.DockMsg](mmo, GCE_DOCK).
    Active().
    Do(func(p *mmokit.Player, msg *gamepb.DockMsg) {
        gw.Queue.Enqueue(PendingDockRequest{ConnID: p.ConnID()})
    })
```

The existing `gw.Queue` + `Pending*` types are untouched — this redesign is about the input *registration* surface, not the game's internal task queue.

### 7.4 `MoveTarget` unification

Today there are two related concepts:
- `pkg/component.MoveTarget` — state component holding `(LocalX, LocalY, CellX, CellY, Active)`
- `pkg/system.SetMoveTarget(*MoveTarget, x, y)` — package function that converts world-absolute coordinates to cell-local and activates

Migration:
1. Add `Sequence uint32` field to `MoveTarget` (used by space game's `handlePlayerInput` for ack/reconciliation traffic; harmless for 4node-basic).
2. Move `SetMoveTarget` body into a `MoveTarget.SetTarget(worldX, worldY float32)` method.
3. `CancelMoveTarget` becomes `MoveTarget.Cancel()`.
4. The package function is deleted.

Bots that today call `mmokit.SetMoveTarget(mt, x, y)` change one character: `mt.SetTarget(x, y)`.

### 7.5 CLAUDE.md update

The current "Game Loop" section claims tick phases include "Send death notifications" and "Spawn loot crates from deaths." Those are not engine phases — they are game-side `PreFlush` / `PostFlush` / `PostTick` hook implementations. The update:

- Replace the bulleted phase list with the actual engine phases from `loop.go`, using hook names rather than space-game examples.
- Add the new `dispatchInput` phase between pending-session processing and `Run all systems`.
- Add a one-paragraph note that input dispatch is no longer a `System`; it is a framework-owned phase, registered via `OnInput` / `OnInputWith`.

## 8. Examples

### 8.1 4node-basic (full main.go input section, before / after)

**Before** (10 lines, 3 ECS leaks):

```go
mmo.AddSystem(mmokit.NewInputSystem(func(router *mmokit.InputRouter, gw *mmokit.Stage) {
    moveTargetMap := ecs.NewMap1[mmokit.MoveTarget](gw.ECSWorld())
    mmokit.Handle(router, basicpb.ClientEventCode_BCE_MOVE_TARGET,
        mmokit.States(mmokit.StateActive),
        func(ctx *mmokit.InputContext, msg *basicpb.MoveTargetMsg) {
            if !moveTargetMap.HasAll(ctx.Entity) {
                return
            }
            mmokit.SetMoveTarget(moveTargetMap.Get(ctx.Entity), msg.TargetX, msg.TargetY)
        })
}))
```

**After** (5 lines, 0 ECS leaks):

```go
mmokit.OnInputWith[basicpb.MoveTargetMsg, MoveDeps](mmo, BCE_MOVE_TARGET).
    Active().
    Do(func(p *mmokit.Player, msg *basicpb.MoveTargetMsg, c *MoveDeps) {
        c.MT.SetTarget(msg.TargetX, msg.TargetY)
    })
```

`type MoveDeps struct { MT *mmokit.MoveTarget }` lives next to the player kind declaration.

### 8.2 Space game `handleDock` (before / after)

**Before:**

```go
router.Handle(uint32(gamepb.GameClientEventCode_GCE_DOCK),
    mmokit.States(mmokit.StateActive),
    handleDock(gw))

func handleDock(gw *GameWorld) func(ctx *mmokit.InputContext, data []byte) {
    return func(ctx *mmokit.InputContext, data []byte) {
        mmokit.Enqueue(gw.Queue, PendingDockRequest{ConnID: ctx.ConnID})
    }
}
```

**After:**

```go
mmokit.OnInput[gamepb.DockMsg](mmo, GCE_DOCK).
    Active().
    Do(func(p *mmokit.Player, msg *gamepb.DockMsg) {
        gw.Queue.Enqueue(PendingDockRequest{ConnID: p.ConnID()})
    })
```

The proto-name capture (`GCE_DOCK` had to be registered separately via `RegisterClientEvent[gamepb.DockMsg]` for schema export) goes away — `[Msg]` carries the type.

### 8.3 Space game `handleEquip` (with deps)

```go
type EquipDeps struct {
    Inv *gamecomp.Inventory
    Eq  *gamecomp.Equipment
}

mmokit.OnInputWith[gamepb.EquipRequestMsg, EquipDeps](mmo, GCE_EQUIP).
    States(StateActive, StateDocked).
    Do(func(p *mmokit.Player, msg *gamepb.EquipRequestMsg, c *EquipDeps) {
        gw.Queue.Enqueue(PendingEquipRequest{
            ConnID: p.ConnID(),
            ItemID: msg.ItemId,
            Slot:   item.EquipSlot(msg.Slot),
        })
    })
```

The deps struct here is illustrative — this particular handler doesn't read the components. In practice, it would either drop `Deps` and use `OnInput[Msg]`, or it would inline the inventory check and skip the queue entirely. The migration starts with the smallest mechanical change (preserve `Pending*` enqueue), then a follow-up pass collapses where appropriate.

## 9. Testing

### 9.1 Unit tests (`pkg/mmokit/input_test.go`, `pkg/engine/input_dispatcher_test.go`)

- **Registration round-trip** — `OnInput[Msg]` and `OnInputWith[Msg, Deps]` populate the binding registry; schema export emits the right proto name.
- **State filter** — handler skipped when player is not in an allowed state; runs when in an allowed state; runs when in any of multiple allowed states.
- **Required deps absent** — handler is silently skipped, log line emitted, no panic.
- **Optional deps absent** — handler runs, optional pointer is nil.
- **Duplicate code** — panics at registration time with the binding code in the message.
- **Guard** — false return skips the handler; true allows it.
- **Per-cell map caching** — two cells share the same registration but maintain separate `ecs.Map1` instances; map is built lazily on first dispatch.
- **Split / merge propagation** — bindings on the parent cell appear on children after split; appear on survivor after merge.

### 9.2 Integration tests

- **`examples/4node-basic/mesh_e2e_test.go`** — existing end-to-end tests must pass unchanged. The bot wire path (`bot spawn`) does not use input handlers, so the test does not need the new API directly, but the player path does — any test that simulates a player click goes through `OnInputWith`.
- **`internal/game/`** — existing space-game tests that depend on `SetupInputHandlers` are updated to call `RegisterInputs` instead. No test logic changes.

### 9.3 Smoke

- `just dev` 4node-basic: connect a real client, click to move, verify the player moves.
- `just dev` space game: connect, dock at a station, verify dock succeeds; equip an item, verify equipment swap; chat, verify cross-cell relay.

### 9.4 Performance

The dispatcher's per-call path resolves deps through an offset table without per-call allocation. Benchmark added to `input_dispatcher_test.go`:

- `BenchmarkDispatch_NoDeps` — measure baseline OnInput dispatch
- `BenchmarkDispatch_OneDep` — single-component deps
- `BenchmarkDispatch_FourDeps` — four-component deps

Target: the new dispatcher is no slower than the old `InputRouter` on the equivalent path (single-component handlers). With the cached map lookups it should be slightly faster.

## 10. Schema export & client SDK

`cmd/sdkgen/` already harvests router/op/entity-kind metadata via `Protocol.AssembleFromProcess(*Process)`. The new `inputBinding` registry is appended to the same `ClientEvents` schema source. SDK output shape is unchanged: clients still see one entry per client-event code, with proto name and code. No client code regenerates differently.

The existing `--dump-schema` flag continues to work without code changes in the SDK generator.

## 11. Rollout

Per `feedback_no_backward_compat`: this lands as one PR with no compatibility shim. The single change-set:

1. Add `pkg/mmokit/input.go` and `pkg/engine/input_dispatcher.go`.
2. Wire `dispatchInput` into `pkg/engine/loop.go`.
3. Add `Process.applyInputBindings` and call it from cell `Build`.
4. Migrate `examples/4node-basic/main.go`.
5. Migrate `internal/game/input_handlers.go` and `internal/game/factory.go`.
6. Delete `pkg/engine/input_router.go` + tests.
7. Delete `mmokit.NewInputSystem`, `mmokit.inputSystem[W]`.
8. Move `MoveTarget` setter into a method; delete the package function.
9. Update CLAUDE.md (tick phases + input-system note).
10. Run `just build` + `just test` + `just test-pg` + 4node-basic + space-game smoke.

Solo dev, merge to main directly per `user_solo_developer`.

## 12. Risks

- **Reflection cost at registration.** Mitigated: paid once per binding at startup. Per-call dispatch is field-offset arithmetic, no reflection.
- **Pooled deps struct aliasing.** The dispatcher reuses one `Deps` value per binding. If a handler retains a pointer to a deps field across calls (e.g. by stashing `c.MT` in a closure), they get a stale view next call. **Mitigation:** documentation explicitly forbids retention; the deps fields are pointers into the *current* player's components, valid only for the duration of the handler call. (Same rule that already applies to ECS map-resolved component pointers.)
- **Handler panic kills the loop.** Same risk as today — the existing `InputRouter` does not recover from handler panics either. Add `defer recover()` around `cb.invoke`, log the panic, drop the message, continue. Matches `processAdminCmds` which already does this.
- **Generic type inference at call site.** `OnInputWith[Msg, Deps]` requires both type params to be explicit (Go can infer from arg position only for trailing parameters). Confirmed acceptable in practice — the existing `RegisterKind[Bundle]` and `RegisterClientEvent[T]` have the same shape and game devs have not complained.

## 13. Open questions

1. **Keep `.Guard()` in v1?** No current consumer in the existing codebase. Ship without it, add later if a use materializes. **Recommendation: ship without.**
2. **Default state filter?** Should an unspecified state filter mean "any state" or panic? Today the explicit pattern is everywhere. **Recommendation: panic at registration if `.States` / `.Active` not called, to force explicitness.**
3. **`HasComponent[T](p)` exposed or hidden?** It is one of two component-access escape hatches. If we want to push hard toward deps structs, hide it. **Recommendation: expose it. It costs nothing and removes the temptation to declare a deps struct just to do a presence check.**
4. **Naming: `OnInputWith` vs `OnInputDeps` vs `BindInput`?** `OnInputWith` reads naturally ("when this input arrives, call my handler with these deps"). `OnInputDeps` is terser. `BindInput` is the most accurate but loses the "subscribe-to-event" feel. **Recommendation: `OnInputWith`.**
5. **Should the engine continue to recover from handler panics?** Today's `InputRouter` does not — a panicking handler kills the loop. §12 proposes adding `defer recover()` around `cb.invoke`, matching `processAdminCmds`. This is a small behavior change worth flagging. **Recommendation: add the recover — a buggy handler should drop one message, not the whole cell.**

## 14. Out of scope / future work

- **Input replay / determinism layer** — recording inputs for deterministic playback (for tooling, debugging cross-host bugs). Sketchable on top of this design but not in v1.
- **Client-side input prediction** — explicitly removed in `2026-04-25-remove-client-prediction-design.md`. Not coming back here.
- **Per-input rate limiting** — would slot into `.Guard` if shipped, or as a `.RateLimit(perSec int)` builder method. v1 ships neither.
- **Auto-generated typed client input methods** — Unity-style `client.input.move({x, y})` rather than `client.send(BCE_MOVE_TARGET, {x, y})`. SDK generator change, not this spec.
- **Service-targeted inputs** — input that routes to a specific `pkg/service/` instance (e.g. chat service) rather than the player's own cell. Operations channel `0x01` already does this; no input-channel equivalent planned.

## 15. Acceptance criteria

This spec is complete when:

- A new game dev can implement a click-to-move handler in 5 lines, with no `ecs.*` import in the file.
- The `Protocol.ClientEvents(...)` callback on a typical game contains only login/ping declarations.
- 4node-basic and the space game both run on the new API with no behavior change visible to clients.
- `just test` and `just test-pg` pass.
- `pkg/engine/input_router.go`, `mmokit.NewInputSystem`, and `mmokit.SetMoveTarget` (package function) are deleted from the tree.
