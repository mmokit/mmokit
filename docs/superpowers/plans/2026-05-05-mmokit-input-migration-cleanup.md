# Input Migration + Full Cleanup Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Realize spec §6.6 — client input is a typed Send. Replace `OnInput[T]` / `OnInputWith[T, Deps]` with `mmokit.HandleClient[T]` (registration verb, not marker). Replace the `ServerOnly()` marker interface with `mmokit.HandleAllInternal[T]` paired registration verb. Split bundled `PlayerInputMsg` into four discrete typed messages. Sweep all Plan D-F loose ends.

**Architecture:** Three registration verbs determine routing + broadcast policy at the wiring site, not on the data type. `HandleAll` (entity→entity, broadcast). `HandleAllInternal` (entity→entity, no broadcast). `HandleClient` (client→player Entity, no broadcast). All three handlers share the same signature `(target Entity, msg *T)` — only the entry-point validation differs. Wire format reuses Plan F's reflection codec; client-input frames use a new channel byte (0x02). sdkgen extends to emit TS classes for client-input types with an `encode` instance method + a `client.send(msg)` method. `PlayerInputMsg` splits into `SetMoveTarget` / `SetLockTarget` / `CastAbility` / `JettisonItem`.

**Tech Stack:** Go 1.24, `pkg/mmokit` facade, reflection codec from Plan F, sdkgen TS class generator, web-pixi WebSocket transport.

**Spec:** `docs/superpowers/specs/2026-05-05-input-migration-design.md`

**Predecessor plans (all on `feat/mmokit-entity-message-api`):** A+B (foundation), C (Damage + Mining), D (StatusEffect + legacy surface), E (Death/Currency + ECS sweep), F (AoI auto-broadcast).

**Branch:** `feat/mmokit-entity-message-api` (continue on this branch — single ongoing dev branch per the solo-developer convention).

---

## Project memory to apply throughout

- `feedback_no_unnecessary_type_args` — drop generic params Go can infer.
- `feedback_no_backward_compat` — change consistently, no shims, no aliases.
- `feedback_mmokit_facade_only` — game code uses `mmokit.*`, never `pkg/` subpaths.
- `feedback_logging` — log significant state changes.
- `feedback_proto_field_cleanup` — never reserve old proto fields; renumber from 1 on changes.
- `feedback_wire_format_schema_runtime_match` — schema dump must match server bytes.
- `feedback_command_arg_style` — required args = positional; optional = --flags.
- IDE diagnostics may be stale — trust `go vet` + `go test`.

---

## File structure

**New files (`pkg/mmokit/`):**

- `handle_internal.go` — `HandleAllInternal[T](world, fn)`. Mirrors `HandleAll` but registers in the dispatcher only, not in the broadcast registry.
- `handle_client.go` — `HandleClient[T](world, fn)`. Registers T in a new client-input registry; framework gateway path dispatches incoming 0x02 frames to the registered handler.
- `player_state.go` — `PlayerStateOf(entity Entity) PlayerState` lookup helper.
- `client_input_test.go` — unit tests for HandleClient registration + PlayerStateOf.

**New files (`internal/game/`):**

- `input_messages.go` — typed Go structs replacing the deleted gamepb input protos: `SetMoveTarget`, `SetLockTarget`, `CastAbility`, `JettisonItem`, `Dock`, `Undock`, `Respawn`, `InventoryTransfer`, `BankRequest`, `Equip`, `LootItem`, `LootAll`, `Chat`.
- `verb_beam_toggle.go` — tiny typed message + handler for the mining-beam toggle press-pulse VFX (Plan F regression restore).
- `input_test.go` — equivalence test: replay (move, lock, ability, jettison) actions through the four split handlers, assert resulting component state matches the legacy bundled handler's behavior.

**Modified files (`pkg/mmokit/`):**

- `messaging.go` / `messaging_all.go` — `Handle` / `HandleAll` simplified: register in broadcast registry unconditionally (no more ServerOnly check).
- `broadcast.go` — `ServerOnly` interface + `IsServerOnly` deleted; `brIsRegistered` / `brSet` unchanged in shape but no longer filtered by marker.
- `broadcast_test.go` — `TestIsServerOnly` deleted; new test covers verb-based registration policy.
- `init.go` — wire `BroadcastHooks.Eligible` to the simplified registry check.
- `entity.go` — possibly add `Entity.PlayerSession()` accessor if helpful (TBD during impl).

**Modified files (`internal/game/`):**

- `verb_death.go` — `func (KillCredit) ServerOnly() {}` deleted. `RegisterDeathVerbs` calls `mmokit.HandleAllInternal` for KillCredit.
- `input_handlers.go` — REWRITTEN. Each `OnInput*` block becomes `mmokit.HandleClient[T]` with the typed Go struct. PlayerInput handler splits into 4 (SetMoveTarget, SetLockTarget, CastAbility, JettisonItem).
- `factory.go::RegisterInputs(coord)` body — rewritten; signature unchanged.
- `system_ability.go` — restore mining-beam press-pulse via `target.Send(&BeamToggle{...})`.
- `commands/kill.go` — drop unused `coord` parameter.
- `commands/registry.go` — call site update for kill.go's signature change.
- `README.md` — rewrite sections referencing `MarkPlayerDeath`, `processDeaths`, `PendingDeaths`, `DeadPlayers`. Document the post-Plan-E death observer + Killed handler architecture.

**Modified files (`pkg/universe/`):**

- `coordinator.go` — delete `inputBindings` field, `AddInputBinding`, `InputBindings`, `dispatchInput` phase wiring.
- `bridge.go` / `cell_bridge_impl.go` / `grpc_bridge.go` — delete `Bridge.SendActionResult` interface method + impls.
- `cell.go` — delete `MsgActionResult` arm.
- `message.go` — delete `MsgActionResult` constant.
- `action.go` — delete `ActionResult` Go type.
- `mesh_frame_codec.go` — delete `ActionResult` encode + decode arms.
- `mesh_frame_codec_test.go` — delete `ActionResult` roundtrip test case.
- `universe_test.go` — delete `TestBridge_SendActionResult`.

**Modified files (`pkg/engine/`):**

- DELETE: `input_dispatcher.go`, `input_dispatcher_test.go`. The dispatchInput phase merges into the typed-message dispatcher.

**Modified files (`proto/`):**

- `proto/gamepb/game.proto` — delete `PlayerInputMsg`, `DockRequestMsg`, `UndockRequestMsg`, `RespawnRequestMsg`, `InventoryTransferMsg`, `BankRequestMsg`, `EquipRequestMsg`, `LootItemMsg`, `LootAllMsg`. Delete `GameClientEventCode_*` enum values for the deleted protos.
- `proto/enginepb/engine.proto` — delete `ChatMsg` from input path (KEEP it for the WorldUpdateMsg.chat_messages broadcast field if not migrated, OR migrate the broadcast to the typed `Chat` Go struct too — see Phase 5 notes). Delete `ClientEventCode_CE_PLAYER_INPUT`, `CE_CHAT` (if migrated).
- `proto/meshpb/mesh.proto` — delete `message ActionResult` + `MeshFrame.action_result` field 11. Renumber subsequent oneof fields per `feedback_proto_field_cleanup`.

**Modified files (`cmd/sdkgen/`):**

- `schema.go` — add `ClientInputTypeSchema` (mirrors `BroadcastTypeSchema`).
- `protoes.go` / `generate.go` — emit per-input TS class with `encode` instance method + a generic `client.send(msg)` method on the SpaceClient.
- `main.go` — delete `snakeToTitle` (unused).

