# Distributed Command System — Foundation Design

## Context

The mmokit mesh now runs across coordinator / host / node / gateway roles in separate processes. Admin tooling has not kept up: [internal/game/commands.go](../../../internal/game/commands.go) registers 12 game-specific verbs (`damage`, `tp`, `kick`, `currency`, `say`, …) by closing over an `allNodes []NodeInfo` slice captured at `OnConsoleReady` time, and the engine's [pkg/engine/console.go](../../../pkg/engine/console.go) `CommandGroup` registry is text-only, single-process, and unaware of who is calling. In `--mode=node` `allNodes` is empty (cells arrive asynchronously); in `--mode=coordinator,host` admin verbs only run against the local cell set; in a future standalone gateway none of them work at all.

A handful of converging needs make "patch this in S9" the wrong move and "design the foundation now" the right move:

1. **Cross-process routing.** S9 needs operator commands at the coordinator console to execute on the right remote node — e.g., `tp alice 100 200` resolves alice → owning host → executes in the right cell.
2. **In-game chat admin** (future requirement). GMs/admins must be able to invoke commands from chat with RBAC enforcement. Same parser, same dispatcher, same handlers as the operator console — only the caller identity differs.
3. **External consumers** (future). A standalone `mmoctl` CLI, an admin web dashboard, possibly a Unity admin client, all need to invoke commands and discover them. They cannot share Go process state — they need a wire protocol with a self-describing schema.
4. **Auditability.** Every command invocation should be loggable (`who → verb → args → result`), regardless of which front-end issued it.

Patching the existing `CommandGroup` to handle any of these would compound technical debt. **Replace it with a single distributed command system** that the console, chat, future CLI, future dashboard, and tests all consume the same way.

This plan covers the foundation only. Migrating space-game verbs and the marketplace cross-host work (the rest of S9) sits on top of the foundation in follow-up plans.

---

## Goals

- **One registry, four front-ends.** `pkg/cmdsys/` is the single source of truth for every command in the mesh. Console, in-game chat, future CLI, future dashboard are thin adapters that all call `Dispatcher.Invoke(caller, verb, args)`.
- **Self-describing.** Every command exposes a JSON Schema derived by reflection from typed Go arg/result structs. The CLI gets `--help` for free, the dashboard renders forms from the schema, the chat parser binds positional tokens to fields by walking the schema.
- **RBAC built in.** Every command declares a capability tag (`entity.tp`, `cell.split`). Every caller carries a glob-matched capability set; `*.*` grants god mode for early operator use, `entity.*` grants any entity command, `cell.split` grants exactly that. Dispatcher rejects before any cross-process traffic.
- **Routing is metadata, not branching.** Every command declares where it runs (`Local`, `Coordinator`, `AllHosts`, `EntityOwner`, `PlayerOwner`, `AllGateways`, `SpecificHost`). Dispatcher resolves the target set, fans out, aggregates results — handlers themselves never know whether they were invoked locally, from chat, or from a remote console.
- **Adding a command is one file.** `commands/entity_tp.go` declares args struct, result struct, capability, routing kind, handler. No proto regen, no manifest, no boilerplate.
- **Cross-process via existing MeshControl stream.** No new gRPC service for v1; one new oneof variant on `CoordMessage` and one on `HostMessage` carry the full wire format. Reuses the established control-plane connection, RegisterAck epoch fencing, and reconnect handling.

## Non-goals (for this foundation plan)

- mTLS or any external network listener for the command API. v1 is in-cluster only.
- A standalone `mmoctl` CLI binary. The wire format will be ready for it; the binary is a follow-up.
- An admin web dashboard. The introspection endpoint will be ready; the dashboard is a follow-up.
- Streaming / long-running commands. v1 is request/response with a 5s default deadline. `tail -f`-style log streaming, progress events for long migrations, etc., are deferred — the dispatcher signature is designed not to box future streaming in.
- Migrating the space-game admin verbs onto the new system. That's a follow-up plan ("S9 step 2") that consumes this foundation.
- Migrating the marketplace cross-host. Same — separate plan on top of the foundation.

