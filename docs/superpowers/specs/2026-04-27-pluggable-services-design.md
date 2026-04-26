# Pluggable Services Framework — Design

**Status:** Draft for review
**Author:** Josh Stout (with Claude)
**Date:** 2026-04-27
**Related memories:** `feedback_no_backward_compat`, `feedback_refactor_over_stopgaps`, `feedback_enginepb_import`, `feedback_logging`

## 1. Summary

mmokit today exposes three engine roles that compose into a process via `--mode=`: `coordinator`, `host`, `gateway`. Game-specific subsystems like the marketplace are stuffed into the cell-host's game loop, sharing tick budget with ECS work. There is no sanctioned way for a game dev to add a non-gameloop subsystem (chat, market, account-mgmt, leaderboards, friends, presence) and run it as a separately-scaled process.

This design adds a fourth engine role — `service` — and a new generic package `pkg/service/` that lets game devs register **service kinds** (descriptors), have them instantiated on `service`-role processes, and route client + server-to-service ops to them via the existing gateway+OpRouter machinery.

v1 is **stateless services only** (DB-backed state, no in-memory authoritative state). Sharding, active/passive sync, and anti-affinity placement are explicitly deferred — the v1 interface is designed so they bolt on without breaking service authors.

## 2. Goals & non-goals

### Goals

- **Plug-in surface.** Game devs register a `service.Kind` and implement a small `Service` interface; the engine handles instantiation, routing, lifecycle, metrics, console, fanout queries.
- **Compose into roles.** A process declares `--mode=...,service --services=chat,market` and runs those kinds. No new "service host" abstraction beyond the existing role machinery.
- **Reuse existing wire.** Client ops on the existing `0x01` operations channel route to services via op-code claim. Server-to-service uses the same path.
- **Independent scaling.** Operators horizontally scale a service kind by deploying another `service`-role process with that kind in `--services=`. No coordinator-side placement logic.
- **Independent lifecycle.** A service-host process restarts without touching world hosts; gateway re-routes within ~1 PeerList hop.
- **Stateless v1 surface; stateful-ready interface.** `Service`, `Kind`, and `Context` are designed so adding sync streams / shard keys / active-passive in v2 is additive.

### Non-goals (v1)

