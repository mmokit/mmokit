# Distributed Server Mesh — Design Specification

**Date:** 2026-04-12
**Roadmap:** Feature #12 (Connection Migration Protocol) + parts of #11 (handoff wiring)
**Branch:** one branch per sub-project (S1, S2, ... S9)
**Status:** Draft

## Overview

Transform the mmokit server mesh from an in-process goroutine-per-cell model to a distributed architecture where cells run across multiple OS processes (hosts) connected via gRPC, clients connect through a stable gateway, and game state persists in PostgreSQL. One binary, composable `--mode` flags, zero client wire-protocol changes.

### Success criterion

`examples/4node-basic` runs as 2 host processes (2 cells each) + 1 coordinator/gateway process, connected via gRPC, clients through the gateway, state in Postgres. The space game (`cmd/server`) does the same. Players cross cell boundaries on different hosts with zero client disconnect. Admin can live-migrate a cell between hosts. Host crash auto-recovers in ~3.5 seconds.

### Non-goals (deferred)

- HA coordinator (Raft/etcd consensus)
- Multiple gateway instances behind a load balancer
- UDP transport for native clients
- Client prediction / rollback
- Event outbox pattern for extracted services
- Runtime-editable partition policy via REST API / admin dashboard
- Slither body-extent border replication
- `uint64 EntityGuid` (multi-cluster identity)
- Load-based auto-rebalancing (L4 — admin-driven migration is the v1 model)

---

## 1. Vocabulary

| Term | Definition |
|---|---|
| **Cell** | One `CellID`, one `ecs.World`, one game loop goroutine. The minimal simulation unit. Renamed from `Node`. |
| **Host** | OS process container. Owns 0..N Cells, one gRPC endpoint, one `pgxpool`. Identified by `HostID` (stable, operator-supplied via `--host-id` or auto-generated UUID). |
| **Coordinator** | Control-plane service. Authoritative `CellID->HostID` table, player routing, net-ID range allocation, topology management, migration orchestration, admin console. SPOF in v1. |
| **Gateway** | Session frontend. Terminates client WebSocket connections, forwards traffic to the cell's host via gRPC (or local shortcut), reroutes on migration. Handles login parsing and ping/pong. |
| **Bridge** | Host-to-host (or intra-host) data plane interface. Two implementations: `grpcBridge` (network) and `localBridge` (colocated shortcut via direct channel/function call). |
| **CellAddress** | Routing key: `{HostID string, CellID CellID}` for external resolution; `CellID` alone for coordinator lookups. |
| **Shadow** | A pre-authority entity on the destination host during handoff. Has full component data (from `TransferBlob`) but is excluded from game systems (like `Replica` and `Ghost`). Promoted to local on commit. |

---

## 2. Topology

### Three-layer architecture

```
COORDINATOR (control plane)
  Cell->Host table, player routing, NetID alloc,
  heartbeat monitor, migration orchestrator, admin console
  gRPC: MeshControl service (:9100)

HOST-A (node)                        HOST-B (node)
  Cell 0,0 (ECS+Loop)                 Cell 0,1 (ECS+Loop)
  Cell 1,0 (ECS+Loop)                 Cell 1,1 (ECS+Loop)
  pgxpool -> PostgreSQL                pgxpool -> PostgreSQL
  MeshData gRPC (:9101)                MeshData gRPC (:9102)

  <--- MeshData bidi stream (border frames, handoff, transfers) --->

GATEWAY (session frontend)
  WebSocket :8080, session->host routing,
  upstream switch on migration, ping/pong
  gRPC: MeshData (client frames <-> hosts)

CLIENTS (Unity, Web-Pixi, Bots)
  WebSocket to Gateway :8080
  Zero knowledge of hosts/cells/topology
```

### Process modes (one binary)

| `--mode` | What runs | Default ports |
|---|---|---|
| `coordinator` | MeshControl server, admin console, reconciler | `:9100` |
| `node` | MeshData server, N cell goroutines, pgxpool | `:9101+` |
| `gateway` | WebSocket listener, MeshData client to hosts | `:8080` (WS) |
| *(no flag)* | All three colocated | `:8080` + `:9100` + `:9101` |