---

## Architecture

### Package layout

A new top-level package `pkg/cmdsys/` containing:

- [pkg/cmdsys/command.go](../../../pkg/cmdsys/command.go) — `Command`, `Caller`, `RouteKind`, `Result`, `Capability` types.
- [pkg/cmdsys/registry.go](../../../pkg/cmdsys/registry.go) — `Registry`, `Register(cmd Command)`, `Lookup(verb)`, `List()`, schema introspection.
- [pkg/cmdsys/schema.go](../../../pkg/cmdsys/schema.go) — reflection-driven JSON Schema derivation from arg/result struct types. Reads `cmd:"..."` struct tags for help text, positional order, default values.
- [pkg/cmdsys/parser.go](../../../pkg/cmdsys/parser.go) — text-to-args binding (`tp alice 100 200` → `TpArgs{Target:"alice", X:100, Y:100}`). Positional + `--name=value` support, walks the schema.
- [pkg/cmdsys/rbac.go](../../../pkg/cmdsys/rbac.go) — capability glob matcher (`*.*`, `entity.*`, `entity.tp`).
- [pkg/cmdsys/dispatcher.go](../../../pkg/cmdsys/dispatcher.go) — `Dispatcher.Invoke(ctx, caller, verb, rawArgs)`. Looks up command, RBAC-checks, resolves route targets, fans out (local or remote), aggregates results.
- [pkg/cmdsys/route.go](../../../pkg/cmdsys/route.go) — `RouteResolver` for each `RouteKind`. The `EntityOwner` resolver consults the coordinator's `PlayerLocation` map; `AllHosts` resolver returns every live host from `PeerList`; `Local` returns the current process; etc.
- [pkg/cmdsys/audit.go](../../../pkg/cmdsys/audit.go) — structured audit log: every invocation produces one record with `time, caller_id, caller_caps, verb, args_json, route_targets, results_per_target, total_duration_ms, error`.
- [pkg/cmdsys/transport.go](../../../pkg/cmdsys/transport.go) — abstraction over the control-plane stream. In-process implementation for tests + colocated mode; MeshControl-backed implementation for cross-process.

Adapters live next to their hosts (so `cmdsys` stays free of game/console imports):

- [pkg/engine/console_cmdsys.go](../../../pkg/engine/console_cmdsys.go) — bridges the interactive readline console to a `Dispatcher`. Replaces the old `CommandGroup` registry. Operator caller identity is hard-coded to `{ID:"console", Capabilities:["*.*"]}`.
- [pkg/universe/cmdsys_chat_adapter.go](../../../pkg/universe/cmdsys_chat_adapter.go) — detects `/`-prefixed chat lines, parses them through `cmdsys.Parser`, dispatches with the player's session-derived caller identity.
- [pkg/universe/cmdsys_meshcontrol.go](../../../pkg/universe/cmdsys_meshcontrol.go) — the MeshControl `CommandRequest`/`CommandResponse` send/receive plumbing.

### Core types (rough sketch)