- Active/passive replication, anti-affinity placement, state sync stream
- Sharding by explicit shard key
- Service-level RBAC / capability tags (services trust connID→username from gateway login)
- Service tick / per-service goroutine loop (event-driven only)
- Service-specific persistence pool (every service shares the cluster's `*postgres.Store`)
- In-process colocation shortcut (uniform gateway-routed path even when colocated)
- Migrating marketplace into the framework (deferred — marketplace genuinely wants in-memory state)
- Dynamic service-kind addition to a *running* `service`-role process (kind list is fixed by `--services=` at startup; growing it requires a graceful restart)
- Cross-kind transactions / workflows / sagas
- Operator-managed allowlist of accepted service kinds at the coordinator
- Runtime client SDK fetch (clients still use SDKs built at compile time)

### Heterogeneous binaries — partially supported in v1

Each `service`-role process compiles in only the kinds it hosts. Gateway and coordinator are **kind-agnostic** at compile time — their routing table is built entirely from `PeerList.services[*].op_codes` announcements. Adding a new service kind to the cluster requires rebuilding only the binary that hosts that kind; other processes (gateway, coordinator, existing service-hosts running other kinds) keep running. See §16 for the discovery model and the path to fully-dynamic v2 capabilities.

## 3. Architecture

```text
┌──────────────┐         ┌──────────────────────┐         ┌────────────────┐
│  ws client   │ op env  │       gateway        │  mesh   │  service-host  │
│  (browser /  │ ──────▶ │  - opCode→kind table │ ──────▶ │  - OpRouter    │
│   game)      │         │  - kind→[]instance   │         │  - chat svc    │
└──────────────┘         │  - hash(connID)%N    │         │  - market svc  │
                         └──────────────────────┘         └────────────────┘
                                   ▲                              │
                                   │ PeerList                     │ register/
                                   │ (services field)             │ heartbeat
                                   │                              ▼
                              ┌──────────────────────────────────────┐
                              │            coordinator               │
                              │  - HostRegistry                      │
                              │  - GatewayRegistry                   │
                              │  - ServiceRegistry  (NEW)            │
                              └──────────────────────────────────────┘
```

Five new pieces:

1. **`pkg/service/`** — generic package with `Kind`, `Service`, `Context`, process-local `Registry`, op-code routing index.
2. **String-keyed `Roles`** — `pkg/universe/roles.go` refactor from `uint8` bitmask to `map[string]struct{}`. New built-in role token `service`.
3. **`ServiceRegistry`** — coordinator-side roster of running service instances. Peer to `HostRegistry` / `GatewayRegistry`.
4. **Gateway routing extension** — gateway gains a second routing table (`opCode → kind → []instance`) on top of today's `session → cell` routing.
5. **Process bootstrap extension** — `Start` instantiates registered kinds whose names appear in `--services=`, calls `Init` + `RegisterOps`, announces instances to the coordinator.

## 4. Package layout

New package, peer to `pkg/universe/`. May import `gen/go/enginepb/` and `gen/go/meshpb/` but not game protos:

```text
pkg/service/
  kind.go          // Kind descriptor (registration record)
  registry.go      // process-local registry of registered Kinds
  service.go       // Service interface
  context.go       // ServiceContext — deps handed to Init
  instance.go      // running instance metadata + health
  router.go        // op-code → kind routing table builder + validator
  registry_coord.go// coordinator-side ServiceRegistry (cluster-wide)
```

Imported by:

- `pkg/universe/` — for the gateway routing extension and PeerList wiring
- `pkg/mmokit/` — for facade re-exports

Game code imports either `pkg/service/` directly or via `mmokit`.

## 5. Roles refactor

`pkg/universe/roles.go` changes:

```go
// Today
type Role uint8
type Roles uint8
const RoleCoordinator Role = 1 << iota  // 1
const RoleHost                          // 2
const RoleGateway                       // 4

// Proposed
type Roles map[string]struct{}

const (
    RoleCoordinator = "coordinator"
    RoleHost        = "host"
    RoleGateway     = "gateway"
    RoleService     = "service"
)
```

API surface:

- `Roles.Has(name string) bool`
- `Roles.Add(name string)`
- `Roles.String() string` — comma-joined sorted keys
- `ParseRoles(s string) (Roles, error)` — validates each token against `coordinator|host|gateway|service`
- `PresetAll = Roles{coordinator, host, gateway, service}` — *(open question — see §15.1)*

Migration: ~10 call sites across `roles.go`, `coordinator.go`, `bootstrap.go`, `host_network.go`, `gateway.go`, `host_registry.go`, plus tests. All sites change from `roles.Has(RoleHost)` (where `RoleHost` was a `Role` constant) to `roles.Has(RoleHost)` (where `RoleHost` is a string constant). Type-level change is mechanical; no backward-compat aliases (per `feedback_no_backward_compat`).

## 6. The `Kind` descriptor

```go
// pkg/service/kind.go
type Kind struct {
    // Unique kind name; matches the token used in --services=.
    Name string

    // Op codes this kind handles. Engine validates at startup that:
    //   - no two registered Kinds claim the same code
    //   - all codes here are actually registered by RegisterOps()
    OpCodes []uint32

    // Constructor — called once per process on services-role startup.
    // Each services-role process instantiates exactly one Service per
    // listed Kind. Factory must not block (real init goes in Service.Init).
    Factory func(ctx *Context) Service

    // If true, engine errors at startup when DB is not configured.
    RequiresDB bool

    // Prometheus metric prefix; defaults to Name when empty.
    MetricsPrefix string

    // Optional liveness probe. Called only on demand by `service info`
    // fanout and `/health` aggregation — not periodically. Must return
    // quickly. Default = nil = "healthy if Init succeeded".
    HealthCheck func(svc Service) error

    // Human-readable; shown in `service info <name>`.
    Description string
}
```

**Convention for op codes.** Service kinds reference protobuf-generated enum constants, never hard-coded integers. Each game declares an `*OpCode` enum in its proto file and casts to `uint32` at the registration site:

```go
// proto/basicpb/basic.proto
enum EchoOpCode {
    BOP_ECHO_UNSPECIFIED = 0;
    BOP_ECHO_PING        = 300;
    BOP_ECHO_PERSIST     = 301;
    BOP_ECHO_FETCH       = 302;
}

// service code
var Kind = service.Kind{
    OpCodes: []uint32{
        uint32(basicpb.EchoOpCode_BOP_ECHO_PING),
        uint32(basicpb.EchoOpCode_BOP_ECHO_PERSIST),
        uint32(basicpb.EchoOpCode_BOP_ECHO_FETCH),
    },
    ...
}
```

Field stays `[]uint32` because `pkg/service/` is generic and cannot import game protos (per `feedback_enginepb_import` — only `enginepb` is allowed in `pkg/`).

## 7. The `Service` interface

```go
// pkg/service/service.go
type Service interface {
    // Init runs once after Factory returns and after the engine has
    // validated dependencies. Use it for slow startup work (DB warm,
    // schema validation, etc).
    Init(ctx *Context) error

    // RegisterOps wires handlers into the process op router. Engine
    // calls this exactly once after a successful Init. Engine cross-
    // checks the *exact set* of registered op codes against Kind.OpCodes
    // — any difference (missing or extra) is a fatal startup error.
    RegisterOps(router *ops.Router) error

    // Shutdown is called on graceful process exit. Block until in-flight
    // handlers drain (engine provides a deadline via ctx).
    Shutdown(ctx context.Context) error
}
```

`RegisterOps` is required even for services with no client wire — the implementation is a one-line `return nil`. Keeping the contract simple beats a separate `OpHandler` interface.

## 8. The `Context` struct

```go
// pkg/service/context.go
type Context struct {
    KindName   string
    InstanceID string                 // unique within cluster
    Logger     *logger.Logger
    Metrics    *metrics.NodeMetrics
    DB         *postgres.Store        // nil iff cluster has no Postgres
    Roles      universe.Roles

    // SendEvent forwards a server event to a connected client through
    // the gateway that owns connID. Implementation goes through the
    // same mesh path as cell-originated events.
    SendEvent func(connID uint32, code uint16, msg proto.Message) error

    // Reserved for v2 extensions: SyncStream, ShardKey, etc. — fields
    // added only when the corresponding feature lands.
}
```

`Context` is a struct, not an interface. Fields are additive across versions; no breaking change for service authors when v2 lands.

## 9. State guarantees

Services **may** hold in-memory state but the framework provides **zero guarantees** about it.

| Pattern                                                      | Works in v1? | Why                                                                 |
| ------------------------------------------------------------ | ------------ | ------------------------------------------------------------------- |
| Local cache of hot rows (LRU, prepared statements)           | ✅            | Per-instance, rebuilds on restart, no cross-instance coherence      |
| Warm-on-Init read-only lookup tables                         | ✅            | Same — local copy, no writes                                        |
| Connection pools, internal counters, log buffers             | ✅            | Process-local infrastructure                                        |
| Per-session state with affinity (hash(connID)→instance)      | ⚠️           | Works *during* steady state. Breaks when N changes — adding/removing an instance reshuffles `connID % N` |
| Authoritative cross-instance state (rooms, order books)      | ❌            | Multiple instances of the same kind have divergent views; no sync   |

**Mental model:** every op handler should be able to start with a DB read and end with a DB write. Anything between is a cache that rebuilds on the next request. Services that genuinely need authoritative in-memory state are v2 services and need the deferred active/passive sync work.

`Context` does not expose any "broadcast to peers of my kind" primitive in v1, so building state-dependent services would require explicitly going around the framework.

## 10. Registration & lifecycle

### Registration API

```go
coord := mmokit.NewCoordinator(cfg)
coord.RegisterService(chat.Kind)
coord.RegisterService(market.Kind)
coord.Start()  // calls Build() internally
```

`RegisterService` must be called before `Build()`. After-Build registration returns an error.

**Where registration is required:** only on processes that *host* the kind (i.e. processes whose `--mode=` includes `service` AND whose `--services=` includes the kind name). Gateway-only and coordinator-only processes don't need to call `RegisterService` for any kind — they learn the routing table from PeerList at runtime. See §16 for the implications.

### CLI flags

- `--mode=...` accepts the new `service` token. e.g. `--mode=coordinator,gateway,service`.
- `--services=chat,market` declares which kinds to instantiate on this process. Stored as `Config.ServiceKinds []string`.

### Build-time validation (fatal)

1. `RoleService ∈ roles` ⇒ `Config.ServiceKinds` non-empty
2. `Config.ServiceKinds` non-empty ⇒ `RoleService ∈ roles`
3. Every name in `Config.ServiceKinds` matches a registered `Kind.Name`
4. No two registered Kinds share an op code
5. Any `Kind.RequiresDB == true` ⇒ `Config.PostgresURL` non-empty

### Cluster-wide validation (registration-time)

When a `service`-role process registers via MeshControl after Build, it announces `{kind, instanceID, opCodes}`. The coordinator's `ServiceRegistry` cross-checks announced op codes against:

- Op codes already claimed by other registered service instances. If the kind name matches, op code lists must match exactly. If the kind name differs, op codes must not overlap.

Conflict ⇒ coordinator rejects the registration; the services-host fails to start with a clear error message naming the conflicting kind + code.

### Lifecycle order on a `service`-role process

```text
1. Bootstrap parses flags → Config populated (incl. --services=)
2. Game code calls coord.RegisterService(kind) for each kind
3. coord.Start() (calls Build internally):
   a. Validate Config.ServiceKinds against the registry
   b. Validate op-code overlap
   c. Validate DB prereqs
   d. For each name in Config.ServiceKinds:
      - svc := kind.Factory(serviceCtx)
      - svc.Init(serviceCtx)
      - svc.RegisterOps(process.opRouter)  // engine cross-checks codes
   e. Announce instances to coordinator via MeshControl
   f. Coordinator validates + adds to ServiceRegistry + broadcasts PeerList
4. Process serves ops via existing OpRouter machinery
5. SIGINT → graceful shutdown:
   a. Send GracefulLeave-equivalent to coordinator
   b. Coordinator removes instances from ServiceRegistry, broadcasts PeerList
   c. After PeerList grace (~2s), service.Shutdown(ctx) per kind
   d. Process exits
```

Step 5b → 5c order matters: gateways must stop routing to the instance *before* `Shutdown` runs, so handlers don't see new ops mid-shutdown.

## 11. Coordinator registry, PeerList, gateway routing

### `ServiceRegistry` (coordinator-side)

```go
// pkg/service/registry_coord.go
type ServiceRegistry struct {
    mu        sync.RWMutex
    instances map[string]ServiceInstance       // key: instanceID
    byKind    map[string][]string              // kind → []instanceID (stable order)
    opIndex   map[uint32]string                // opCode → kind
}

type ServiceInstance struct {
    Kind       string
    InstanceID string
    HostID     string         // joins HostRegistry.HostRecord
    OpCodes    []uint32
    JoinedAt   time.Time
}
```

Methods: `Register`, `Unregister`, `LookupByOpCode(code) (kind, found)`, `InstancesOfKind(kind) []ServiceInstance`. `Register` is the validation choke-point.

### PeerList extension (proto change, no backwards compat)

```protobuf
message PeerList {
  repeated HostRecord     hosts    = 1;
  repeated CellOwnership  cells    = 2;
  repeated GatewayRecord  gateways = 3;
  repeated ServiceRecord  services = 4;  // NEW
}

message ServiceRecord {
  string kind                = 1;
  string instance_id         = 2;
  string host_id             = 3;  // joins HostRecord.host_id
  repeated uint32 op_codes   = 4;
}
```

PeerList still fires on every topology change (any service join/leave triggers re-broadcast). Same delivery path — coordinator pushes to every registered host + gateway.

### Gateway-side routing tables

```go
type opRouting struct {
    opToKind   map[uint32]string                  // built from ServiceRecord.op_codes
    instances  map[string][]ServiceInstanceRoute  // kind → instances (stable order)
}

type ServiceInstanceRoute struct {
    InstanceID string
    HostID     string  // dial via existing HostRegistry mesh-data stream
}
```

`instances[kind]` is sorted lexicographically by `InstanceID` (deterministic across processes) so every gateway picks the same instance for the same connID without coordination.

### Op-receive flow on the gateway

```text
1. Client sends op envelope on operations channel (0x01)
2. Decode op_code from envelope header
3. opRouting.opToKind[code] lookup:
   a. found → service routing path
   b. not found → existing session→cell path (unchanged)
4. (Service path) instances := opRouting.instances[kind]
   - empty → reply with retryable error "no healthy instance of kind X"
   - non-empty → instance := instances[hash(connID) % len(instances)]
5. Forward MeshFrame to instance.HostID via the existing per-host MeshData stream
6. Destination host's OpRouter dispatches by code → service handler runs
7. Response flows back via the same MeshData stream → gateway → client
```

### Failure modes

- **Instance dies during in-flight op.** Host's MeshData stream closes; gateway sees the error; replies to the client with a retryable error code. Client retries; PeerList has propagated by then; new pick lands on a survivor.
- **Last instance of a kind dies.** Gateway returns "service unavailable" to clients. Coordinator's heartbeat infra detects + removes from registry within ~3s (host heartbeat threshold). PeerList re-broadcast clears the now-empty kind entry. Operator must deploy another instance.
- **PeerList lag during instance leave.** Gateway routes to a leaving instance for up to one PeerList hop (~1 RTT). Service `Shutdown(ctx)` has a grace deadline (default 5s) — drains in-flight handlers before exit.

### Server-to-service path

A game system on a host that wants to call a service constructs the same op envelope and sends it to its known gateway via the MeshData stream. Gateway routes it like any client op. **No in-process shortcut** even when service is colocated. Cost: one extra hop in colocated mode (cheap; service ops are DB-bound). Future v2 work may add a colocation shortcut analogous to `--gateway-mode=local-shortcut`.

### Instance picker

`instances[hash(connID) % len(instances)]`. Stable across gateways (same `instances` ordering everywhere via PeerList). Gives free cache locality for steady-state traffic. Churns on N change — that's the v1 trade-off documented in §9.

## 12. Console, cmdsys, observability

### Console commands (registered on `RoleCoordinator`)

| Command                | Output                                                                                  | RouteKind         |
| ---------------------- | --------------------------------------------------------------------------------------- | ----------------- |
| `service list`         | Every instance cluster-wide: kind, instanceID, hostID, joinedAt, opCount, lastError     | `RouteAllHosts`   |
| `service info <kind>`  | Per-kind detail: declared op codes, instance count, per-instance status, op rate, total | `RouteAllHosts`   |
| `service kinds`        | *Registered* (not running) kinds in this binary: name, op codes, RequiresDB, Description| `RouteLocal`      |
| `service ops`          | Op-code routing dump: code → kind → instances; cell-fallback range                      | `RouteLocal`      |

`service list` and `service info` use `RouteAllHosts` — the handler returns empty results from hosts without `RoleService`; coordinator aggregates non-empty rows. This avoids a new route kind.

Each command is a typed cmdsys command. Args/Result schemas exposed at `GET /commands/service.list` etc. Same data is reachable from a future CLI/dashboard with no new code.

### Metrics

Service handlers are auto-wrapped at registration. Per-kind `NodeMetrics` sub-collector publishes:

```text
service_ops_total{kind, op_code, status}
service_op_duration_seconds{kind, op_code}
service_in_flight{kind, op_code}
service_errors_total{kind, op_code, error_type}
```

Prefix from `Kind.MetricsPrefix` (defaults to `Kind.Name`). Auto-registered on the existing `/metrics` Prometheus endpoint. Service authors get custom metrics via `ctx.Metrics.Counter("custom_thing")`.

### Logging

Log category `services:<kind>` auto-registered on `Service.Init`. Per `feedback_logging`: services-role processes ship significant state changes through `ctx.Logger.Log(serviceCat, ...)`. Engine doesn't enforce; the demo service models the pattern.

### Health endpoint

`/health` on services-role processes aggregates per-kind health from `Kind.HealthCheck` (default = "healthy if Init succeeded"). Returns 503 if any kind reports unhealthy. K8s/load-balancer hook.

### Auth / authorization

Same as today: gateway authenticates the connection at login; op envelopes carry the connID; services trust the connID→username mapping established at login. **No service-level RBAC** in v1 — service handlers see the username and self-enforce. Capability tags (analogous to cmdsys `Grant`) are explicit follow-up work.

## 13. Demo service: `echo`

Lives in `examples/4node-basic/services/echo/` (kept inside the example, not in `pkg/`).

### Proto extension to `proto/basicpb/basic.proto`

```protobuf
enum EchoOpCode {
    BOP_ECHO_UNSPECIFIED = 0;
    BOP_ECHO_PING        = 300;
    BOP_ECHO_PERSIST     = 301;
    BOP_ECHO_FETCH       = 302;
}

message EchoPingRequest    { string msg = 1; }
message EchoPingResponse   { string msg = 1; string instance_id = 2; }
message EchoPersistRequest { string key = 1; string value = 2; }
message EchoPersistResponse{ bool   ok  = 1; string instance_id = 2; }
message EchoFetchRequest   { string key = 1; }
message EchoFetchResponse  { string value = 1; int64 found_at_ms = 2; string instance_id = 3; }
```

### Kind registration

```go
var Kind = service.Kind{
    Name:        "echo",
    OpCodes:     []uint32{
        uint32(basicpb.EchoOpCode_BOP_ECHO_PING),
        uint32(basicpb.EchoOpCode_BOP_ECHO_PERSIST),
        uint32(basicpb.EchoOpCode_BOP_ECHO_FETCH),
    },
    Factory:     New,
    RequiresDB:  true,
    Description: "demo: ping returns instanceID; persist/fetch round-trip a row",
}
```

### Behaviour

- `BOP_ECHO_PING` returns `(msg, instanceID)` — visualizes hash-affinity
- `BOP_ECHO_PERSIST` writes `(key, value, ts)` to a `demo_echo` table
- `BOP_ECHO_FETCH` reads by key — visualizes cross-instance DB consistency

### Wiring into 4node-basic

- New `--mode=service --services=echo` recipe in [examples/4node-basic/justfile](examples/4node-basic/justfile) `distributed` target
- 4-process tmux setup grows by one process: coordinator + 2 hosts + gateway + 1 service-host
- One migration for the `demo_echo` table — see Open Question 15.4 for placement

### Web-client echo test panel

New file [examples/4node-basic/web/src/echo_panel.ts](examples/4node-basic/web/src/echo_panel.ts) — a collapsible HTML overlay (top-right corner, hidden by default, toggled by `e` key) with three sections:

- **PING** input + button; response shows `msg` + which instanceID handled it
- **PERSIST** key/value inputs + button; response shows ok + handling instanceID
- **FETCH** key input + button; response shows value + foundAt + handling instanceID

Wires through the auto-generated SDK (`cmd/sdkgen` reads from registered op codes; SDK gains typed methods for the three echo ops).

Lets the user visually confirm:

- Hash-affinity holds (repeated PING from same browser → same instanceID)
- Cross-instance consistency (PERSIST on one instance, FETCH lands on another → still returns value, since DB is source of truth)
- Failover (kill the service-host process → next click shows "no healthy instance" briefly → on retry lands on a survivor)

~150 LOC TS, no CSS framework, dev-UX only.

## 14. Testing strategy

| Layer        | What                                                                                                         | How                                                            |
| ------------ | ------------------------------------------------------------------------------------------------------------ | -------------------------------------------------------------- |
| Unit         | `service.Kind` validation (op-code dup, RequiresDB, name conflicts)                                         | Table tests in `pkg/service/kind_test.go`                      |
| Unit         | `ServiceRegistry` (register, unregister, lookup, op-code conflict rejection)                                 | `pkg/service/registry_coord_test.go`                           |
| Unit         | Roles refactor (string parsing, unknown tokens, mode validation)                                             | Existing `pkg/universe/roles_test.go`, extended                |
| Integration  | End-to-end op routing: client → gateway → service → response                                                 | New `pkg/universe/service_e2e_test.go` using cluster-fixture   |
| Integration  | Hash affinity holds across N=2 instances                                                                     | 100 ops from one connID; assert all land on the same instanceID|
| Integration  | Instance leave: kill 1 of 2 instances, in-flight op fails with retryable error, retry lands on survivor      | Reuse SIGINT/graceful-shutdown infra from `s7_graceful_shutdown_test.go` |
| Integration  | Op-code partition: ops claimed by service don't reach cells; unclaimed ops still cell-route                  | Pin both cell ops and service ops in one fixture, assert handler-call sites |
| Integration  | RequiresDB enforcement: bring up service-host without `POSTGRES_URL` → process errors at Start, not first op | Negative test                                                  |
| Smoke        | `service list` / `service info echo` round-trip via console                                                  | Existing console-test pattern                                  |
| Manual UX    | Web echo panel visualizes affinity / consistency / failover                                                  | Run `just distributed` + open browser                          |

The cluster fixture (`pkg/universe/cluster_fixture_*_test.go`) gains a `WithServiceHost(kind, n)` option so service tests get the same ergonomic startup as cell tests.

## 15. Open questions

### 15.1 PresetAll content

Today `PresetAll = coordinator|host|gateway` and a bare `--mode=` defaults to it. With `service` added:

- **Option A:** `PresetAll` stays as `coordinator|host|gateway` (no implicit service). A bare `--mode=` keeps today's single-process dev-server semantics. Adding `service` requires explicit `--mode=...,service`. Backward-compatible behaviour.
- **Option B:** `PresetAll = coordinator|host|gateway|service`. A bare `--mode=` runs everything. Forces all dev runs to set `--services=` or fail.

**Recommendation: Option A.** Keep dev-server defaults stable; service is opt-in.

### 15.2 Engine-reserved op-code range

Should we reserve a range (e.g. 0–99) for engine ops and require service ops to be ≥ 100? Today op codes are flat. Reserving avoids future collisions when the engine adds first-class ops (e.g. presence, telemetry).

**Recommendation: defer.** No engine ops exist today; if/when added, registration validation can enforce the reserved range without touching service authors.

### 15.3 InstanceID format

Two options:

- **Random UUID** — simple, unique trivially, opaque
- **Structured** `<host>-<kind>-<n>` — readable in `service list`, easier to grep logs

**Recommendation: structured.** `host-a-echo-0`, `host-a-echo-1`, etc. Follows the readability bias of the existing host/cell IDs.

### 15.4 Where do example-specific migrations live?

CLAUDE.md says all migrations live under `pkg/persist/postgres/migrations/*.sql` and are embedded into the binary at build time. Putting `demo_echo` in the engine package pollutes generic infrastructure with example-only tables. Two options:

- **Option A:** Add a `Config.ExtraMigrations fs.FS` hook so examples and games can layer their own migration directory on top of the engine's. Migrations from extras run after engine migrations. Echo's migration lives in `examples/4node-basic/migrations/`.
- **Option B:** Keep all migrations in `pkg/persist/postgres/migrations/` even when example-specific. Simpler; pollutes the engine package.

**Recommendation: Option A.** It's a real general-purpose hook (every game using mmokit will eventually need its own tables), the implementation is small (couple-of-line `golang-migrate` source extension), and it removes a category of "engine package contains game stuff" smell. Land alongside this design.

## 16. Service discovery

The framework's routing topology is **announcement-driven, not registry-driven**: gateway and coordinator never reference a service kind by name in compile-time code; both build their view of the cluster from `PeerList.services[*]` records that service-hosting processes emit at startup. This makes the v1 architecture meaningfully discovery-friendly even without explicit dynamic-loading machinery.

### What v1 supports today

| Process kind | Must rebuild to deploy a new service? | Why |
| --- | --- | --- |
| Service-host binary running the new kind | ✅ yes | Factory, Init, handlers must be compiled in |
| Coordinator | ❌ no | Validates announcements at registration time; no compile-time list of valid kinds |
| Gateway | ❌ no | Routing table is `PeerList.services[*].op_codes` |
| Other service-hosts (running unrelated kinds) | ❌ no | Independent processes, independent kind registries |
| Client | ⚠️ depends | Updated SDK needed for typed access; untyped passthrough works without |

**Operational story:** "I want to add a friends service" = build a new `friends` binary, deploy it as `--mode=service --services=friends`, it announces itself to the running coordinator, gateways pick up the new op codes via the next PeerList broadcast (~ms), clients with the updated SDK can call it. The world host running ECS, the gateway terminating WebSockets, and any other already-running service hosts keep running unchanged.

### What v1 does NOT support — the path to fully-dynamic v2

1. **Hot-load a new kind into a *running* `service`-role process.** v1 fixes the kind list via `--services=` at startup. Growing it requires a graceful restart of that one process. Go plugins (`plugin.Open`) are platform-fragile and rarely worth it; the more realistic path is operator tooling that does a graceful relaunch with an extended kind list — and the existing service-host graceful-shutdown infra makes this a small operational delta, not a v1 framework gap.

2. **Runtime client SDK fetch.** `cmd/sdkgen` today reads from registered op codes at example-binary build time and emits a static TS/C# SDK. For a freshly-deployed service kind, the client needs an updated SDK to use it typed. Two future paths:
   - **Schema endpoint:** extend the existing `GET /commands` JSON-Schema infrastructure to publish the live op-code → request/response schema for service ops; client tooling fetches at build *or* connect time. Most of the machinery already exists for cmdsys.
   - **Untyped passthrough:** the client SDK exposes a raw "send op-code + bytes, get bytes" call alongside the typed surface. Tooling and admin clients use it; main game clients keep using typed SDKs. Lower friction than the schema endpoint but loses type safety per call site.

3. **Cluster-side kind allowlist.** v1 accepts any kind a service-host announces (provided op codes don't conflict). A future flag `--allowed-kinds=chat,market` on the coordinator gates announcements, useful when running mmokit as a multi-tenant or security-sensitive cluster.

4. **Discovery-aware operator console.** `service kinds` today lists only kinds known to *this binary* (compile-time registrations). A v2 console verb `service known` could enumerate every kind the cluster is currently running based on PeerList state — letting an operator see what's deployed without knowing in advance.

None of these foreclose v1's design. They land on top of the announcement-driven routing model already in place.

## 17. Migration & rollout

- Roles refactor (`uint8` → `map[string]struct{}`) is mechanical, lands in one PR alongside the new role token.
- `pkg/service/` lands as a new package with no callers initially.
- Coordinator + gateway changes (PeerList field, routing table, service registry) land together — gateway routing depends on PeerList, both depend on the registry.
- Demo service + web panel land last as the validation surface.
- 4node-basic `justfile` gets the new `service`-role process recipe.
- Marketplace stays exactly where it is. **No migration in this PR.**

Estimated scope: ~1500–2000 LOC across `pkg/service/` (new), `pkg/universe/` (modified), `proto/meshpb/` (new field), `examples/4node-basic/` (new files), tests.

## 18. Future work (deferred from v1)

- Active/passive sync stream (`Context.SyncStream`, `Service.Snapshot()` / `Apply(delta)`)
- Anti-affinity placement (coordinator-side, ACTIVE/PASSIVE replicas of a shard never colocated)
- Sharded services (shard key per op, rendezvous-hashed shard→host mapping like cells)
- Service tick / per-service goroutine loop
- Service-level RBAC / capability tags
- In-process colocation shortcut (`--service-mode=local-shortcut`)
- Marketplace migration (depends on stateful-service support)
- Chat service (depends on stateful-service support if rooms become first-class)
- Hot-load of new kinds into running `service`-role processes (graceful-relaunch tooling instead of in-process plugins)
- Runtime client SDK fetch via extended `GET /commands` schema endpoint (or untyped passthrough as the lighter alternative)
- Coordinator-side `--allowed-kinds=` allowlist for multi-tenant clusters
- Discovery-aware console verb (`service known`) listing every cluster-deployed kind from PeerList