Default (no `--mode` flag) = all three colocated in one process. Matches today's zero-friction `./bin/server` experience. Users opt into distributed topology explicitly via flags.

### Command-line flags

| Flag | Default | Purpose |
|---|---|---|
| `--mode` | `coordinator,node,gateway` | Composable, comma-separated |
| `--coordinator-addr` | `localhost:9100` | Where hosts/gateways dial the coordinator |
| `--host-addr` | `:9101` | This host's gRPC listen address |
| `--host-id` | auto UUID | Stable host identity (operator should set in production) |
| `--persist-dsn` | `postgres://mmokit:mmokit@localhost:5432/mmokit?sslmode=disable` | PostgreSQL connection string |
| `--persist-store` | `postgres` | `postgres` or `memory` (memory for tests only) |
| `--gateway-mode` | `local-shortcut` | `local-shortcut` or `always-proxy` (forces gRPC even colocated, for testing) |

### Authentication

- `MMOKIT_CLUSTER_TOKEN` env var. When set, every gRPC stream carries `authorization: Bearer <token>` in initial metadata. Coordinator and hosts validate on stream open.
- When unset (default dev): no auth. Colocated mode needs none.
- No mTLS, no per-host credentials, no rotation in v1.

---

## 3. Cell Lifecycle & Assignment

### Bootstrap (cold start)

1. Coordinator starts, listens on `:9100`. Waits indefinitely for the first host to register.
2. First host registers via `MeshControl.Control` bidi stream (`Register{HostID, GrpcAddr}`). Coordinator opens a **5-second settle window**.
3. Additional hosts that register during the settle window are accumulated.
4. Settle window closes. Coordinator computes **rendezvous hash** over `(allCellIDs x liveHostIDs)`: for each `CellID`, `score = fnv64(cellID || hostID)`, highest score wins.
5. Coordinator pushes `CellAssign{CellID}` to each host. Host spawns the cell's `ecs.World`, game loop goroutine, and `WorldBase`.
6. Coordinator pushes `PeerList{[]HostRecord}` to every host. Hosts open `MeshData` streams to neighbors lazily.
7. Cells call `GameWorld.Init()`. Game is live.

**Single-host bootstrap (no `--mode` flag):** Steps 1-7 happen in-process via function calls. Coordinator sees one host, assigns all cells to it. Identical code path, zero network.

**Late-joining hosts (after initial settle):** join immediately (no new settle window). Coordinator adds the host to the live set but does NOT auto-migrate cells to it. Cells move only via admin `cell balance` command or split-triggered rehash. No surprise migrations on a healthy cluster.

### Rendezvous hashing

For each `CellID`, compute `fnv64(cellID.String() || hostID)` for every live host. The host with the highest score owns that cell.

Properties:

- **Deterministic:** same `(cell set, host set)` -> same assignment.
- **Minimal churn:** adding one host to N moves ~`K/(N+1)` cells on average.
- **No state to persist:** assignment is recomputed from inputs.
- **Weightable (future):** multiply score by host capacity weight for non-uniform distribution.

### Dynamic cell splits (distributed fan-out)

1. Partition policy (on coordinator, fed by cell metrics from heartbeats) decides cell X should split.
2. Coordinator computes rendezvous for all 4 child `CellID`s over current live hosts.
3. Coordinator sends `CellSplit{ParentCellID, Children: [{CellID, TargetHostID} x 4]}` to the host owning the parent.
4. Parent host partitions entities by quadrant. Children assigned locally: spawn in-process, move entities directly. Children assigned remotely: serialize entity bucket, send `CellSpawnTransfer{CellID, Entities, Topology}` to each target host.
5. Remote hosts create fresh cells from the transferred entities and start ticking.
6. Parent cell shuts down. All 4 children report `CellReady` to coordinator. Coordinator updates cell->host table, pushes `PeerList`.