```go
package cmdsys

type Command struct {
    Verb        string          // "entity.tp"
    Capability  Capability      // "entity.tp" — usually mirrors Verb but can be coarser
    Description string
    Route       RouteKind
    Args        any             // typed zero-value struct, e.g. TpArgs{}
    Result      any             // typed zero-value struct, e.g. TpResult{}
    Handler     HandlerFunc
}

type RouteKind uint8
const (
    RouteLocal         RouteKind = iota // run wherever the dispatcher lives
    RouteCoordinator                    // forward to coordinator role
    RouteAllHosts                       // fan out to every live host
    RoutePlayerOwner                    // resolve via PlayerLocation, run on owning host
    RouteEntityOwner                    // resolve via NetID owner lookup, run on owning host
    RouteAllGateways                    // fan out to every live gateway
    RouteSpecificHost                   // requires HostID arg field
    RouteSpecificCell                   // requires CellID arg field
)

type Caller struct {
    ID           string     // "console", "player:alice", "rpc:dashboard-1", ...
    Source       CallerSource
    Capabilities []Capability // grant set; glob-matched against Command.Capability
}
type CallerSource uint8
const (
    SourceConsole CallerSource = iota
    SourceChat
    SourceMeshControl
    SourceTest
)

type HandlerFunc func(ctx context.Context, env *Env, args any) (any, error)

type Env struct {
    Caller Caller
    Local  *LocalContext // role-specific handles: Coordinator, GameWorld, Gateway
    Logger logger.Logger
}

type Result struct {
    Verb         string
    Caller       Caller
    PerTarget    []TargetResult // one entry per route target
    Aggregate    any            // for fan-out commands, optional caller-defined merge
    DurationMS   int64
}
type TargetResult struct {
    TargetID string  // hostID, gatewayID, "local"
    OK       bool
    Result   any     // typed result struct
    Error    string
}
```

### Routing flow (single example: `tp alice 100 200` from the operator console)

1. Operator types `tp alice 100 200` in the coordinator process console.
2. Console adapter constructs `Caller{ID:"console", Source:SourceConsole, Capabilities:["*.*"]}`.
3. Console adapter calls `dispatcher.Invoke(ctx, caller, "entity.tp", rawText)`.
4. Dispatcher finds `Command{Verb:"entity.tp", Capability:"entity.tp", Route:RoutePlayerOwner, Args:TpArgs{}, ...}` in registry.
5. RBAC: glob-match `entity.tp` against caller's `["*.*"]` → ok.
6. Parser: `rawText "alice 100 200"` + `TpArgs{Target string, X float32, Y float32}` reflected schema → `TpArgs{Target:"alice", X:100, Y:100}`.
7. Route resolver for `RoutePlayerOwner`: looks up `coord.ActiveUserHost("alice")` → `"node-2"`. Targets = `[{HostID:"node-2"}]`.
8. Transport: serializes `{verb, args_json, caller}` into `CoordMessage.CommandRequest{request_id, ...}` and writes to node-2's MeshControl stream. Awaits `HostMessage.CommandResponse{request_id, ...}` with a 5s deadline.
9. On node-2: incoming `CommandRequest` → dispatch local handler → handler runs on the owning cell's `PendingAdminCmds` (so it lands inside a tick) → returns `TpResult{OldCell:..., NewCell:...}` → marshalled into `CommandResponse` → sent back on the stream.
10. Coordinator dispatcher receives the response, builds `Result{PerTarget:[{TargetID:"node-2", OK:true, Result:TpResult{...}}]}`.
11. Console adapter renders the result via the existing `Table` utility.
12. Audit logger writes one record covering the whole flow.

### Cross-process wire format

One new variant on each side of the MeshControl oneof:

```proto
// HostMessage adds:
//   CommandResponse command_response = 15;
//   CommandRequest  command_request  = 16; // host -> coord (e.g. fan-out to coord-only verbs)

// CoordMessage adds:
//   CommandRequest  command_request  = 15;
//   CommandResponse command_response = 16; // for host -> coord -> host bounces

message CommandRequest {
  uint64 request_id = 1;     // pending-promise key
  string verb       = 2;
  bytes  args_json  = 3;     // pre-marshalled struct
  Caller caller     = 4;     // serialized caller identity
  uint32 deadline_ms = 5;    // remaining deadline
}

message CommandResponse {
  uint64 request_id  = 1;
  bool   ok          = 2;
  bytes  result_json = 3;    // pre-marshalled struct, empty on error
  string error       = 4;
  string target_id   = 5;    // who answered ("node-2")
}

message Caller {
  string id           = 1;
  uint32 source       = 2;   // CallerSource enum
  repeated string caps = 3;
}
```

JSON-on-the-wire (not proto Any) keeps args/results decoupled from protobuf evolution. The wire framing is fenced by `request_id` so out-of-order responses are a non-event.

### Caller identity sources

