# Protocol Unification Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Eliminate duplicate registration in `dumpProtocolSchema` files by making runtime registration self-describing, then have the engine assemble + dump the schema automatically.

**Architecture:** Introduce a `ServerEventRegistry` (the only registration type that lacks a runtime registry today), add typed `Register[Req, Res]` + `Schema()` to the existing operations router, then have the engine intercept `--dump-schema` after `Build()` and assemble the full schema from the runtime registries. Game-side `schema.go` files are deleted; `cfg.Protocol` becomes a single declaration site for what's not already self-describing. Final phase chains SDK regen into `just build`.

**Tech Stack:** Go 1.23+ generics, protobuf, existing `pkg/mmokit` / `pkg/universe` / `pkg/ops` packages, `cmd/sdkgen` TS codegen.

**Spec:** [docs/superpowers/specs/2026-04-24-protocol-unification-design.md](../specs/2026-04-24-protocol-unification-design.md)

---

## Phase 1: ServerEventRegistry + MakeEvent migration

### Task 1.1: Server-event name derivation helper

**Files:**
- Create: `pkg/mmokit/server_events_name.go`
- Test: `pkg/mmokit/server_events_name_test.go`

Name derivation strips known event-code prefixes (`SE_`, `GSE_`, `CE_`, `GCE_`, `SSE_`, `BCE_`) and converts `SCREAMING_SNAKE` → `camelCase`. Pure string transform, no proto reflection.

- [ ] **Step 1: Write failing tests for `deriveEventName`**

```go
// pkg/mmokit/server_events_name_test.go
package mmokit

import "testing"

func TestDeriveEventName(t *testing.T) {
    cases := []struct {
        in, want string
    }{
        {"SE_PLAYER_SPAWNED", "playerSpawned"},
        {"GSE_BANK_CONTENTS", "bankContents"},
        {"SE_CELL_TOPOLOGY", "cellTopology"},
        {"GSE_CURRENCY_UPDATE", "currencyUpdate"},
        {"SE_PONG", "pong"},
        {"SSE_LEADERBOARD", "leaderboard"},
        {"BCE_LOGIN", "login"},
        {"CE_PING", "ping"},
        {"PLAYER_SPAWNED", "playerSpawned"}, // no prefix
        {"SE_X", "x"},                       // single segment
    }
    for _, tc := range cases {
        if got := deriveEventName(tc.in); got != tc.want {
            t.Errorf("deriveEventName(%q) = %q, want %q", tc.in, got, tc.want)
        }
    }
}
```

- [ ] **Step 2: Run test, verify it fails**

```bash
go test ./pkg/mmokit/ -run TestDeriveEventName -v
```

Expected: `undefined: deriveEventName`.

- [ ] **Step 3: Implement `deriveEventName`**

```go
// pkg/mmokit/server_events_name.go
package mmokit

import "strings"

var eventCodePrefixes = []string{"SE_", "GSE_", "CE_", "GCE_", "SSE_", "BCE_"}

// deriveEventName converts a proto enum constant name like SE_PLAYER_SPAWNED
// into a camelCase SDK method name like "playerSpawned". Strips known event-
// code prefixes and lowercases the first word.
func deriveEventName(constName string) string {
    s := constName
    for _, p := range eventCodePrefixes {
        if strings.HasPrefix(s, p) {
            s = strings.TrimPrefix(s, p)
            break
        }
    }
    parts := strings.Split(s, "_")
    var b strings.Builder
    for i, part := range parts {
        if part == "" {
            continue
        }
        if i == 0 {
            b.WriteString(strings.ToLower(part))
            continue
        }
        b.WriteString(strings.ToUpper(part[:1]))
        b.WriteString(strings.ToLower(part[1:]))
    }
    return b.String()
}
```

- [ ] **Step 4: Run test, verify pass**

```bash
go test ./pkg/mmokit/ -run TestDeriveEventName -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/mmokit/server_events_name.go pkg/mmokit/server_events_name_test.go
git commit -m "feat(mmokit): add server-event name derivation"
```

---

### Task 1.2: `ServerEvents` registry core

**Files:**
- Create: `pkg/mmokit/server_events.go`
- Test: `pkg/mmokit/server_events_test.go`

Defines `ServerEvents`, `RegisterServerEvent[T, P, C]`, `Build(code, msg) []byte`, `Send(connMgr, connID, code, msg)`, `Schema()`, `WithName(string)` option. Build wraps the existing `MakeEvent` byte layout so the wire format is unchanged.

- [ ] **Step 1: Write failing test for Register + Schema**

```go
// pkg/mmokit/server_events_test.go
package mmokit

import (
    "reflect"
    "testing"

    enginepb "github.com/zenion/mmokit/gen/go/enginepb"
)

func TestServerEventsRegisterAndSchema(t *testing.T) {
    e := NewServerEvents()
    RegisterServerEvent[enginepb.SpawnedMsg](e, enginepb.ServerEventCode_SE_PLAYER_SPAWNED)
    RegisterServerEvent[enginepb.PongMsg](e, enginepb.ServerEventCode_SE_PONG, WithEventName("pingResponse"))

    schema := e.Schema()
    want := []ServerEventSchema{
        {Code: uint32(enginepb.ServerEventCode_SE_PLAYER_SPAWNED), Name: "playerSpawned", ProtoName: "enginepb.SpawnedMsg"},
        {Code: uint32(enginepb.ServerEventCode_SE_PONG), Name: "pingResponse", ProtoName: "enginepb.PongMsg"},
    }
    if !reflect.DeepEqual(schema, want) {
        t.Errorf("Schema() = %+v, want %+v", schema, want)
    }
}

func TestServerEventsDuplicateRegistrationPanics(t *testing.T) {
    e := NewServerEvents()
    RegisterServerEvent[enginepb.SpawnedMsg](e, enginepb.ServerEventCode_SE_PLAYER_SPAWNED)
    defer func() {
        if r := recover(); r == nil {
            t.Fatal("expected panic on duplicate registration")
        }
    }()
    RegisterServerEvent[enginepb.PongMsg](e, enginepb.ServerEventCode_SE_PLAYER_SPAWNED)
}

func TestServerEventsBuildUnregisteredPanics(t *testing.T) {
    e := NewServerEvents()
    defer func() {
        if r := recover(); r == nil {
            t.Fatal("expected panic on Build with unregistered code")
        }
    }()
    e.Build(uint32(enginepb.ServerEventCode_SE_PONG), &enginepb.PongMsg{})
}

func TestServerEventsBuildWrongTypePanics(t *testing.T) {
    e := NewServerEvents()
    RegisterServerEvent[enginepb.SpawnedMsg](e, enginepb.ServerEventCode_SE_PLAYER_SPAWNED)
    defer func() {
        if r := recover(); r == nil {
            t.Fatal("expected panic on type mismatch")
        }
    }()
    e.Build(uint32(enginepb.ServerEventCode_SE_PLAYER_SPAWNED), &enginepb.PongMsg{})
}
```

- [ ] **Step 2: Run tests, verify fail**

```bash
go test ./pkg/mmokit/ -run TestServerEvents -v
```

Expected: `undefined: NewServerEvents` (and friends).

- [ ] **Step 3: Implement `pkg/mmokit/server_events.go`**