Entities move exactly once — from parent to final destination. No intermediate local spawn + migration.

### Dynamic cell merges (distributed fan-in)

1. Coordinator decides 4 siblings should merge. Picks a **survivor** via rendezvous on the parent `CellID`.
2. Coordinator sends `CellMerge{Survivor, Donors[3], SurvivorHostID}` to all involved hosts.
3. Each donor cell serializes its entities, sends `CellMergeTransfer{FromCellID, Entities}` to the survivor host.
4. Survivor cell absorbs entities (deserialize, add to ECS world, expand spatial bounds).
5. Donor cells shut down, report `CellStopped`.
6. Survivor reports `MergeComplete`. Coordinator renames survivor's CellID to parent, updates table, pushes topology.

No pre-consolidation needed. Donors can be on any host; they ship their entities directly to the survivor.

### Admin commands

| Command | Effect |
|---|---|
| `host list` | Show live hosts, cell counts, heartbeat status |
| `cell list` | Show every cell and its current host |
| `cell info <cellID>` | Entity count, tick budget, owner host |
| `cell migrate <cellID> <hostID>` | Move one cell to target host via handoff protocol |
| `cell balance` | Compute rendezvous, print proposed plan, require `confirm` |
| `cell split <cellID>` | Force-split (bypass policy threshold) |
| `cell merge <cellID>` | Force-merge siblings (bypass policy threshold) |

### Crash recovery (automatic)

1. Host heartbeat stops -> 3-second grace -> coordinator marks host dead.
2. Coordinator recomputes rendezvous over `liveHosts - dead` for each cell the dead host owned.
3. For each affected cell: coordinator sends `CellAssign` to the new target host. Target spawns a fresh cell, loads `player_state` from Postgres, respawns persistent entities. Ephemeral entities (projectiles, loot, NPCs) are lost — same as a single-node crash today.
4. Coordinator pushes `UpstreamSwitchBatch` to gateway for affected player sessions.
5. Recovery time: 3s heartbeat grace + ~200ms cell spawn + ~50ms gateway flip = ~3.5s total.

### Graceful shutdown

1. Host receives SIGINT -> sends `GracefulLeave` on `MeshControl`.
2. Coordinator migrates each of the leaving host's cells to surviving hosts via the full handoff protocol (entities and players transfer live, no data loss).
3. All migrations complete -> host exits cleanly.
4. If no other hosts exist: save state, exit. Players disconnect.

---

## 4. Handoff Protocol

Replaces the existing `MsgTransfer` + `Ghost` + `ArrivalConfirm` protocol entirely. One unified protocol for all entity ownership transfers.

### Single entity handoff (player crosses cell boundary)

```
Tick N:    Entity enters AoI margin of neighbor cell boundary
           BorderDispatcher sends border frames to destination host
           HandoffStateMachine: Unseen -> Border

Tick N+K:  Entity enters PromoteRadius (or trajectory predicts crossing)
           State machine: Border -> Promoted
           Source sends MsgHandoffPrepare to destination:
             - TransferBlob (full entity serialization)
             - ClientBaselines (per-client acked snapshots)
             - NewEpoch (current + 1)
           Destination creates shadow entity (full components, excluded from systems)
           Destination begins receiving full-rate border frames

Tick N+K+5: WarmupCount >= MinWarmupTicks (5 ticks = 250ms)
            Entity has physically crossed the boundary
            State machine: Promoted -> Handoff
            Source sends MsgHandoffCommit:
              - NetID, NewEpoch, CommitTick
            Source: bumps epoch, downgrades entity to replica
            Destination: promotes shadow to authoritative local entity
            Coordinator: updates routing, pushes UpstreamSwitch to gateway
            State machine: enters cooldown (20 ticks = 1s)

Tick N+K+6: Gateway has flipped upstream
            Late input at old host forwarded via MsgForwardInput
            Player sees zero discontinuity
```

### Shadow entity

A shadow is distinct from both `Replica` and `Ghost`:

- Created from `HandoffPreparePayload.TransferBlob` — has the full component set, not just border-frame components.
- `Shadow` component marker. Default `mmokit.Query[T]` exclusions include `Shadow` alongside `Ghost` and `Replica`.
- The `ReplicationSystem` DOES iterate shadows — they appear in client frames on the destination host so nearby players see the approaching entity before authority commits.
- On `HandoffCommit`: remove `Shadow` component -> entity becomes a normal local entity.

### Admin teleport

`tp xennion 5 5` uses the same handoff protocol with an **immediate** Border->Promoted->Commit transition (no warmup wait). The entity is force-moved, so the warmup guarantee is irrelevant.

### Cell migration (admin-driven)

`cell migrate cell_1_0 host-C` is the handoff protocol applied in bulk:

1. Coordinator sends `CellMigrate{CellID, DestHostID}` to source host.
2. Source enters "draining" mode: keeps ticking, no new entity spawns, no new player routing.
3. Source flushes all dirty players in the cell to Postgres (`FlushCell`).
4. Source serializes entire cell state (all entities, player sessions, metadata) into `CellMigratePayload`, sends to destination via `MeshData`.
5. Destination creates fresh Cell, deserializes everything, starts ticking.
6. Destination reports `CellMigrateReady` to coordinator.
7. Coordinator atomically: updates cell->host table, pushes `UpstreamSwitchBatch` for affected players, pushes `PeerList`.
8. Source receives `CellMigrateCommit`, stops cell, tears down ECS world, confirms `CellStopped`.

Player downtime: ~1-2 ticks (50-100ms) during gateway flip.

### Constants (tunable via coordinator config)

| Constant | Value | Purpose |
|---|---|---|
| `MinWarmupTicks` | 5 (250ms @ 20Hz) | Minimum co-simulation before commit |
| `CrossingCooldownTicks` | 20 (1s @ 20Hz) | Anti-thrash after handoff |
| `PromoteRadius` | `tier.Radius * 0.5` | Distance from boundary to begin Prepare |
| `CellDrainTimeout` | 10s | Max time for cell migration drain before force-stop |

### Retired protocol

`MsgTransfer`, `MsgArrivalConfirm`, and the `Ghost`-as-transfer-mechanism pattern are retired. The `Ghost` component stays but is repurposed: it marks an entity on the source host during the brief post-commit cleanup window, not as a transfer mechanism.

---

## 5. gRPC Transport & Proto Schema

### New proto package: `proto/meshpb/mesh.proto`

Two services, one proto file.

#### MeshControl (coordinator <-> hosts, coordinator <-> gateway)

```protobuf
service MeshControl {
  rpc Control(stream HostMessage) returns (stream CoordMessage);
}

message HostMessage {
  oneof msg {
    RegisterHost       register        = 1;
    Heartbeat          heartbeat       = 2;
    CellReady          cell_ready      = 3;
    CellStopped        cell_stopped    = 4;
    SplitComplete      split_complete  = 5;
    MergeComplete      merge_complete  = 6;
    MigrateReady       migrate_ready   = 7;
    PersistBatch       persist_batch   = 8;
    PlayerHandoff      player_handoff  = 9;
  }
}

message CoordMessage {
  oneof msg {
    RegisterAck        register_ack    = 1;
    CellAssign         cell_assign     = 2;
    CellRelease        cell_release    = 3;
    CellSplit          cell_split      = 4;
    CellMerge          cell_merge      = 5;
    CellMigrate        cell_migrate    = 6;
    CellMigrateCommit  migrate_commit  = 7;
    PeerList           peer_list       = 8;
    NetIDRangeGrant    netid_range     = 9;
    PersistResult      persist_result  = 10;
    UpstreamSwitch     upstream_switch = 11;
  }
}
```

#### MeshData (host <-> host, gateway <-> host)