| Source | How identity is constructed | Default capabilities |
|---|---|---|
| Operator console | `{ID:"console", Source:SourceConsole, Caps:["*.*"]}` | `*.*` (god mode, hardcoded; cannot be revoked locally) |
| In-game chat | `{ID:"player:"+username, Source:SourceChat, Caps: lookupGrants(username)}` | Empty by default; granted via separate `cmdsys.GrantStore` (Postgres-backed, future) |
| MeshControl peer | Caller field is forwarded verbatim from the originating process. Receiving role re-validates against its own grant store before executing. | (forwarded) |
| Test | `cmdsys.TestCaller("test", "*.*")` helper for unit tests | `*.*` |

Forwarded `Caller` is **re-validated, not trusted**. A compromised host cannot escalate by forging caller caps because the executing role re-checks the caps against the local grant store before running the handler. Inside a trusted cluster (mTLS-fenced future) this is paranoia, but it's cheap and protects against a buggy peer doing the wrong thing.

### Schema reflection rules

`cmdsys.SchemaOf(zeroStruct any) Schema` walks an arg or result struct's exported fields and emits:

- Field name (camelCase from Go name unless `cmd:"name=foo"`)
- JSON Schema type from Go type (`string`, `int32`, `float32`, `bool`, slice, nested struct, enum via `cmd:"enum=foo|bar|baz"`)
- Help string from `cmd:"help=Username or netID"`
- Positional order from declaration order; opt out with `cmd:"named-only"`
- Default value from `cmd:"default=10"`
- Required vs optional from `cmd:"optional"` (default required)

The same schema drives the parser (positional + `--name=value`), the JSON Schema introspection endpoint, the dashboard form generator, and the in-process validation step before handler execution.

### Audit log

Single structured log line per invocation, written via `gameLog.Log(CatCmdAudit, ...)` (new category):

```text
[cmd] caller=player:alice src=chat verb=entity.tp args={"target":"bob","x":100,"y":100} targets=[node-2] ok=true dur=14ms
```

Categorized so operators can `log only cmd` to tail just admin activity. Already-implemented `pkg/logger/` category system supports this without changes.

---

## Migration strategy

The plan ships in 6 commits, each independently green:

1. **C1 — Foundation package.** Create `pkg/cmdsys/` with `command.go`, `registry.go`, `schema.go`, `parser.go`, `rbac.go`, `dispatcher.go` (in-process only — `RouteLocal` works, others return "not yet wired"), `audit.go`. Unit tests cover registration, schema reflection, parser binding (positional + named), RBAC glob matching, in-process dispatch. No engine integration yet. ~800 lines + tests.

