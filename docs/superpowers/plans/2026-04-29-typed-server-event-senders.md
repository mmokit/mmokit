# Typed Server-Event Senders Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the type-erased `ServerEvents.Send(connMgr, connID, code, msg proto.Message)` API with typed `*Sender[T]` handles returned at registration. Eliminate the entire class of "registered as X, sent as Y" runtime panics — make them Go compile-time errors.

**Architecture:** `RegisterServerEvent[T]` returns `*Sender[T]`. The sender carries the registered code internally; its `Send(sender, connID, *T)` method is statically typed so passing the wrong message type fails to compile. Games collect senders into a struct (e.g. `SpaceSenders`) for ergonomic call-site access. The legacy `ServerEvents.Send` and `ServerEvents.Build` are deleted.

**Tech Stack:** Go 1.23 generics, protobuf, mmokit facade.

**Motivation (from real-world bug):** during the runtime-debug-gating refactor, `SE_DEBUG_INFO` switched from carrying `CellTopologyMsg` to `DebugInfoMsg`. The space game's `sendCellTopology` still called `ServerEvents().Send(connMgr, connID, SE_DEBUG_INFO, *CellTopologyMsg{...})`. Both messages satisfy `proto.Message`, so it compiled. The mismatch was only caught at runtime — by panic, on the first player spawn.

**Spec/precedent:** Mirrors the `mmokit.OnInputWith[Msg, Deps]` typed-input redesign (2026-04-28-player-input-api-design.md). Same philosophy — wire-format mismatches become Go compile-time errors.

---

## Conventions for every task

- **Working directory:** `.`
- **Branch:** TBD when starting (suggest `feature/typed-server-events`)
- **Build verification:** `just build` (NEVER `go build ./...`)
- **Vet verification:** `go vet ./...`
- **Test verification:** `go test ./<pkg>/...`
- **Web typecheck:** `cd web-pixi && bun run typecheck` and `cd examples/4node-basic/web && bun run typecheck`
- **Commit format:** Conventional Commits with `Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>` trailer
- **No backward compat:** the old `ServerEvents.Send` / `ServerEvents.Build` are deleted in the same PR per `feedback_no_backward_compat`

---

# Phase 1 — Add `Sender[T]` to mmokit

Goal: introduce the typed sender. Keep the legacy untyped `Send`/`Build` alive so migration can happen incrementally; they're deleted in Phase 4.

## Task 1.1: Define `Sender[T]` and rewire `RegisterServerEvent[T]` to return one

**Files:**
- Modify: `pkg/mmokit/server_events.go`
- Modify: `pkg/mmokit/server_events_test.go`

- [ ] **Step 1: Read the current `RegisterServerEvent` and `ServerEvents.Send` impl**

```bash
sed -n '46,108p' pkg/mmokit/server_events.go
```

Note `RegisterServerEvent` returns nothing today; `Send`/`Build` look up by code and runtime-typecheck via reflection.

- [ ] **Step 2: Write a failing test for the new typed sender**

Append to `pkg/mmokit/server_events_test.go`:

```go
func TestSender_TypedSendCompiles(t *testing.T) {
    e := NewServerEvents()
    sender := RegisterServerEvent[enginepb.PongMsg](e, enginepb.ServerEventCode_SE_PONG)
    if sender == nil {
        t.Fatal("RegisterServerEvent returned nil sender")
    }
    if got := sender.Code(); got != uint32(enginepb.ServerEventCode_SE_PONG) {
        t.Errorf("Code() = %d, want SE_PONG", got)
    }
    // Build a frame — should not panic on type mismatch (no mismatch possible).
    frame := sender.Build(&enginepb.PongMsg{ClientTime: 42})
    if frame == nil {
        t.Fatal("Build returned nil frame for valid msg")
    }
}
```

- [ ] **Step 3: Run the test and verify it fails**

```bash
go test ./pkg/mmokit/ -run TestSender_ -v -count=1
```

Expected: FAIL — `*Sender[T]` and `RegisterServerEvent` returning a value are undefined.

- [ ] **Step 4: Implement `Sender[T]` in `pkg/mmokit/server_events.go`**

Append:

```go
// Sender is a typed handle for emitting one server-event code. Constructed
// by RegisterServerEvent[T] at protocol setup; held by game code (typically
// in a per-game senders struct) and used at every call site instead of the
// untyped ServerEvents.Send.
//
// The generic parameter T is the proto message body type (not a pointer).
// Send takes *T — passing the wrong type fails to compile, eliminating the
// runtime "registered as X, sent as Y" panic class.
type Sender[T any] struct {
    code   uint32
    name   string // for diagnostics — matches schema entry name
}

// Code returns the wire code this sender emits.
func (s *Sender[T]) Code() uint32 { return s.code }

// Build marshals msg, asserts the registered code matches, and returns a
// channel-0x00 wire frame. Use when broadcasting a single frame to many
// connections — Build once, SendReliable per recipient.
func (s *Sender[T]) Build(msg *T) []byte {
    pmsg, ok := any(msg).(proto.Message)
    if !ok {
        panic(fmt.Sprintf("Sender[%T]: msg does not satisfy proto.Message — generic Sender requires a proto type", *new(T)))
    }
    return MakeEvent(s.code, pmsg)
}

// Send builds the frame and writes it to one connection.
func (s *Sender[T]) Send(sender net.ConnSender, connID uint32, msg *T) {
    sender.SendReliable(connID, s.Build(msg))
}
```

- [ ] **Step 5: Update `RegisterServerEvent` to return `*Sender[T]`**

Change the signature:

```go
func RegisterServerEvent[T any, P interface {
    *T
    proto.Message
}, C engine.EventCode](e *ServerEvents, code C, opts ...ServerEventOption) *Sender[T] {
    var zero P = new(T)
    enumName := enumConstantName(code)
    entry := serverEventEntry{
        code:      uint32(code),
        name:      deriveEventName(enumName),
        protoName: string(proto.MessageName(zero)),
        protoType: reflect.TypeOf(zero),
        enumName:  enumName,
    }
    for _, opt := range opts {
        opt(&entry)
    }
    e.entries[entry.code] = entry
    return &Sender[T]{code: entry.code, name: entry.name}
}
```

- [ ] **Step 6: Run the test and verify it passes**

```bash
go test ./pkg/mmokit/ -run TestSender_ -v -count=1
```

Expected: PASS.

- [ ] **Step 7: Run all mmokit tests**

```bash
go test ./pkg/mmokit/ -count=1
```

Expected: all pass. The legacy `ServerEvents.Send` / `Build` callers (existing tests) keep working — they don't use the new return value.

- [ ] **Step 8: Vet + build**

```bash
go vet ./...
just build
```

Expected: clean.

- [ ] **Step 9: Commit**

```bash
git add pkg/mmokit/server_events.go pkg/mmokit/server_events_test.go
git commit -m "$(cat <<'EOF'
feat(mmokit): RegisterServerEvent[T] returns *Sender[T]

Adds typed Sender[T] with Code(), Build(*T), Send(connSender, connID, *T).
RegisterServerEvent[T] now returns one. Legacy ServerEvents.Send /
Build kept for the migration window — deleted in Phase 4.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

## Task 1.2: Add a regression test that verifies type-mismatch fails to compile

**Files:**
- Create: `pkg/mmokit/server_events_typesafety_test.go`

- [ ] **Step 1: Add a test that uses `Sender[T].Send` with the right type**

The compile-time guarantee is the whole point — any test that compiles is implicitly verifying the typing. Add an explicit positive-case test:

```go
package mmokit

import (
    "testing"

    enginepb "github.com/zenion/mmoserver/gen/go/enginepb"
)