**Modified files (`web-pixi/sdk/` and `examples/4node-basic/web/sdk/`):**

- regenerated; new `inputs.ts` (or similar) appears with TS classes for each `HandleClient` registration. `client.ts` gains `client.send(msg)` method.
- `client.sendMoveTarget`-shaped methods deleted.

**Modified files (`web-pixi/src/` and `examples/4node-basic/web/src/`):**

- `network.ts` — migrate from `client.sendMoveTarget(...)` to `client.send(new SetMoveTarget(...))` etc.
- input-bundling logic in `state.ts` (or wherever) — refactored to dispatch separate messages on each input event.

---

## Phase 1: Registration verbs + ServerOnly removal

### Task 1.1: HandleAllInternal primitive

**Files:**

- Create: `pkg/mmokit/handle_internal.go`

- [ ] **Step 1: Implement**

```go
// pkg/mmokit/handle_internal.go
package mmokit

import (
    pkguniverse "github.com/zenion/mmoserver/pkg/universe"
)

// HandleAllInternal registers fn as the handler for messages of type M on
// every Stage owned by world, marking M as server-internal — the framework
// will NOT auto-broadcast post-handler.
//
// Use for messages that are pure server-side accounting (e.g. KillCredit:
// currency rewards routed to the killer's authoritative cell with no
// client visibility intended). Same Send semantics as HandleAll —
// target.Send(&msg) routes to the authoritative cell, handler runs there,
// post-handler the framework skips the broadcast queue push.
//
// Compare HandleAll (broadcasts to AoI viewers) and HandleClient
// (validates client-origin connection ownership).
func HandleAllInternal[M any](world *pkguniverse.Process, fn func(target Entity, msg *M)) {
    world.OnStageInit(func(stage *pkguniverse.Stage) {
        Handle(stage, fn)
    })
    // Note: Handle registers in the broadcast registry unconditionally
    // (post-Plan-G). HandleAllInternal explicitly excludes by NOT calling
    // RegisterBroadcastType. See pkg/mmokit/messaging.go.
}
```

NOTE: this requires Handle to NOT auto-register broadcast eligibility. Restructure (Step 2):

- [ ] **Step 2: Decouple Handle from broadcast registration**

In `pkg/mmokit/messaging.go::Handle`, remove the broadcast registration:

```go
// BEFORE:
func Handle[M any](stage *pkguniverse.Stage, fn func(target Entity, msg *M)) {
    d := stage.Dispatcher()
    d.SetEntityCtor(entityCtorAdapter)
    var zero M
    msgType := reflect.TypeOf(zero)
    d.Register(typeKeyOf(msgType), msgType, reflect.ValueOf(fn))
    if !IsServerOnly(msgType) {
        RegisterBroadcastType(msgType)
    }
}

// AFTER:
func Handle[M any](stage *pkguniverse.Stage, fn func(target Entity, msg *M)) {
    d := stage.Dispatcher()
    d.SetEntityCtor(entityCtorAdapter)
    var zero M
    msgType := reflect.TypeOf(zero)
    d.Register(typeKeyOf(msgType), msgType, reflect.ValueOf(fn))
}
```

In `pkg/mmokit/messaging_all.go::HandleAll`:

```go
func HandleAll[M any](world *pkguniverse.Process, fn func(target Entity, msg *M)) {
    var zero M
    RegisterBroadcastType(reflect.TypeOf(zero))  // NEW: register here, not in Handle
    world.OnStageInit(func(stage *pkguniverse.Stage) {
        Handle(stage, fn)
    })
}
```

This way: `HandleAll` adds to broadcast registry; `HandleAllInternal` doesn't.

- [ ] **Step 3: Verify build**

```bash
go vet ./...
go test ./pkg/mmokit/...
```

Existing tests should still pass — broadcast registration logic is the same, just lives in HandleAll instead of Handle.

- [ ] **Step 4: Commit**

```bash
git add pkg/mmokit/messaging.go pkg/mmokit/messaging_all.go pkg/mmokit/handle_internal.go
git commit -m "feat(mmokit): HandleAllInternal registration verb

Decouples broadcast registration from Handle. HandleAll registers in
the broadcast registry; HandleAllInternal does not. Sets up Plan G's
shift from ServerOnly() marker to verb-based policy."
```

### Task 1.2: Migrate KillCredit; delete ServerOnly marker

**Files:**

- Modify: `internal/game/verb_death.go` (delete `ServerOnly()` method; change KillCredit registration to `HandleAllInternal`)
- Modify: `pkg/mmokit/broadcast.go` (delete `ServerOnly` interface + `IsServerOnly` helper)
- Modify: `pkg/mmokit/integration_killcredit_test.go` (delete `killCreditMsg.ServerOnly()`; use `HandleAllInternal` to register the test handler)
- Modify: `pkg/mmokit/auto_broadcast_test.go` (delete `testServerOnlyMsg.ServerOnly()`; rewrite `TestAutoBroadcast_ServerOnly_DoesNotBroadcast` to register via `HandleAllInternal`)
- Modify: `pkg/mmokit/broadcast_test.go` (delete `TestIsServerOnly`; replace with verb-based test)

- [ ] **Step 1: Update KillCredit registration**

In `internal/game/verb_death.go`:

```go
// DELETE:
// func (KillCredit) ServerOnly() {}

// In RegisterDeathVerbs:
func RegisterDeathVerbs(p *mmokit.Process) {
    mmokit.HandleAll(p, killedHandler)
    mmokit.HandleAllInternal(p, killCreditHandler)  // CHANGED: was HandleAll
}
```

- [ ] **Step 2: Delete ServerOnly interface + IsServerOnly**

In `pkg/mmokit/broadcast.go`:

```go
// DELETE the ServerOnly interface declaration.
// DELETE the serverOnlyType var.
// DELETE the IsServerOnly function.
```

- [ ] **Step 3: Update test stand-ins**

In `pkg/mmokit/integration_killcredit_test.go`:

```go
// DELETE:
func (killCreditMsg) ServerOnly() {}

// Test setup CHANGE: use HandleAllInternal instead of HandleAll for the stand-in registration.
mmokit.HandleAllInternal(proc, func(target mmokit.Entity, msg *killCreditMsg) { ... })
```

In `pkg/mmokit/auto_broadcast_test.go`:

```go
// DELETE testServerOnlyMsg.ServerOnly() method.
// In TestAutoBroadcast_ServerOnly_DoesNotBroadcast:
//   Register via HandleAllInternal; Send still doesn't broadcast.
```

- [ ] **Step 4: Update broadcast_test.go**

Delete `TestIsServerOnly`. Add (or update) a test that verifies HandleAllInternal does NOT add to the broadcast registry:

```go
func TestHandleAllInternal_NoBroadcastRegistration(t *testing.T) {
    // ... setup ...
    type internalMsg struct{ X uint32 }
    mmokit.HandleAllInternal(proc, func(target mmokit.Entity, msg *internalMsg) {})

    types := mmokit.BroadcastTypes()
    for _, ty := range types {
        if ty == reflect.TypeFor[internalMsg]() {
            t.Fatal("HandleAllInternal incorrectly registered in broadcast registry")
        }
    }
}
```

- [ ] **Step 5: Verify**

```bash
go vet ./...
go test ./pkg/... ./internal/...
```

All green. The KillCredit cross-cell test should still pass (routing logic unchanged; only the registration verb changed).

- [ ] **Step 6: Commit**