2. **C2 — Console adapter + replace `CommandGroup`.** Add [pkg/engine/console_cmdsys.go](../../../pkg/engine/console_cmdsys.go). Migrate every existing engine builtin (`config get/set/list/save/reset`, `entity *`, `node *`, `log *`, `cell list/info/split/merge/cooldowns/config`) onto the new system as `RouteLocal` commands. Delete the old `CommandGroup`/`Cmd`/`Table`-route plumbing in `pkg/engine/console.go` (Table stays — it's a renderer, not a registry). Console adapter renders results via `Table` from the result struct. After this commit the engine has no `CommandGroup`. Tests confirm every existing `cell list` etc. UX is identical. ~600 lines net (mostly replacing equal lines).

3. **C3 — Cross-process transport via MeshControl.** Add `CommandRequest`/`CommandResponse`/`Caller` to [proto/meshpb/mesh.proto](../../../proto/meshpb/mesh.proto). Implement the MeshControl-backed `cmdsys.Transport` in [pkg/universe/cmdsys_meshcontrol.go](../../../pkg/universe/cmdsys_meshcontrol.go). Wire `RoutePlayerOwner`, `RouteEntityOwner`, `RouteAllHosts`, `RouteCoordinator`, `RouteSpecificHost`, `RouteAllGateways` resolvers (consulting `coord.PlayerLocation` / `PeerList` / sessionRoutes). Add an integration test that registers a dummy verb on a fake host, dispatches it from the coordinator under `TestHosts`, asserts result round-trips correctly. ~500 lines + test.

4. **C4 — Chat adapter + caller-identity flow.** Add `cmdsys_chat_adapter.go` to `pkg/universe/`. Hook it into the existing chat ingestion path so `/`-prefixed messages are intercepted before they're broadcast as chat. Build the caller identity from the player session; consult the (initially-stubbed) `GrantStore` for capabilities. Add a tiny `GrantStore` interface with an in-memory implementation; Postgres backing is a follow-up. Default grants: nobody has anything, but `--dev-grant-all` flag grants `*.*` to all players for local testing. Audit log fires for every chat-driven command. ~300 lines + test. **Not wired into the space game yet — the dispatcher is reachable but there are no game commands to invoke through it.**

5. **C5 — Migrate space-game verbs.** Move every command in [internal/game/commands.go](../../../internal/game/commands.go) to the new system. Each verb becomes a small file in `internal/game/commands/` (one struct + one handler + one registration). Capability tags use `entity.*` / `player.*` / `chat.*` namespaces. The current `OnConsoleReady`-based registration is deleted; commands are registered at game-setup time against the coordinator's `cmdsys.Registry`. After this commit `tp`, `damage`, `kick`, `currency`, etc., all work from the coordinator console **and** from in-game chat (when granted). ~1000 lines net change (mostly moving + retyping existing code).

6. **C6 — Introspection HTTP endpoint.** Add `GET /commands` and `GET /commands/<verb>` to the existing engine HTTP server. Returns the JSON Schema for every registered command. No auth in v1 — same trust model as the metrics endpoint. Future CLI / dashboard use this. ~150 lines + test.

After this 6-commit sequence:

- Old `CommandGroup` is gone.
- Every operator command works in single-process and distributed mode.
- Chat-driven admin commands work for granted players.
- The wire format and schema endpoint are ready for `mmoctl` / dashboard.
- Audit log captures every invocation.
- The S9 admin-commands line item is closed (steps 1–5 satisfy it).

---

## Critical files

**New:**

- [pkg/cmdsys/command.go](../../../pkg/cmdsys/command.go), [registry.go](../../../pkg/cmdsys/registry.go), [schema.go](../../../pkg/cmdsys/schema.go), [parser.go](../../../pkg/cmdsys/parser.go), [rbac.go](../../../pkg/cmdsys/rbac.go), [dispatcher.go](../../../pkg/cmdsys/dispatcher.go), [route.go](../../../pkg/cmdsys/route.go), [audit.go](../../../pkg/cmdsys/audit.go), [transport.go](../../../pkg/cmdsys/transport.go)
- [pkg/cmdsys/grants.go](../../../pkg/cmdsys/grants.go) — `GrantStore` interface + in-memory impl
- [pkg/engine/console_cmdsys.go](../../../pkg/engine/console_cmdsys.go)
- [pkg/universe/cmdsys_meshcontrol.go](../../../pkg/universe/cmdsys_meshcontrol.go)
- [pkg/universe/cmdsys_chat_adapter.go](../../../pkg/universe/cmdsys_chat_adapter.go)
- [internal/game/commands/](../../../internal/game/commands/) — per-verb files

**Modified:**

- [proto/meshpb/mesh.proto](../../../proto/meshpb/mesh.proto) — `CommandRequest`/`CommandResponse`/`Caller` messages + 2 oneof slots on each side
- [pkg/engine/console.go](../../../pkg/engine/console.go) — `CommandGroup` deleted, builtins moved to cmdsys-registered form
- [pkg/universe/coordinator.go](../../../pkg/universe/coordinator.go) — owns the cluster `cmdsys.Registry`, exposes it via accessor
- [pkg/universe/mesh_control_server.go](../../../pkg/universe/mesh_control_server.go), [mesh_control_client.go](../../../pkg/universe/mesh_control_client.go) — handle the new oneof variants
- [internal/game/commands.go](../../../internal/game/commands.go) — deleted (replaced by `internal/game/commands/`)
- [cmd/server/main.go](../../../cmd/server/main.go) — register space-game commands at game-setup time

**Deleted:**

- `pkg/engine/console.go` — `CommandGroup` and `Cmd` types (Table renderer stays)
- `internal/game/commands.go`
- The `execOnPlayerNode` / `execOnEntityNode` helpers (unused after migration)

---

## Verification

- `go vet ./...` clean after every commit.
- `go test -count=1 ./...` green — all existing tests, especially `pkg/universe/s7_*_test.go`, `examples/4node-basic/mesh_e2e_test.go`.
- New unit tests:
  - Schema reflection covers all supported field types + tags
  - Parser handles positional, named, missing required, defaults
  - RBAC glob matcher: `*.*`, `entity.*`, `entity.tp`, mismatched cases
  - In-process dispatch: register → invoke → result
  - Cross-process dispatch under `TestHosts`: dummy verb on remote host → result round-trips
- New integration test: drive `tp alice 100 200` from the coordinator's `Dispatcher` against a 2-host `TestHosts` setup, assert alice's position changed on the correct host (uses `execOnLoop` from S8).
- Manual smoke: `just db-up && just dev`, open coordinator console, run `cell list`, `entity list`, `damage <self> 10`, `tp <self> 100 100`. Same UX as today.
- Manual smoke: future "chat-driven `/help`" returns the verb list filtered to the player's grants.

---

## Risks & mitigations

- **Migrating every console builtin in one commit (C2) is invasive.** Mitigation: the new `CommandGroup`-replacement adapter ships with `Table`-rendered output identical to today's. The migration is mechanical (every existing `Cmd.Run([]string)` becomes a typed handler) and the test suite covers the existing UX.
- **Reflection-based schema misses an edge case.** Mitigation: the foundation commit ships with explicit support for the types currently used by builtins (`string`, `int32`, `float32`, `bool`, `[]string`, nested struct one level deep, named enums via tag). Anything else fails loud at registration time, not at runtime. New types are added one-at-a-time as needed.
- **Cross-process round-trip latency makes the console feel slow.** Mitigation: in-process short-circuit when `RouteX` resolves to the local process — the dispatch never touches MeshControl. Only true remote dispatches incur the ~1ms gRPC hop, which is well under operator-perceptible.
- **Caller identity forgery from a buggy/compromised peer.** Mitigation: every receiving role re-validates the caller's caps against its own `GrantStore` before executing. The cluster only trusts the originating identity for audit logging; trust for execution is local-decision.
- **Capability namespace creep.** Mitigation: lock the v1 namespaces to `entity.*`, `player.*`, `cell.*`, `host.*`, `gateway.*`, `chat.*`, `config.*`, `debug.*`. New top-level namespaces require explicit code review.
- **C5 surfaces game-side bugs in the existing admin commands.** Almost certain — the existing closures were never tested under multi-host. Mitigation: every migrated command gets a small `dispatcher_test.go` covering its happy path under `TestHosts`. Bugs surfaced are fixed inline as part of C5.

---

## Out of scope (deferred to follow-up plans)

- **`mmoctl` standalone CLI binary.** Wire format ready; binary is a 1-day follow-up after C6.
- **Admin web dashboard.** Schema endpoint ready; React/Vue dashboard is its own project.
- **mTLS / external auth on the command endpoint.** Today's trust model is "in-cluster only." External exposure requires auth design.
- **Postgres-backed `GrantStore`.** v1 ships with in-memory grants + a `--dev-grant-all` flag. Persisted grants are a follow-up.
- **Streaming / long-running commands.** v1 is sync request/response. Streaming results need a different transport pattern (likely a separate gRPC service).
- **Marketplace cross-host.** Separate plan; uses this command system for the operator-facing pieces but the data plane is its own design.
- **Player-routing-respects-StationCell + `just distributed-space` recipe.** Tracked in the previous S9 plan, picked up after the foundation lands.