```go
// pkg/mmokit/server_events.go
package mmokit

import (
    "fmt"
    "reflect"
    "sort"

    "google.golang.org/protobuf/proto"

    "github.com/zenion/mmokit/pkg/engine"
    "github.com/zenion/mmokit/pkg/net"
)

// ServerEvents is a typed registry of server→client event codes mapped to
// their proto payload types. Each (code, protoType) pair is declared once at
// game wiring time and consumed by both the runtime emit path (Build/Send)
// and the schema dump (Schema). Validates at Build that the caller's payload
// matches the registered type — eliminates the silent drift that ad-hoc
// MakeEvent calls allow today.
type ServerEvents struct {
    entries map[uint32]serverEventEntry
}

type serverEventEntry struct {
    code      uint32
    name      string
    protoName string
    protoType reflect.Type // pointer type, e.g. *enginepb.SpawnedMsg
    enumName  string       // raw enum constant for diagnostics
}

// ServerEventOption customizes a server-event registration.
type ServerEventOption func(*serverEventEntry)

// WithEventName overrides the auto-derived camelCase name. Use when the
// derived name collides or reads poorly.
func WithEventName(name string) ServerEventOption {
    return func(e *serverEventEntry) { e.name = name }
}

// NewServerEvents creates an empty server-event registry.
func NewServerEvents() *ServerEvents {
    return &ServerEvents{entries: make(map[uint32]serverEventEntry)}
}

// RegisterServerEvent declares a server→client event with its proto payload
// type. Panics on duplicate code. Name auto-derives from the enum constant
// (SE_PLAYER_SPAWNED → "playerSpawned"); override via WithEventName.
func RegisterServerEvent[T any, P interface {
    *T
    proto.Message
}, C engine.EventCode](e *ServerEvents, code C, opts ...ServerEventOption) {
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
    if existing, ok := e.entries[entry.code]; ok {
        panic(fmt.Sprintf("ServerEvents: duplicate registration for code %d (%s and %s)",
            entry.code, existing.enumName, entry.enumName))
    }
    e.entries[entry.code] = entry
}

// Build marshals msg, validates it matches the registered type for code, and
// returns a channel-0x00 wire frame. Panics if the code wasn't registered or
// if the payload type doesn't match. Use when broadcasting a single frame to
// many connections.
func (e *ServerEvents) Build(code uint32, msg proto.Message) []byte {
    entry, ok := e.entries[code]
    if !ok {
        panic(fmt.Sprintf("ServerEvents: code %d not registered — call RegisterServerEvent first", code))
    }
    if got := reflect.TypeOf(msg); got != entry.protoType {
        panic(fmt.Sprintf("ServerEvents: code %d (%s) registered as %v, but Build called with %v",
            code, entry.enumName, entry.protoType, got))
    }
    return MakeEvent(code, msg)
}

// Send builds the frame and writes it to the given connection. Convenience
// wrapper for the common single-recipient case.
func (e *ServerEvents) Send(connMgr *net.ConnManager, connID uint32, code uint32, msg proto.Message) {
    frame := e.Build(code, msg)
    if frame != nil {
        connMgr.SendReliable(connID, frame)
    }
}

// Schema returns the registered events as a deterministically-ordered slice
// (sorted by code) for schema export.
func (e *ServerEvents) Schema() []ServerEventSchema {
    out := make([]ServerEventSchema, 0, len(e.entries))
    for _, entry := range e.entries {
        out = append(out, ServerEventSchema{
            Code:      entry.code,
            Name:      entry.name,
            ProtoName: entry.protoName,
        })
    }
    sort.Slice(out, func(i, j int) bool { return out[i].Code < out[j].Code })
    return out
}

// enumConstantName returns the proto enum constant name (e.g. "SE_PLAYER_SPAWNED")
// for a typed enum value. Falls back to the numeric form if the type doesn't
// implement the proto enum interface.
func enumConstantName[C engine.EventCode](code C) string {
    type stringer interface{ String() string }
    if s, ok := any(code).(stringer); ok {
        return s.String()
    }
    return fmt.Sprintf("%d", code)
}
```

- [ ] **Step 4: Run tests, verify pass**

```bash
go test ./pkg/mmokit/ -run TestServerEvents -v
```

Expected: PASS for all 4 tests.

- [ ] **Step 5: Commit**

```bash
git add pkg/mmokit/server_events.go pkg/mmokit/server_events_test.go
git commit -m "feat(mmokit): add ServerEvents typed registry"
```

---

### Task 1.3: Wire `Protocol.ServerEvents` builder

**Files:**
- Modify: `pkg/mmokit/protocol.go`
- Test: `pkg/mmokit/protocol_test.go`

