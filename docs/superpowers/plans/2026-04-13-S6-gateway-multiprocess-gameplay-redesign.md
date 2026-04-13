# S6: Gateway / Multi-Process Gameplay — Clean-Slate Plan (v2)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Each task uses checkbox (`- [ ]`) syntax. This plan **REPLACES** the S6 portion of `2026-04-13-S4.5-S5-S6-distributed-mesh-continuation.md`.
>
> **v2 revision (2026-04-13):** rewritten to make `--mode=gateway` first-class instead of deferred. The user wants gateways to scale independently of the coordinator because they have different scaling profiles (many lightweight gateway instances fronting a smaller number of game-logic nodes). Research surfaced that the composite session key + `RegisterGateway` proto stub must land in S6 even if the standalone gateway binary mode is minimal, because otherwise the wire format breaks when the feature is properly built later.

**Branch:** stay on `feature/distributed-mesh`.

---

## Why this plan exists

Phase S6 is the capstone: clients connect to a gateway, get proxied to the authoritative node hosting their cell, and walk across host boundaries with no disconnect. The original master plan (`2026-04-13-S4.5-S5-S6-distributed-mesh-continuation.md`) assumed the gateway is always in-process with the coordinator. The user explicitly wants the gateway to be a runnable standalone process from S6 onward, because gateways and coordinators have different scaling profiles:

- **Gateways:** many instances, lightweight, scale horizontally behind a load balancer, primarily I/O bound (WebSocket terminations)
- **Coordinator:** one instance, holds cluster state, bottleneck for routing decisions, scale vertically
- **Nodes:** proportional to world size / player count, run game logic, scale on CPU