```protobuf
service MeshData {
  rpc Data(stream MeshFrame) returns (stream MeshFrame);
}

message MeshFrame {
  oneof msg {
    BorderFrame        border_frame     = 1;
    HandoffPrepare     handoff_prepare  = 2;
    HandoffCommit      handoff_commit   = 3;
    ForwardInput       forward_input    = 4;
    CellSpawnTransfer  cell_spawn       = 5;
    CellMergeTransfer  cell_merge       = 6;
    CellMigratePayload cell_migrate     = 7;
    ClientInput        client_input     = 8;
    ClientFrame        client_frame     = 9;
    ChatRelay          chat_relay       = 10;
    CrossNodeAction    cross_action     = 11;
    ActionResult       action_result    = 12;
  }
}
```

### Design choices

- **Bidi streams, not unary RPCs.** Single long-lived stream per connection. Stream close = disconnect detection.
- **`oneof` envelope.** One stream to manage per peer. Ordered delivery within a stream (Prepare arrives before Commit).
- **`BorderFrame.data` is `bytes`.** The existing `pkg/replication.Frame.Encode/Decode` binary format is wrapped in the proto envelope without double-encoding.
- **`ClientInput` / `ClientFrame` carry raw `bytes`.** Gateway is protocol-agnostic; hosts parse the game protocol as they do today.

### Connection topology

```
Coordinator
  MeshControl stream <- Host-A
  MeshControl stream <- Host-B
  MeshControl stream <- Gateway

Host-A
  MeshData stream <-> Host-B (lazy, opened on first send)
  MeshData stream <-> Gateway

Host-B
  MeshData stream <-> Host-A
  MeshData stream <-> Gateway
```

### Bridge interface (two implementations)

```go
type Bridge interface {
    SendBorderFrame(destCellID CellID, frame []byte)
    SendHandoffPrepare(destCellID CellID, payload *HandoffPreparePayload)
    SendHandoffCommit(destCellID CellID, payload *HandoffCommitPayload)
    SendForwardInput(destCellID CellID, payload *ForwardInputPayload)
    SendClientFrame(gatewayID string, connID uint32, data []byte)
    // ... remaining methods
}
```

- `localBridge`: direct channel send for colocated cells on the same host. Zero serialization.
- `grpcBridge`: serializes to `MeshFrame` proto, sends via `MeshData` stream.
- Selection: `host.IsLocal(destHostID)` -> `localBridge`, else `grpcBridge`.
- `--gateway-mode=always-proxy` forces `grpcBridge` even for local destinations.

### Reconnect & backpressure

- **Host -> Coordinator:** exponential backoff 100ms -> 30s, jittered, retry forever.
- **Host -> Host:** exponential backoff 100ms -> 30s. After 30s failure, drop pending messages. Coordinator's heartbeat is authoritative for peer liveness.
- **Border frame backpressure:** if gRPC `Send` would block, the `BorderDispatcher` drops the frame for that tick (matches existing `select{default}` drop semantics). The 30-tick forced resync recovers the receiver.
- **Handoff messages:** must not be dropped. gRPC flow control (HTTP/2 window) handles backpressure; sender blocks rather than drops.

---

## 6. Gateway & Client Session Model

### Responsibilities

1. Terminate client WebSocket connections.
2. Read first message, expect `CE_LOGIN`. Run `LoginHandler(connID, loginMsg) -> (username, sessionData, error)`.
3. Ask coordinator `RoutePlayer(username)` -> target host.
4. Maintain `sessionID -> upstreamHostID` routing table.
5. Forward client <-> host traffic transparently (no game protocol parsing).
6. Flip upstream on `UpstreamSwitch` from coordinator (flip-then-forward model).
7. Handle ping/pong (RTT measurement at gateway, closest to client).

### Session lifecycle

```
Client connects (WebSocket :8080)
  -> Gateway assigns sessionID (= connID, uint32, monotonic)
  -> Gateway reads first message, expects CE_LOGIN
  -> LoginHandler parses login -> (username, sessionData)
  -> Gateway asks Coordinator: RoutePlayer(username) -> targetHostID
  -> Coordinator sends CellAssign / PlayerAssignment to target host
  -> Gateway pins session to target host
  -> Session ACTIVE: bidirectional forwarding

Upstream switch (handoff or migration):
  -> Coordinator pushes UpstreamSwitch{sessionID, newHostID}
  -> Gateway atomically flips routing
  -> In-flight input at old host forwarded via ForwardInput

Client disconnect (WebSocket close):
  -> Gateway notifies Coordinator: SessionDisconnected{sessionID, username}
  -> Host enters disconnect grace period (entity stays alive)
```