// TestSender_TypeSafe_Compiles asserts at compile time that Sender[T].Send
// only accepts *T. If the typing regresses (e.g. Send accidentally takes
// proto.Message again), an unrelated test file will break — but this one
// stays as the canonical positive-case anchor.
func TestSender_TypeSafe_Compiles(t *testing.T) {
    e := NewServerEvents()
    pongSender := RegisterServerEvent[enginepb.PongMsg](e, enginepb.ServerEventCode_SE_PONG)

    // Build with the right type — compiles + builds a valid frame.
    frame := pongSender.Build(&enginepb.PongMsg{ClientTime: 1, ServerTime: 2})
    if len(frame) == 0 {
        t.Fatal("expected non-empty frame")
    }
    // Negative case — passing a different message type would fail to
    // compile. Document that here for future readers:
    //
    //     pongSender.Build(&enginepb.LoginRejectedMsg{Reason: "x"})  // compile error
    //
    // This is the entire point of the redesign — uncomment that line and
    // confirm the build fails before believing the typing.
}
```

- [ ] **Step 2: Run + commit**

```bash
go test ./pkg/mmokit/ -run TestSender_TypeSafe -v -count=1
```

Expected: PASS.

```bash
git add pkg/mmokit/server_events_typesafety_test.go
git commit -m "$(cat <<'EOF'
test(mmokit): canonical positive-case for typed Sender[T]

Documents the compile-time guarantee with a commented negative-case
example. The redesign's whole point is that the negative case fails
to compile — there's no runtime test for that, by design.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

# Phase 2 — Migrate the space game

Goal: replace every `gw.ServerEvents().Send(...)` and `gw.ServerEvents().Build(...)` call site with typed `Sender[T]` access. The space game has ~25 call sites across 8 files. Introduce a `SpaceSenders` struct that holds all senders.

## Task 2.1: Define `SpaceSenders` and populate during `Protocol.ServerEvents` setup

**Files:**
- Create: `internal/game/senders.go`
- Modify: `cmd/server/main.go`

- [ ] **Step 1: Create the senders struct**

Create `internal/game/senders.go`:

```go
package game

import (
    enginepb "github.com/zenion/mmoserver/gen/go/enginepb"
    gamepb "github.com/zenion/mmoserver/gen/go/gamepb"
    "github.com/zenion/mmoserver/pkg/mmokit"
)

// SpaceSenders holds typed Sender handles for every server→client event
// the space game emits. Populated once at protocol-setup time; held on
// GameWorld and consulted at every emit call site.
//
// All fields are non-nil after setup. A nil field means the registration
// at setup time was missed — fix the registration, don't add nil-guards
// at call sites.
type SpaceSenders struct {
    LoginRejected   *mmokit.Sender[enginepb.LoginRejectedMsg]
    PlayerSpawned   *mmokit.Sender[gamepb.PlayerSpawnedMsg]
    WorldUpdate     *mmokit.Sender[gamepb.WorldUpdateMsg]
    PlayerDied      *mmokit.Sender[gamepb.PlayerDiedMsg]
    PlayerOwnState  *mmokit.Sender[gamepb.PlayerOwnStateMsg]
    BankContents    *mmokit.Sender[gamepb.BankContentsMsg]
    TransferResult  *mmokit.Sender[gamepb.TransferResultMsg]
    EquipResult     *mmokit.Sender[gamepb.EquipResultMsg]
    DockingState    *mmokit.Sender[gamepb.DockingStateMsg]
    Docked          *mmokit.Sender[gamepb.DockedMsg]
    MapData         *mmokit.Sender[gamepb.MapDataMsg]
    CurrencyUpdate  *mmokit.Sender[gamepb.CurrencyUpdateMsg]
}
```

- [ ] **Step 2: Populate in `cmd/server/main.go`'s `ServerEvents` callback**

Find the existing block:

```go
ServerEvents(func(e *mmokit.ServerEvents) {
    mmokit.RegisterServerEvent[gamepb.PlayerSpawnedMsg](e, ...)
    // ... etc
})
```

Replace with:

```go
senders := &game.SpaceSenders{}
coordCfg.Protocol = mmokit.NewProtocol("space").
    ClientEvents(/* unchanged */).
    ServerEvents(func(e *mmokit.ServerEvents) {
        senders.PlayerSpawned = mmokit.RegisterServerEvent[gamepb.PlayerSpawnedMsg](e,
            enginepb.ServerEventCode_SE_PLAYER_SPAWNED, mmokit.WithEventName("playerSpawned"))
        senders.WorldUpdate = mmokit.RegisterServerEvent[gamepb.WorldUpdateMsg](e,
            enginepb.ServerEventCode_SE_WORLD_UPDATE, mmokit.WithEventName("worldUpdate"))
        senders.PlayerDied = mmokit.RegisterServerEvent[gamepb.PlayerDiedMsg](e,
            gamepb.GameServerEventCode_GSE_PLAYER_DIED)
        senders.PlayerOwnState = mmokit.RegisterServerEvent[gamepb.PlayerOwnStateMsg](e,
            enginepb.ServerEventCode_SE_PLAYER_OWN_STATE)
        senders.BankContents = mmokit.RegisterServerEvent[gamepb.BankContentsMsg](e,
            gamepb.GameServerEventCode_GSE_BANK_CONTENTS)
        senders.TransferResult = mmokit.RegisterServerEvent[gamepb.TransferResultMsg](e,
            gamepb.GameServerEventCode_GSE_TRANSFER_RESULT)
        senders.EquipResult = mmokit.RegisterServerEvent[gamepb.EquipResultMsg](e,
            gamepb.GameServerEventCode_GSE_EQUIP_RESULT)
        senders.DockingState = mmokit.RegisterServerEvent[gamepb.DockingStateMsg](e,
            gamepb.GameServerEventCode_GSE_DOCKING_STATE)
        senders.Docked = mmokit.RegisterServerEvent[gamepb.DockedMsg](e,
            gamepb.GameServerEventCode_GSE_DOCKED)
        senders.MapData = mmokit.RegisterServerEvent[gamepb.MapDataMsg](e,
            gamepb.GameServerEventCode_GSE_MAP_DATA)
        senders.CurrencyUpdate = mmokit.RegisterServerEvent[gamepb.CurrencyUpdateMsg](e,
            gamepb.GameServerEventCode_GSE_CURRENCY_UPDATE)
        // LoginRejected uses an engine-default registration; capture the
        // sender it returns. NewProtocol auto-registered the engine
        // default — call RegisterServerEvent[LoginRejectedMsg] again to
        // re-register and capture the typed sender (last-wins).
        senders.LoginRejected = mmokit.RegisterServerEvent[enginepb.LoginRejectedMsg](e,
            enginepb.ServerEventCode_SE_LOGIN_REJECTED)
    })
```

- [ ] **Step 3: Plumb senders into `GameWorld`**

Add a field to `GameWorld` (in `internal/game/world.go` or wherever the struct is defined):

```go
Senders *SpaceSenders
```

Pass it through `WorldFactory`:

```go
func WorldFactory(
    gameCfg *GameConfig,
    playerDB *PlayerRepo,
    playerSessions *mmokit.PlayerSessions,
    senders *SpaceSenders, // NEW
) func(base *mmokit.Stage) mmokit.GameWorld {
    return func(base *mmokit.Stage) mmokit.GameWorld {
        // ...
        gw := NewGameWorld(...)
        gw.Senders = senders
        // ...
    }
}
```

Update `cmd/server/main.go` to pass the senders into `WorldFactory`:

```go
coordCfg.World = game.WorldFactory(&gameCfg, playerDB, playerSessions, senders)
```

- [ ] **Step 4: Vet + build (the new fields will compile but are unused at this point)**

```bash
go vet ./...
just build
```

Expected: clean. Senders struct exists but no call sites use it yet (next task).

- [ ] **Step 5: Commit**