Add `Protocol.serverEventsRegistry *ServerEvents` field. Add `Protocol.ServerEvents(fn func(*ServerEvents)) *Protocol` builder method. `Protocol.Schema()` pulls server events from the registry when set, otherwise falls through to the existing manual `serverEvents` slice (for tests that haven't migrated yet).

- [ ] **Step 1: Write failing test**

```go
// pkg/mmokit/protocol_test.go — append
func TestProtocolServerEventsBuilder(t *testing.T) {
    p := NewProtocol("game").
        ServerEvents(func(e *ServerEvents) {
            RegisterServerEvent[enginepb.SpawnedMsg](e, enginepb.ServerEventCode_SE_PLAYER_SPAWNED)
        })
    schema := p.Schema()
    found := false
    for _, ev := range schema.ServerEvents {
        if ev.Code == uint32(enginepb.ServerEventCode_SE_PLAYER_SPAWNED) {
            if ev.Name != "playerSpawned" || ev.ProtoName != "enginepb.SpawnedMsg" {
                t.Errorf("wrong server event metadata: %+v", ev)
            }
            found = true
        }
    }
    if !found {
        t.Fatal("SE_PLAYER_SPAWNED not present in schema")
    }
}
```

- [ ] **Step 2: Run test, verify fail**

```bash
go test ./pkg/mmokit/ -run TestProtocolServerEventsBuilder -v
```

Expected: `p.ServerEvents undefined`.

- [ ] **Step 3: Modify `pkg/mmokit/protocol.go`**

Add field to the `Protocol` struct:

```go
type Protocol struct {
    game             string
    clientEvents     []engine.ClientEventSchema
    serverEvents     []ServerEventSchema
    operations       []OperationSchema
    entityNames      []entityNameEntry
    router           *engine.InputRouter
    replicators      *system.ReplicatorRegistry
    clientRenderMode ClientRenderMode

    // NEW
    serverEventsRegistry *ServerEvents
}
```

Add the builder method (place after `SetClientRenderMode`):

```go
// ServerEvents declares the server→client events for this protocol.
// The callback receives a fresh registry; register every event your game
// emits via RegisterServerEvent[T]. Returns the protocol for chaining.
func (p *Protocol) ServerEvents(fn func(*ServerEvents)) *Protocol {
    if p.serverEventsRegistry == nil {
        p.serverEventsRegistry = NewServerEvents()
    }
    fn(p.serverEventsRegistry)
    return p
}

// ServerEventsRegistry returns the underlying registry — used by the engine
// at runtime emit time. Games normally don't call this directly.
func (p *Protocol) ServerEventsRegistry() *ServerEvents {
    return p.serverEventsRegistry
}
```

Modify `Schema()` to pull from the registry when present:

```go
func (p *Protocol) Schema() ProtocolSchema {
    mode := p.clientRenderMode
    if mode == "" {
        mode = ClientRenderSnap
    }
    serverEvents := p.serverEvents
    if p.serverEventsRegistry != nil {
        // Registry takes precedence; manual entries appended for legacy callers
        // until everything migrates (then this branch becomes the only path).
        serverEvents = append(p.serverEventsRegistry.Schema(), p.serverEvents...)
    }
    ps := ProtocolSchema{
        Game:             p.game,
        ClientEvents:     p.clientEvents,
        ServerEvents:     serverEvents,
        Operations:       p.operations,
        ClientRenderMode: mode,
    }
    if p.router != nil {
        ps.ClientEvents = append(ps.ClientEvents, p.router.Schema()...)
    }
    if p.replicators != nil {
        ps.Entities = p.replicators.Schema()
        for i := range ps.Entities {
            for _, en := range p.entityNames {
                if ps.Entities[i].Kind == en.kind {
                    ps.Entities[i].Name = en.name
                }
            }
        }
    }
    return ps
}
```

- [ ] **Step 4: Run test, verify pass + full mmokit suite still passes**

```bash
go test ./pkg/mmokit/ -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/mmokit/protocol.go pkg/mmokit/protocol_test.go
git commit -m "feat(mmokit): add Protocol.ServerEvents builder"
```

---

### Task 1.4: Add `Config.Protocol` field + Process accessors

**Files:**
- Modify: `pkg/universe/coordinator.go` (Config struct + Process accessors)
- Modify: `pkg/universe/world_base.go` (passthrough accessor for game code)

Add `Config.Protocol *Protocol` field. Expose `Process.Protocol()` and `Process.ServerEvents()` accessors. Add `WorldBase.ServerEvents()` so game code can reach the registry from inside any system.

Note: `Protocol` lives in `pkg/mmokit` and `pkg/universe.Config` lives in `pkg/universe`. To avoid an import cycle, declare `Config.Protocol` as `any` in `pkg/universe` and have `pkg/mmokit` provide a typed wrapper. Verify the existing import direction first.

- [ ] **Step 1: Verify import direction**

```bash
go list -deps github.com/zenion/mmokit/pkg/mmokit 2>&1 | grep universe
go list -deps github.com/zenion/mmokit/pkg/universe 2>&1 | grep mmokit
```

Expected: `pkg/mmokit` imports `pkg/universe`, NOT vice versa. So we cannot reference `*mmokit.Protocol` from `pkg/universe`.

- [ ] **Step 2: Add `Config.Protocol any` field in `pkg/universe/coordinator.go`**

Find the `Config` struct (search `type Config struct`) and add:

```go
// Protocol holds the game's *mmokit.Protocol declaration — declared as any
// to avoid an import cycle (pkg/mmokit imports pkg/universe). The
// pkg/mmokit layer asserts back to *mmokit.Protocol via accessors.
Protocol any
```

- [ ] **Step 3: Add Process accessors in `pkg/universe/coordinator.go`**

Place after the `func New` definition:

```go
// Protocol returns the user-supplied Config.Protocol unchanged. Callers in
// pkg/mmokit type-assert to *mmokit.Protocol.
func (c *Process) Protocol() any {
    return c.cfg.Protocol
}
```

- [ ] **Step 4: Add typed accessors in `pkg/mmokit/mmokit.go`**

Place near other Process accessor wrappers (search for an existing wrapper pattern):

```go
// Protocol returns the *Protocol from Config.Protocol, or nil if unset.
func ProtocolOf(c *Process) *Protocol {
    if p, ok := c.Protocol().(*Protocol); ok {
        return p
    }
    return nil
}

// ServerEventsOf returns the registry from cfg.Protocol, or nil if unset.
func ServerEventsOf(c *Process) *ServerEvents {
    if p := ProtocolOf(c); p != nil {
        return p.ServerEventsRegistry()
    }
    return nil
}
```

- [ ] **Step 5: Add `WorldBase.ServerEvents()` passthrough in `pkg/universe/world_base.go`**

Find the existing pattern of WorldBase accessors that delegate to coord/process state. WorldBase already holds a back-reference to its owning Process (search for `process *Process` or similar). Add:

```go
// Protocol returns the user-supplied Config.Protocol via the owning Process.
// Game code retrieves the typed *mmokit.Protocol via mmokit.ProtocolOf.
func (b *WorldBase) Protocol() any {
    if b.process == nil {
        return nil
    }
    return b.process.Protocol()
}
```

If `WorldBase` lacks a back-reference to Process, add one as part of this step — wire it during cell construction in `pkg/universe/coordinator.go` (search for `Base = ...` or `WorldBase{...}` literals during Cell creation).

- [ ] **Step 6: Build + run existing tests**

```bash
go vet ./...
go test ./pkg/mmokit/ ./pkg/universe/ -count=1
```

Expected: PASS — no behavior change yet.

- [ ] **Step 7: Commit**

```bash
git add pkg/mmokit/mmokit.go pkg/universe/coordinator.go pkg/universe/world_base.go
git commit -m "feat(universe): add Config.Protocol field and Process accessors"
```

---

### Task 1.5: Migrate cmd/server (space game) — declare Protocol

**Files:**
- Modify: `cmd/server/main.go`
- Modify: `cmd/server/schema.go` (slim, not yet deleted)

Declare every server event the space game emits in a single `cfg.Protocol = mmokit.NewProtocol(...).ServerEvents(...)` block. Slim `dumpProtocolSchema` to no longer hand-list server events (it pulls from the registry now via `Protocol.Schema()`).

- [ ] **Step 1: Add Protocol declaration in `cmd/server/main.go`**

In `main()`, after the `coordCfg := mmokit.Config{...}` literal but before `coordCfg.BindFlags()`, add:

```go
coordCfg.Protocol = mmokit.NewProtocol("space").
    ServerEvents(func(e *mmokit.ServerEvents) {
        // Engine events
        mmokit.RegisterServerEvent[enginepb.SpawnedMsg](e,
            enginepb.ServerEventCode_SE_PLAYER_SPAWNED, mmokit.WithEventName("playerSpawned"))
        mmokit.RegisterServerEvent[gamepb.WorldUpdateMsg](e,
            enginepb.ServerEventCode_SE_WORLD_UPDATE, mmokit.WithEventName("worldUpdate"))
        mmokit.RegisterServerEvent[enginepb.PongMsg](e,
            enginepb.ServerEventCode_SE_PONG)
        mmokit.RegisterServerEvent[enginepb.PlayerDiedMsg](e,
            enginepb.ServerEventCode_SE_PLAYER_DIED)
        mmokit.RegisterServerEvent[enginepb.LoginRejectedMsg](e,
            enginepb.ServerEventCode_SE_LOGIN_REJECTED)
        mmokit.RegisterServerEvent[gamepb.PlayerOwnStateMsg](e,
            enginepb.ServerEventCode_SE_PLAYER_OWN_STATE)
        mmokit.RegisterServerEvent[enginepb.CellChangeMsg](e,
            enginepb.ServerEventCode_SE_CELL_CHANGE)
        mmokit.RegisterServerEvent[enginepb.CellTopologyMsg](e,
            enginepb.ServerEventCode_SE_CELL_TOPOLOGY)

        // Game events
        mmokit.RegisterServerEvent[gamepb.BankContentsMsg](e,
            gamepb.GameServerEventCode_GSE_BANK_CONTENTS)
        mmokit.RegisterServerEvent[gamepb.TransferResultMsg](e,
            gamepb.GameServerEventCode_GSE_TRANSFER_RESULT)
        mmokit.RegisterServerEvent[gamepb.EquipResultMsg](e,
            gamepb.GameServerEventCode_GSE_EQUIP_RESULT)
        mmokit.RegisterServerEvent[gamepb.DockingStateMsg](e,
            gamepb.GameServerEventCode_GSE_DOCKING_STATE)
        mmokit.RegisterServerEvent[gamepb.DockedMsg](e,
            gamepb.GameServerEventCode_GSE_DOCKED)
        mmokit.RegisterServerEvent[gamepb.MapDataMsg](e,
            gamepb.GameServerEventCode_GSE_MAP_DATA)
        mmokit.RegisterServerEvent[gamepb.CurrencyUpdateMsg](e,
            gamepb.GameServerEventCode_GSE_CURRENCY_UPDATE)
    })
```

Note: `SE_DELTA_WORLD_UPDATE` and `SE_PLAYER_SPAWNED` use the *override* `WithEventName` to keep the historical SDK names (some have nuanced naming the auto-derivation matches; the explicit override here documents that the schema dump verified these). `WorldUpdateMsg` registers under `SE_WORLD_UPDATE`. The binary `SE_DELTA_WORLD_UPDATE` is **not** registered here — it has no proto type and is sent via `MakeEventRaw`; it stays in the manual schema list.

- [ ] **Step 2: Slim `cmd/server/schema.go`**

Remove every `mmokit.ServerEvent(proto, ...)` line that's now in the registry — that's all of them EXCEPT `SE_DELTA_WORLD_UPDATE` (binary event, no proto type, kept in manual list). Keep client events and operations untouched (Phase 2 handles ops; client events stay manual until Phase 3).

The diff to `cmd/server/schema.go`: delete the comment block "--- Engine server → client events ---" through the line registering `SE_DELTA_WORLD_UPDATE`, then re-add only the binary event:

```go
// Binary-encoded server events (no proto type; not in ServerEvents registry)
mmokit.ServerEvent(proto, enginepb.ServerEventCode_SE_DELTA_WORLD_UPDATE, "deltaWorldUpdate", "")
```

Also delete the "--- Game server → client events ---" block entirely.

- [ ] **Step 3: Build + verify schema dump output unchanged**

```bash
go build -o /tmp/server-pre ./cmd/server
git stash
go build -o /tmp/server-base ./cmd/server
/tmp/server-base --dump-schema > /tmp/schema-base.json
git stash pop
go build -o /tmp/server-after ./cmd/server
/tmp/server-after --dump-schema > /tmp/schema-after.json
diff <(jq -S '.serverEvents | sort_by(.code)' /tmp/schema-base.json) \
     <(jq -S '.serverEvents | sort_by(.code)' /tmp/schema-after.json)
```

Expected: empty diff. The serverEvents arrays match (after sorting).

If diff appears, it's almost certainly a name mismatch — find the missing `WithEventName(...)` override and add it.

- [ ] **Step 4: Commit**

```bash
git add cmd/server/main.go cmd/server/schema.go
git commit -m "feat(server): declare server events via Protocol registry"
```

---

### Task 1.6: Migrate cmd/server `MakeEvent` call sites

**Files (all in `internal/game/` + `cmd/server/`):**
- Modify: `internal/game/lifecycle.go`
- Modify: `internal/game/system_network.go`
- Modify: `internal/game/system_economy.go`
- Modify: `internal/game/system_equipment.go`
- Modify: `internal/game/system_docking.go`
- Modify: `internal/game/entity_ship.go`
- Modify: `internal/game/combat_helpers.go`
- Modify: `internal/game/commands/currency.go`
- Modify: `internal/game/game.go`

Replace every `data := mmokit.MakeEvent(uint32(code), msg); ConnMgr.SendReliable(connID, data)` with `gw.ServerEvents().Send(ConnMgr, connID, uint32(code), msg)`. Where the same frame is broadcast to many connections, use `gw.ServerEvents().Build(uint32(code), msg)` once and `SendReliable` per recipient.

`gw.ServerEvents()` is a thin helper added to `*GameWorld` that does `mmokit.ServerEventsOf(gw.process)` (or equivalent) — add it to `internal/game/game.go` once, use it from every site.

- [ ] **Step 1: Add `ServerEvents()` helper to `*GameWorld`**

In `internal/game/game.go`, add a method:

```go
// ServerEvents returns the typed server-event registry declared in main.go's
// cfg.Protocol. Used by every site that emits a server event.
func (gw *GameWorld) ServerEvents() *mmokit.ServerEvents {
    return mmokit.ServerEventsOf(gw.process)
}
```

If `*GameWorld` doesn't already hold a back-reference to `*mmokit.Process`, plumb one through during construction. Search for where `GameWorld` is constructed (`NewGameWorld` or via `coord.SetWorld(...)`) — the factory receives `*WorldBase`, and `WorldBase.process` (added in Task 1.4 step 5) is the back-reference.

If easier: have `gw.ServerEvents()` call `mmokit.ServerEventsOf(gw.WorldBase.process)` directly, where `gw.WorldBase` is the embedded base.

- [ ] **Step 2: Convert call sites file-by-file**

For each file in the list, do a find-and-replace from the `MakeEvent` pattern to `Send`/`Build`. Conversion table:

| Before | After |
|--------|-------|
| `data := mmokit.MakeEvent(uint32(CODE), msg)`<br>`if data != nil { gw.eng.ConnMgr.SendReliable(connID, data) }` | `gw.ServerEvents().Send(gw.eng.ConnMgr, connID, uint32(CODE), msg)` |
| `frame := mmokit.MakeEvent(uint32(CODE), msg)`<br>`for ... { ConnMgr.SendReliable(c.ConnID, frame) }` | `frame := gw.ServerEvents().Build(uint32(CODE), msg)`<br>`for ... { ConnMgr.SendReliable(c.ConnID, frame) }` |

Apply to every site in:

- `internal/game/lifecycle.go` — 3 sites
- `internal/game/system_network.go` — 4 sites
- `internal/game/system_economy.go` — 2 sites
- `internal/game/system_equipment.go` — 1 site
- `internal/game/system_docking.go` — 1 site
- `internal/game/entity_ship.go` — 6 sites
- `internal/game/combat_helpers.go` — 2 sites
- `internal/game/commands/currency.go` — 1 site
- `internal/game/game.go` — 1 site

Cross-check with `grep -rn "mmokit.MakeEvent\b" internal/` — count must match (and end at zero remaining occurrences).

- [ ] **Step 3: Verify the package builds and tests pass**

```bash
go vet ./internal/...
go test ./internal/... -count=1
```

Expected: PASS. Compilation catches any miss.

- [ ] **Step 4: End-to-end smoke**

Start the server with the game, open the web client, log in, move, dock at a station, deposit currency. Each of those exercises a different `Send` site. Watch the console for any `ServerEvents:` panic — that means a code is being emitted that wasn't registered.

```bash
just dev
# In a browser: http://localhost:8080 — log in as test user, walk through actions
```

Expected: no panics, normal gameplay.

- [ ] **Step 5: Commit**

```bash
git add internal/
git commit -m "refactor(game): emit server events via ServerEvents.Send"
```

---

### Task 1.7: Migrate 4node-basic — declare Protocol + convert MakeEvent

**Files:**
- Modify: `examples/4node-basic/main.go`
- Modify: `examples/4node-basic/world.go`
- Modify: `examples/4node-basic/schema.go` (slim)

- [ ] **Step 1: Add Protocol declaration in `examples/4node-basic/main.go`**

After the `cfg := mmokit.Config{...}` literal:

```go
cfg.Protocol = mmokit.NewProtocol("basic").
    ServerEvents(func(e *mmokit.ServerEvents) {
        mmokit.RegisterServerEvent[enginepb.SpawnedMsg](e,
            enginepb.ServerEventCode_SE_PLAYER_SPAWNED, mmokit.WithEventName("playerSpawned"))
        mmokit.RegisterServerEvent[enginepb.CellTopologyMsg](e,
            enginepb.ServerEventCode_SE_CELL_TOPOLOGY)
    })
```

Add `enginepb "github.com/zenion/mmokit/gen/go/enginepb"` to imports if not already present.

- [ ] **Step 2: Convert MakeEvent in `examples/4node-basic/world.go`**

Search `mmokit.MakeEvent` in `examples/4node-basic/`. There's one site at `world.go:119`. Replace using the same pattern as Task 1.6.

- [ ] **Step 3: Slim `examples/4node-basic/schema.go`**

Remove the two `mmokit.ServerEvent(...)` lines that registered `SE_PLAYER_SPAWNED` and `SE_CELL_TOPOLOGY`. Keep `SE_DELTA_WORLD_UPDATE` (binary, no proto type).

- [ ] **Step 4: Verify schema unchanged + smoke test**

```bash
git stash
go run ./examples/4node-basic --dump-schema > /tmp/schema-4node-base.json
git stash pop
go run ./examples/4node-basic --dump-schema > /tmp/schema-4node-after.json
diff <(jq -S '.serverEvents | sort_by(.code)' /tmp/schema-4node-base.json) \
     <(jq -S '.serverEvents | sort_by(.code)' /tmp/schema-4node-after.json)
```

Expected: empty diff. Then:

```bash
cd examples/4node-basic && just dev
# Browser: http://localhost:8080 — log in, move around
```

Expected: no panics, normal play.

- [ ] **Step 5: Commit**

```bash
git add examples/4node-basic/main.go examples/4node-basic/world.go examples/4node-basic/schema.go
git commit -m "refactor(4node-basic): declare Protocol + use ServerEvents.Send"
```

---

### Task 1.8: Migrate slither — declare Protocol + convert MakeEvent

**Files:**
- Modify: `examples/slither/main.go`
- Modify: `examples/slither/replication.go`

Slither has no `schema.go` (no `--dump-schema` support today). We still declare `cfg.Protocol` so emit sites can use the typed registry consistently across all three games.

- [ ] **Step 1: Add Protocol declaration in `examples/slither/main.go`**

After the existing `cfg := mmokit.Config{...}`:

```go
cfg.Protocol = mmokit.NewProtocol("slither").
    ServerEvents(func(e *mmokit.ServerEvents) {
        mmokit.RegisterServerEvent[slitherpb.LeaderboardMsg](e,
            slitherpb.SlitherServerEventCode_SSE_LEADERBOARD)
        mmokit.RegisterServerEvent[slitherpb.KillFeedMsg](e,
            slitherpb.SlitherServerEventCode_SSE_KILL_FEED)
    })
```

Verify the actual proto message types match what `replication.go:200,214` use. If types differ, adjust.

- [ ] **Step 2: Convert MakeEvent in `examples/slither/replication.go`**

Two sites use `MakeEvent` (lines 200, 214). One uses `MakeEventRaw` (line 231) — keep that one, it's a binary event. The two `MakeEvent` calls become `gw.ServerEvents().Build(...)` (they look like they construct frames for broadcast — verify the surrounding code).

- [ ] **Step 3: Smoke test**

```bash
cd examples/slither && just dev
# Browser: open the slither URL, play briefly, watch the leaderboard
```

Expected: leaderboard updates, kill feed renders.

- [ ] **Step 4: Commit**

```bash
git add examples/slither/main.go examples/slither/replication.go
git commit -m "refactor(slither): declare Protocol + use ServerEvents.Send"
```

---

### Task 1.9: Phase 1 cleanup — confirm no `MakeEvent` callers outside engine internals

**Files:** none (verification only)

- [ ] **Step 1: Verify**

```bash
grep -rn "mmokit\.MakeEvent\b" --include="*.go" . | grep -v "_test.go" | grep -v "^./pkg/"
```

Expected: zero results outside `./pkg/`. The remaining `MakeEvent` references in `pkg/` are the engine's own internal frame construction (e.g., `MakeEventRaw` for the binary delta channel) — those stay.

- [ ] **Step 2: Tag Phase 1 complete**

```bash
git tag phase1-protocol-unification
```

(Local tag, no push needed — just a marker.)

---

## Phase 2: Operations schema + typed Register

### Task 2.1: Typed `Register[Req, Res]` + `Schema()` in `pkg/ops`

**Files:**
- Modify: `pkg/ops/router.go`
- Test: `pkg/ops/router_test.go` (create if missing)

Add a generic `Register[Req, Res]` that captures both proto types, builds the unmarshal+marshal wrapper, and stores schema metadata on the router. Add `Router.Schema() []OperationSchema`.

`OperationSchema` lives in `pkg/mmokit/protocol.go` today — to avoid `pkg/ops` importing `pkg/mmokit`, define a parallel struct in `pkg/ops` (`ops.OperationSchema`) with identical shape; `pkg/mmokit` re-exports/aliases.

- [ ] **Step 1: Write failing test**

```go
// pkg/ops/router_test.go
package ops

import (
    "context"
    "testing"

    "google.golang.org/protobuf/proto"

    gamepb "github.com/zenion/mmokit/gen/go/gamepb"
    "github.com/zenion/mmokit/pkg/net"
)

func TestRouterTypedRegisterAndSchema(t *testing.T) {
    r := NewRouter(net.NewConnManager(), NewPlayerSessions(), 1, dummyParser, dummyFrameBuilder)
    Register[gamepb.MarketBrowseRequest, gamepb.MarketOrderBookResponse](
        r, uint32(gamepb.OperationCode_OP_MARKET_BROWSE), "marketBrowse",
        func(ctx *OpContext, req *gamepb.MarketBrowseRequest) (*gamepb.MarketOrderBookResponse, error) {
            return &gamepb.MarketOrderBookResponse{ItemId: req.ItemId}, nil
        })

    schema := r.Schema()
    if len(schema) != 1 {
        t.Fatalf("Schema() returned %d entries, want 1", len(schema))
    }
    s := schema[0]
    if s.Code != uint32(gamepb.OperationCode_OP_MARKET_BROWSE) {
        t.Errorf("Code = %d, want %d", s.Code, gamepb.OperationCode_OP_MARKET_BROWSE)
    }
    if s.Name != "marketBrowse" {
        t.Errorf("Name = %q, want %q", s.Name, "marketBrowse")
    }
    if s.RequestProto != "gamepb.MarketBrowseRequest" {
        t.Errorf("RequestProto = %q", s.RequestProto)
    }
    if s.ResponseProto != "gamepb.MarketOrderBookResponse" {
        t.Errorf("ResponseProto = %q", s.ResponseProto)
    }

    // Verify untyped Register still works (back-compat with current handler.go).
    r.Register(uint32(gamepb.OperationCode_OP_MARKET_CANCEL_ORDER), func(ctx *OpContext, payload []byte) ([]byte, error) {
        return nil, nil
    })
    if got := len(r.Schema()); got != 1 {
        t.Errorf("untyped Register should not appear in Schema(); got %d entries", got)
    }
    _ = context.Background()
    _ = proto.Marshal
}

func dummyParser(raw []byte) (ParsedRequest, error)              { return ParsedRequest{}, nil }
func dummyFrameBuilder(_, _ uint32, _ int32, _ string, _ []byte) []byte { return nil }
```

- [ ] **Step 2: Run test, verify fail**

```bash
go test ./pkg/ops/ -run TestRouterTypedRegisterAndSchema -v
```

Expected: `undefined: Register` and `undefined: r.Schema`.

- [ ] **Step 3: Implement in `pkg/ops/router.go`**

Add types:

```go
// OperationSchema describes one request/response operation for schema export.
type OperationSchema struct {
    Code          uint32 `json:"code"`
    Name          string `json:"name"`
    RequestProto  string `json:"requestProto"`
    ResponseProto string `json:"responseProto"`
}

// EventCode is any integer type usable as a message code (proto enums are int32).
type EventCode interface{ ~int32 | ~uint32 }
```

Add `schemas` field to `Router`:

```go
type Router struct {
    handlers   map[uint32]OperationHandler
    schemas    map[uint32]OperationSchema
    connMgr    *net.ConnManager
    sessions   *PlayerSessions
    workers    int
    reqCh      chan routedRequest
    parser     RequestParser
    buildFrame ResponseFrameBuilder
}
```

Update `NewRouter` to initialize `schemas: make(map[uint32]OperationSchema)`.

Add the typed Register generic:

```go
// Register registers a typed operation handler. Captures request and response
// proto names for schema export. Panics on duplicate code.
func Register[Req any, ReqP interface {
    *Req
    proto.Message
}, Res any, ResP interface {
    *Res
    proto.Message
}, C EventCode](r *Router, code C, name string,
    handler func(ctx *OpContext, req ReqP) (ResP, error)) {

    var reqZero ReqP = new(Req)
    var resZero ResP = new(Res)
    opCode := uint32(code)

    if _, exists := r.handlers[opCode]; exists {
        panic(fmt.Sprintf("ops.Router: duplicate handler for code %d", opCode))
    }

    r.handlers[opCode] = func(ctx *OpContext, payload []byte) ([]byte, error) {
        var req ReqP = new(Req)
        if err := proto.Unmarshal(payload, req); err != nil {
            return nil, fmt.Errorf("op %d: unmarshal request: %w", opCode, err)
        }
        resp, err := handler(ctx, req)
        if err != nil {
            return nil, err
        }
        return proto.Marshal(resp)
    }
    r.schemas[opCode] = OperationSchema{
        Code:          opCode,
        Name:          name,
        RequestProto:  string(proto.MessageName(reqZero)),
        ResponseProto: string(proto.MessageName(resZero)),
    }
}

// Schema returns operation metadata in code order.
func (r *Router) Schema() []OperationSchema {
    out := make([]OperationSchema, 0, len(r.schemas))
    for _, s := range r.schemas {
        out = append(out, s)
    }
    sort.Slice(out, func(i, j int) bool { return out[i].Code < out[j].Code })
    return out
}
```

Add `import "google.golang.org/protobuf/proto"`, `"fmt"`, `"sort"` if not already present.

- [ ] **Step 4: Run test, verify pass**

```bash
go test ./pkg/ops/ -run TestRouterTypedRegisterAndSchema -v
```

Expected: PASS.

- [ ] **Step 5: Re-export from `pkg/mmokit/mmokit.go`**

Find where `OpRouter`, `NewOpRouter` are re-exported and add:

```go
// RegisterOp is the typed wrapper around ops.Router.Register that captures
// proto request/response types for schema export. Prefer this over the
// untyped Register for any op that participates in the SDK.
func RegisterOp[Req any, ReqP interface {
    *Req
    proto.Message
}, Res any, ResP interface {
    *Res
    proto.Message
}, C ops.EventCode](r *ops.Router, code C, name string,
    handler func(ctx *ops.OpContext, req ReqP) (ResP, error)) {
    ops.Register[Req, ReqP, Res, ResP, C](r, code, name, handler)
}
```

(Adjust signature to whatever the actual generic constraint is — the wrapper is mechanical.)

- [ ] **Step 6: Commit**

```bash
git add pkg/ops/router.go pkg/ops/router_test.go pkg/mmokit/mmokit.go
git commit -m "feat(ops): add typed Register and Schema()"
```

---

### Task 2.2: Migrate marketplace handlers to typed Register

**Files:**
- Modify: `internal/marketplace/handler.go`

Convert each of the 5 `router.Register(uint32(code), func(...) (..., error) {...})` calls to `mmokit.RegisterOp[Req, Res](router, code, name, func(...) (...) {...})`.

- [ ] **Step 1: Convert each handler**

For each operation:

| Code | Name | Request | Response |
|------|------|---------|----------|
| `OP_MARKET_BROWSE` | `"marketBrowse"` | `MarketBrowseRequest` | `MarketOrderBookResponse` |
| `OP_MARKET_CREATE_ORDER` | `"marketCreateOrder"` | `MarketCreateOrderRequest` | `MarketOrderResultResponse` |
| `OP_MARKET_CANCEL_ORDER` | `"marketCancelOrder"` | `MarketCancelOrderRequest` | `MarketOrderResultResponse` |
| `OP_MARKET_MY_ORDERS` | `"marketMyOrders"` | `MarketMyOrdersRequest` | `MarketMyOrdersResponse` |
| `OP_MARKET_INSTANT_TRADE` | `"marketInstantTrade"` | `MarketInstantTradeRequest` | `MarketOrderResultResponse` |

Refactor pattern (showing one):

Before:
```go
router.Register(uint32(gamepb.OperationCode_OP_MARKET_BROWSE), func(ctx *mmokit.OpContext, payload []byte) ([]byte, error) {
    var req gamepb.MarketBrowseRequest
    if err := proto.Unmarshal(payload, &req); err != nil {
        return nil, fmt.Errorf("invalid browse request: %w", err)
    }
    view := svc.Browse(stationID, req.ItemId)
    resp := &gamepb.MarketOrderBookResponse{...}
    return proto.Marshal(resp)
})
```

After:
```go
mmokit.RegisterOp[gamepb.MarketBrowseRequest, gamepb.MarketOrderBookResponse](
    router, gamepb.OperationCode_OP_MARKET_BROWSE, "marketBrowse",
    func(ctx *mmokit.OpContext, req *gamepb.MarketBrowseRequest) (*gamepb.MarketOrderBookResponse, error) {
        view := svc.Browse(stationID, req.ItemId)
        resp := &gamepb.MarketOrderBookResponse{...}
        return resp, nil
    })
```

The unmarshal+marshal boilerplate is gone. Drop the `proto` import if no other call needs it.

- [ ] **Step 2: Build + test**

```bash
go vet ./internal/marketplace/
go test ./internal/marketplace/ -count=1
```

Expected: PASS.

- [ ] **Step 3: Smoke test**

```bash
just dev
# Browser: log in, dock at a station, browse market, place a buy order, cancel it
```

Expected: marketplace UI works end-to-end.

- [ ] **Step 4: Commit**

```bash
git add internal/marketplace/handler.go
git commit -m "refactor(marketplace): use typed RegisterOp"
```

---

### Task 2.3: Drop hand-listed operations from `cmd/server/schema.go`

**Files:**
- Modify: `cmd/server/schema.go`

`Protocol.Schema()` will pull operations from the router (Task 3 wires this). For now, just delete the hand-listed `mmokit.Operation(...)` block — the schema dump output's operations array becomes empty until Task 3.2 plumbs the router in. That's acceptable as an intermediate state because the SDK regen isn't gated on schema completeness during refactor.

If the team wants the operations array to remain populated through this intermediate state, *defer Task 2.3 until Task 3.2 lands* and treat them as a single commit. Recommended: defer.

- [ ] **Step 1: Decide whether to defer**

Recommended: skip this task and roll the deletion into Task 3.3.

If proceeding: delete the `--- Marketplace operations ---` block in `cmd/server/schema.go` (lines registering `OP_MARKET_*`).

- [ ] **Step 2 (if proceeded): Commit**

```bash
git add cmd/server/schema.go
git commit -m "refactor(server): drop hand-listed operations from schema dump"
```

---

## Phase 3: Engine-owned `--dump-schema` + delete game schema files

### Task 3.1: Plumb router accessors through Process

**Files:**
- Modify: `pkg/universe/coordinator.go` (Process accessors for InputRouter + OpRouter)
- Modify: `pkg/mmokit/mmokit.go` (typed accessors)

The InputRouter is per-cell (created in `inputSystem.Init()`). For schema export, we read from any single cell — they're identical across cells of the same world. The OpRouter is process-scoped (in `cmd/server`, lives in main.go's local var).