### VirtualConnManager

Hosts no longer own real WebSocket connections. `Engine.ConnMgr` becomes an interface:

- **Gateway process:** uses the real `pkg/net.ConnManager` (owns WebSockets).
- **Host process:** uses `VirtualConnManager` where `Send(connID, data)` routes through `MeshData.ClientFrame` to the gateway.

The swap is transparent to game code. `ReplicationSystem`, input handling, and all engine internals call `ConnMgr.Send` without knowing whether it's a local WebSocket or a gRPC hop.

**Colocated shortcut:** when gateway and node are in the same process, `VirtualConnManager.Send` detects the gateway is local and calls the real `ConnManager.Send` directly. `--gateway-mode=always-proxy` disables this for testing.

### Client compatibility

Zero wire-protocol changes. Clients send `enginepb.ClientEvent` envelopes over WebSocket to `:8080`. The gateway is invisible. Unity, web-pixi, and bot clients work unchanged.

### Multiple gateways (future, not v1)

The architecture supports it: coordinator tracks `sessionID -> {gatewayID, hostID}`, `UpstreamSwitch` targets a specific gateway. Not implemented — single gateway is sufficient for v1.

---

## 7. Persistence

### Architecture

```
Game Loop (20Hz)
  -> PlayerRepo (in-memory map, authoritative)
     MarkDirty() on mutation
  -> FlushDirty() every 300 ticks (~15s), jittered per cell
     -> PersistStore.Put()
        -> postgres backend: pgx.Batch of UPSERTs
        -> memory backend: map write (tests only)
     -> PostgreSQL
```

### PersistStore interface

Two implementations:

- `postgres` — `pgx/v5` + `pgxpool`. Production default.
- `memory` — in-memory map. Unit tests only.

BoltDB is retired. The existing `pkg/persist.Store` interface stays; implementation selected at startup via `--persist-store` flag.

### PostgreSQL schema

```sql
CREATE TABLE player_state (
    username     TEXT PRIMARY KEY,
    state        JSONB NOT NULL,
    last_flush   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    home_cell    TEXT NOT NULL DEFAULT '',
    version      BIGINT NOT NULL DEFAULT 0
);

CREATE TABLE kv_store (
    bucket       TEXT NOT NULL,
    key          TEXT NOT NULL,
    value        BYTEA NOT NULL,
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (bucket, key)
);

CREATE TABLE event_outbox (
    id           BIGSERIAL PRIMARY KEY,
    aggregate    TEXT NOT NULL,
    event_type   TEXT NOT NULL,
    payload      JSONB NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    processed    BOOLEAN NOT NULL DEFAULT FALSE
);
```

- `player_state`: hot path. JSONB for schema flexibility, queryable for analytics.
- `kv_store`: generic escape hatch, 1:1 match for the old BoltDB bucket API.
- `event_outbox`: stubbed for future event-driven services (marketplace, audit). Not used in v1.

### Write path

`FlushDirty()` collects N dirty players -> `pgx.Batch` of `INSERT INTO player_state (username, state, last_flush, home_cell, version) VALUES ($1, $2, NOW(), $3, 0) ON CONFLICT (username) DO UPDATE SET state=EXCLUDED.state, version=player_state.version+1, last_flush=NOW()` -> single round-trip.

Game loop never blocks on I/O. If batch commit fails, players stay dirty and retry next flush.

### Read path (login only)

`SELECT state FROM player_state WHERE username=$1`. Synchronous, blocks login flow. ~1ms local Postgres.

### Jittered flush offset

Each cell picks `flushOffset = rand.IntN(flushInterval)` at startup. Flushes when `(currentTick - flushOffset) % flushInterval == 0`. With 16 cells and a 300-tick interval, flushes spread ~1 second apart. Postgres sees a steady trickle, not a synchronized wall.