```bash
git add internal/game/senders.go internal/game/world.go internal/game/factory.go cmd/server/main.go
git commit -m "$(cat <<'EOF'
feat(game): add SpaceSenders + populate during protocol setup

SpaceSenders holds a typed mmokit.Sender[T] for every server-event
the space game emits. Populated once in cmd/server/main.go's
ServerEvents callback; threaded through WorldFactory onto every
GameWorld instance for cell-local access.

Call sites still use the legacy ServerEvents().Send path — migrated
in the next task.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

## Task 2.2: Migrate every call site to typed Sender

**Files (mechanical migration of ~25 call sites):**
- Modify: `internal/game/entity_ship.go` (4 sites)
- Modify: `internal/game/lifecycle.go` (2 sites)
- Modify: `internal/game/system_equipment.go` (1 site)
- Modify: `internal/game/system_economy.go` (2 sites)
- Modify: `internal/game/system_docking.go` (1 site)
- Modify: `internal/game/system_network.go` (4 sites — uses both Send and Build)
- Modify: `internal/game/combat_helpers.go` (2 sites)
- Modify: `internal/game/game.go` (1 site)
- Modify: `internal/game/commands/currency.go` (1 site)
- Modify: `cmd/server/main.go` (2 sites — `events.Send` calls)

- [ ] **Step 1: Find every call site**

```bash
grep -rn "ServerEvents()\.Send\|ServerEvents()\.Build\|events\.Send" internal/ cmd/server/ 2>/dev/null | grep -v "_test\.go"
```

Confirms the list above. Each site looks like one of these patterns:

```go
// Send pattern:
gw.ServerEvents().Send(gw.eng.ConnMgr, connID, uint32(SE_FOO), &fooMsg{...})
// becomes:
gw.Senders.Foo.Send(gw.eng.ConnMgr, connID, &fooMsg{...})

// Build pattern (used for broadcast):
frame := gw.ServerEvents().Build(uint32(SE_FOO), &fooMsg{...})
// becomes:
frame := gw.Senders.Foo.Build(&fooMsg{...})
```

- [ ] **Step 2: Migrate `internal/game/entity_ship.go`**

Open the file. Lines 158, 173, 180, 232, 242, 249 have the pattern. Each `gw.ServerEvents().Send(gw.eng.ConnMgr, connID, uint32(<code>), <msg>)` becomes `gw.Senders.<Name>.Send(gw.eng.ConnMgr, connID, <msg>)`. Map by code:

| Old code | New sender field |
|---|---|
| `enginepb.ServerEventCode_SE_PLAYER_SPAWNED` | `gw.Senders.PlayerSpawned` |
| `gamepb.GameServerEventCode_GSE_MAP_DATA` | `gw.Senders.MapData` |
| `gamepb.GameServerEventCode_GSE_CURRENCY_UPDATE` | `gw.Senders.CurrencyUpdate` |

Do the substitution for each line.

Run `go vet ./internal/game/` after to catch any typos.

- [ ] **Step 3: Migrate the remaining files using the same code-to-sender mapping**

Apply identical mechanical migration to each of:
- `internal/game/lifecycle.go` (PlayerDied + Docked)
- `internal/game/system_equipment.go` (EquipResult)
- `internal/game/system_economy.go` (TransferResult + BankContents)
- `internal/game/system_docking.go` (DockingState)
- `internal/game/system_network.go` (WorldUpdate via Send and Build, PlayerOwnState via Build)
- `internal/game/combat_helpers.go` (CurrencyUpdate × 2)
- `internal/game/game.go` (MapData)
- `internal/game/commands/currency.go` (BankContents — note this is in a sub-package; access via `gw.Senders` should work as long as `*GameWorld` is in scope)

For `system_network.go`'s `Build` pattern at line 156/170/244, change `gw.ServerEvents().Build(uint32(<code>), <msg>)` to `gw.Senders.<Name>.Build(<msg>)` — same field-name mapping.

- [ ] **Step 4: Migrate `cmd/server/main.go` Send call sites**

Two `events.Send(connMgr, connID, uint32(<code>), <msg>)` calls in main.go:
- Line ~173: `SE_LOGIN_REJECTED` → `senders.LoginRejected.Send(connMgr, connID, msg)`
- Line ~282 (inside the marketplace `SendBankUpdate` closure): `GSE_BANK_CONTENTS` → `senders.BankContents.Send(connMgr, connID, msg)`

The closure already captures `events`; capture `senders` instead (or alongside).

- [ ] **Step 5: Vet + build**

```bash
go vet ./...
just build
```

Expected: clean.

- [ ] **Step 6: Run tests**

```bash
go test ./internal/game/... ./cmd/server/...
```

Expected: pass.

- [ ] **Step 7: Smoke-test the space game manually**

```bash
just dev
# in another terminal: connect via browser, log in, verify spawn + UI events
```

Expected: spawn works, no panics, full UI flow.

- [ ] **Step 8: Commit**

```bash
git add -A
git commit -m "$(cat <<'EOF'
refactor(game): migrate every emit site to typed SpaceSenders.X.Send