```bash
git add internal/game/verb_death.go pkg/mmokit/broadcast.go pkg/mmokit/broadcast_test.go pkg/mmokit/integration_killcredit_test.go pkg/mmokit/auto_broadcast_test.go
git commit -m "refactor(mmokit,game): replace ServerOnly() marker with HandleAllInternal verb

Empty marker methods are syntactic ceremony with no behavior. Trust +
broadcast policy belongs at the wiring site (registration), not on the
data type. KillCredit + test stand-ins all migrate.

ServerOnly interface + IsServerOnly helper deleted. Plan G § 4.2."
```

---

## Phase 2: HandleClient primitive + dispatch path

### Task 2.1: Client-input registry + HandleClient

**Files:**

- Create: `pkg/mmokit/handle_client.go`
- Modify: `pkg/universe/stage.go` (add client-input dispatch helper)

- [ ] **Step 1: Implement HandleClient**

```go
// pkg/mmokit/handle_client.go
package mmokit

import (
    "reflect"
    "sync"

    pkguniverse "github.com/zenion/mmoserver/pkg/universe"
)

// HandleClient registers fn as the handler for client-originated typed
// messages of type M. The framework dispatches when a connection sends a
// message of type M on the client-input wire channel (0x02).
//
// Routing: framework looks up the player Entity owned by the sending
// connection, decodes the message body via the reflection codec, and
// invokes fn with (player Entity, *msg).
//
// Trust contract:
//   - Framework guarantees: connection owns the target Entity; Entity is
//     alive on this stage; message body decoded successfully.
//   - Framework does NOT validate field values, player state, or rate.
//     The handler validates as appropriate for the input.
//
// Compare HandleAll (entity → entity, broadcast) and HandleAllInternal
// (entity → entity, no broadcast).
func HandleClient[M any](world *pkguniverse.Process, fn func(player Entity, msg *M)) {
    var zero M
    msgType := reflect.TypeOf(zero)
    registerClientInputType(msgType)

    world.OnStageInit(func(stage *pkguniverse.Stage) {
        Handle(stage, fn)
    })
}

var (
    ciMu  sync.RWMutex
    ciSet = map[reflect.Type]struct{}{}
)

func registerClientInputType(t reflect.Type) {
    ciMu.Lock()
    ciSet[t] = struct{}{}
    ciMu.Unlock()
}

// ClientInputTypes returns the registered client-input types in
// deterministic order (sorted by reflect.Type.String()). Used by sdkgen.
func ClientInputTypes() []reflect.Type {
    ciMu.RLock()
    defer ciMu.RUnlock()
    out := make([]reflect.Type, 0, len(ciSet))
    for t := range ciSet {
        out = append(out, t)
    }
    sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
    return out
}

// IsClientInputType reports whether t is registered as a client-input type.
// Used by the gateway-side dispatch to validate incoming 0x02 frames.
func IsClientInputType(t reflect.Type) bool {
    ciMu.RLock()
    _, ok := ciSet[t]
    ciMu.RUnlock()
    return ok
}
```

(Add `"sort"` to imports.)

- [ ] **Step 2: Wire dispatch path**

The wire path needs:
1. Gateway reads incoming WebSocket frame.
2. If channel byte is 0x02 (new): parse client-input frame.
3. Look up typeID → Go reflect.Type via the client-input registry.
4. Resolve connection → player Entity (via existing session→connID→player mapping).
5. Decode body via `pkguniverse.ReflectUnmarshalOnStage(stage, body, ptr)`.
6. Dispatch via `stage.Dispatcher().Invoke(playerNetID, msgPtr)` — same primitive as Send.

This requires:
- A new helper somewhere in the gateway path (likely `pkg/universe/gateway.go` or wherever WebSocket frames are dispatched today) that routes 0x02 frames.
- Access to the typeID-→reflect.Type registry from the gateway (cross-package; use the indirection pattern via `BroadcastHooks` or a new `ClientInputHooks` struct).

Read `pkg/universe/coordinator.go` and any gateway-frame-dispatch file to find the existing 0x00/0x01 split. Add the 0x02 case.

- [ ] **Step 3: Tests**

`pkg/mmokit/client_input_test.go`:

```go
func TestHandleClient_RegistersInClientInputRegistry(t *testing.T) {
    // ... setup ...
    type myInput struct{ X uint32 }
    mmokit.HandleClient(proc, func(player mmokit.Entity, msg *myInput) {})

    if !mmokit.IsClientInputType(reflect.TypeFor[myInput]()) {
        t.Fatal("HandleClient did not register in client-input registry")
    }
}

func TestHandleClient_DispatchesOnClientInputFrame(t *testing.T) {
    // ... setup ...
    var receivedMsg *myInput
    mmokit.HandleClient(proc, func(player mmokit.Entity, msg *myInput) {
        receivedMsg = msg
    })

    // Simulate a client-input frame arriving on the gateway:
    // [0x02][typeID][bodyLen][body bytes]
    // Use the gateway's frame-handling entry point directly.
    // ... assert receivedMsg has the expected fields.
}
```

- [ ] **Step 4: Commit**

```bash
git add pkg/mmokit/handle_client.go pkg/mmokit/client_input_test.go pkg/universe/stage.go pkg/universe/coordinator.go
git commit -m "feat(mmokit,universe): HandleClient + client-input wire channel (0x02)

HandleClient[T](world, fn) registers a handler for client-originated
typed messages. Gateway path: reads 0x02 frames, validates connection
→ player Entity ownership, decodes via reflection codec, dispatches
to the registered handler.

Three registries now: broadcast (HandleAll), entity-internal
(HandleAllInternal — dispatcher only), client-input (HandleClient).
All three handlers share the (target Entity, msg *T) signature."
```

### Task 2.2: PlayerStateOf helper

**Files:**

- Create: `pkg/mmokit/player_state.go`
- Add tests

- [ ] **Step 1: Implement**

```go
// pkg/mmokit/player_state.go
package mmokit

import (
    gamecomp "github.com/zenion/mmoserver/internal/component"
    pkguniverse "github.com/zenion/mmoserver/pkg/universe"
)

// PlayerStateOf returns the current PlayerState for the given Entity,
// resolving via the entity's PlayerConn → session lookup. Returns the
// zero-value PlayerState if entity has no PlayerConn or the session is
// not found.
//
// Use in HandleClient handlers as the first-line state filter:
//
//   if mmokit.PlayerStateOf(player) != mmokit.StateActive { return }
//
// Replaces the .States(...) builder API on the deleted OnInput.
func PlayerStateOf(e Entity) PlayerState {
    conn := Get[gamecomp.PlayerConn](e)
    if conn == nil {
        return PlayerState(0) // zero-value
    }
    stage := e.Stage()
    if stage == nil {
        return PlayerState(0)
    }
    eng := stage.Engine()
    if eng == nil || eng.Players == nil {
        return PlayerState(0)
    }
    s := eng.Players.ByConnID(conn.ConnID)
    if s == nil {
        return PlayerState(0)
    }
    return s.State
}
```

NOTE: this can't import `internal/component` from `pkg/mmokit` (layer violation — internal/component imports pkg/component which imports pkg/universe; mmokit imports universe but should not reach into internal). Need to handle PlayerConn differently — likely the real implementation uses the existing `pkg/component.PlayerConn` (the framework-side type), or makes PlayerConn part of the mmokit facade.

Investigate: where does PlayerConn live? `pkg/component/player_conn.go` (framework-side, replicated component) or `internal/component/components.go` (game-specific)?

```bash
grep -rn "type PlayerConn struct" pkg/component/ internal/component/
```

