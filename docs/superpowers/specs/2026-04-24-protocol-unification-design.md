# Protocol Unification: One Source of Truth for Runtime + Schema

**Date:** 2026-04-24
**Branch target:** new branch off `main` (after current `replication-timeline-redesign` lands)
**Scope:** Eliminate the duplicate-registration boilerplate in `dumpProtocolSchema` by making runtime registration self-describing, then have the engine assemble + dump the schema automatically.

## Problem

Each game today maintains a separate `dumpProtocolSchema` function that re-registers every client event, server event, and operation that's *already* registered at runtime. The two registrations must stay in sync by hand.

Concrete cost:

- `examples/4node-basic/schema.go` — 33 lines, ~4 events restated
- `cmd/server/schema.go` — 80 lines, ~30 events + 5 ops restated
- ~16 ad-hoc `mmokit.MakeEvent(uint32(code), msg)` call sites scattered through `internal/game/` with no central registry — the dump file is the only place server events are enumerated

For an open-source-grade engine, the user-facing model should be: **declare your protocol once at game wiring time. SDK generation is the engine's problem.**

## Current State

### Client events — already self-describing

`mmokit.Handle[T,P,C](router, code, states, fn)` at [pkg/mmokit/mmokit.go:1142-1163](../../../pkg/mmokit/mmokit.go#L1142-L1163) auto-captures `proto.MessageName(*new(T))` via `engine.WithProtoName`. The InputRouter exports `Schema() []ClientEventSchema` ([pkg/engine/input_router.go:122-145](../../../pkg/engine/input_router.go#L122)).

The dump file's 14 `mmokit.ClientEvent(...)` lines per game are pure duplication — the router already knows.

### Operations — NOT self-describing

`ops.Router.Register(opCode uint32, handler OperationHandler)` at [pkg/ops/router.go:72](../../../pkg/ops/router.go#L72) takes an untyped `func(req []byte) ([]byte, error)`. No proto types captured. Schema dump must hand-list every op with its request/response proto names.

### Server events — no registry

`mmokit.MakeEvent(code uint32, payload proto.Message) []byte` at [pkg/mmokit/mmokit.go:1363](../../../pkg/mmokit/mmokit.go#L1363) marshals on the spot at every call site. Nothing tracks which (code, type) pairs are valid. The dump file is the catalog.

### Entity kinds — already shared

`EntityKindDef` flows through both `BuildEntityKindDefs` (runtime) and `BuildReplicators` (schema). This is the model the rest of the protocol should match.

## Design

### End-state user code

```go
// examples/4node-basic/main.go
cfg := mmokit.Config{
    InvariantMode:    universe.InvariantPanic,
    StrictNetIDIndex: true,
    CellsX: CellsX, CellsY: CellsY, CellSize: CellSize,
    TickRate: TickRate, AoIRadius: AoIRadius,
    StaticFS: webDist, StaticFSPrefix: "web/dist",
    DefaultSpawn: mmokit.Location{X: CellSize*0.85, Y: CellSize*0.85},
    DynamicPartitioning: mmokit.DisabledPartitionConfig(),
    LoginHandler: mmokit.HandleLogin(...),

    // NEW: declarative protocol — runtime + schema source of truth
    Protocol: mmokit.NewProtocol("basic").
        ServerEvents(func(e *mmokit.ServerEvents) {
            mmokit.RegisterServerEvent[enginepb.SpawnedMsg](e,
                enginepb.ServerEventCode_SE_PLAYER_SPAWNED)
            mmokit.RegisterServerEvent[enginepb.CellTopologyMsg](e,
                enginepb.ServerEventCode_SE_CELL_TOPOLOGY)
        }),
}

mmo := mmokit.New(cfg)               // engine intercepts --dump-schema and exits if set
mmo.SetWorld(NewWorld)
mmo.AddSystem("Input", mmokit.NewInputSystem(setupInputHandlers))
// ...
mmo.Start(context.Background())
```

Game-side files removed:

- `examples/4node-basic/schema.go` — **deleted** (33 lines)
- `cmd/server/schema.go` — **deleted** (80 lines)
- `--dump-schema` flag declaration in each game's `main.go` — **deleted** (engine owns it)

### Architecture overview

```text
┌─────────────────────────────────────────────────────────────┐
│ cfg.Protocol *mmokit.Protocol                               │
│   ├── ServerEvents *ServerEventRegistry                     │
│   │     └─ entries: code → {Name, ProtoType reflect.Type}   │
│   ├── EntityKinds  []EntityKindDef                          │
│   ├── Game         string                                   │
│   └── ClientRenderMode                                      │
└─────────────────────────────────────────────────────────────┘
            │
            │ mmokit.New(cfg)
            ▼
┌─────────────────────────────────────────────────────────────┐
│ Process / Cell construction                                 │
│   ├── InputRouter ←─ setupInputHandlers (Handle[T,P,C])     │
│   ├── OpRouter    ←─ setupOperations  (Register[Req,Res])   │
│   └── ServerEvents wired into engine for typed Send()       │
└─────────────────────────────────────────────────────────────┘
            │
            │ if --dump-schema:
            ▼
┌─────────────────────────────────────────────────────────────┐
│ engine.dumpSchema()                                         │
│   ProtocolSchema {                                          │
│     ClientEvents:  router.Schema()                          │
│     ServerEvents:  cfg.Protocol.ServerEvents.Schema()       │
│     Operations:    opRouter.Schema()                        │
│     Entities:      replicatorRegistry.Schema()              │
│   }                                                         │
│   → JSON to stdout, os.Exit(0)                              │
└─────────────────────────────────────────────────────────────┘
```

### New types

#### `ServerEventRegistry`

```go
// pkg/mmokit/server_events.go
package mmokit

type ServerEvents struct {
    entries map[uint32]serverEventEntry
}

type serverEventEntry struct {
    code      uint32
    name      string         // camelCase, derived from enum const, override-able
    protoName string         // proto.MessageName(zero)
    protoType reflect.Type   // for runtime validation in Send()
}

// RegisterServerEvent declares a server→client event with its proto payload type.
// Name auto-derives from the enum constant (SE_PLAYER_SPAWNED → "playerSpawned").
// Pass WithName("…") to override.
func RegisterServerEvent[T any, P interface{ *T; proto.Message }, C engine.EventCode](
    e *ServerEvents, code C, opts ...ServerEventOption,
) { … }

// Send marshals msg, builds a channel-0x00 ServerEvent frame, and writes it.
// Panics if (code, T) wasn't registered — turns silent runtime divergence into
// a loud startup-time error.
func (e *ServerEvents) Send(connMgr *net.ConnManager, connID uint32, code uint32, msg proto.Message) { … }

// SendAll broadcasts to every connection (replaces ad-hoc loops).
func (e *ServerEvents) SendAll(connMgr *net.ConnManager, code uint32, msg proto.Message) { … }

func (e *ServerEvents) Schema() []ServerEventSchema { … }
```

Name derivation: `SE_PLAYER_SPAWNED` → strip `SE_`/`GSE_`/`CE_`/`GCE_` prefix → split on `_` → camelCase. Single deterministic transform; override available for edge cases.

#### Operations — typed Register

```go
// pkg/ops/router.go
func Register[Req any, ReqP interface{ *Req; proto.Message },
              Res any, ResP interface{ *Res; proto.Message },
              C EventCode](
    r *Router, code C, name string,
    handler func(ctx *Context, req ReqP) (ResP, error),
) { … }

func (r *Router) Schema() []OperationSchema { … }
```

`name` stays explicit (used as the SDK method name — `marketBrowse`, not auto-derivable from `OP_MARKET_BROWSE` because of casing conventions; could be derived but adding it is one extra arg per op and worth the readability).

#### `Protocol` builder

```go
// pkg/mmokit/protocol.go (extended)
type Protocol struct { … existing fields … ServerEvents *ServerEvents }

func NewProtocol(game string) *Protocol
func (p *Protocol) ServerEvents(fn func(*ServerEvents)) *Protocol
func (p *Protocol) SetClientRenderMode(mode ClientRenderMode) *Protocol  // already exists
```

`ClientEvents`, `Operations`, and `EntityKinds` are NOT builder methods — they're discovered from the InputRouter, OpRouter, and EntityKindRegistry that the game wires up via systems and `world.Init()`. The engine plumbs those into the schema dumper. Server events are the only registration type that needs an explicit Protocol-level builder because they have no current runtime registry — the rest are already self-describing once we add `OpRouter.Schema()`.

### Engine intercepts `--dump-schema`

`Process.Start(ctx)` (called by `mmo.Start(...)`) checks for `--dump-schema` immediately after `Build()` returns. If set:

1. `Build()` has already run all cell construction + system Init hooks — that's enough to populate InputRouter, OpRouter, and the entity-kind registry.
2. Skip listener startup (HTTP, MeshControl, Postgres, console, game loop).
3. Assemble `ProtocolSchema` from `cfg.Protocol.ServerEvents.Schema()`, the InputRouter's `Schema()`, the OpRouter's `Schema()`, and the entity-kind registry's replicator schema.
4. Write JSON to stdout.
5. `os.Exit(0)`.

The engine registers the `--dump-schema` flag in `BindFlags`. Games never declare it.

Note: `Build()` already constructs cells without starting them — the schema dump path is just `Build()` + dump + exit, no new lifecycle stage.

### Migration strategy: three phases

Each phase is independently shippable and individually verifiable.

#### Phase 1 — `ServerEventRegistry` + `MakeEvent` migration

**Why first:** biggest user-facing benefit, biggest game-side touch. Doing this first proves the registry pattern before extending it.

Steps:

1. Add `pkg/mmokit/server_events.go` with `ServerEvents`, `RegisterServerEvent[T,P,C]`, `Send`, `SendAll`, `Schema`, name-derivation helper, `WithName(...)` option.
2. Add `Protocol.ServerEvents(fn)` builder method that creates a registry, runs the registration callback, stores it on the Protocol.
3. Add `Config.Protocol *Protocol`. Process construction stores `cfg.Protocol` and exposes `(*Process).ServerEvents()` accessor. `WorldBase` gets a passthrough `ServerEvents()` so game code can reach it from any cell.
4. Migrate every `mmokit.MakeEvent(uint32(code), msg)` call site:
   - `internal/game/` — ~14 call sites
   - `examples/4node-basic/` — search for any
   - `examples/slither/` — search for any
   - `pkg/universe/` — engine-internal MakeEvent uses (e.g., `SE_SERVER_CONFIG`, `SE_CELL_TOPOLOGY` if pushed by engine itself) move to engine-owned auto-registration
5. Register every server event used today in each game's `Protocol.ServerEvents(...)` block. Running the binary without registration → `Send` panics → catches every miss at first emit.
6. **Keep `MakeEvent` exported** for now — it's still used internally by `Send` to build the wire frame, and removal is part of Phase 3 cleanup.
7. Update the existing `dumpProtocolSchema` to pull server events from `cfg.Protocol.ServerEvents.Schema()` instead of hand-listing them. Keep the file alive but shrink it dramatically.

**Done when:** every server event flows through `Send`, all tests pass, both example games + `cmd/server` run end-to-end, `dumpProtocolSchema` files have lost their server-event sections.

#### Phase 2 — Operations schema + typed Register

Steps:

1. Add `Register[Req, ReqP, Res, ResP, C]` generic to `pkg/ops`. Keep raw `Register(opCode, handler)` available; the typed wrapper builds on it.
2. Add `Router.Schema() []OperationSchema` returning code+name+request/response proto names.
3. Migrate every op registration in the codebase (only `cmd/server` uses ops today — 5 marketplace ops in `internal/marketplace/`).
4. Update `dumpProtocolSchema` to pull ops from the OpRouter instead of hand-listing.

**Done when:** dump files have lost their operation sections.

#### Phase 3 — Engine-owned `--dump-schema` + delete game schema files

Steps:

1. Engine registers `--dump-schema` in `BindFlags`; game-side `flag.Bool("dump-schema", ...)` lines deleted.
2. `mmokit.New(cfg)` (or first `Build()` call) checks the flag. If set: construct registries, dump, exit.
3. Plumb `*Process` access to `InputRouter` and `OpRouter` so the dumper can read their schemas without needing to know which cell holds them. (Routers are process-scoped, not cell-scoped — this should already be true but verify.)
4. Delete `examples/4node-basic/schema.go` and `cmd/server/schema.go`.
5. Update `just client-sdk` recipe — should still work without changes (`go run ./examples/4node-basic --dump-schema | go run ./cmd/sdkgen ...`).
6. Update CLAUDE.md "Client SDK Codegen" section to reflect the new model.

**Done when:** game `main.go` files mention nothing about schemas. `just client-sdk` works end-to-end. `dumpProtocolSchema` no longer exists in the codebase.

## Tradeoffs and decisions

### `Send(connMgr, connID, code, msg)` vs typed `events.PlayerSpawned.Send(connID, &msg{...})`

Considered: generate a typed accessor per event for compile-time pairing. Rejected for now — adds codegen complexity (or heavy reflection at startup), and the runtime panic on unregistered (code, T) pairs already catches the failure mode early. Keep `Send(connMgr, connID, code, msg)` simple. Revisit if the panic-at-startup ergonomic proves insufficient.

### `connMgr` arg on `Send`

Could be captured at registry construction time. Decision: pass it explicitly. Game code calling `Send` already has `gw.Engine().ConnMgr` or equivalent in scope; passing it keeps the registry stateless and avoids a second wiring step.

### Schema dump constructing a "throwaway" world

To populate the InputRouter and OpRouter for schema export, the engine must run the game's system Init hooks — which means constructing a World. This is acceptable because:

- World construction is cheap (no Postgres, no listeners — those are gated separately).
- The schema dump path already constructs a throwaway ECS world today (the entity-kinds section does it).
- Keeps "register handler" and "describe handler" as a single act.

### Backward compatibility

Per project policy ([no backward compat](../../../.claude/projects/-home-josh-projects-zenion-mmoserver/memory/feedback_no_backward_compat.md)) — no shims, no aliases. `MakeEvent` deletion happens in Phase 3 once `Send` is the only caller.

## Verification

Each phase ships with:

- All existing tests passing.
- New unit tests for the registry types (registration, Send validation, name derivation, schema output shape).
- `just client-sdk examples/4node-basic` regenerates without diff.
- `just dev` and `just distributed` run cleanly end-to-end.

Phase 1 also includes a "register-but-never-emit" test that exercises `Send`'s panic-on-unregistered path.

## Out of scope

- Migrating `MakeEvent` *internal* uses (e.g., the engine sending `SE_SERVER_CONFIG` itself) — these become engine-owned auto-registrations, but the user-facing change is the same.
- Generating typed accessors per server event (e.g., `events.PlayerSpawned.Send(...)`).
- Replacing `MakeOpResponse` / op-side framing (Phase 2 covers schema + typed Register; the response framing path is unchanged).
- Eliminating the `--dump-schema` IPC pattern in favor of importing a schema package directly into `cmd/sdkgen`. The current pipe pattern is fine and works without dependency tangles.

## Open questions

None — direction approved during brainstorming session. Phase boundaries chosen for shippability; reorderable if execution reveals dependencies between phases.