Replaces 25 gw.ServerEvents().Send / Build calls with typed sender
access. Wrong-type sends now fail to compile (would have caught the
SE_DEBUG_INFO / CellTopologyMsg vs DebugInfoMsg regression at vet
time instead of at first-spawn panic).

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

# Phase 3 — Migrate 4node-basic + engine internals

Goal: same pattern, smaller surface. The 4node-basic example only emits a couple of events; the engine itself emits server events from a few internal paths that need the typed shape too.

## Task 3.1: Migrate `examples/4node-basic` if any non-engine sends

**Files:**
- Modify: `examples/4node-basic/main.go` (if any RegisterServerEvent + Send pairs)

- [ ] **Step 1: Audit**

```bash
grep -n "ServerEvents()\.Send\|ServerEvents()\.Build\|RegisterServerEvent" examples/4node-basic/
```

If any direct sends exist, declare a small senders struct (e.g. `BasicSenders` with one or two fields) and migrate. If none, skip — 4node-basic relies on engine-default events only.

- [ ] **Step 2: Vet + build + commit if changes made**

## Task 3.2: Migrate `pkg/mmokit/mmokit.go:1489` and `pkg/universe/stage.go` engine-internal sends

**Files:**
- Modify: `pkg/mmokit/mmokit.go`
- Modify: `pkg/universe/stage.go`

- [ ] **Step 1: Examine each site**

```bash
sed -n '1485,1495p' pkg/mmokit/mmokit.go
sed -n '565,575p' pkg/universe/stage.go
```