If `PlayerConn` is in `pkg/component`, the mmokit helper imports it directly (already a dependency). If in `internal/component`, push it down to `pkg/component` as part of this phase, OR write the lookup to use a different component (NetworkID + a player-session lookup that doesn't need the component).

- [ ] **Step 2: Tests**

```go
func TestPlayerStateOf_ReturnsState(t *testing.T) {
    // ... spawn a player Entity, register session, transition states ...
    s := mmokit.PlayerStateOf(playerEntity)
    if s != mmokit.StateActive {
        t.Fatalf("got %v, want StateActive", s)
    }
}

func TestPlayerStateOf_NoPlayerConn(t *testing.T) {
    // ... spawn an entity without PlayerConn ...
    s := mmokit.PlayerStateOf(npcEntity)
    if s != mmokit.PlayerState(0) {
        t.Fatalf("got %v, want zero-value PlayerState", s)
    }
}
```

- [ ] **Step 3: Commit**

```bash
git add pkg/mmokit/player_state.go pkg/mmokit/player_state_test.go
git commit -m "feat(mmokit): PlayerStateOf(entity) lookup helper

Replaces the .States(...) builder API on the deleted OnInput[T].
Handlers use as a first-line state filter:
  if mmokit.PlayerStateOf(player) != mmokit.StateActive { return }"
```

---

## Phase 3: Split PlayerInputMsg into 4 typed messages

### Task 3.1: Define typed input messages

**Files:**

- Create: `internal/game/input_messages.go`

- [ ] **Step 1: Define the structs**

```go
// internal/game/input_messages.go
package game

import "github.com/zenion/mmoserver/pkg/mmokit"

// SetMoveTarget — continuous-state click-to-move target. Active=false clears.
type SetMoveTarget struct {
    Sequence uint32
    Active   bool
    X, Y     float32
}

// SetLockTarget — change of target lock. TargetNetID=0 clears the lock.
type SetLockTarget struct {
    Sequence    uint32
    TargetNetID uint32
}

// CastAbility — discrete ability press.
type CastAbility struct {
    Sequence uint32
    Slot     uint8
}

// JettisonItem — discrete cargo jettison.
type JettisonItem struct {
    Sequence uint32
    ItemID   uint32
}

// Dock — request dock at nearest station.
type Dock struct {
    Sequence uint32
}

// Undock — request undock from current station.
type Undock struct {
    Sequence uint32
}

// Respawn — request respawn after death.
type Respawn struct {
    Sequence uint32
}

// Equip — request equip an item from cargo.
type Equip struct {
    Sequence uint32
    ItemID   uint32
    Slot     uint8
}

// InventoryTransfer — move items between inventory containers.
type InventoryTransfer struct {
    Sequence    uint32
    From        uint8 // gamecomp.ContainerKind
    To          uint8
    ItemID      uint32
    Quantity    int32
}

// BankRequest — request a bank operation (deposit/withdraw).
type BankRequest struct {
    Sequence uint32
    Op       uint8 // BankOpDeposit / BankOpWithdraw
    ItemID   uint32
    Quantity int32
}

// LootItem — request to take a specific item from a loot crate.
type LootItem struct {
    Sequence    uint32
    LootCrateID uint32
    ItemID      uint32
    Quantity    int32
}

// LootAll — request to take everything from a loot crate.
type LootAll struct {
    Sequence    uint32
    LootCrateID uint32
}

// Chat — chat message broadcast to nearby players (broadcast via HandleAll).
type Chat struct {
    Sequence uint32
    Text     string
}
```

NOTE: Chat is dual-purpose — both a HandleClient registration (player sends) AND a HandleAll registration (server broadcasts to nearby players). After the migration the broadcast leg is the typed-Send broadcast pipeline (Plan F's auto-broadcast), so the Chat struct serves both purposes. Field shape stays compact.

- [ ] **Step 2: Verify build (no callers yet)**

```bash
go vet ./internal/game/...
```

- [ ] **Step 3: Commit**

```bash
git add internal/game/input_messages.go
git commit -m "feat(game): typed input messages (no callers yet)

Defines the Go structs that replace gamepb.PlayerInputMsg, DockRequestMsg,
UndockRequestMsg, RespawnRequestMsg, InventoryTransferMsg, BankRequestMsg,
EquipRequestMsg, LootItemMsg, LootAllMsg, enginepb.ChatMsg (input path).

PlayerInputMsg splits into SetMoveTarget + SetLockTarget + CastAbility +
JettisonItem — discrete events become discrete typed messages.

Registration via mmokit.HandleClient lands in Phase 4."
```

### Task 3.2: Migrate input handlers to HandleClient

**Files:**

- Rewrite: `internal/game/input_handlers.go`

- [ ] **Step 1: Rewrite each handler**

Read the current `input_handlers.go` end to end. Each `OnInput[T]` block becomes `HandleClient[T]` with the typed Go struct. State filter migrates inline. Deps migrate to direct `mmokit.Get[T]` calls.

Pattern:

```go
// BEFORE:
mmokit.OnInput[gamepb.DockRequestMsg](mmo, gamepb.GameClientEventCode_GCE_DOCK).
    Active().
    Do(func(p *mmokit.Player, _ *gamepb.DockRequestMsg) {
        gw := gameWorldFromPlayer(p)
        if gw == nil { return }
        // ... handler logic
    })

// AFTER:
mmokit.HandleClient(mmo, func(player mmokit.Entity, msg *Dock) {
    if mmokit.PlayerStateOf(player) != mmokit.StateActive { return }
    gw := gameWorldOfEntity(player)
    if gw == nil { return }
    // ... handler logic — convert Player methods to Entity equivalents
})
```

For PlayerInputMsg: split into 4 separate `HandleClient` registrations, each touching its corresponding component (MoveTarget, PlayerInput.LockTargetNetID, PlayerInput.AbilityCast, ...).

- [ ] **Step 2: Update factory.go**

`RegisterInputs(coord)` body changes; signature stays the same. Called from `GameSetup` as before.

- [ ] **Step 3: Verify**

```bash
go vet ./internal/game/...
```

Build will be broken at this stage because:
- `gamepb.DockRequestMsg`, etc. still exist (Phase 6 deletes them).
- The OnInput call sites are gone but the wire path that delivers proto frames still exists (Phase 8 deletes the old infrastructure).

This is expected. Bundling Phases 3 + 6 + 7 + 8 in lockstep keeps the build green at commit boundaries OR we accept temporarily-broken intermediate commits. Recommend: write the migration but defer commit until Phases 6+7+8 land, then squash-commit the whole input migration as one atomic change.

ACTUALLY: the cleanest strategy is to land HandleClient + the new typed structs FIRST, then migrate input_handlers.go in one big commit alongside the proto deletion + sdkgen extension. So this Task 3.2 commits AFTER Phases 4 + 5 + 6 below.

For now: hold this work in progress (don't commit yet). Phase 5+6 below cover the bundled migration commit.

NOTE TO IMPLEMENTER: this means Task 3.2's commit lands later. Do the rewrite, but stash it / hold the diff until the later phases catch up. Or commit as a "WIP" commit and squash later.

---

## Phase 4: sdkgen extension for client inputs

### Task 4.1: Schema dump exposes client-input types

**Files:**

- Modify: `pkg/mmokit/protocol.go::AssembleFromProcess`
- Modify: `cmd/sdkgen/schema.go`

- [ ] **Step 1: Mirror the broadcast-types pass for client inputs**

Add `ClientInputTypes []ClientInputTypeSchema` to `Protocol` (and the matching shape in `cmd/sdkgen/schema.go`). The schema struct mirrors `BroadcastTypeSchema`:

```go
type ClientInputTypeSchema struct {
    Name   string                  `json:"name"`
    TypeID uint32                  `json:"type_id"`
    Fields []BroadcastFieldSchema  `json:"fields"`  // reuse the existing field schema
}
```

In `AssembleFromProcess`:

```go
for _, t := range mmokit.ClientInputTypes() {
    p.ClientInputTypes = append(p.ClientInputTypes, mmokit.ClientInputTypeOf(t))
}
```

Add `ClientInputTypeOf(t reflect.Type) ClientInputTypeSchema` to `pkg/mmokit/handle_client.go` — same body as `BroadcastTypeOf` but yields a `ClientInputTypeSchema`. Or unify into one `MessageTypeSchema` shape since they're identical.

- [ ] **Step 2: Verify dump**

```bash
go run ./examples/4node-basic --dump-schema | jq '.client_input_types | length'
# expect 0 in 4node-basic (no game-side HandleClient calls until web-pixi migration)
```

- [ ] **Step 3: Commit**

```bash
git add pkg/mmokit/handle_client.go pkg/mmokit/protocol.go cmd/sdkgen/schema.go
git commit -m "feat(mmokit,sdkgen): client-input registry exposed via --dump-schema"
```

### Task 4.2: sdkgen emits TS classes with encode + client.send

**Files:**

- Modify: `cmd/sdkgen/generate.go` (or wherever the broadcast TS class emit lives)
- Output: `web-pixi/sdk/inputs.ts` (new), `examples/4node-basic/web/sdk/inputs.ts` (new)

- [ ] **Step 1: Mirror broadcast class emit, add encode method**

Each generated input class:

```ts
export class SetMoveTarget {
    static readonly typeID = 0xa1b2c3d4;
    sequence: number = 0;
    active: boolean = false;
    x: number = 0;
    y: number = 0;

    constructor(init?: Partial<SetMoveTarget>) {
        if (init) Object.assign(this, init);
    }

    encode(): Uint8Array {
        const buf = new Uint8Array(/* sum of field sizes */);
        const dv = new DataView(buf.buffer);
        let off = 0;
        dv.setUint32(off, this.sequence, true); off += 4;
        dv.setUint8(off, this.active ? 1 : 0); off += 1;
        dv.setFloat32(off, this.x, true); off += 4;
        dv.setFloat32(off, this.y, true); off += 4;
        return buf;
    }
}
```

For mmokit.Entity fields (encoded as uint32 NetID): `dv.setUint32(off, this.fieldName, true); off += 4;`.

For string fields (length-prefixed): `dv.setUint16(off, len, true); off += 2; copyBytes(...)`.

- [ ] **Step 2: Add client.send method on SpaceClient**

In the generated `client.ts`:

```ts
type ClientInputMsg = { typeID: number; encode(): Uint8Array };

send<T extends ClientInputMsg>(msg: T): void {
    const body = msg.encode();
    const frame = new Uint8Array(1 + 4 + 4 + body.length);
    const dv = new DataView(frame.buffer);
    let off = 0;
    dv.setUint8(off, 0x02); off += 1;       // channel byte
    dv.setUint32(off, msg.typeID, true); off += 4;
    dv.setUint32(off, body.length, true); off += 4;
    frame.set(body, off);
    this.transport.sendBinary(frame);
}
```

(Method may already need integration with the existing transport's send mechanism — read existing client.ts to fit conventions.)

- [ ] **Step 3: Regenerate SDKs**

```bash
just client-sdk examples/4node-basic
just space-sdk
```

Verify generated `web-pixi/sdk/inputs.ts` and `examples/4node-basic/web/sdk/inputs.ts` exist with the expected classes.

- [ ] **Step 4: Commit**

```bash
git add cmd/sdkgen/ web-pixi/sdk/ examples/4node-basic/web/sdk/
git commit -m "feat(sdkgen,client): TS classes + client.send for client-input messages

Each HandleClient registration → one TS class with encode() instance
method + a static typeID constant. SpaceClient gains client.send(msg)
method that builds the 0x02 wire frame."
```

---

## Phase 5: Migrate web-pixi + 4node-basic clients

### Task 5.1: Migrate input call sites

**Files:**

- Modify: `web-pixi/src/network.ts`, `web-pixi/src/state.ts` (or wherever input-bundling happens)
- Modify: `examples/4node-basic/web/src/network.ts`, `examples/4node-basic/web/src/state.ts`
- Delete: any `client.sendMoveTarget(...)`-shaped methods from `web-pixi/sdk/client.ts` and `examples/4node-basic/web/sdk/client.ts` (these were sdkgen-generated for the proto-keyed inputs; sdkgen no longer emits them)

- [ ] **Step 1: Find existing input call sites**

```bash
grep -rn "sendMoveTarget\|sendDock\|sendUndock\|sendRespawn\|sendBank\|sendEquip\|sendLoot\|sendChat\|sendInventoryTransfer\|sendPlayerInput" web-pixi/src/ examples/4node-basic/web/src/
```

- [ ] **Step 2: Migrate each call site**

```ts
// BEFORE:
state.client.sendMoveTarget({
    targetX: state.moveTargetX,
    targetY: state.moveTargetY,
    sequence: state.inputSeq,
});

// AFTER:
import { SetMoveTarget } from '../sdk/inputs';
state.client.send(new SetMoveTarget({
    sequence: state.inputSeq,
    active: true,
    x: state.moveTargetX,
    y: state.moveTargetY,
}));
```

Same shape for every input. The bundled PlayerInputMsg in web-pixi/state.ts splits into separate sends per input event (move click → SetMoveTarget; ability key → CastAbility; etc.).

- [ ] **Step 3: Verify TS build**

```bash
cd web-pixi && bun run typecheck && bun run build
cd examples/4node-basic/web && bun run typecheck && bun run build
```

- [ ] **Step 4: Commit**

```bash
git add web-pixi/ examples/4node-basic/web/
git commit -m "refactor(client): migrate to client.send() with typed input classes

PlayerInputMsg-bundled sends decompose into per-input messages. Each
input event (move click, ability key, target click, etc.) sends its
own typed message. Idle players send zero input frames."
```

---

## Phase 6: Bundled migration commit (input_handlers.go + proto deletion)

### Task 6.1: Migrate server-side input handlers + delete protos

This is the bundled wire-format break commit. Combines:
- Task 3.2's input_handlers.go rewrite
- Proto deletion (PlayerInputMsg, DockRequestMsg, etc.)
- Event-code enum deletion
- The OnInput / OnInputWith code path is no longer reached but stays present for now — Phase 8 deletes it.

**Files:**

- Modify: `internal/game/input_handlers.go` (full rewrite per Task 3.2)
- Modify: `proto/gamepb/game.proto` (delete input message types)
- Modify: `proto/enginepb/engine.proto` (delete CE_PLAYER_INPUT, CE_CHAT if migrating Chat)
- Regenerate: `gen/go/`, `gen/csharp/`, `gen/es/`

- [ ] **Step 1: Edit protos**

Delete from `proto/gamepb/game.proto`:
- `message PlayerInputMsg`
- `message DockRequestMsg`, `UndockRequestMsg`, `RespawnRequestMsg`
- `message InventoryTransferMsg`, `BankRequestMsg`, `EquipRequestMsg`
- `message LootItemMsg`, `LootAllMsg`
- `GameClientEventCode_GCE_DOCK`, `_GCE_UNDOCK`, `_GCE_RESPAWN`, `_GCE_INVENTORY_TRANSFER`, `_GCE_BANK_REQUEST`, `_GCE_EQUIP`, `_GCE_LOOT_ITEM`, `_GCE_LOOT_ALL`

Per `feedback_proto_field_cleanup`: don't reserve. Just delete. Renumber remaining `GameClientEventCode_*` enum values from 1 if needed (depends on if any non-deleted values exist after the deletions).

In `proto/enginepb/engine.proto`:
- `ClientEventCode_CE_PLAYER_INPUT` — delete.
- `ClientEventCode_CE_CHAT` — delete (Chat migrates to typed Send).
- `enginepb.ChatMsg` — DECISION: if Chat is now both a HandleClient input AND a HandleAll broadcast (per Phase 3 §1 design), the proto type can be deleted entirely; the typed Go `Chat` struct serves both purposes via the reflection codec on the broadcast wire (Plan F's `WorldUpdateMsg.events`). However, `WorldUpdateMsg.chat_messages` still exists and uses `enginepb.ChatMsg`. So either:
  - (a) Delete `enginepb.ChatMsg` AND `WorldUpdateMsg.chat_messages`. Chat goes through typed broadcast like Damage. Cleaner.
  - (b) Keep `WorldUpdateMsg.chat_messages` + `enginepb.ChatMsg` for now; just remove from input event code. Pragmatic.
  - Recommendation: **(a)**. Plan G's "no protos for game messages" goal is the cleaner end state. Includes a small refactor of how chat broadcasts to docked players (today it routes via `WorldUpdateMsg.chat_messages` to docked players; after (a), chat is a typed broadcast that ServerEvents-style routes to docked players via the same channel as ability events to non-docked players).

- [ ] **Step 2: Regenerate protos**

```bash
just proto
```

- [ ] **Step 3: Rewrite input_handlers.go (per Task 3.2)**

Full rewrite. Each former `OnInput*` becomes `HandleClient[T]` with the typed Go struct. PlayerInput splits into 4. Chat handler dual-registers: `HandleClient` for the input + `HandleAll` for the broadcast.

- [ ] **Step 4: Update server-side broadcast path for chat (if option (a))**

Today `system_network.go` builds `WorldUpdateMsg{ChatMessages: ...}` for the AoI broadcast. After (a), chat messages go through Plan F's typed-broadcast `WorldUpdateMsg.events` instead. The auto-broadcast framework picks them up automatically once Chat is HandleAll-registered; the docked-player path needs an explicit subscriber (since docked players don't have an Entity in spatial AoI).

For docked players: keep a tick-end pass that drains pending `Chat` broadcasts and reliable-sends to docked players (mirrors today's pattern but consumes the typed broadcast queue instead of `pendingChat` slice). Or move docked players to ALSO be AoI viewers via a virtual position — bigger change.

Pragmatic: keep a small docked-player chat fanout that consumes the typed Chat broadcast queue alongside the existing event-bundle path.

- [ ] **Step 5: Delete generated input proto types from web-pixi clients**

The sdkgen output that generated `client.sendMoveTarget(...)` proto-typed methods no longer applies. Regenerate the SDKs to reflect the proto deletions.

```bash
just client-sdk examples/4node-basic
just space-sdk
```

Generated `client.ts` should no longer have `sendMoveTarget`-shaped methods (the protos they targeted are gone).

- [ ] **Step 6: Verify build**

```bash
go vet ./...
go test ./pkg/... ./internal/...
cd web-pixi && bun run typecheck && bun run build
cd examples/4node-basic/web && bun run typecheck && bun run build
```

- [ ] **Step 7: Commit (single big commit)**

```bash
git add proto/ gen/ internal/game/input_handlers.go internal/game/factory.go internal/game/system_network.go web-pixi/sdk/ examples/4node-basic/web/sdk/
git commit -m "refactor(game,proto,client): migrate inputs to typed Send (Plan G core)

Server side: input_handlers.go rewritten as HandleClient[T] registrations
with typed Go structs. PlayerInputMsg splits into SetMoveTarget +
SetLockTarget + CastAbility + JettisonItem.

Proto side: PlayerInputMsg, DockRequestMsg, UndockRequestMsg,
RespawnRequestMsg, InventoryTransferMsg, BankRequestMsg, EquipRequestMsg,
LootItemMsg, LootAllMsg, ChatMsg-input-path all deleted. Event-code
enums for the deleted protos deleted. WorldUpdateMsg.chat_messages
deleted; chat goes through typed broadcast.

Client side: SDKs regenerated. Input call sites migrate to
client.send(new TypedMessage(...)).

Wire-format break for clients; SDK + example clients regenerate in
lockstep."
```

---

## Phase 7: Delete OnInput infrastructure

### Task 7.1: Delete OnInput/OnInputWith/InputBuilder/InputBinding

**Files:**

- Delete: `pkg/mmokit/input.go` (entire file)
- Delete: `pkg/engine/input_dispatcher.go`, `input_dispatcher_test.go`
- Modify: `pkg/universe/coordinator.go` (delete `inputBindings` field, `AddInputBinding`, `InputBindings`, `dispatchInput` phase wiring)

- [ ] **Step 1: Verify nothing references the deleted symbols**

```bash
grep -rn "OnInput\|OnInputWith\|InputBuilder\|InputBinding\|AddInputBinding\|dispatchInput" pkg/ internal/ examples/ cmd/
```

After Phase 6's input_handlers.go rewrite, all callers should be gone. Production code: zero hits.

- [ ] **Step 2: Delete the files + the Process methods**

```bash
git rm pkg/mmokit/input.go pkg/engine/input_dispatcher.go pkg/engine/input_dispatcher_test.go
```

In `pkg/universe/coordinator.go`: delete the `inputBindings []*engine.InputBinding` field, `AddInputBinding`, `InputBindings`, and the `dispatchInput` phase invocation in the cell-loop wiring.

- [ ] **Step 3: Verify**

```bash
go vet ./...
go test ./pkg/... ./internal/...
```

- [ ] **Step 4: Commit**

```bash
git add pkg/universe/coordinator.go
git rm pkg/mmokit/input.go pkg/engine/input_dispatcher.go pkg/engine/input_dispatcher_test.go
git commit -m "refactor(mmokit,engine,universe): delete OnInput/OnInputWith infrastructure

After Plan G's HandleClient migration, the OnInput/OnInputWith/
InputBuilder/InputBinding/dispatchInput surface has zero callers.

Spec §10 step 6: substantively complete after this phase."
```

---

## Phase 8: Cleanup phase 1 — dead ActionResult infrastructure

### Task 8.1: Delete Bridge.SendActionResult + ActionResult + MsgActionResult

**Files:**

- Modify: `pkg/universe/bridge.go` (delete `Bridge.SendActionResult` interface method + `NoopBridge.SendActionResult`)
- Modify: `pkg/universe/cell_bridge_impl.go` (delete `cellBridge.SendActionResult`)
- Modify: `pkg/universe/grpc_bridge.go` (delete `grpcBridge.SendActionResult`)
- Modify: `pkg/universe/cell.go` (delete `MsgActionResult` arm)
- Modify: `pkg/universe/message.go` (delete `MsgActionResult` constant)
- Modify: `pkg/universe/action.go` (delete `ActionResult` Go type)
- Modify: `pkg/universe/mesh_frame_codec.go` (delete encode + decode arms)
- Modify: `pkg/universe/mesh_frame_codec_test.go` (delete the roundtrip case)
- Modify: `pkg/universe/universe_test.go` (delete `TestBridge_SendActionResult`)
- Modify: `proto/meshpb/mesh.proto` (delete `message ActionResult` + `MeshFrame.action_result` field 11; renumber subsequent oneof fields per `feedback_proto_field_cleanup`)
- Regenerate: `gen/go/meshpb/`

- [ ] **Step 1: Verify no producers**

```bash
grep -rn "SendActionResult\|ActionResult\|MsgActionResult\|action_result" pkg/ internal/ examples/ cmd/
```

After Plans D-F + Plan G Phases 1-7, the only references should be:
- The interface declaration + impls
- The codec + test
- The Go type definition
- Anything in `gen/`

Zero production producers. Confirmed.

- [ ] **Step 2: Edit the proto (renumber oneof fields)**

In `proto/meshpb/mesh.proto`, the `MeshFrame.msg` oneof has these fields:

```
1: BorderFrame
2: Handoff
3: ForwardInput
4: CellTransfer
5: CellTransferReady
6: CellTransferAbort
7: ClientInput
8: ClientFrame
9: ChatRelay
10: CrossCellAction
11: ActionResult           ← DELETE
12: PlayerAssignment       ← renumber to 11
13: SessionTransfer        ← renumber to 12
14: SpawnTransfer          ← renumber to 13
15: ClientDisconnect       ← renumber to 14
```

Plus delete `message ActionResult { ... }`.

- [ ] **Step 3: Regenerate proto**

```bash
just proto
```

- [ ] **Step 4: Delete the Go-side infrastructure**

Edit each file per the list above. Mostly small deletions.

- [ ] **Step 5: Verify**

```bash
go vet ./...
go test ./pkg/... ./internal/...
```

- [ ] **Step 6: Commit**

```bash
git add proto/ gen/ pkg/universe/
git commit -m "refactor(universe,proto): delete dead ActionResult infrastructure

After Plan E removed gw.SideEffects.Emit, nothing produces ActionResult
anymore. Plan D's reviewer flagged this as deletable; Plan G executes.

Deleted: pkg/universe.ActionResult Go type, Bridge.SendActionResult
interface method + 3 impls, MsgActionResult constant + cell.go arm,
codec encode/decode arms + roundtrip test, TestBridge_SendActionResult,
proto ActionResult message + MeshFrame.action_result oneof field 11
(subsequent fields renumbered)."
```

---

## Phase 9: Cleanup phase 2 — README, comments, deferrals, mining VFX, reflect.Pointer sweep

### Task 9.1: README rewrite

**Files:**

- Modify: `internal/game/README.md`

- [ ] **Step 1: Find stale references**

```bash
grep -n "MarkPlayerDeath\|MarkNPCDeath\|processDeaths\|PendingDeaths\|DeadPlayers\|combat_helpers\|side_effects\|SideEffectCollector\|SideEffectRegistry" internal/game/README.md
```

- [ ] **Step 2: Rewrite the affected sections**

Describe the post-Plan-E death flow:
1. `ApplyDamage` mutates Health + writes `LastDamagedByNetID`
2. Death observer (`OnTickEachAll[Health]`) fires `Killed` typed message when Current ≤ 0
3. `Killed` handler runs cleanup (player death cue, loot drop, transition to StateDead) + Sends KillCredit per currency drop
4. `KillCredit` handler credits killer's account (HandleAllInternal — server-internal)

Update the "Hook" / "System" tables to reflect actual current state.

- [ ] **Step 3: Commit**

```bash
git add internal/game/README.md
git commit -m "docs(game): rewrite README to describe post-Plan-E death architecture"
```

### Task 9.2: Tombstone comments + duplicate test + unusedparams

**Files:**

- Modify: `internal/game/verb_death.go` (lines ~105, 182 — tombstone comments)
- Modify: `internal/game/cross_cell_kill_credit_test.go` (line 18 — historical comment)
- Modify: `pkg/mmokit/integration_killcredit_test.go` (line 7 — historical comment)
- Modify: `internal/game/death_observer_test.go` (delete `TestDeathObserver_DoesNotRefireWhenDeathFiredTrue`)
- Modify: `internal/game/commands/kill.go` (drop `coord` parameter)
- Modify: `internal/game/commands/registry.go` (call site update)
- Modify: `cmd/sdkgen/main.go` (delete `snakeToTitle`)

- [ ] **Step 1: Sweep**

Touch each file, make the small edit per the punch list. Each is 1-3 lines.

- [ ] **Step 2: Verify**

```bash
go vet ./...
go test ./pkg/... ./internal/...
```

- [ ] **Step 3: Commit**

```bash
git add internal/game/verb_death.go internal/game/cross_cell_kill_credit_test.go internal/game/death_observer_test.go internal/game/commands/kill.go internal/game/commands/registry.go pkg/mmokit/integration_killcredit_test.go cmd/sdkgen/main.go
git commit -m "chore: drop tombstone comments, duplicate test, unused params (Plan D-F sweep)"
```

### Task 9.3: Foundation deferral (c) — Set-on-dead-entity log

**Files:**

- Modify: `pkg/mmokit/components.go::Set`

- [ ] **Step 1: Add the log line**

```go
func Set[T any](e Entity, v T) {
    h := e.resolveHandle()
    if h == (ecs.Entity{}) || e.stage == nil {
        return
    }
    if !e.stage.ECSWorld().Alive(h) {
        if e.netID != 0 {
            e.stage.Engine().Log.Log("mmokit", "Set: entity netID=%d not alive (probable use-after-free)", e.netID)
        }
        return
    }
    // ... existing
}
```

Use the existing `mmokit` log category if one exists, or add it.

- [ ] **Step 2: Commit**

```bash
git add pkg/mmokit/components.go
git commit -m "feat(mmokit): log Set() on dead entities (foundation deferral c)

Catches use-after-free patterns in dev. Single log line at category
'mmokit'; no behavior change beyond the log."
```

### Task 9.4: Mining-beam press-pulse VFX (typed Send restore)

**Files:**

- Create: `internal/game/verb_beam_toggle.go`
- Modify: `internal/game/system_ability.go` (in `AbilityTypeMiningBeam` case, Send the typed message)
- Modify: `internal/game/factory.go` (register `BeamToggle` handler — actually empty handler, just needs auto-broadcast)

- [ ] **Step 1: Define the typed message**

```go
// internal/game/verb_beam_toggle.go
package game

import "github.com/zenion/mmoserver/pkg/mmokit"

// BeamToggle is dispatched when a mining beam toggles on/off. The payload
// is purely visual — server-side state is on the MiningLaser component.
// The framework auto-broadcasts to AoI viewers (HandleAll, no internal
// state mutation needed).
type BeamToggle struct {
    Caster mmokit.Entity
    Beam   uint8
    Active bool
}

// beamToggleHandler is a no-op; broadcasts go through the framework.
// Registered for the broadcast registry to pick up the type.
func beamToggleHandler(target mmokit.Entity, msg *BeamToggle) {
    // Empty: no server-side state mutation; the broadcast is the entire effect.
}

func RegisterBeamToggleVerb(p *mmokit.Process) {
    mmokit.HandleAll(p, beamToggleHandler)
}
```

- [ ] **Step 2: Wire registration**

In `internal/game/factory.go::GameSetup`:

```go
RegisterBeamToggleVerb(coord)
```

- [ ] **Step 3: Send from system_ability.go**

In the `AbilityTypeMiningBeam` case (around line 226):

```go
case item.AbilityTypeMiningBeam:
    // ... existing toggle logic ...
    
    // Auto-broadcast a press-pulse VFX to nearby viewers.
    casterE.Send(&BeamToggle{
        Caster: casterE,
        Beam:   uint8(beamIdx),
        Active: laser.Beams[beamIdx].Active,
    })
```

- [ ] **Step 4: Add TS handler in web-pixi**

Once sdkgen regenerates with the new BeamToggle type, web-pixi can subscribe:

```ts
client.typedEvents.on(BeamToggle, (msg) => {
    // render press-pulse at the caster's position
});
```

- [ ] **Step 5: Verify + commit**

```bash
go vet ./...
go test ./pkg/... ./internal/...
just client-sdk examples/4node-basic
just space-sdk
git add internal/game/ web-pixi/ examples/4node-basic/web/sdk/
git commit -m "feat(game,client): restore mining-beam press-pulse via typed Send

Plan F intentionally regressed the mining-beam press pulse VFX (the
manual Enqueue was deleted; the beam visual continued via ActiveMining
replication). Plan G restores via a tiny BeamToggle typed message —
zero state mutation; the auto-broadcast is the entire effect."
```

### Task 9.5: Unity SDK regen

- [ ] **Step 1: Re-run proto codegen**

```bash
just proto
```

`gen/csharp/` should be in sync with the post-Plan-G proto state.

- [ ] **Step 2: Verify nothing else changed**

```bash
git diff gen/csharp/
```

If there are pending updates from Plan F's proto deletions, this commit captures them.

- [ ] **Step 3: Commit (if any diff)**

```bash
git add gen/csharp/
git commit -m "chore(gen): regenerate Unity SDK protos (post-Plans F + G)"
```

### Task 9.6: reflect.Ptr → reflect.Pointer codebase sweep

- [ ] **Step 1: Grep**

```bash
grep -rn "reflect\.Ptr\b" pkg/ internal/ cmd/ examples/ | wc -l
```

- [ ] **Step 2: Sweep**

`reflect.Ptr` is the deprecated form; `reflect.Pointer` is the modern equivalent (same constant value, renamed in Go 1.18). Mechanical replacement:

```bash
find pkg/ internal/ cmd/ examples/ -name "*.go" | xargs sed -i 's/reflect\.Ptr\b/reflect.Pointer/g'
```

- [ ] **Step 3: Verify**

```bash
go vet ./...
go test ./pkg/... ./internal/...
```

- [ ] **Step 4: Commit**

```bash
git add -u
git commit -m "chore: reflect.Ptr → reflect.Pointer sweep (gopls inline)"
```

---

## Phase 10: Closeout

### Task 10.1: Spec update + smoke + final report

- [ ] **Step 1: Run full suite + smoke**

```bash
go vet ./...
go test ./pkg/... ./internal/...
mkdir -p /tmp/mmo-build && go build -o /tmp/mmo-build/4node-basic ./examples/4node-basic/ && rm -rf /tmp/mmo-build
cd web-pixi && bun run typecheck && bun run build
cd examples/4node-basic/web && bun run typecheck && bun run build
```

All green.

- [ ] **Step 2: Update spec §10 step 6**

In `docs/superpowers/specs/2026-05-03-entity-message-passing-design.md` §10:

```diff
-6. **Migrate input handling.** Convert `InputBindings` to be a special case of `Handle[T]` with a "from-client-trust" tag. Delete the parallel input plumbing.
+6. **[done — 2026-05-05, Plan G]** **Migrate input handling.** `OnInput[T]` / `OnInputWith[T, Deps]` replaced by `mmokit.HandleClient[T]` (registration verb, not marker). `ServerOnly()` marker replaced by `mmokit.HandleAllInternal[T]`. `PlayerInputMsg` split into `SetMoveTarget` + `SetLockTarget` + `CastAbility` + `JettisonItem`. All gamepb input proto types deleted. `pkg/mmokit/input.go`, `pkg/engine/input_dispatcher*`, `Process.AddInputBinding/InputBindings/dispatchInput` all deleted.
```

Update the closing prose:

```diff
-Each step is independent and revertible. Steps 1-5 are landed (Plans A+B, C, D, E), and §4.5's AoI auto-broadcast is landed (Plan F, 2026-05-05). TargetLock + Dock turned out not to need cross-cell migration on inspection (both are local-cell systems; lock visibility was already handled by replication). Remaining: input handling migration (step 6, Plan G).
+Each step is landed. Spec §10 migration plan: complete. Plans A+B (foundation), C (Damage + Mining), D (StatusEffect + legacy surface), E (Death/Currency + ECS sweep), F (AoI auto-broadcast), G (input migration + full cleanup).
```

In §4.5: status line already added in Plan F closeout; add Plan G mention if relevant.

In §6.6: add "Status: implemented 2026-05-05, Plan G" line.

- [ ] **Step 3: Commit spec update**

```bash
git add docs/superpowers/specs/2026-05-03-entity-message-passing-design.md
git commit -m "docs(spec): mark Plan G (input migration + cleanup) landed; spec §10 complete"
```

- [ ] **Step 4: Final report**

Summarize:
- Spec §10 migration plan: ALL 6 STEPS LANDED.
- Phase summary table.
- Lines added vs deleted.
- Branch state.
- What's next: this branch is the entire entity-message-passing redesign. The branch can be force-pushed / merged to main when desired.

---

## Out of scope / not in this plan

- **Replace `WorldUpdateMsg` envelope with binary frame.** Separate plan; closes the protobuf-vestigiality story for game events.
- **Remove protobufs from server↔client engine envelopes broadly** (login frame, op request/response). Separate plan.
- **OpRouter migration to typed-Send-with-reply.** Different shape; future plan.
- **Client-side input prediction / rate limiting.** Per-handler concerns; not framework features.
- **Unity client typed-Send dispatcher.** Generated proto types regenerate as side effects of proto edits; the typed-Send dispatcher stays web-pixi-only.

---

## Quick orientation for a fresh agent

If you're picking this up cold:

- **Branch:** `feat/mmokit-entity-message-api`. Continue on this branch.
- **Latest commit before Plan G:** `7bdbce5` (Plan F closeout) → `e2fbe67` (Plan G spec).
- **What's done:** Plans A+B (foundation), C (Damage + Mining + Process-level wrappers), D (StatusEffect + legacy CrossCellAction surface deletion), E (Death/Currency composition + ECS-access full sweep), F (AoI auto-broadcast + sdkgen TS classes for broadcast types). `internal/game/` is fully on the mmokit facade. Spec §5 composition example exists in `verb_death.go`. Spec §4.5 auto-broadcast exists in the framework + sdkgen + TS dispatcher.
- **What you'll need to read first:**
  - `docs/superpowers/specs/2026-05-05-input-migration-design.md` (Plan G design)
  - `pkg/mmokit/input.go` (the OnInput/OnInputWith API being deleted)
  - `internal/game/input_handlers.go` (the call sites being rewritten)
  - `pkg/mmokit/messaging.go` + `messaging_all.go` + `broadcast.go` (the typed-message dispatcher you're extending)
  - `pkg/universe/reflect_marshal.go` + `pkg/universe/reflect_codec_registry.go` (the wire format pieces)
  - `pkg/universe/coordinator.go` around `dispatchInput` (the phase being deleted)
  - `cmd/sdkgen/` and `web-pixi/sdk/inputs.ts`-style output (for Phase 4 sdkgen extension)

The plan is concrete; mirror existing patterns (verb_*.go family, broadcast registry, sdkgen) and don't over-think it. If genuinely ambiguous after exploration, make the reasonable choice and report DONE_WITH_CONCERNS.