Before writing this plan I audited the codebase and researched modern separated-gateway architectures (Nakama, Metaplay, GameLift, SpatialOS, IT Hare's front-end server chapters, WebSocket load balancer patterns). Key findings that shape the design:

### Reality check vs the original master plan

- **There's no `ConnTransport` interface to extract** — `net.ConnManager` is a concrete struct and every caller uses it directly. The master plan's proposed method list (`Broadcast`, `ByConnID`) doesn't match reality. The actual hot-path surface a virtual implementation needs is much narrower.
- **Session routing state already exists** on the coordinator as `c.players` (username→`PlayerLocation`) + `c.connIndex` (connID→nodeID). Extend these with an `epoch` field + composite session key rather than introduce a parallel `SessionRouter` type.
- **Cross-host handoff has a notification gap:** in multi-process mode today, `OnPlayerTransfer` only updates the local coordinator's `connIndex`. When a cell on Node A hands off an entity to Node B, the coordinator never finds out. S6 has to close this gap explicitly.
- **All the session-related proto messages already exist:** `MeshFrame.ClientInput`, `ClientFrame`, `PlayerAssignment`, `ForwardInput`, and `CoordMessage.UpstreamSwitch` are all defined. They need **extension** (gateway_id field) but not invention.

### Research-driven design choices

- **Composite `{gatewayID, connID}` is the canonical session key.** IT Hare's front-end server chapter names the frontend as the scope of connection identity and makes `(frontendID, localConnID)` the unit on internal messages. Nakama uses `(userID, sessionID, nodeID)` — the `nodeID` part is internal routing state. Do the same.
- **Login runs on the gateway, not coordinator.** Nakama, Metaplay, and Valve GNS all co-locate auth with the socket. The gateway has the bytes, runs `LoginHandler` inline, then calls `PlayerRouter` locally using cached `PeerList` data. Async "session announce" to the coordinator is fire-and-forget. Zero login round-trip.
- **`UpstreamSwitch` is targeted, not broadcast.** Coordinator looks up `sessionRoutes[key].GatewayID → stream` and sends one message to the one gateway holding the session. SpatialOS v2's lesson: avoid broadcast if you can identify the target precisely.
- **`MeshControl` gains `RegisterGateway` variant** instead of a new gRPC service. Same bidi stream, parallel `gatewayStreams` map on `meshControlServer`. Nakama-style. Justified because the gateway message lifecycle (register → heartbeat → session events → UpstreamSwitch) is nearly identical to the node message lifecycle.
- **Gateway connects to nodes via `MeshData`**, same as node↔node. Gateway opens bidi streams lazily as sessions route to new hosts. Extend `HostNetwork.peers` with a `peerKind` enum (`node` vs `gateway`) rather than introducing a parallel `GatewayNetwork` — code simplicity wins over separation.
- **Session tokens still deferred past S6.** Capstone scope is "multi-process playable + transparent handoffs," not "survive coordinator or gateway crash with reconnect." Gateway crash = client reconnect + full re-login for S6. Tokens land in a follow-up phase.
- **Gateway dead-threshold: 5s** (vs nodes' 3s). Gateway death has direct client-visible impact (mass disconnects); more grace for restart-based recovery.
- **`--gateway-mode=local-shortcut` (default) vs `always-proxy`.** When the session's target host is colocated with the gateway (in-process coordinator mode), skip the MeshData codec. `always-proxy` forces the codec path for integration tests and CI.

### Sources worth knowing for anyone extending this design

- [Nakama architecture + session management](https://heroiclabs.com/docs/nakama/getting-started/architecture/) — canonical reference for the composite-key + co-located-auth pattern
- [Metaplay game server architecture](https://docs.metaplay.io/game-server-programming/introduction-to-the-game-server-architecture) — concrete worked example of gateway-node split
- [IT Hare: Front-End Servers and Client-Side Random Load Balancing](http://ithare.com/chapter-vib-server-side-architecture-front-end-servers-and-client-side-random-load-balancing/) — composite session ID rationale
- [SpatialOS v2 runtime: why broadcast fails at scale](https://www.improbable.io/blog/why-did-we-rebuild-the-spatialos-runtime/) — targeted delivery lesson
- [Valve Steam Datagram Relay](https://developer.valvesoftware.com/wiki/Steam_Datagram_Relay) — signed routing tickets (future session-token reference)
- [HAProxy WebSocket load balancing](https://www.haproxy.com/blog/websockets-load-balancing-with-haproxy) — WebSocket stickiness is inherent to the TCP upgrade; no cookies needed
- [Path of Exile 2 seamless zone transfer (GDC 2023)](https://gdcvault.com/play/1029584) — validates the 1-2 tick risk window + interpolation bridging

---

## Current state (audited 2026-04-13)

### What works end-to-end today

- **Coordinator/Node split (S4):** `--mode=coordinator` runs MeshControl on `:9100`, `--mode=node` registers and hosts cells via rendezvous assignment. Heartbeats, graceful leave, crash reassignment all work.
- **Cross-node MeshData routing (S4.5):** cells on different nodes exchange border frames and handoff messages over the gRPC MeshData stream. `PeerList` broadcasts populate each node's `cellToHostMap` + `HostNetwork.peers`. Verified by `TestS45CrossNodeBorderFrameAndHandoff`.
- **Postgres persistence (S5):** player state, marketplace orders/trades, and game config live in Postgres via typed repositories. Verified by `just test-pg`.
- **Handoffs within a process:** `HandoffDriver` drives Border→Promoted→Commit. Entity serialized, epoch bumped, destination cell promotes shadow on commit.
- **In-process multi-host testing:** `--two-hosts` creates two in-process `Host` instances with real `HostNetwork` gRPC loopback.

### Key infrastructure S6 builds on

**`net.ConnManager`** (concrete, `pkg/net/server.go`) — actual method surface:

| Method | Called by | Node-side needed? |
|---|---|---|
| `Send(connID, data)` | engine hot path | YES (virtual impl) |
| `SendReliable(connID, data)` | engine hot path | YES |
| `InjectInput(connID, data)` | `cell.go` ForwardInput handler | YES |
| `DrainInput(connID)` | `input_router.go` (every tick per player) | YES |
| `DrainOpInput(connID)` | ops router | YES |
| `AddTransport(t) uint32` | `HandleWebSocket` only | NO (gateway-only) |
| `HandleWebSocket(w, r)` | example mains | NO (gateway-only) |
| `ActiveConnIDs()` | topology broadcast | NO (gateway-only) |
| `Remove(id)` / `Unregister(id)` | disconnect path | NO (gateway-only) |
| `Events() <-chan PlayerEvent` | coordinator's `routeEvents` loop | NO (gateway-only) |
| `TotalBytesSent/Recv/ConnectionCount` | metrics | NO (gateway-only) |

**Existing proto messages (extended in T1):**
- `MeshFrame.ClientInput { conn_id, data }` (field 9) — **needs** `gateway_id` (field 3)
- `MeshFrame.ClientFrame { conn_id, data }` (field 10) — **needs** `gateway_id` (field 3)
- `MeshFrame.PlayerAssignment { from_cell_id, conn_id, username, is_reconnect, data }` (field 14) — **needs** `gateway_id` + `to_cell_id`
- `MeshFrame.ForwardInput { from_cell_id, conn_id, input_blob }` (field 5) — **needs** `gateway_id`
- `CoordMessage.UpstreamSwitch { session_id, new_host_id }` (field 11) — needs refactor: composite session key instead of uint32 session_id
- **New:** `MeshFrame.ClientDisconnect { gateway_id, conn_id, reason }`
- **New:** `HostMessage.RegisterGateway { gateway_id, grpc_addr, ws_addr }`
- **New:** `HostMessage.PlayerMigrated { gateway_id, conn_id, from_host_id, to_host_id, to_cell_id }`
- **New:** `HostMessage.SessionAnnounce { gateway_id, conn_id, username, target_host_id, target_cell_id }` (gateway → coordinator: "player X is now on me, route notifications here")

### What's missing for S6

1. **No interface abstraction** — engine depends on concrete `*net.ConnManager`
2. **No gateway role/binary mode** — `--mode=gateway` doesn't exist; node mode skips WebSocket entirely
3. **No cross-host handoff notification** — `OnPlayerTransfer` doesn't cross processes
4. **No `UpstreamSwitch` send site**
5. **No `VirtualConnManager`** for nodes
6. **No composite session key** — connIDs are per-gateway-local but treated as globally unique
7. **No gateway registry** — coordinator only tracks nodes
8. **Login flow is coordinator-local** — no gateway handoff for the auth path

---

## Target architecture

### Three process types, runnable in any combination

```
┌─────────────────────┐      MeshControl     ┌─────────────────────┐
│     Coordinator     │◄─────────────────────│     Gateway (1..M)  │
│  (HostRegistry,     │                      │  (WebSocket,        │
│   AssignEngine,     │                      │   LoginHandler,     │
│   GatewayRegistry,  │                      │   sessionRoutes)    │
│   Admin console)    │                      └──────────┬──────────┘
└──────────┬──────────┘                                 │
           │                                            │ MeshData
           │ MeshControl                                │ (ClientInput/
           │                                            │  ClientFrame)
           ▼                                            ▼
┌─────────────────────┐      MeshData        ┌─────────────────────┐
│      Node 1..N      │◄────────────────────►│      Node 1..N      │
│   (Cells,           │   border frames +    │   (Cells,           │
│    game loop,       │   handoffs +         │    game loop,       │
│    VirtualConnMgr,  │   ClientInput/       │    VirtualConnMgr)  │
│    HandoffDriver)   │   ClientFrame proxy  │                     │
└─────────────────────┘                      └─────────────────────┘
```

**In-process modes:**
- `--mode=all-in-one` — one process runs coordinator + gateway + single host with all cells. Default for dev.
- `--mode=coordinator` — coordinator + in-process gateway + no local cells. Nodes connect externally.
- `--mode=node --coordinator-addr=...` — nodes connect to coordinator, receive cells. No WebSocket listener.
- `--mode=gateway --coordinator-addr=...` — standalone gateway process. Accepts WebSockets, registers with coordinator, opens MeshData streams to nodes lazily. No cells, no admin console.

The **coordinator process always runs an in-process gateway role** (serves WebSockets directly) unless the operator explicitly disables it with `--no-inproc-gateway`. This keeps the "single process for dev" story simple while the standalone gateway exists for scale-out.

### Design principles

1. **Gateway is a role, runnable in-process or standalone.** Same code path for both; the gateway worker doesn't care whether it's embedded in coordinator mode or running as its own process.
2. **Composite session key `{GatewayID, ConnID}` everywhere.** Internal only — clients never see it. No coordinator round-trip at login. Scales to arbitrary gateway counts.
3. **Narrow `ConnSender` interface on the engine side.** Concrete `*net.ConnManager` (full method surface) only used by gateway-role code.
4. **Extend existing session tracking.** `c.connIndex` becomes `c.sessionRoutes` keyed on `SessionKey{GatewayID, ConnID}`, value is `SessionRoute{HostID, CellID, Epoch}`.
5. **Epoch-discriminated routing.** Every routing entry carries a monotonic epoch. Cross-host handoff bumps it. Virtual conn managers on nodes compare epoch on inbound input and drop stale.
6. **Targeted `UpstreamSwitch`.** Coordinator sends to just the gateway holding the session. The gatewayID in the composite key gives the routing directly. No broadcast.
7. **Gateway runs `LoginHandler` inline.** Uses cached `PeerList` for `PlayerRouter`. Async `SessionAnnounce` to coordinator. Zero login round-trip.
8. **`MeshControl` reused** with `RegisterGateway` variant. Parallel `gatewayStreams` map on `meshControlServer`.
9. **`local-shortcut` (default) vs `always-proxy`** gateway mode. In-process coordinator+gateway+local-host can skip MeshData for same-process sessions. `always-proxy` forces the codec path.
10. **Session tokens deferred.** Gateway crash → client reconnect → full re-login. T13+ adds tokens.

### Session key + routing table

```go
// pkg/universe/session_routes.go

// SessionKey uniquely identifies a client session across the entire
// cluster. GatewayID is the stable identifier of the gateway process
// that terminates the WebSocket; ConnID is local to that gateway.
// Composite because connIDs are gateway-local monotonic counters and
// not globally unique.
type SessionKey struct {
    GatewayID string
    ConnID    uint32
}

func (k SessionKey) String() string {
    return k.GatewayID + ":" + strconv.FormatUint(uint64(k.ConnID), 10)
}

// SessionRoute is the coordinator's authoritative record of which
// node+cell holds the player entity for a given session.
type SessionRoute struct {
    Key      SessionKey
    Username string
    HostID   string  // node hosting the session's cell
    CellID   string  // specific cell within that node
    Epoch    uint64  // incremented on every cross-host migration
}

// sessionRoutes is the coordinator's authoritative SessionKey →
// SessionRoute map. Guarded by a dedicated RWMutex so gateway-proxy
// hot-path reads don't contend with control-plane writes on the
// broader coordinator mu. Only the coordinator process holds this —
// gateways keep their own local socket maps keyed on ConnID.
type sessionRoutes struct {
    mu     sync.RWMutex
    routes map[SessionKey]*SessionRoute
}
```

**What each process type holds:**

| Process | sessionRoutes (cluster-wide) | Local session state |
|---|---|---|
| Coordinator | YES (authoritative) | - |
| Gateway | NO | `map[ConnID]*localSession` (own sessions only) |
| Node | NO | `map[SessionKey]*virtualSession` in `VirtualConnManager` |

The gateway never asks the coordinator "where does this session go?" at the hot path — it caches the answer during login (from its own `PlayerRouter` call using cached `PeerList` data). The coordinator only hears about the session via the async `SessionAnnounce` + `PlayerMigrated` flow.

### Gateway role + in-process embedding

```go
// pkg/universe/gateway.go

// Gateway is the role/worker that terminates WebSocket connections,
// runs the login handler, and proxies client I/O to the authoritative
// node via MeshData. A Gateway can be embedded in the coordinator
// process (all-in-one or coordinator mode with --inproc-gateway) or
// run standalone via --mode=gateway.
//
// In all modes, the Gateway holds:
// - Its own ConnManager (the real WebSocket server)
// - A local map of ConnID → localSession for sessions it terminates
// - Its own cached cellToHostMap (populated from PeerList broadcasts)
// - A MeshControl bidi stream to the coordinator (for SessionAnnounce,
//   heartbeats, UpstreamSwitch)
// - Lazy MeshData streams to nodes (opened on first session routed
//   there, reused for all subsequent sessions)
type Gateway struct {
    id           string               // stable gateway ID
    coordAddr    string               // coordinator's MeshControl addr
    connMgr      *net.ConnManager     // real WebSocket server
    loginHandler LoginHandler         // game-provided auth
    playerRouter PlayerRouter         // game-provided routing

    // Local session map (guarded by mu)
    mu       sync.RWMutex
    sessions map[uint32]*localSession

    // Cached mesh topology (populated by PeerList broadcasts from coordinator)
    topology *cachedTopology

    // Control + data connections
    controlClient *meshControlClient      // bidi to coordinator
    nodeStreams   *nodeMeshStreams        // lazy MeshData streams to nodes
}

type localSession struct {
    connID   uint32
    username string
    hostID   string  // current authoritative host
    cellID   string
    epoch    uint64  // current epoch (updated on UpstreamSwitch)
}
```

**In-process embedding:** when `Coordinator.cfg.Mode == "all-in-one"` or `"coordinator"` with `--inproc-gateway`, `Build()` constructs a `Gateway` with:
- `id = "inproc"` (or `cfg.GatewayID` if explicitly set)
- `connMgr = c.ConnMgr` (shared with the coordinator's WebSocket server)
- `controlClient = nil` (the in-process gateway calls coordinator methods directly)
- `nodeStreams` = reuses the coordinator's own `HostNetwork` for cross-process node dispatch

**Standalone mode:** `--mode=gateway --coordinator-addr=...` constructs a fresh `Gateway` with:
- Random or flag-provided `GatewayID`
- Fresh `*net.ConnManager`
- Full `meshControlClient` dialing the coordinator
- Fresh `nodeStreams` (opens MeshData streams to nodes lazily via PeerList data)
- No coordinator in the same process; everything goes over the wire

### GatewayRegistry on coordinator

Parallel to `HostRegistry`. Tracks live gateways for control-plane routing (targeted `UpstreamSwitch` dispatch) and liveness watching.

```go
// pkg/universe/gateway_registry.go

type RemoteGateway struct {
    ID             string
    WSAddr         string       // where clients connect
    GRPCAddr       string       // for MeshData streams from nodes
    RegisteredAt   time.Time
    LastHeartbeat  time.Time
    State          RemoteGatewayState
    Sessions       map[SessionKey]bool  // SessionKeys terminated by this gateway
}

type RemoteGatewayState uint8

const (
    RemoteGatewayUnknown RemoteGatewayState = iota
    RemoteGatewayRegistered
    RemoteGatewayLive
    RemoteGatewayDead
    RemoteGatewayLeaving
)

type GatewayRegistry struct {
    mu       sync.RWMutex
    gateways map[string]*RemoteGateway
}
```

Gateway dead-threshold: **5s** (vs nodes' 3s — gateway death is more user-visible, more grace for restart).

### Flow diagrams

**Login (standalone gateway mode):**

```
Client → Gateway: WebSocket upgrade
Gateway: mint local connID=42
Client → Gateway: LoginMsg bytes
Gateway: run LoginHandler(bytes) → username="alice"
Gateway: PlayerRouter("alice") → "cell_2_1" (using cached topology)
Gateway: lookup cell_2_1 in cached cellToHostMap → hostID="node-beta"
Gateway: sessions[42] = &localSession{connID:42, username:"alice", hostID:"node-beta", cellID:"cell_2_1", epoch:1}
Gateway → Coordinator (MeshControl): HostMessage.SessionAnnounce{gatewayID:"gw-1", connID:42, username:"alice", targetHost:"node-beta", targetCell:"cell_2_1"}
Coordinator: sessionRoutes[{gw-1,42}] = {gw-1, 42, "alice", "node-beta", "cell_2_1", epoch:1}
Gateway → Node: MeshData.PlayerAssignment{gatewayID:"gw-1", connID:42, username:"alice", toCellID:"cell_2_1"}
Node: VirtualConnManager.RegisterSession({gw-1, 42}, "alice", epoch:1)
Node: forward PlayerAssignment to cell_2_1 inbox → game loop spawns player entity
```

No blocking round-trip. `SessionAnnounce` is fire-and-forget; if the coordinator crashes before processing it, the next PeerList reconciliation rebuilds the routing from node heartbeats (future work).

**Client input (standalone gateway mode):**

```
Client → Gateway: input bytes
Gateway: conn.input queue fills
Gateway per-session goroutine: drain input → forward via MeshData
Gateway → Node: MeshData.ClientInput{gatewayID:"gw-1", connID:42, data:bytes}
Node: meshDataServer dispatch → VirtualConnManager.InjectInput({gw-1,42}, bytes)
Node game loop: next tick, input_router.DrainInput({gw-1,42}) picks up the bytes
Node game loop: processes input, updates entity state, queues replication frame
Node game loop system: ConnMgr.Send({gw-1,42}, frame) → VirtualConnManager.Send
VirtualConnManager: encode MeshFrame.ClientFrame{gatewayID:"gw-1", connID:42, data:frame}
Node → Gateway: via MeshData stream (looked up by gatewayID in node's peer map)
Gateway: meshDataServer dispatch → connMgr.Send(42, frame) → real WebSocket
Client: receives server frame
```

**Cross-host handoff:**

```
Node A HandoffDriver: entity crosses cell boundary, destination is on Node B
Node A: sends MeshFrame.HandoffPrepare + HandoffCommit via grpcBridge (existing)
Node A: handoff commits locally, bridge.OnPlayerTransfer fires
Node A → Coordinator (MeshControl): HostMessage.PlayerMigrated{gatewayID, connID, fromHost, toHost, toCell}
Coordinator: sessionRoutes[{gw-1,42}].HostID = toHost; bump Epoch
Coordinator → Gateway (MeshControl): CoordMessage.UpstreamSwitch{sessionKey, newHostID, newEpoch}
                    (targeted send — Coordinator looks up gateway stream via sessionKey.GatewayID)
Gateway: sessions[42].hostID = toHost; sessions[42].epoch = newEpoch
Gateway per-session goroutine: subsequent input now forwards to Node B
Coordinator → Node A (MeshControl): CoordMessage.UpstreamSwitch (targeted — or nodes get a broader signal via PeerList)
Node A VirtualConnManager: DropSession({gw-1,42})
Node B: already has the session registered via prior PlayerAssignment during HandoffPrepare flow
```

The risk window is 1-2 ticks. The client's dead-reckoning interpolator bridges the gap invisibly.

### Out of scope for S6

- **Session tokens + gateway/coordinator crash recovery** — T13+. Without tokens, gateway crash = client reconnect + full re-login.
- **Multiple coordinators (HA)** — different concern. One coordinator per cluster for S6.
- **Gateway load balancer config** — operator responsibility. HAProxy/NGINX/ALB all work with WebSocket upgrade stickiness out of the box.
- **UDP proxying for native clients** — WebSocket only for S6.
- **Input rate limiting per session** — hardening task; strong recommendation from research but not capstone.
- **Cross-region gateway failover** — operational.
- **Distributed cell splits/merges across nodes** — S7.
- **Gateway-initiated cell migration hints** — coordinator still owns assignment.

---

## File structure

### Created

| Path | Responsibility |
|---|---|
| `pkg/net/conn_sender.go` | `ConnSender` interface + `*ConnManager` compile-time assertion |
| `pkg/universe/session_routes.go` | `SessionKey`, `SessionRoute`, `sessionRoutes` types |
| `pkg/universe/gateway.go` | `Gateway` role (worker type, embeddable or standalone) |
| `pkg/universe/gateway_registry.go` | `GatewayRegistry` on coordinator (parallel to HostRegistry) |
| `pkg/universe/virtual_conn_manager.go` | Node-side `VirtualConnManager` implementing `ConnSender` |
| `pkg/universe/mesh_gateway_client.go` | Standalone gateway's `meshControlClient` (dials coordinator, handles RegisterGateway handshake, heartbeats, UpstreamSwitch dispatch) |
| `pkg/universe/s6_gateway_test.go` | Integration test: coord + 2 nodes + standalone gateway + fake client + cross-host handoff |

### Modified

| Path | What changes |
|---|---|
| `proto/meshpb/mesh.proto` | Add `gateway_id` to `ClientInput`, `ClientFrame`, `PlayerAssignment`, `ForwardInput`; add `ClientDisconnect` variant to `MeshFrame`; add `RegisterGateway`, `SessionAnnounce`, `PlayerMigrated` variants to `HostMessage`; refactor `UpstreamSwitch` to use composite session key |
| `pkg/engine/engine.go` | `ConnMgr` field type changes from `*net.ConnManager` to `net.ConnSender` |
| `pkg/universe/coordinator.go` | Replace `connIndex` with `sessionRoutes`; add `GatewayRegistry` field; `Config.GatewayMode` gains `always-proxy` value; `Config.GatewayID` + `Config.NoInprocGateway`; Build() branches on new `Mode == "gateway"` value + wires in-process gateway when applicable |
| `pkg/universe/mesh_data_server.go` | Route inbound `ClientInput` → `VirtualConnManager.InjectInput`; route inbound `ClientFrame` → gateway's real `ConnMgr.Send`; route `PlayerAssignment` → cell inbox + `VirtualConnManager.RegisterSession`; route `ClientDisconnect` → cell + `VirtualConnManager.DropSession` |
| `pkg/universe/mesh_control_server.go` | Dispatch on first message: `RegisterHost` (existing) vs `RegisterGateway` (new); parallel `gatewayStreams` map; handle `SessionAnnounce`, `PlayerMigrated`; implement targeted `UpstreamSwitch` dispatch; gateway liveness watcher (5s dead-threshold) |
| `pkg/universe/mesh_control_client.go` | Gains gateway registration path (when running as gateway role); dispatch `UpstreamSwitch` → local gateway session update |
| `pkg/universe/handoff_driver.go` | After commit dispatch, send `PlayerMigrated` via MeshControl when srcHost != destHost |
| `pkg/universe/cell_bridge_impl.go` + `grpc_bridge.go` | `OnPlayerTransfer` emits `PlayerMigrated` instead of updating `connIndex` directly |
| `pkg/universe/cell.go` | Handle new `MsgPlayerDisconnected` CellMessage type |
| `pkg/universe/message.go` | Add `MsgPlayerDisconnected` constant + `DisconnectPayload` struct |
| `pkg/universe/host_network.go` | `peers` map entries gain a `peerKind` field (node vs gateway); `ConnectPeer` accepts a kind; send methods unchanged |
| `cmd/server/main.go` | `--mode=gateway` + `--gateway-id` + `--gateway-mode` + `--no-inproc-gateway` flags plumbed |
| `examples/4node-basic/main.go` | Support `--mode=gateway` + coordinator mode runs in-process gateway by default; demo becomes 4-process (coord, gateway, node, node) playable |
| `CLAUDE.md` | Multi-process gameplay section rewritten |

### Deleted

Nothing.

---

## Task breakdown

### Task 1: Proto schema extension for gateway support

**Files:** `proto/meshpb/mesh.proto`

All the wire format changes land in one commit so downstream tasks can reference them cleanly. This is the **most constraining** piece of S6 — doing it now avoids a breaking change later.

- [ ] **Step 1: Extend MeshFrame variants with `gateway_id`**

```protobuf
message ClientInput {
  string gateway_id = 3;  // NEW — empty string = in-process / not-yet-assigned
  uint32 conn_id    = 1;
  bytes  data       = 2;
}

message ClientFrame {
  string gateway_id = 3;  // NEW
  uint32 conn_id    = 1;
  bytes  data       = 2;
}

message PlayerAssignment {
  string from_cell_id = 1;
  uint32 conn_id      = 2;
  string username     = 3;
  bool   is_reconnect = 4;
  bytes  data         = 5;
  string to_cell_id   = 6;  // NEW — coordinator tells node which cell to spawn in
  string gateway_id   = 7;  // NEW
}

message ForwardInput {
  string from_cell_id = 1;
  uint32 conn_id      = 2;
  bytes  input_blob   = 3;
  string gateway_id   = 4;  // NEW
}
```

- [ ] **Step 2: New `ClientDisconnect` variant on MeshFrame**

```protobuf
message ClientDisconnect {
  string gateway_id = 1;
  uint32 conn_id    = 2;
  string reason     = 3;
}
```

Add to `MeshFrame.msg` oneof at the next free field number.

- [ ] **Step 3: New HostMessage variants**

```protobuf
message RegisterGateway {
  string gateway_id = 1;
  string ws_addr    = 2;  // where clients connect
  string grpc_addr  = 3;  // where nodes dial MeshData streams back
}

message SessionAnnounce {
  string gateway_id     = 1;
  uint32 conn_id        = 2;
  string username       = 3;
  string target_host_id = 4;
  string target_cell_id = 5;
}

message PlayerMigrated {
  string gateway_id   = 1;
  uint32 conn_id      = 2;
  string from_host_id = 3;
  string to_host_id   = 4;
  string to_cell_id   = 5;
}
```

Add each as a new variant to `HostMessage.msg` oneof.

- [ ] **Step 4: Refactor UpstreamSwitch**

```protobuf
message UpstreamSwitch {
  string gateway_id   = 1;  // CHANGED — was session_id (uint32)
  uint32 conn_id      = 2;  // CHANGED — was part of session_id
  string new_host_id  = 3;
  uint64 new_epoch    = 4;  // NEW — for deadline-free epoch discrimination
}
```

This is a **breaking proto change**. Acceptable because no Go code references `UpstreamSwitch` yet (verified in audit).

- [ ] **Step 5: Regenerate + verify**

```bash
just proto
go vet ./...
```

Existing tests unchanged — no Go code reads the new fields yet.

- [ ] **Step 6: Commit**

```
feat(meshpb): gateway-role proto extensions

Adds gateway_id to MeshFrame.ClientInput, ClientFrame,
PlayerAssignment, and ForwardInput so the wire format carries
a composite session key {GatewayID, ConnID} instead of just a
gateway-local uint32 connID. Required once multiple gateway
instances exist — connIDs are gateway-local monotonic counters
and not globally unique.

Adds MeshFrame.ClientDisconnect for graceful disconnect
propagation from gateway to node.

Adds HostMessage variants:
- RegisterGateway: gateway's first message on a MeshControl stream
- SessionAnnounce: gateway tells coordinator "player X is on me"
- PlayerMigrated: handoff-source node tells coordinator about a
  cross-host entity migration

UpstreamSwitch CoordMessage is refactored to carry the composite
session key and a new_epoch field. Breaking proto change but no
Go consumer exists yet (verified in codebase audit).

PlayerAssignment gains to_cell_id (6) so the coordinator can tell
the node which cell to spawn the player in without the node
needing its own routing table.

This is the most constraining S6 change — landing it now avoids
breaking the wire format when standalone gateway mode is
implemented. Downstream tasks consume these types.
```

---

### Task 2: Narrow `ConnSender` interface + engine decoupling

**Files:** `pkg/net/conn_sender.go` (new), `pkg/engine/engine.go`, call sites

Same as v1 plan. The interface is:

```go
type ConnSender interface {
    Send(connID uint32, data []byte)
    SendReliable(connID uint32, data []byte)
    InjectInput(connID uint32, data []byte)
    DrainInput(connID uint32) [][]byte
    DrainOpInput(connID uint32) [][]byte
}
```

- [ ] **Step 1: Create `pkg/net/conn_sender.go`** with the interface + `var _ ConnSender = (*ConnManager)(nil)`
- [ ] **Step 2: Change `engine.Engine.ConnMgr` type** from `*net.ConnManager` to `net.ConnSender`
- [ ] **Step 3: Update call sites.** Gateway-only methods (`HandleWebSocket`, `ActiveConnIDs`, `Events`, `AddTransport`, `Remove`, `Unregister`, byte counters) stay on the concrete type held separately by `Coordinator.ConnMgr`. Engine consumers use the narrow interface.
- [ ] **Step 4: Verify** `go vet ./... && go test ./... && just build`
- [ ] **Step 5: Commit**

```
refactor(net): extract narrow ConnSender interface for engine hot path
```

---

### Task 3: `SessionKey` + `sessionRoutes` typed map

**Files:** `pkg/universe/session_routes.go` (new), `pkg/universe/coordinator.go`

- [ ] **Step 1: Create `session_routes.go`** with:

```go
type SessionKey struct {
    GatewayID string
    ConnID    uint32
}
func (k SessionKey) String() string

type SessionRoute struct {
    Key      SessionKey
    Username string
    HostID   string
    CellID   string
    Epoch    uint64
}

type sessionRoutes struct {
    mu     sync.RWMutex
    routes map[SessionKey]*SessionRoute
}

func newSessionRoutes() *sessionRoutes
func (r *sessionRoutes) Set(route *SessionRoute)
func (r *sessionRoutes) Get(key SessionKey) (*SessionRoute, bool)  // deep copy
func (r *sessionRoutes) Remove(key SessionKey)
func (r *sessionRoutes) Migrate(key SessionKey, newHost, newCell string) (uint64, bool)
func (r *sessionRoutes) RemoveByGateway(gatewayID string) int  // for gateway crash cleanup
func (r *sessionRoutes) RemoveByHost(hostID string) int        // for host crash cleanup
```

Dedicated RWMutex so gateway-proxy hot-path reads don't contend with the broader coordinator `mu`.

- [ ] **Step 2: Replace `c.connIndex`** on Coordinator with `c.sessionRoutes *sessionRoutes`. Update every read/write site in `coordinator.go`:
  - `setPlayerNode(connID, nodeID)` → `sessionRoutes.Set(&SessionRoute{Key: SessionKey{GatewayID: "inproc", ConnID: connID}, ...})`
  - `removePlayerNode(connID)` → `sessionRoutes.Remove(...)`
  - `getPlayerNode(connID)` → lookup

Use `"inproc"` as the gateway ID for the in-process gateway role. Standalone gateways pick their own ID.

- [ ] **Step 3: Verify + commit**

```
refactor(universe): SessionKey + sessionRoutes replace c.connIndex

Composite SessionKey{GatewayID, ConnID} identifies sessions
uniquely across N gateway processes. The in-process gateway role
(coordinator mode with --inproc-gateway default) uses "inproc" as
its gateway ID; standalone gateways pick their own.

sessionRoutes guards the map with a dedicated RWMutex so gateway
proxy reads don't contend with the broader coordinator mu.
```

---

### Task 4: `GatewayRegistry` + MeshControl dispatch on first message

**Files:** `pkg/universe/gateway_registry.go` (new), `pkg/universe/mesh_control_server.go`

- [ ] **Step 1: Create `GatewayRegistry`**

Parallel to `HostRegistry`. Tracks `map[gatewayID]*RemoteGateway`, supports `Register`, `Touch`, `MarkDead`, `MarkLeaving`, `Remove`, `LiveGateways`. Gateway state: `Unknown | Registered | Live | Dead | Leaving`.

- [ ] **Step 2: Dispatch on first message in `meshControlServer.Control`**

```go
first, err := stream.Recv()
switch v := first.Msg.(type) {
case *meshpb.HostMessage_Register:
    return s.handleHostControl(stream, v.Register)
case *meshpb.HostMessage_RegisterGateway:
    return s.handleGatewayControl(stream, v.RegisterGateway)
default:
    return fmt.Errorf("first message must be RegisterHost or RegisterGateway, got %T", first.Msg)
}
```

Move existing host handling to `handleHostControl`. New `handleGatewayControl` mirrors it with gateway semantics: insert into `GatewayRegistry`, send `RegisterAck`, drain messages handling `Heartbeat`, `SessionAnnounce`, `PlayerMigrated`, and the existing graceful-leave detection.

- [ ] **Step 3: `gatewayStreams` parallel map**

```go
type meshControlServer struct {
    // ... existing fields ...
    gatewayStreams map[string]meshpb.MeshControl_ControlServer
    gatewayMu      map[string]*sync.Mutex
    gatewayKill    map[string]chan struct{}
    gatewayRegistry *GatewayRegistry
}
```

Parallel to the host-side maps. `sendCoordMessage` becomes `sendCoordMessageToHost` + `sendCoordMessageToGateway`. Existing callers update.

- [ ] **Step 4: Gateway liveness watcher**

Mirror of `checkLiveness` for nodes, but with 5s dead-threshold:

```go
const gatewayDeadThreshold = 5 * time.Second
```

When a gateway is marked dead, clean up `sessionRoutes` entries via `RemoveByGateway(gatewayID)`. Log the count cleaned. Sessions are not reassigned — clients will reconnect.

- [ ] **Step 5: Verify + commit**

```
feat(universe): GatewayRegistry + RegisterGateway dispatch

MeshControl.Control now dispatches on the first message variant:
RegisterHost opens a node control stream (existing path unchanged);
RegisterGateway opens a gateway control stream stored in a parallel
gatewayStreams map.

GatewayRegistry tracks remote gateways with a 5s dead-threshold
(vs 3s for nodes — gateway death is more user-visible, more grace
for restart). Dead gateway cleanup removes its sessions from
sessionRoutes; clients will reconnect.

This is the control-plane prerequisite for standalone --mode=gateway
in T9. Existing node path is unchanged; existing tests pass.
```

---

### Task 5: `Gateway` role + in-process embedding

**Files:** `pkg/universe/gateway.go` (new), `pkg/universe/coordinator.go`

This is the core gateway worker that runs in two modes: embedded in the coordinator process or standalone. Both share identical code.

- [ ] **Step 1: `Gateway` type** with fields from the architecture section above.

```go
type Gateway struct {
    id           string
    coordAddr    string               // "" when embedded in coordinator
    connMgr      *net.ConnManager
    loginHandler LoginHandler
    playerRouter PlayerRouter
    log          *logger.Logger

    mu       sync.RWMutex
    sessions map[uint32]*localSession

    topology *cachedTopology  // cellID → hostID, updated by PeerList

    // Only non-nil when running standalone:
    controlClient *meshControlClient
}
```

- [ ] **Step 2: Gateway drain loop** per active session. Polls `connMgr.DrainInput(connID)` and forwards to `hostID` via MeshData:

```go
func (g *Gateway) runSessionPump(connID uint32) {
    defer g.StopSession(connID)
    for {
        select {
        case <-g.done:
            return
        default:
        }
        msgs := g.connMgr.DrainInput(connID)
        ops  := g.connMgr.DrainOpInput(connID)
        if len(msgs) == 0 && len(ops) == 0 {
            time.Sleep(time.Millisecond)  // yield; tune later
            continue
        }
        sess := g.lookupSession(connID)
        if sess == nil {
            continue
        }
        // local-shortcut check
        if g.isLocalShortcut(sess.hostID) {
            continue  // local input_router drains directly
        }
        for _, m := range msgs {
            frame := buildClientInputFrame(g.id, connID, m)
            _ = g.sendToNode(sess.hostID, frame, lossy)
        }
        for _, m := range ops {
            frame := buildClientInputFrame(g.id, connID, m)
            _ = g.sendToNode(sess.hostID, frame, reliable)
        }
    }
}
```

The `sendToNode` helper either uses the gateway's own `meshControlClient` to dial-and-stream (standalone) or the coordinator's `HostNetwork` (embedded). A small `NodeStreamer` interface abstracts this.

- [ ] **Step 3: Login handling**

`Gateway.OnConnect(connID)` gets called from `connMgr.Events()`. On first message batch:

```go
func (g *Gateway) processLogin(connID uint32, msgs [][]byte) error {
    username, data, err := g.loginHandler(connID, msgs)
    if err != nil {
        return err
    }
    cellID := g.playerRouter(username)            // pure function, uses cached topology
    hostID := g.topology.HostForCell(cellID)      // cached from PeerList
    if hostID == "" {
        return fmt.Errorf("no host for cell %s", cellID)
    }
    sess := &localSession{connID: connID, username: username, hostID: hostID, cellID: cellID, epoch: 1}
    g.mu.Lock()
    g.sessions[connID] = sess
    g.mu.Unlock()

    // Announce to coordinator (async, fire-and-forget)
    g.announceSession(sess)

    // Send PlayerAssignment to target node via MeshData
    assign := &meshpb.MeshFrame{
        Msg: &meshpb.MeshFrame_PlayerAssignment{
            PlayerAssignment: &meshpb.PlayerAssignment{
                GatewayId:   g.id,
                ConnId:      connID,
                Username:    username,
                ToCellId:    cellID,
                IsReconnect: false,
                Data:        encodeLoginData(data),
            },
        },
    }
    g.sendToNode(hostID, assign, reliable)

    // Start the per-session drain goroutine
    go g.runSessionPump(connID)
    return nil
}
```

The login flow runs entirely on the gateway — no synchronous coordinator round-trip.

`g.announceSession` sends `HostMessage.SessionAnnounce` via the control stream (standalone) or calls `coord.sessionRoutes.Set(...)` directly (embedded).

- [ ] **Step 4: Embed into coordinator Build()**

In `coordinator.go` `Build()`, when `Mode != "node"` and `!cfg.NoInprocGateway`, construct an in-process `Gateway`:

```go
if cfg.Mode != "node" && !cfg.NoInprocGateway {
    gwID := cfg.GatewayID
    if gwID == "" {
        gwID = "inproc"
    }
    c.gateway = &Gateway{
        id:           gwID,
        connMgr:      c.ConnMgr,   // shared with the coordinator's listener
        loginHandler: cfg.LoginHandler,
        playerRouter: /* adapted from cfg.PlayerRouter */,
        log:          c.Log,
        topology:     newCachedTopology(c),  // directly reads coordinator state
        // controlClient nil — embedded mode talks to coord directly
    }
    // ... wire into coordinator's routeEvents loop ...
}
```

The coordinator's existing `routeEvents` goroutine now delegates login handling to `c.gateway.processLogin(...)` instead of running it inline. Non-gateway coordinator modes (pure `coordinator` mode with `--no-inproc-gateway`) keep the existing login logic as a fallback for tests that don't need gateway behavior.

- [ ] **Step 5: Verify + commit**

```
feat(universe): Gateway role + in-process embedding

Gateway is the worker type that terminates WebSocket connections,
runs LoginHandler inline, and proxies client I/O to authoritative
nodes via MeshData. Runs either embedded in the coordinator
process (all-in-one or coordinator mode, default) or standalone
via --mode=gateway (T9).

Login flow runs entirely on the gateway: LoginHandler → PlayerRouter
(using cached PeerList topology) → local session record →
fire-and-forget SessionAnnounce to coordinator → PlayerAssignment
to target node via MeshData → per-session drain goroutine. No
synchronous coordinator round-trip at login time.

The per-session drain goroutine uses 1ms polling over
connMgr.DrainInput for v1; channel-driven design is a follow-up
optimization if CPU becomes a concern.

local-shortcut mode: when the session's target host is colocated
with the gateway (in-process coordinator + local cell owner), the
drain loop skips MeshData forwarding; the local input_router
drains the same queue directly.
```

---

### Task 6: `VirtualConnManager` on nodes + extended `HostNetwork`

**Files:** `pkg/universe/virtual_conn_manager.go` (new), `pkg/universe/host_network.go`, `pkg/universe/mesh_data_server.go`, `pkg/universe/coordinator.go`

- [ ] **Step 1: Extend `HostNetwork.peers` with peer kind**

```go
type peerKind uint8

const (
    peerKindNode peerKind = iota
    peerKindGateway
)

type hostPeer struct {
    // ... existing fields ...
    kind peerKind
}

// ConnectPeer signature gains kind parameter (or a new ConnectGateway variant)
func (n *HostNetwork) ConnectPeer(hostID, grpcAddr string, kind peerKind) error
```

Node-side: when a `MeshData.Data` stream is opened FROM a gateway, the receiving node registers the gateway as a `peerKindGateway` peer. Used by `VirtualConnManager.Send` to look up the return path for `ClientFrame`.

- [ ] **Step 2: Create `VirtualConnManager`** implementing `net.ConnSender`

```go
type VirtualConnManager struct {
    coord *Coordinator
    log   *logger.Logger

    mu       sync.RWMutex
    sessions map[SessionKey]*virtualSession
}

type virtualSession struct {
    key      SessionKey
    username string
    epoch    uint64

    inputMu  sync.Mutex
    input    [][]byte
    opInput  [][]byte
}

var _ net.ConnSender = (*VirtualConnManager)(nil)
```

But wait — `ConnSender` takes `connID uint32`, not `SessionKey`. On a node, when the game loop calls `Send(connID, data)`, how does the VCM know which `gatewayID` to use for the return route?

**Resolution:** VCM holds a reverse map `connID → SessionKey`. Every registered session is indexed by both composite key (for incoming lookups from meshDataServer) and bare connID (for outgoing from the game loop). The assumption: on any single node, a given `connID` is unique because the node only ever holds one session per `{GatewayID, ConnID}` pair. If two gateways happen to mint the same connID and both route to the same node, the node's connID→SessionKey lookup is ambiguous. **Fix:** use composite key all the way through the node's engine.ConnMgr path.

This is a bigger refactor than the v1 plan anticipated. The narrow `ConnSender` interface needs to take `SessionKey` instead of `connID`. But that ripples through `engine.input_router`, `pkg/system/frame_writer`, etc.

**Alternative:** on the node side, `ConnSender` still takes `connID uint32` but the node mints a local composite-to-local-uint32 mapping. When a PlayerAssignment arrives with `{gw-1, 42}`, the node allocates a local connID (say, 1001) and maps `1001 ↔ {gw-1, 42}`. Every node-side call to `Send(1001, ...)` is translated back to the composite on the wire.

This is cleaner because it preserves the `uint32 connID` contract for all existing engine code. The translation happens only at the VCM boundary.

**Decision:** go with the alternative. The VCM owns the bidirectional mapping. The node's game loop, input router, and frame writers continue to use `uint32 connID` without knowing about gateway IDs.

```go
type VirtualConnManager struct {
    coord *Coordinator
    log   *logger.Logger

    mu         sync.RWMutex
    nextLocal  uint32
    byLocal    map[uint32]*virtualSession
    byKey      map[SessionKey]*virtualSession
}

type virtualSession struct {
    key      SessionKey   // {gatewayID, originalConnID}
    localID  uint32       // node-local monotonic
    username string
    epoch    uint64
    // ... input queues ...
}
```

`RegisterSession(key SessionKey, username string, epoch uint64)` allocates a new `localID`, stores both indexes. `Send(localID, data)` looks up the `key`, builds a `MeshFrame.ClientFrame{gateway_id: key.GatewayID, conn_id: key.ConnID}`, forwards to the gateway peer.

- [ ] **Step 3: Inbound dispatch in `mesh_data_server.go`**

When the node receives `MeshFrame.ClientInput{gateway_id, conn_id, data}`, look up the session by `SessionKey{gateway_id, conn_id}` in VCM, get the local ID, and `InjectInput(localID, data)`.

When the node receives `MeshFrame.PlayerAssignment{gateway_id, conn_id, username, to_cell_id}`, call `VCM.RegisterSession(...)`, then push the CellMessage into the target cell's inbox with the allocated localID. The cell's game loop sees a normal `MsgPlayerAssignment{connID: 1001}` — it doesn't know about gateways.

- [ ] **Step 4: Wire VCM into node-mode Build()**

In `coordinator.go`, node mode constructs a `VirtualConnManager` and passes it as the engine's `ConnSender`. The coordinator's own `c.ConnMgr` can remain nil in node mode (or a separate stub).

- [ ] **Step 5: Verify + commit**

```
feat(universe): VirtualConnManager on nodes + HostNetwork peer kinds

VirtualConnManager implements net.ConnSender on node-mode
processes. It owns a bidirectional mapping between the wire-format
SessionKey {GatewayID, ConnID} and node-local uint32 connIDs so
the node's engine game loop continues using uint32 without
knowing about gateways. Translation happens at the VCM boundary.

Outbound Send and SendReliable encode MeshFrame.ClientFrame and
forward to the gateway peer looked up in HostNetwork.peers
(tagged with peerKindGateway). Inbound bytes arrive via
meshDataServer's ClientInput dispatch and land in per-session
input buffers that input_router drains every tick.

HostNetwork.peers entries now carry a peerKind enum (node vs
gateway) so Send methods can route correctly.
```

---

### Task 7: Cross-host handoff notification (PlayerMigrated + UpstreamSwitch)

**Files:** `pkg/universe/handoff_driver.go`, `pkg/universe/mesh_control_server.go`, `pkg/universe/mesh_control_client.go`, `pkg/universe/grpc_bridge.go`

- [ ] **Step 1: HandoffDriver sends PlayerMigrated**

After `bridge.OnPlayerTransfer(evt.ConnID, evt.DestCellID)` in the commit path, when the transfer is cross-host (destination host differs from source), send a `HostMessage.PlayerMigrated` via the node's MeshControl stream:

```go
if isCrossHost(sourceHost, destHost) {
    if client := hd.base.coord.controlClient; client != nil {
        _ = client.send(&meshpb.HostMessage{
            Msg: &meshpb.HostMessage_PlayerMigrated{
                PlayerMigrated: &meshpb.PlayerMigrated{
                    GatewayId:  sessionGatewayID,  // from the virtual session
                    ConnId:     sessionConnID,
                    FromHostId: sourceHost,
                    ToHostId:   destHost,
                    ToCellId:   evt.DestCellID,
                },
            },
        })
    }
}
```

- [ ] **Step 2: Coordinator handles PlayerMigrated**

In `handleHostControl`'s recv loop, add:

```go
case *meshpb.HostMessage_PlayerMigrated:
    pm := v.PlayerMigrated
    key := SessionKey{GatewayID: pm.GatewayId, ConnId: pm.ConnId}
    newEpoch, ok := s.coord.sessionRoutes.Migrate(key, pm.ToHostId, pm.ToCellId)
    if !ok {
        s.log.Log(CatMeshCell, "coordinator: PlayerMigrated for unknown session %s", key)
        continue
    }
    // Targeted UpstreamSwitch to the gateway holding this session
    s.dispatchUpstreamSwitch(key, pm.ToHostId, newEpoch)
```

- [ ] **Step 3: Targeted `UpstreamSwitch`**

```go
func (s *meshControlServer) dispatchUpstreamSwitch(key SessionKey, newHost string, newEpoch uint64) {
    msg := &meshpb.CoordMessage{
        CoordEpoch: s.coord.coordEpoch,
        Msg: &meshpb.CoordMessage_UpstreamSwitch{
            UpstreamSwitch: &meshpb.UpstreamSwitch{
                GatewayId:  key.GatewayID,
                ConnId:     key.ConnID,
                NewHostId:  newHost,
                NewEpoch:   newEpoch,
            },
        },
    }
    // Targeted: look up the gateway stream by GatewayID
    if err := s.sendCoordMessageToGateway(key.GatewayID, msg); err != nil {
        s.log.Log(CatMeshCell, "coordinator: UpstreamSwitch to gateway %s failed: %v", key.GatewayID, err)
    }
    // Also notify the losing host so its VCM drops the session
    // (the winning host already has the session registered via prior PlayerAssignment)
    // This could be a separate targeted send to the old host, or rely on the
    // handoff commit flow having already cleaned up. Decide during implementation.
}
```

- [ ] **Step 4: Gateway handles UpstreamSwitch**

On the gateway (embedded or standalone), the `meshControlClient.dispatch` gets a new case:

```go
case *meshpb.CoordMessage_UpstreamSwitch:
    us := v.UpstreamSwitch
    if us == nil { return }
    g := c.coord.gateway  // or standalone gateway ref
    if g == nil { return }
    g.OnUpstreamSwitch(us.ConnId, us.NewHostId, us.NewEpoch)
```

`Gateway.OnUpstreamSwitch(connID, newHost, newEpoch)` updates `sessions[connID].hostID = newHost; .epoch = newEpoch`. Subsequent `ClientInput` frames from this session's drain loop route to the new host.

- [ ] **Step 5: Verify + commit**

```
feat(universe): cross-host handoff notification + UpstreamSwitch targeting

HandoffDriver on the source node sends HostMessage.PlayerMigrated
to the coordinator after committing a cross-host entity transfer.
Coordinator atomically bumps the session's epoch in sessionRoutes
and sends a TARGETED CoordMessage.UpstreamSwitch to the single
gateway holding that session (looked up via SessionKey.GatewayID
→ gatewayStreams). No broadcast.

Gateway updates its local session record on UpstreamSwitch. The
per-session drain loop now forwards to the new host. Risk window
between routing flip and new host's first frame is 1-2 ticks,
bridged invisibly by client dead-reckoning interpolation.
```

---

### Task 8: Disconnect propagation

**Files:** `proto/meshpb/mesh.proto` (already done in T1), `pkg/universe/message.go`, `pkg/universe/cell.go`, `pkg/universe/mesh_data_server.go`, `pkg/universe/gateway.go`

- [ ] **Step 1: `MsgPlayerDisconnected` CellMessage + DisconnectPayload**

In `pkg/universe/message.go`:

```go
MsgPlayerDisconnected MsgType = 107 // next free

type DisconnectPayload struct {
    ConnID uint32
    Reason string
}

// Add to CellMessage:
Disconnect *DisconnectPayload
```

- [ ] **Step 2: Gateway sends ClientDisconnect on WebSocket close**

When `Gateway` gets a `net.PlayerEvent{Disconnect: true, ConnID: n}` from `connMgr.Events()`:

```go
sess := g.lookupAndRemove(connID)
if sess == nil { return }
frame := &meshpb.MeshFrame{
    Msg: &meshpb.MeshFrame_ClientDisconnect{
        ClientDisconnect: &meshpb.ClientDisconnect{
            GatewayId: g.id,
            ConnId:    connID,
        },
    },
}
if !g.isLocalShortcut(sess.hostID) {
    g.sendToNode(sess.hostID, frame, reliable)
}
// Also tell the coordinator so sessionRoutes removes the entry
g.announceDisconnect(sess.key)
```

- [ ] **Step 3: Node handles ClientDisconnect**

`mesh_data_server.go` routes `ClientDisconnect` via VCM:

```go
case *meshpb.MeshFrame_ClientDisconnect:
    cd := v.ClientDisconnect
    key := SessionKey{GatewayID: cd.GatewayId, ConnID: cd.ConnId}
    vcm := s.coord.virtualConnMgr()
    if vcm == nil { return nil }
    localID, ok := vcm.DropSession(key)
    if !ok { return nil }
    // Route to cell as MsgPlayerDisconnected
    return s.routeDisconnectToCell(localID, cd.Reason)
```

- [ ] **Step 4: Cell handles MsgPlayerDisconnected**

`cell.go` `DrainInbox` case forwards to `eng.Players.OnDisconnect(localID)` which triggers the existing grace-period logic.

- [ ] **Step 5: Verify + commit**

```
feat(universe): disconnect propagation via MeshData.ClientDisconnect
```

---

### Task 9: `--mode=gateway` standalone binary mode

**Files:** `pkg/universe/mesh_gateway_client.go` (new), `pkg/universe/coordinator.go`, `cmd/server/main.go`

- [ ] **Step 1: `meshGatewayClient` — dials coordinator as a gateway**

Mirror of `meshControlClient` but sends `RegisterGateway` as the first message. Handles heartbeats, dispatches `UpstreamSwitch`, reconnects with exponential backoff (same pattern as S4).

- [ ] **Step 2: `Coordinator.Build()` branch for `Mode == "gateway"`**

```go
if cfg.Mode == "gateway" {
    if cfg.CoordinatorAddr == "" {
        return fmt.Errorf("gateway mode requires CoordinatorAddr")
    }
    gwID := cfg.GatewayID
    if gwID == "" {
        gwID = "gateway-" + strconv.FormatInt(time.Now().UnixNano(), 36)
    }
    // Build the gateway worker
    c.gateway = &Gateway{
        id:           gwID,
        coordAddr:    cfg.CoordinatorAddr,
        connMgr:      c.ConnMgr,
        loginHandler: cfg.LoginHandler,
        playerRouter: /* adapted — uses cached topology instead of coord.NodeAtPosition */,
        log:          c.Log,
        topology:     newCachedTopology(nil),  // populated by control-plane PeerList
    }
    // Open the gateway control stream
    gc, err := newMeshGatewayClient(c.gateway, cfg.CoordinatorAddr)
    if err != nil {
        return err
    }
    c.gateway.controlClient = gc
    gc.Start(ctx)
    // Start the WebSocket listener (ConnMgr handles it)
    // Gateway mode does NOT run: AssignmentEngine, HostRegistry, admin console, MeshControl server
}
```

- [ ] **Step 3: `cmd/server/main.go` flags**

```go
var (
    mode            = flag.String("mode", "all-in-one", "mode: all-in-one | coordinator | node | gateway")
    gatewayID       = flag.String("gateway-id", "", "stable gateway identifier (gateway mode only)")
    gatewayMode     = flag.String("gateway-mode", "local-shortcut", "local-shortcut | always-proxy")
    noInprocGateway = flag.Bool("no-inproc-gateway", false, "disable the in-process gateway on coordinator mode")
    coordinatorAddr = flag.String("coordinator-addr", "", "coordinator MeshControl address (node + gateway modes)")
)
```

- [ ] **Step 4: Smoke test**

```bash
just db-up
./bin/server --mode=coordinator --no-inproc-gateway &
./bin/server --mode=node --coordinator-addr=localhost:9100 --host-id=alpha &
./bin/server --mode=node --coordinator-addr=localhost:9100 --host-id=beta &
./bin/server --mode=gateway --coordinator-addr=localhost:9100 --gateway-id=gw-1 &
# Now 4 processes. Connect a browser to the gateway's port (default :8080).
```

- [ ] **Step 5: Commit**

```
feat(universe): --mode=gateway standalone binary

Gateway mode dials the coordinator via meshGatewayClient (mirror
of meshControlClient but sends RegisterGateway as the first
message), runs a local WebSocket server, and proxies client I/O
to nodes via MeshData streams opened lazily.

A standalone gateway does NOT run: AssignmentEngine, HostRegistry,
admin console, or MeshControl server. It's pure I/O: terminate
WebSocket, run LoginHandler, forward to MeshData, proxy server
frames back.

Gateway ID is supplied via --gateway-id (or auto-generated with
a timestamp suffix if omitted). --no-inproc-gateway disables the
in-process gateway on coordinator mode so a standalone gateway
can take its place without port conflicts.
```

---

### Task 10: `--gateway-mode=local-shortcut` vs `always-proxy`

**Files:** `pkg/universe/gateway.go`, `pkg/universe/coordinator.go`

- [ ] **Step 1: `isLocalShortcut` helper**

```go
func (g *Gateway) isLocalShortcut(hostID string) bool {
    if g.coord != nil && g.coord.cfg.GatewayMode == "always-proxy" {
        return false
    }
    // In-process: gateway shares the coordinator, which knows the local host
    if g.coord != nil {
        local := g.coord.localHost()
        if local != nil && local.ID == hostID {
            return true
        }
    }
    // Standalone: never a local shortcut (gateway doesn't own cells)
    return false
}
```

- [ ] **Step 2: Apply in drain loop** (already referenced in T5) — gateway drain loop skips MeshData forwarding when `isLocalShortcut` returns true.

- [ ] **Step 3: Smoke test interactively** both modes — the in-process gateway with default mode should behave identically to today's single-process dev mode.

- [ ] **Step 4: Commit**

```
feat(universe): --gateway-mode local-shortcut vs always-proxy
```

---

### Task 11: Handoff-across-nodes integration test (standalone gateway)

**Files:** `pkg/universe/s6_gateway_test.go` (new)

- [ ] **Step 1: Test setup**

Stand up coordinator + 2 nodes + 1 standalone gateway in-process. The 4th "process" is in-process for the test; the real binary mode is T9 but the test uses the same code paths by constructing a `Gateway` with a `meshGatewayClient`.

Use the same host ID trick as `TestS45CrossNodeBorderFrameAndHandoff` to force a 2-2 cell split so cross-host handoff is actually exercised.

Use `--gateway-mode=always-proxy` so the MeshData codec path is forced even in-process.

- [ ] **Step 2: `TestS6HandoffAcrossNodes`**

Shape:

1. Spin up coordinator with `NoInprocGateway: true`
2. Spin up 2 nodes pointed at the coordinator
3. Spin up a standalone gateway pointed at the coordinator (as a `Gateway` object connected via `meshGatewayClient`)
4. Add a fake `Transport` to the gateway's `ConnMgr` via `AddTransport` (simulates a WebSocket client)
5. Inject login bytes via `fakeTransport.InjectInput`
6. Wait for `sessionRoutes` to show the session
7. Inject input bytes that move the player across a cell boundary that straddles the 2-2 host split
8. Wait for `sessionRoutes[key].HostID` to flip (epoch bumped)
9. Capture outbound frames from the fake transport's buffer
10. Assert: no frame gap larger than 2 ticks
11. Inject disconnect; assert `sessionRoutes` entry removed

- [ ] **Step 3: Run**

```bash
just db-up
go test -count=1 -timeout 180s -run TestS6HandoffAcrossNodes ./pkg/universe/
```

- [ ] **Step 4: Commit**

```
test(universe): handoff-across-nodes integration test with standalone gateway

TestS6HandoffAcrossNodes stands up coordinator + 2 nodes +
standalone gateway in-process (using --gateway-mode=always-proxy
so the MeshData codec path is exercised regardless), drives a
fake client through login + cross-host handoff + disconnect, and
verifies the session survives the host boundary crossing with no
frame gap larger than 2 ticks.

The S6 capstone test: if this passes, multi-process playable
gameplay across cell boundaries with independently scalable
gateway works end-to-end.
```

---

### Task 12: 4node-basic multi-process demo + CLAUDE.md docs

**Files:** `examples/4node-basic/main.go`, `CLAUDE.md`

- [ ] **Step 1: 4node-basic supports all 4 modes**

Extend `main.go` to handle `--mode=gateway` (uses the same config pattern as node mode). Coordinator mode runs the in-process gateway by default. Node mode still has no WebSocket listener.

- [ ] **Step 2: Smoke test 4-process setup**

```bash
just db-up
./bin/4node-basic --mode=coordinator --no-inproc-gateway &
./bin/4node-basic --mode=node --coordinator-addr=localhost:9100 --host-id=alpha &
./bin/4node-basic --mode=node --coordinator-addr=localhost:9100 --host-id=beta &
./bin/4node-basic --mode=gateway --coordinator-addr=localhost:9100 --gateway-id=gw-1 --port=8080 &
# Browser to http://localhost:8080
```

Play. Verify movement across cell boundaries, handoff logs, console `host list` and `cell list` on the coordinator.

- [ ] **Step 3: CLAUDE.md rewrite**

Replace the "Multi-process gameplay is deferred to S6" paragraph with a new section describing:
- 4 process types (all-in-one, coordinator, node, gateway)
- Gateway = role, runnable in-process with coordinator OR standalone
- Composite session key `{GatewayID, ConnID}` on all wire messages
- Login runs on the gateway; zero coordinator round-trip
- Targeted `UpstreamSwitch` on cross-host handoffs (not broadcast)
- `local-shortcut` (default) vs `always-proxy`
- Gateway crash = client reconnect + full re-login (session tokens are future work)

- [ ] **Step 4: Commit**

```
docs: S6 gateway + multi-process gameplay in CLAUDE.md

4node-basic now demonstrates the 4-process setup: coordinator
(with --no-inproc-gateway), standalone gateway, and two nodes.
Clients connect to the gateway's WebSocket, get proxied through
MeshData to the authoritative node, and walk across host
boundaries with transparent handoffs.

CLAUDE.md updated to describe the gateway role, the composite
{GatewayID, ConnID} session key on all wire messages, targeted
UpstreamSwitch dispatch, and local-shortcut vs always-proxy
gateway modes.
```

---

## Verification checklist

- [ ] `go vet ./...` clean
- [ ] `go test -count=1 ./...` all pass (without Postgres)
- [ ] `just test-pg` all pass (with Postgres, S5 regression check)
- [ ] `go test -count=1 -run S6 ./pkg/universe/` passes (T11 integration test)
- [ ] `just build` produces `bin/server`
- [ ] **Proto:** `gateway_id` on `ClientInput`, `ClientFrame`, `PlayerAssignment`, `ForwardInput`; `ClientDisconnect` variant; `RegisterGateway`, `SessionAnnounce`, `PlayerMigrated` host messages; `UpstreamSwitch` refactored to composite key + epoch
- [ ] **`net.ConnSender`** interface extracted; `*net.ConnManager` satisfies it
- [ ] **`SessionKey` + `sessionRoutes`** replace `c.connIndex` with typed composite-key routes + epoch field
- [ ] **`GatewayRegistry`** parallel to HostRegistry; 5s dead-threshold; cleanup on gateway loss
- [ ] **`Gateway` role** runs embedded or standalone with identical code; login runs on the gateway
- [ ] **`VirtualConnManager`** on nodes owns SessionKey↔localID bidirectional mapping
- [ ] **Targeted `UpstreamSwitch`** dispatch — not broadcast
- [ ] **`PlayerMigrated`** sent from handoff driver on cross-host commits
- [ ] **`--mode=gateway`** is a first-class runnable binary mode
- [ ] **`--gateway-mode=local-shortcut`** (default) + **`--gateway-mode=always-proxy`**
- [ ] **`--no-inproc-gateway`** disables the in-process gateway on coordinator mode
- [ ] `examples/4node-basic` is playable in 4-process setup (coord + gateway + 2 nodes) via a browser client
- [ ] `CLAUDE.md` "Multi-process gameplay" section updated

---

## Risk notes

- **Gateway drain loop polling.** 1ms sleep is crude. Channel-driven is cleaner but requires wiring into `net.Conn.readPump`. Defer the optimization if CPU cost becomes a problem.

- **Epoch races.** Stale input can reach the OLD host during the single-tick flip window. Mitigation: include the session epoch in `MeshFrame.ClientInput` and have the receiving VCM discard stale epochs. Add the field in T1 (or as a follow-up if the T11 test surfaces the race).

- **Gateway connection to nodes.** Gateways open `MeshData.Data` streams to nodes lazily. Gateway needs to know the node's gRPC address — comes from the cached `PeerList` which the gateway subscribes to via its `meshControlClient` (standalone) or direct read (embedded). If the gateway's topology cache is stale at handoff time, the outbound ClientInput for the new host fails until the next PeerList tick — acceptable for v1 since the risk window is brief, but watch the integration test.

- **Broadcasting PeerList to gateways.** The existing PeerList broadcast path in S4.5 sends to all registered hosts. Extend to also send to all registered gateways. Same shape of message, different target set. `coord_assignment.go`'s `broadcastPeerList` gets a parallel `broadcastPeerListToGateways` call — or unify them.

- **`connID` vs `localID` discipline.** On nodes, `VirtualConnManager` maps composite SessionKey to a node-local `uint32 connID`. Every engine consumer that uses `connID` is consuming the *local* ID, not the gateway-originated one. Don't leak the local ID outside the VCM boundary. This is a subtle invariant — audit carefully during T6.

- **Gateway crash + session orphans.** Gateway dies → its sessions become unreachable → coordinator's `sessionRoutes` entries point to a dead gateway → handoff notifications get sent to a dead stream and fail. The liveness watcher removes the entries but there's a race window. Mitigation: nodes should treat `Send` failures to a gateway peer as "drop the virtual session" and rebuild from the next fresh PlayerAssignment. Log but don't crash.

- **Session token deferral consequences.** Without tokens, every gateway crash = client reconnect + full re-login. For a dev branch this is fine. For production, tokens are the next must-have hardening task.

- **Engine `ConnMgr` type change ripple.** T2 changes the field to `net.ConnSender`. Grep every use site (same as v1 plan). Anything outside engine that needs gateway-only methods (topology broadcast, `routeEvents`, etc.) must hold the concrete type separately.

- **`HostNetwork.peers` kind extension.** Existing node↔node usage must keep working. The `peerKind` enum defaults to `peerKindNode` for backward compatibility with tests that construct peers directly. Audit every `ConnectPeer` call site.

---

## Approval gate

Before executing, the user reviews this plan and confirms:

1. **Approved as-is** → proceed to T1.
2. **Approved with changes** → list the changes, update the plan, then proceed.
3. **Defer or redesign** → revisit.

If approved, this plan replaces the S6 portion of `2026-04-13-S4.5-S5-S6-distributed-mesh-continuation.md`. The master plan stays for historical context.