Note whether they emit a hardcoded engine-default code (e.g. SE_DEBUG_INFO, SE_CELL_CHANGE) or a generic any-code path. If hardcoded, the engine itself owns a small typed sender per code (constructed from `engine`'s default-events block in `NewProtocol`). If generic, this site cannot be made fully typed — keep the legacy path until Phase 4 reveals what to do (probably: deprecate the generic emit by replacing it with a per-code helper).

- [ ] **Step 2: Migrate the hardcoded sites**

For sites emitting a known engine-default code, declare an `engineSenders` struct in `pkg/mmokit/protocol.go` populated by `NewProtocol`'s default-events block (which currently calls `RegisterServerEvent` but discards the return value). Capture the senders into `engineSenders` and expose via accessors so universe/engine internals can use them.

- [ ] **Step 3: Vet + build + test + commit**

```bash
go vet ./...
just build
go test ./pkg/...
```

---

# Phase 4 — Delete the legacy untyped API

Goal: `ServerEvents.Send` and `ServerEvents.Build` are gone. Future emits MUST go through a typed `*Sender[T]`.

## Task 4.1: Delete `ServerEvents.Send` and `ServerEvents.Build`

**Files:**
- Modify: `pkg/mmokit/server_events.go`

- [ ] **Step 1: Confirm no remaining callers**

```bash
grep -rn "ServerEvents()\.Send\|ServerEvents()\.Build\|\.Send(.*proto\.Message\b\|\.Build(.*proto\.Message\b" --include="*.go" .
```

Expected: zero hits in non-test code (unit tests for the registry itself are allowed).

If any hits remain, those are stragglers — fix and re-run.

- [ ] **Step 2: Delete the methods**

In `pkg/mmokit/server_events.go`, remove `func (e *ServerEvents) Send(...)` and `func (e *ServerEvents) Build(...)`. Keep `RegisterServerEvent[T]` and `Sender[T]`.

- [ ] **Step 3: Update any tests in `pkg/mmokit/server_events_test.go` that still reference the deleted methods**

Replace test sites with `Sender[T].Send` / `Sender[T].Build`.

- [ ] **Step 4: Vet + full test pass + smoke**

```bash
go vet ./...
just build
go test ./...
just test-pg  # if Postgres is up
just dev      # smoke
```

- [ ] **Step 5: Commit**

```bash
git add pkg/mmokit/server_events.go pkg/mmokit/server_events_test.go
git commit -m "$(cat <<'EOF'
refactor(mmokit): delete untyped ServerEvents.Send / Build

All emit sites use typed Sender[T] now. The untyped path is dead
code and a footgun — every "code 14 registered as X but Build called
with Y" panic in the codebase came from this path. Gone.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

## Task 4.2: Update CLAUDE.md

**Files:**
- Modify: `CLAUDE.md`

- [ ] **Step 1: Find and update any reference to `ServerEvents.Send` or "untyped emit"**

```bash
grep -n "ServerEvents()\.Send\|ServerEvents\.Send\|RegisterServerEvent" CLAUDE.md
```

- [ ] **Step 2: Add a short paragraph documenting the typed pattern**

Append to the relevant section (probably under "Networking & Replication" or near the schema-export discussion):

```markdown
### Typed server-event emit

Every emit goes through a typed `*mmokit.Sender[T]` returned by
`RegisterServerEvent[T]`. Games declare a senders struct (e.g.
`SpaceSenders` with one `*Sender[FooMsg]` field per registered code),
populate it in the `Protocol.ServerEvents(...)` callback, and call
`senders.Foo.Send(connMgr, connID, &fooMsg{...})` at every emit site.
Wrong-type sends fail to compile, eliminating the entire class of
"registered as X, sent as Y" runtime panics.
```

- [ ] **Step 3: Commit**

---

# Phase 5 — Final verification

## Task 5.1: Full vet + build + test + manual smoke

- [ ] **Step 1: Vet entire tree**

```bash
go vet ./...
```

- [ ] **Step 2: Build**

```bash
just build
```

- [ ] **Step 3: Run all unit tests**

```bash
go test ./... -count=1 -timeout 180s
```

- [ ] **Step 4: Run Postgres integration tests**

```bash
just test-pg
```

- [ ] **Step 5: Run e2e mesh tests**

```bash
go test ./examples/4node-basic/... -timeout 120s
```

- [ ] **Step 6: Web typecheck both clients**

```bash
cd web-pixi && bun run typecheck
cd ../examples/4node-basic/web && bun run typecheck
```

- [ ] **Step 7: Regen SDKs and verify zero diff**

```bash
just client-sdk examples/4node-basic
just space-sdk
git diff --stat examples/4node-basic/web/sdk/ web-pixi/sdk/
```

Expected: zero or near-zero diff.

- [ ] **Step 8: Manual smoke**

- 4node-basic: connect, click to move, see entity move
- Space game: connect, dock, equip, chat, undock — all UI events should arrive

## Task 5.2: Push branch

```bash
git push -u origin feature/typed-server-events
```

---

# Self-review checklist

- [x] **Spec coverage:** every concern from the bug post-mortem (compile-time type safety, no untyped Send) is addressed in Phases 1+4. Phases 2+3 mechanically migrate every call site.
- [x] **Placeholder scan:** no TBD / TODO / "fill in details".
- [x] **Type consistency:** `*mmokit.Sender[T]`, `RegisterServerEvent[T]`, `SpaceSenders` are spelled the same way every time.
- [x] **Bite-sized tasks:** each task is ~5–15 minutes of mechanical work + verification.
- [x] **No skipped phases:** Phase 1 adds non-breaking; Phases 2+3 migrate; Phase 4 deletes legacy after the migration is complete; Phase 5 verifies.

---

# Open questions

1. **Should `Sender[T]` be exposed in `mmokit.go` as `Sender = mmokit_internal.Sender`?** Per `feedback_mmokit_facade_only`, all game-facing types should re-export through `pkg/mmokit/mmokit.go`. Recommendation: yes — add the alias as part of Task 1.1.

2. **What about engine-default events that NewProtocol auto-registers?** They currently discard the `Sender` return. The engine has a few internal paths that emit these (PONG, LOGIN_REJECTED, SERVER_CONFIG, etc.). Phase 3.2 covers these — capture the senders into a small package-level `engineSenders` struct.

3. **Should we codegen the senders struct from the schema?** Possible follow-up. The hand-rolled `SpaceSenders` is fine for one game; if more games join, a `cmd/sendergen` (similar to `cmd/sdkgen`) would emit the struct. Not in scope for this plan.