### Cell migration write safety

Before cell migration starts, source host calls `FlushCell(cellID)` — synchronous flush of all dirty players in that cell. Destination loads fresh state from Postgres on `CellAssign`.

### Host-to-Postgres topology

Each host dials Postgres directly via `--persist-dsn`. No coordinator proxy. The mesh-level "single writer per entity" guarantee means two hosts never race on the same row.

### Migrations (SQL)

Versioned SQL files under `sql/migrations/` using `golang-migrate/migrate`. Auto-run on startup (idempotent). `just migrate` for explicit runs.

### Dev experience

```yaml
# docker-compose.yml
services:
  postgres:
    image: postgres:16
    environment:
      POSTGRES_DB: mmokit
      POSTGRES_USER: mmokit
      POSTGRES_PASSWORD: mmokit
    ports:
      - "5432:5432"
    volumes:
      - pgdata:/var/lib/postgresql/data
volumes:
  pgdata:
```

`just dev` brings up Postgres via docker compose, runs migrations, starts server + vite.

---

## 8. Entity Identity

### NetworkID (unchanged wire format)

```go
type NetworkID struct {
    ID    uint32  // unique within cluster, from host's allocated range
    Epoch uint32  // bumped on every authority transfer
}
```

### Range allocation

- Coordinator maintains `next_range` counter in `kv_store`.
- On host registration: `NetIDRangeGrant{base: next_range * 10_000_000, size: 10_000_000}`.
- Range grants are persisted: restarting host with same `--host-id` gets the same range.
- Different host IDs always get different ranges.
- `uint32` supports 429 unique ranges. Sufficient for single-cluster.

### Epoch semantics

- Every `HandoffCommit` bumps `NetworkID.Epoch` by 1.
- Cell migration bumps epoch for every entity in the cell (batch bump at commit).
- Receivers compare epoch against `highestSeenEpoch[netID]`, drop stale frames.
- Crash recovery: entities from Postgres get epoch 0; in-flight frames from dead host carry old epoch and are dropped.

---

## 9. Failure Modes

### Host crash

Heartbeat timeout (3s) -> coordinator marks dead -> auto-reassigns cells via rendezvous over surviving hosts -> fresh cell spawn from Postgres -> gateway reroutes players. ~3.5s total recovery. Ephemeral entities (projectiles, loot, NPCs) lost.

### Coordinator crash (SPOF)

Cluster loses control plane. Hosts keep ticking but can't receive assignments, migrations, or new logins. Full restart from persisted state. HA coordinator is a documented follow-up.

### Network partition (hosts can't reach each other but can reach coordinator)

Dropped border frames reduce neighbor-cell fidelity temporarily. Dropped handoff messages abort in-flight migration; coordinator retries with a different destination. No split-brain: coordinator is single arbiter.

### Split failure (partial fan-out)

If a remote host dies mid-split before receiving its `CellSpawnTransfer`: the surviving children keep running, the failed child's entities are recovered via crash recovery (Postgres load). Splits are durable, migrations are retryable.

---

## 10. Sub-project Decomposition

Each sub-project ends at a green build with tests. Plans created incrementally.

### S1 — Rename Node->Cell + 1:N cell hosting (in-process)

Mechanical rename + introduce `Host` container. Intra-host cells communicate via direct channel.

**Success:** `4node-basic` runs as 1 host with 4 cells. Space game runs. `go test ./...` green.

### S2 — Handoff protocol wiring (in-process)

Wire `HandoffStateMachine`, emit/receive Prepare/Commit/ForwardInput, Shadow entities, `UpdatePlayerRoute`. Retire `MsgTransfer` + `Ghost` + `ArrivalConfirm`.

**Success:** player crosses boundary via handoff. `tp` works. Loopback bridge tests with 50ms/100ms latency prove correctness. `go test ./...` green.

### S3 — Proto schema + gRPC bridge