For the OpRouter to be reachable from the engine's schema dumper, it has to live on the Process. Add `Config.OpRouter *ops.Router` (or have games hand it to `Process` via a setter post-construction). Existing `cmd/server/main.go` constructs the OpRouter locally; we plumb it onto `coordCfg`.

- [ ] **Step 1: Add `Config.OpRouter` field**

In `pkg/universe/coordinator.go`:

```go
// OpRouter, when set, is exposed via Process for schema export. Optional —
// games without operations leave this nil.
OpRouter *ops.Router
```

Add the import: `"github.com/zenion/mmokit/pkg/ops"` (verify no cycle — `pkg/universe` likely doesn't import `pkg/ops` today; if it does cycle, declare the field as `any` like `Config.Protocol`).

- [ ] **Step 2: Add Process accessors**

```go
// AnyInputRouter returns the InputRouter from the first cell that has one,
// or nil. Used by schema export — every cell's router has the same registered
// handlers, so the choice of cell is arbitrary.
func (c *Process) AnyInputRouter() *engine.InputRouter {
    for _, cell := range c.Cells {
        for _, sys := range cell.Engine.Systems() {
            if ir, ok := sys.(interface{ Router() *engine.InputRouter }); ok {
                if r := ir.Router(); r != nil {
                    return r
                }
            }
        }
    }
    return nil
}

// OpRouter returns the configured ops router or nil.
func (c *Process) OpRouter() *ops.Router {
    return c.cfg.OpRouter
}
```

For `AnyInputRouter` to work, the input-system implementation needs a `Router() *engine.InputRouter` method. Add it in `pkg/mmokit/mmokit.go`:

```go
// Router exposes the InputRouter for schema introspection. Implementation of
// the unexported `interface{ Router() *engine.InputRouter }` used by
// Process.AnyInputRouter.
func (s *inputSystem[W]) Router() *engine.InputRouter { return s.router }
```

If `Engine.Systems()` doesn't exist yet, add it as an accessor returning the slice — or alternatively iterate `cell.World.Systems()` if that exists. Verify the right traversal route in the existing code.

- [ ] **Step 3: Update `cmd/server/main.go` to set `coordCfg.OpRouter`**

Find where `opRouter = mmokit.NewOpRouter(...)` is called. Immediately after, set:

```go
coordCfg.OpRouter = opRouter
```

(Field assignment after coordCfg is already constructed — make sure this happens before `coord := mmokit.New(coordCfg)`.)

- [ ] **Step 4: Build + verify**

```bash
go vet ./...
go test ./pkg/universe/ -count=1
```

Expected: PASS — accessors are additive, no behavior change.

- [ ] **Step 5: Commit**

```bash
git add pkg/universe/coordinator.go pkg/mmokit/mmokit.go cmd/server/main.go
git commit -m "feat(universe): expose InputRouter and OpRouter for schema export"
```

---

### Task 3.2: Engine intercepts `--dump-schema`

**Files:**
- Modify: `pkg/universe/bootstrap.go` (register flag)
- Modify: `pkg/universe/coordinator.go` (Process.Start checks flag, dumps + exits)
- Modify: `pkg/mmokit/protocol.go` (assemble full schema from registries)

- [ ] **Step 1: Register `--dump-schema` flag in `BindFlags`**

In `pkg/universe/bootstrap.go`, add inside `BindFlags()` (after the existing `flag.BoolVar(&c.Headless, ...)`):

```go
flag.BoolVar(&c.DumpSchema, "dump-schema", false,
    "dump protocol schema JSON to stdout and exit (after Build, before Start)")
```

Add `DumpSchema bool` to the `Config` struct.

- [ ] **Step 2: Intercept in `Process.Start`**

In `pkg/universe/coordinator.go`, modify `Start`:

```go
func (c *Process) Start(ctx context.Context) {
    c.Build()

    if c.cfg.DumpSchema {
        c.dumpSchemaAndExit()
        return // unreachable
    }

    c.startHTTPListener()
    // ... rest unchanged
}
```

Add the dumper:

```go
func (c *Process) dumpSchemaAndExit() {
    p, ok := c.cfg.Protocol.(interface {
        AssembleFromProcess(*Process)
        WriteSchema(io.Writer) error
    })
    if !ok {
        fmt.Fprintln(os.Stderr, "dump-schema: Config.Protocol is nil or not a *mmokit.Protocol")
        os.Exit(1)
    }
    p.AssembleFromProcess(c)
    if err := p.WriteSchema(os.Stdout); err != nil {
        fmt.Fprintf(os.Stderr, "dump-schema: %v\n", err)
        os.Exit(1)
    }
    os.Exit(0)
}
```

The `interface{ ... }` shape exists to keep `pkg/universe` independent of `pkg/mmokit`. The actual `*mmokit.Protocol` will satisfy it after Task 3.2 step 3 adds `AssembleFromProcess`. Add `"io"`, `"fmt"`, `"os"` imports if missing.

- [ ] **Step 3: Add `AssembleFromProcess` on `*Protocol`**

In `pkg/mmokit/protocol.go`:

```go
// AssembleFromProcess hydrates the Protocol with runtime-discovered registries:
// client events from the process's InputRouter (any cell's router suffices —
// all cells in the same world register the same handlers), operations from
// the OpRouter, and entity replicators from any cell's EntityKindDefs.
//
// Called by the engine's --dump-schema path after Build() has populated
// every registry but before Start has begun the game loop.
func (p *Protocol) AssembleFromProcess(proc *universe.Process) {
    if r := proc.AnyInputRouter(); r != nil {
        p.SetRouter(r)
    }
    if op := proc.OpRouter(); op != nil {
        p.operations = append(p.operations, fromOpsSchemas(op.Schema())...)
    }
    // Entity replicators: build from the first cell's EntityKindDefs.
    for _, cell := range proc.Cells {
        defs := cell.Base.EntityKindDefs()
        if len(defs) == 0 {
            continue
        }
        defSlice := make([]universe.EntityKindDef, 0, len(defs))
        for _, def := range defs {
            defSlice = append(defSlice, *def)
            p.EntityName(def.Kind, def.Name)
        }
        // Construct replicators against a throwaway world (the dump path
        // doesn't run the game loop, so the registry just needs the schema).
        w := ecs.NewWorld()
        p.SetReplicators(BuildReplicators(w, nil, defSlice...))
        break
    }
}

func fromOpsSchemas(in []ops.OperationSchema) []OperationSchema {
    out := make([]OperationSchema, len(in))
    for i, s := range in {
        out[i] = OperationSchema(s)
    }
    return out
}
```

Add necessary imports. If `OperationSchema` shapes match exactly between `pkg/ops` and `pkg/mmokit`, the `OperationSchema(s)` cast works directly.

- [ ] **Step 4: Add `Process.Cells` accessor visibility**

`Cells` is already exported. `Cell.Base` is exported. Verify by grep:

```bash
grep -n "Cells.*map\|Cells \[\]" pkg/universe/coordinator.go | head
```

If not exported, expose a `Process.AllCells() []*Cell` accessor.

- [ ] **Step 5: Verify schema dump still works**

```bash
go run ./cmd/server --dump-schema | jq '.serverEvents | length'
go run ./cmd/server --dump-schema | jq '.operations | length'
go run ./cmd/server --dump-schema | jq '.clientEvents | length'
go run ./cmd/server --dump-schema | jq '.entities | length'
```

Expected: each count matches what the existing `dumpProtocolSchema` produced.

For comparison:

```bash
git stash
go run ./cmd/server --dump-schema > /tmp/schema-before.json
git stash pop
go run ./cmd/server --dump-schema > /tmp/schema-after.json
diff <(jq -S . /tmp/schema-before.json) <(jq -S . /tmp/schema-after.json)
```

Expected: empty diff (modulo any deliberate name overrides).

- [ ] **Step 6: Commit**

```bash
git add pkg/universe/bootstrap.go pkg/universe/coordinator.go pkg/mmokit/protocol.go
git commit -m "feat(engine): intercept --dump-schema, assemble from runtime registries"
```

---

### Task 3.3: Delete game `schema.go` files + `--dump-schema` flag in main.go

**Files:**
- Delete: `examples/4node-basic/schema.go`
- Delete: `cmd/server/schema.go`
- Modify: `examples/4node-basic/main.go`
- Modify: `cmd/server/main.go`

The engine now owns `--dump-schema`. Game-side flag handling is dead code.

- [ ] **Step 1: Delete schema files**

```bash
git rm examples/4node-basic/schema.go cmd/server/schema.go
```

- [ ] **Step 2: Remove `dump-schema` flag handling from `examples/4node-basic/main.go`**

Delete:
```go
dumpSchema := flag.Bool("dump-schema", false, "...")
```
And:
```go
if *dumpSchema {
    dumpProtocolSchema(cfg)
    return
}
```

- [ ] **Step 3: Remove `dump-schema` flag handling from `cmd/server/main.go`**

Delete the equivalent `flag.Bool("dump-schema", ...)` and the `if *dumpSchema { dumpProtocolSchema(...); return }` block.

- [ ] **Step 4: Verify SDK regen still works**

```bash
just client-sdk examples/4node-basic
just space-sdk
```

Expected: both run, produce TS output. Diff against the previous SDK output:

```bash
git diff examples/4node-basic/web/sdk web-pixi/sdk
```

Expected: empty diff (or trivial whitespace).

- [ ] **Step 5: Commit**

```bash
git add -A examples/4node-basic/ cmd/server/
git commit -m "feat(engine): engine owns --dump-schema; delete game schema files"
```

---

## Phase 4: Chain SDK regeneration into builds

### Task 4.1: Wire SDK regen into `just build` recipes

**Files:**
- Modify: `justfile` (root)
- Modify: `examples/4node-basic/justfile`
- Modify: `examples/slither/justfile` (if applicable — slither has no SDK today)

- [ ] **Step 1: Root justfile — make `space-sdk` a build dep**

In root `justfile`, change `build`:

```justfile
# build web client + server into bin/server (also regenerates the TS SDK)
build: space-sdk build-web build-go
```

If running `space-sdk` requires the binary to first compile, that's a chicken-and-egg problem — the current implementation uses `go run` which compiles on the fly, so it's fine. Verify by `just clean && just build` from a clean checkout.

- [ ] **Step 2: 4node-basic justfile — make `sdk` a build dep**

In `examples/4node-basic/justfile`, change `build`:

```justfile
# build web client + Go binary (regenerates the TS SDK first)
build: sdk build-web build-go
```

- [ ] **Step 3: Smoke test**

```bash
cd examples/4node-basic && just clean && just build
# Expect: SDK files regenerated under examples/4node-basic/web/sdk/
ls -la examples/4node-basic/web/sdk/
```

Expected: SDK files present, recent timestamps. Same for the space game:

```bash
cd . && just clean && just build
ls -la web-pixi/sdk/
```

- [ ] **Step 4: Commit**

```bash
git add justfile examples/4node-basic/justfile
git commit -m "build: regenerate TS SDKs as part of just build"
```

---

### Task 4.2: Update CLAUDE.md "Client SDK Codegen" section

**Files:**
- Modify: `CLAUDE.md`

Update the docs to reflect: (1) the engine owns `--dump-schema`, (2) games declare server events via `cfg.Protocol`, (3) SDK regen happens automatically during `just build`. Remove references to game-side `dumpProtocolSchema` functions.

- [ ] **Step 1: Replace the section**

Find the "Client SDK Codegen" section in `CLAUDE.md`. Replace with:

```markdown
### Client SDK Codegen

`cmd/sdkgen/` auto-generates typed TypeScript client SDKs from protocol schema. Go is the single source of truth; the engine assembles the schema from the runtime registries (InputRouter, OpRouter, EntityKindDefs, ServerEvents). `just build` regenerates the SDK automatically — no manual step.

To regenerate just the SDK without a full build:

```bash
just client-sdk examples/4node-basic
just space-sdk
```

Games declare their server-event registry via `cfg.Protocol` in `main.go`:

```go
cfg.Protocol = mmokit.NewProtocol("name").
    ServerEvents(func(e *mmokit.ServerEvents) {
        mmokit.RegisterServerEvent[mygame.FooMsg](e, mygame.SE_FOO)
        // ...
    })
```

Client events and operations are discovered from the InputRouter and OpRouter that systems wire up at `Init()`. Entity-replication schema is discovered from `EntityKindDef` registrations. Nothing else is required on the game side — the engine intercepts `--dump-schema` and assembles the full schema before the game loop starts.
```

- [ ] **Step 2: Commit**

```bash
git add CLAUDE.md
git commit -m "docs: update Client SDK Codegen section for protocol unification"
```

---

## Self-review summary

**Spec coverage check:**

- ✅ Phase 1 (ServerEventRegistry + MakeEvent migration) — Tasks 1.1-1.9
- ✅ Phase 2 (Operations schema + typed Register) — Tasks 2.1-2.3
- ✅ Phase 3 (Engine-owned --dump-schema + delete game schema files) — Tasks 3.1-3.3
- ✅ Phase 4 (Chain SDK regen into build) — Tasks 4.1-4.2
- ✅ Server-event name derivation — Task 1.1
- ✅ Build-time validation of (code, type) pairs — Task 1.2 (Build panics on mismatch)
- ✅ Sorted/deterministic schema output — Tasks 1.2 + 2.1 use `sort.Slice`
- ✅ Backward-compat policy honored — `MakeEvent` stays in `pkg/` for engine internals; deleted from game code
- ✅ Verification via schema-output diff at each migration step — Tasks 1.5, 1.7, 3.2

**Type consistency:**

- `mmokit.RegisterServerEvent[T, P, C]` signature consistent across Tasks 1.2, 1.5, 1.7, 1.8
- `mmokit.RegisterOp[Req, ReqP, Res, ResP, C]` signature consistent across Tasks 2.1, 2.2
- `gw.ServerEvents()` accessor used consistently in Task 1.6

**Open items deliberately deferred:**

- Internal engine `MakeEvent` calls (e.g., engine sending `SE_SERVER_CONFIG`) — out of scope per spec
- Operations done via `MakeOpResponse` framing — out of scope per spec
- Per-event typed accessors (`events.PlayerSpawned.Send(...)`) — future work per spec

---

**Plan complete and saved to [docs/superpowers/plans/2026-04-24-protocol-unification.md](docs/superpowers/plans/2026-04-24-protocol-unification.md).**

## Two execution options:

**1. Subagent-Driven (recommended)** — I dispatch a fresh subagent per task, review between tasks, fast iteration.

**2. Inline Execution** — Execute tasks in this session using executing-plans, batch execution with checkpoints.

Which approach?