`proto/meshpb/mesh.proto` with `MeshControl` + `MeshData`. `grpcBridge` + `localBridge` implementations. `--gateway-mode` flag.

**Success:** `4node-basic` runs with 2 hosts in separate goroutines via gRPC loopback. `go test ./...` green.

### S4 — Coordinator as control-plane service

`--mode` flags. MeshControl server: registration, heartbeat, rendezvous assignment, PeerList, NetIDRangeGrant. Settle window. Crash recovery. Graceful shutdown. Admin commands.

**Success:** 2 processes (coordinator + node) on localhost. Kill node -> coordinator detects in 3s, reassigns on new node join. `go test ./...` green.

### S5 — Persistence: BoltDB -> Postgres

`pkg/persist/postgres.go` via pgx/v5. Remove BoltDB. SQL migrations. Docker compose. JSONB player state. Jittered flushes. `FlushCell` for migration safety.

**Success:** space game boots against Postgres, state persists across restart. `just test-pg` integration tests. `go test ./...` green.

### S6 — Gateway

Gateway process. `VirtualConnManager`. `UpstreamSwitch`. ForwardInput safety path. Login handling. Ping/pong at gateway.

**Success:** client plays through gateway. Boundary crossing -> gateway flips upstream -> zero disconnect. `go test ./...` green.

### S7 — Distributed cell splits + merges

Split fan-out to remote hosts. Merge fan-in from remote hosts. Partition policy on coordinator. Children rehash across hosts.

**Success:** space game on 2 hosts. Cell splits -> children distribute. Siblings merge back. `go test ./...` green.

### S8 — Multi-process 4node demo

`examples/4node-basic` as 1 coordinator/gateway + 2 node processes. `just distributed` recipe. End-to-end integration test with bots.

**Success:** 4 cells on 2 hosts, gRPC mesh, gateway, Postgres. Working demo.

### S9 — Space game full distributed

Admin commands route through coordinator. Marketplace cross-host. Player routing respects StationCell. `just distributed-space` recipe.

**Success:** space game runs distributed. Everything the single-process version does, the distributed version does.

### Dependency graph

```
S1 (rename) -> S2 (handoff) -> S3 (gRPC) -> S4 (coordinator) -> S6 (gateway) -> S7 (splits) -> S8 (demo) -> S9 (space game)
                                                |
S5 (postgres) --- can parallel with S3/S4 ------+
```

---

## 11. Future Extensions (documented, not in scope)

- **HA coordinator** via Raft/etcd consensus. Eliminates SPOF.
- **Multiple gateway instances** behind a load balancer. Coordinator tracks `sessionID -> {gatewayID, hostID}`.
- **Load-based auto-rebalancing (L4).** Coordinator watches metrics, autonomously migrates cells. Ships as a policy plugin on the existing reconciler.
- **Runtime-editable partition policy** via REST API + admin dashboard.
- **UDP transport** for native clients (Unity). Gateway terminates both WebSocket and UDP.
- **Client prediction / rollback** for owner-predicted player actors.
- **Event outbox pattern.** `event_outbox` table is already stubbed; background worker + CDC for extracted services.
- **`uint64 EntityGuid`** for multi-cluster identity and cross-restart continuity.

---

## References

- [Target architecture](../../../docs/planning/mmokit-target-architecture.md)
- [Tiered push replication spec](2026-04-11-tiered-push-replication-design.md)
- [Tiered push replication plan](../plans/2026-04-11-tiered-push-replication.md)
- [mmokit roadmap](../../../docs/planning/mmokit-roadmap.md)
- [From the MMO trenches: PostgreSQL](https://jahej.com/alt/2011_08_08_from-the-mmo-trenches-using-postgresql-for-the-game-database.html)
- [MMO Architecture: Source of truth (HN)](https://news.ycombinator.com/item?id=37702632)
- [pgx v5 documentation](https://pkg.go.dev/github.com/jackc/pgx/v5)
- [Rendezvous hashing (Wikipedia)](https://en.wikipedia.org/wiki/Rendezvous_hashing)
