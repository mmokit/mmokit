# S6: Gateway / Multi-Process Gameplay — Clean-Slate Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Each task uses checkbox (`- [ ]`) syntax. This plan **REPLACES** the S6 portion of `2026-04-13-S4.5-S5-S6-distributed-mesh-continuation.md` — that earlier draft was written before the S4/S5 work landed and makes several wrong assumptions about the current code (e.g., non-existent `Broadcast`/`ByConnID` methods on ConnManager, a parallel `SessionRouter` type instead of extending the existing `c.players`/`c.connIndex`, incomplete handoff-notification design).

**Branch:** stay on `feature/distributed-mesh`.

---

## Why this plan exists

Phase S6 is the capstone: clients connect to the coordinator, get proxied to the authoritative node hosting their cell, and walk across host boundaries with no disconnect. Before writing the plan I audited the real codebase and researched modern gateway patterns. Several of the master-plan assumptions turned out to be wrong. The most important:

- **There's no `ConnTransport` interface to extract** — `net.ConnManager` is a concrete struct and every caller uses it directly. The master plan's proposed method list (`Broadcast`, `ByConnID`) doesn't match reality. The actual hot-path surface that a virtual implementation needs is much narrower.
- **Session routing state already exists** on the coordinator as `c.players` (username→`PlayerLocation`) + `c.connIndex` (connID→nodeID). We should extend these with an `epoch` field rather than introduce a parallel `SessionRouter` type.
- **Cross-host handoff has a notification gap**: in multi-process mode today, `OnPlayerTransfer` only updates the local coordinator's `connIndex`. When a cell on Node A hands off an entity to Node B, the coordinator never finds out. S6 has to close this gap explicitly — it's not just a "broadcast UpstreamSwitch" problem.
- **All the proto messages already exist**: `MeshFrame.ClientInput`, `ClientFrame`, `PlayerAssignment`, `ForwardInput`, and `CoordMessage.UpstreamSwitch` are all defined. Zero schema changes needed.

Research direction that informed the design:
- **Gateway in-process with coordinator** is the right indie starting point (Nakama pattern). `--mode=gateway` split is deferred past S6.
- **Epoch-discriminated routing** is the canonical "flip then broadcast" handoff primitive. Routing flip is atomic under a write lock; in-flight inputs sort themselves out via epoch comparison.
- **Narrow `ConnSender` interface** beats a broad `ConnTransport` interface. Keep gateway-only methods on the concrete type.
- **Session tokens for crash recovery are deferred**. The S6 scope is "multi-process playable + transparent handoffs across hosts," not "survive a coordinator crash." Tokens land in a later phase.

**Sources worth knowing about** (for anyone extending this design):
- Nakama's session registry and transport abstraction — [github.com/heroiclabs/nakama](https://github.com/heroiclabs/nakama)
- Path of Exile 2 seamless zone transfer (GDC 2023) — validates the 1-2 tick risk window pattern
- Valve's GNS virtual connection model — session migration + interface abstraction
- Gabriel Gambetta's 2024 client-server architecture notes — interpolation bridging transfer gaps

---

## Current state (audited 2026-04-13)

### What works end-to-end today

- **Coordinator/Node split (S4):** `--mode=coordinator` runs MeshControl on `:9100`, `--mode=node` registers with the coordinator and hosts cells via rendezvous assignment. Heartbeats, graceful leave, crash reassignment all work.
- **Cross-node MeshData routing (S4.5):** cells on different nodes exchange border frames and handoff messages over the gRPC MeshData stream. `PeerList` broadcasts populate each node's `cellToHostMap` + `HostNetwork.peers`. Verified by `TestS45CrossNodeBorderFrameAndHandoff`.
- **Postgres persistence (S5):** player state, marketplace orders/trades, and game config live in Postgres via typed repositories. `PlayerFlusher` batches upserts via `pgx.Batch`. Integration tests under `-tags=pgtest` all green.
- **Handoffs within a process:** `HandoffDriver` drives Border→Promoted→Commit. Entity serialized, epoch bumped, destination cell promotes shadow on commit. `OnPlayerTransfer` updates coordinator routing via `bridge.OnPlayerTransfer → coord.setPlayerNode`.
- **In-process multi-host testing:** `--two-hosts` creates two in-process `Host` instances with real `HostNetwork` gRPC loopback. Used by `TestTwoHostHandoffPrepareRoundTrip`.

### Key infrastructure S6 builds on

**`net.ConnManager`** (concrete, `pkg/net/server.go`):

| Method | Called by | S6 usage |
|---|---|---|
| `Send(connID, data)` | engine hot path (frame writer, player manager) | MUST work on virtual impl |
| `SendReliable(connID, data)` | engine hot path | MUST work on virtual impl |
| `InjectInput(connID, data)` | `cell.go` ForwardInput handler | MUST work on virtual impl |
| `DrainInput(connID)` | `input_router.go` (every tick per active player) | MUST work on virtual impl |
| `DrainOpInput(connID)` | ops router | MUST work on virtual impl |
| `AddTransport(t) uint32` | `HandleWebSocket` only | Gateway-only, stays on concrete |
| `HandleWebSocket(w, r)` | example mains | Gateway-only |
| `ActiveConnIDs()` | topology broadcast | Gateway-only (on concrete) |
| `Remove(id)` / `Unregister(id)` | disconnect path | Gateway-only (on concrete) |
| `Events() <-chan PlayerEvent` | coordinator's `routeEvents` loop | Gateway-only (on concrete) |
| `TotalBytesSent/Recv/ConnectionCount` | metrics | Gateway-only (on concrete) |

**Key types `coordinator.go`:**
- `c.players map[string]*PlayerLocation` (username → `{NodeID, Active}`) — guarded by `c.mu`
- `c.connIndex map[uint32]string` (connID → nodeID) — guarded by `c.mu`
- `c.ConnMgr *net.ConnManager` — concrete type
- `setPlayerNode(connID, nodeID)` — called by `bridge.OnPlayerTransfer` at handoff commit

**Existing proto messages (no schema changes needed):**
- `MeshFrame.ClientInput { conn_id, data }` (field 9)
- `MeshFrame.ClientFrame { conn_id, data }` (field 10)
- `MeshFrame.PlayerAssignment { from_cell_id, conn_id, username, is_reconnect, data }` (field 14)
- `MeshFrame.ForwardInput { from_cell_id, conn_id, input_blob }` (field 5)
- `CoordMessage.UpstreamSwitch { session_id, new_host_id }` (field 11) — defined, zero Go usage

### What's missing for S6

1. **No interface abstraction** — engine depends on concrete `*net.ConnManager`. Can't run a node without a real WebSocket listener.
2. **No gateway proxy loop** — coordinator's `processLogins` → `routeAuthenticatedPlayer` path assumes local cells. No MeshData-side forwarding of client input.
3. **No cross-host handoff notification** — `OnPlayerTransfer` updates `connIndex` locally in whichever process the handoff happened, but in multi-process mode that's the node, not the coordinator. The coordinator's routing table never learns about cross-host handoffs.
4. **No `UpstreamSwitch` send site** — the proto message exists, but nothing sends it. Nodes have no way to know when their `cellToHostMap` is stale for a given session.
5. **No `VirtualConnManager`** — nodes don't have anything that can impersonate a `ConnManager` on the game-loop side.
6. **Node mode skips the HTTP listener** — `examples/4node-basic/main.go:117` explicitly skips WebSocket binding in node mode. Multi-process playable gameplay doesn't exist yet.

---

## Target architecture

### Design principles

1. **Gateway = coordinator process.** No separate `--mode=gateway`. Defer process split past S6.
2. **Narrow `ConnSender` interface on the engine side.** Concrete `*net.ConnManager` (with its full gateway-facing method surface) is only used by gateway-side code.
3. **Extend, don't replace, the existing session tracking.** `c.connIndex` becomes `c.sessionRoutes` with an `epoch` field and an explicit `HostID`. `c.players` stays as-is for username-based lookups.
4. **Epoch-discriminated routing.** Every routing entry carries a monotonic epoch. Client input forwarded over MeshData carries its session's epoch. Nodes reject inputs with stale epochs.
5. **Explicit handoff notification.** When a cross-host handoff commits, the handoff-source node sends a `HostMessage.PlayerMigrated` (new variant) to the coordinator. The coordinator updates `sessionRoutes` atomically (bump epoch, change `HostID`), then broadcasts `CoordMessage.UpstreamSwitch` to every node. The old node drops its virtual conn entry; the new node accepts inputs.
6. **Session tokens deferred.** S6's scope is transparent handoffs, not coordinator crash survival. Clients that disconnect must re-run the login flow. T13+ adds token-based reconnect.
7. **`local-shortcut` vs `always-proxy` as runtime config.** Default behavior in all-in-one mode: if the authoritative cell is in the same process, skip the MeshData codec. `--gateway-mode=always-proxy` forces the codec path for testing.
8. **Always-proxy is the default for integration tests.** Local shortcut is a performance optimization; correctness is proven on the codec path.

### Package layout

```
pkg/net/
├── conn_sender.go               # NEW: narrow ConnSender interface
├── server.go                    # (existing) ConnManager — now implements ConnSender implicitly

pkg/universe/
├── virtual_conn_manager.go      # NEW: node-side VirtualConnManager (implements ConnSender)
├── gateway_proxy.go              # NEW: coordinator-side proxy (per-session drain + MeshData forward)
├── session_routes.go             # NEW: typed session routing table (replaces c.connIndex)
├── mesh_data_server.go          # (existing) gains ClientInput/ClientFrame/PlayerAssignment dispatch
├── mesh_control_server.go       # (existing) gains UpstreamSwitch broadcast + PlayerMigrated receive
├── mesh_control_client.go       # (existing) gains UpstreamSwitch handler + PlayerMigrated send
├── coordinator.go               # (existing) swap c.connIndex for sessionRoutes, wire gateway proxy
├── handoff_driver.go            # (existing) send PlayerMigrated HostMessage on cross-host commit
```

### Interface design

```go
// pkg/net/conn_sender.go
package net

// ConnSender is the narrow connection interface that the engine game
// loop depends on. The concrete *net.ConnManager on the gateway
// implements it; so does VirtualConnManager on nodes. Gateway-only
// methods (HandleWebSocket, AddTransport, Events, ActiveConnIDs,
// Remove, Unregister, TotalBytesSent/Recv, ConnectionCount) stay on
// the concrete type and are not part of this interface.
//
// Hot path: DrainInput is called every tick per active player from
// pkg/engine/input_router.go. The virtual implementation must not
// block or allocate per call.
type ConnSender interface {
    Send(connID uint32, data []byte)
    SendReliable(connID uint32, data []byte)
    InjectInput(connID uint32, data []byte)
    DrainInput(connID uint32) [][]byte
    DrainOpInput(connID uint32) [][]byte
}

// Compile-time assertion that ConnManager satisfies the interface.
// Placed in conn_sender.go so adding this file doesn't force a
// server.go edit.
var _ ConnSender = (*ConnManager)(nil)
```

### SessionRoutes

Replaces `c.connIndex map[uint32]string`. Lives alongside `c.players` on `Coordinator`.

```go
// pkg/universe/session_routes.go
package universe

import "sync"

// SessionRoute identifies which node + cell owns a given client
// connection, with a monotonic epoch for atomic handoff.
//
// ConnID is the coordinator-owned connection identifier (from the
// real WebSocket at the gateway). HostID identifies the node process
// hosting the session's cell. CellID is the specific cell within
// that host. Epoch is incremented on every cross-host handoff; the
// virtual conn managers on nodes use it to discriminate "this input
// is for my session vs a stale one."
type SessionRoute struct {
    ConnID   uint32
    Username string
    HostID   string
    CellID   string
    Epoch    uint64
}

// sessionRoutes is the coordinator's connID → SessionRoute map.
// Guarded by a dedicated RWMutex so gateway-proxy hot-path reads
// don't contend with control-plane writes on the broader coordinator
// mu.
type sessionRoutes struct {
    mu     sync.RWMutex
    routes map[uint32]*SessionRoute
}
```

Moved from the broader `c.mu` to its own `sync.RWMutex` because the gateway proxy's inbound drain loop reads it every tick per active session. This is the one place where we expect non-trivial lock contention at scale.

### Gateway flow

**Login → routing assignment (unchanged shape, extended target):**

1. Client WebSocket connects → coordinator's `routeEvents` loop creates a login pending session
2. First messages drain into login handler → `routeAuthenticatedPlayer(connID, username, data)`
3. `playerRouter(username)` returns target `hostID` + implicit `cellID`
4. Coordinator writes `sessionRoutes[connID] = {connID, username, hostID, cellID, epoch: 1}`
5. Coordinator sends `MeshFrame.PlayerAssignment{conn_id, username, is_reconnect: false, data}` to target host via the existing `HostNetwork.SendReliable`
6. Target host's `meshDataServer` receives the frame, calls `VirtualConnManager.RegisterSession(connID, username)`, then routes the frame into the target cell's inbox as a `MsgPlayerAssignment` (existing path)
7. Target cell's game loop picks it up, spawns the player entity

**Inbound path (WebSocket → MeshData):**

8. Coordinator has a per-session goroutine (spawned at login) that drains `ConnMgr.DrainInput(connID)` every tick and, for each message, calls `gatewayProxy.ForwardInput(connID, msg)`
9. `ForwardInput` reads the current `SessionRoute` under RLock, and either:
   - `HostID == localHost && gatewayMode == "local-shortcut"` → skip forwarding (the game loop on the local host picks up the input directly from `DrainInput`)
   - else → encode as `MeshFrame.ClientInput{conn_id, data}` with the session epoch encoded in an extra field, send via `HostNetwork.SendLossy` to `hostID`
10. Target node's `meshDataServer` receives the frame, looks up the session in its local `VirtualConnManager`. If epoch matches, calls `vcm.InjectInput(connID, data)`. If epoch is stale, logs and drops.

**Outbound path (MeshData → WebSocket):**

11. On a node, game systems call `conn.Send(connID, bytes)` through whatever `net.ConnSender` was passed to the engine. In node mode that's `*VirtualConnManager`.
12. `VirtualConnManager.Send(connID, data)` encodes `MeshFrame.ClientFrame{conn_id, data}` and sends via `HostNetwork.SendLossy` to the coordinator — which is now a peer in the node's `HostNetwork.peers` map.
13. Coordinator's `meshDataServer` receives `ClientFrame`, calls `ConnMgr.Send(conn_id, data)` directly — the real WebSocket transport sends it to the client.

**Handoff across hosts:**

14. `HandoffDriver` on Node A decides to promote an entity to a cell on Node B. Prepare + Commit messages send via `grpcBridge` over MeshData (existing, works).
15. After dispatch, the driver calls `bridge.OnPlayerTransfer(connID, destCellID)`, which (in grpcBridge multi-process mode) sends a new `HostMessage.PlayerMigrated{conn_id, from_host, to_host, to_cell}` via the MeshControl stream to the coordinator.
16. Coordinator's `meshControlServer` handles `PlayerMigrated`: under the `sessionRoutes` write lock, looks up the session, bumps `Epoch`, changes `HostID` and `CellID`, releases the lock, then broadcasts `CoordMessage.UpstreamSwitch{session_id: connID, new_host_id: toHost}` to every host.
17. Node A's `meshControlClient` receives `UpstreamSwitch`: calls `vcm.DropSession(connID)` which stops accepting inputs for that connID and flushes any pending output.
18. Node B's `meshControlClient` receives `UpstreamSwitch`: confirms its session is live (the new cell has already spawned the entity via HandoffCommit). Subsequent inputs from the coordinator land here.
19. The risk window is 1-2 ticks. The client sees a missed server frame, which the dead-reckoning interpolator bridges invisibly.

**Disconnect:**

20. Client WebSocket closes → coordinator's `routeEvents` receives a disconnect event
21. Coordinator deletes the session from `sessionRoutes`, sends `MeshFrame.ClientDisconnect{conn_id}` (new MeshFrame variant — added in T7) to the authoritative host
22. Target host's `meshDataServer` routes the frame to the cell's inbox as `MsgPlayerDisconnected` (new CellMessage type) so the cell can transition the player session to `StateDisconnected` + start the grace period

### Out of scope for S6

- **Session tokens + coordinator crash recovery** — deferred to S7 or S6.5
- **Multiple gateway instances behind a load balancer** — spec §11
- **`--mode=gateway` separate process** — deferred, design the interface for it though
- **UDP proxying for native clients** — WebSocket only for S6
- **Client-side reconnect UI** — server-side support lands; client-side UX comes later
- **Distributed cell splits + merges across nodes** — S7
- **Input rate limiting** — research recommends it but it's a hardening task, not a capstone task

---

## File structure

### Created

| Path | Responsibility |
|---|---|
| `pkg/net/conn_sender.go` | `ConnSender` interface + `*ConnManager` compile-time assertion |
| `pkg/universe/session_routes.go` | `SessionRoute` struct + `sessionRoutes` map type |
| `pkg/universe/virtual_conn_manager.go` | Node-side `VirtualConnManager` implementing `ConnSender` |
| `pkg/universe/gateway_proxy.go` | Coordinator-side gateway proxy: per-session drain loop + outbound routing |
| `pkg/universe/s6_gateway_test.go` | Integration test: coord + 2 nodes + fake client, full login + input + server-frame + handoff-across-nodes loop |

### Modified

| Path | What changes |
|---|---|
| `pkg/engine/engine.go` | `ConnMgr` field type changes from `*net.ConnManager` to `net.ConnSender` |
| `pkg/universe/coordinator.go` | Replace `connIndex` with `sessionRoutes`; `Config.GatewayMode` field gains `always-proxy` value; Build() wires `VirtualConnManager` for node mode |
| `pkg/universe/mesh_data_server.go` | Route inbound `ClientInput` → `VirtualConnManager.InjectInput`; route inbound `ClientFrame` → `ConnMgr.Send`; route inbound `PlayerAssignment` → cell inbox + `VirtualConnManager.RegisterSession`; route inbound `ClientDisconnect` → cell inbox + `VirtualConnManager.DropSession` |
| `pkg/universe/mesh_control_server.go` | Handle `HostMessage.PlayerMigrated`; implement `BroadcastUpstreamSwitch` |
| `pkg/universe/mesh_control_client.go` | Dispatch `CoordMessage.UpstreamSwitch` → `VirtualConnManager.OnUpstreamSwitch`; send `HostMessage.PlayerMigrated` |
| `pkg/universe/handoff_driver.go` | After commit dispatch, send `PlayerMigrated` via the outer Bridge when `srcHost != destHost` |
| `pkg/universe/cell_bridge_impl.go` + `grpc_bridge.go` | `OnPlayerTransfer` gains a "is cross-host?" path that emits `PlayerMigrated` instead of updating local `connIndex` |
| `pkg/universe/cell.go` | Handle new `MsgPlayerDisconnected` CellMessage type |
| `pkg/universe/message.go` | Add `MsgPlayerDisconnected` constant + `DisconnectPayload` struct |
| `proto/meshpb/mesh.proto` | Add `ClientDisconnect` variant to `MeshFrame.msg`; add `PlayerMigrated` variant to `HostMessage.msg` |
| `pkg/net/server.go` | Add `IsVirtual()` helper (or similar) so callers can opt into different behavior where required — TBD during implementation |
| `cmd/server/main.go` | `--gateway-mode` flag plumbed through `mmokit.Config.GatewayMode` |
| `examples/4node-basic/main.go` | Node mode now runs a `VirtualConnManager` instead of skipping the HTTP listener; coordinator mode runs the real listener + gateway proxy |
| `CLAUDE.md` | Multi-process gameplay section rewritten |

### Deleted

Nothing. S6 is purely additive (plus one type rename from `connIndex` to `sessionRoutes`).

---

## Task breakdown

### Task 1: Narrow `ConnSender` interface + engine decoupling

**Files:** `pkg/net/conn_sender.go` (new), `pkg/engine/engine.go`, call sites that use concrete `*net.ConnManager`

- [ ] **Step 1: Create `pkg/net/conn_sender.go`** with the interface above + compile-time assertion on `*ConnManager`.

- [ ] **Step 2: Change `engine.Engine.ConnMgr` field type** from `*net.ConnManager` to `net.ConnSender`. This is the pivot point: every engine consumer that touches the field now gets the narrower type.

- [ ] **Step 3: Fix up broken call sites.** `grep -rn "ConnMgr\." --include="*.go"` — for each hit, decide:
  - If the method is on the narrow interface (`Send`, `SendReliable`, `InjectInput`, `DrainInput`, `DrainOpInput`), the call still compiles.
  - If the method is gateway-only (`ActiveConnIDs`, `Events`, `HandleWebSocket`, `AddTransport`, `Remove`, `Unregister`, `TotalBytesSent/Recv`, `ConnectionCount`), the caller must either (a) hold the concrete type itself or (b) type-assert.
  - Gateway-only callers are all in `pkg/universe/coordinator.go` (topology broadcast, routeEvents, HTTP listener wiring, metrics). Keep a separate `*net.ConnManager` reference at the Coordinator level — the interface type on `Engine` is for node-side consumers only. The Coordinator holds `ConnMgr *net.ConnManager` (concrete, unchanged) and passes it into `engine.New` where Go's interface assignment converts it to `ConnSender`.

- [ ] **Step 4: Verify**

```bash
go vet ./...
go test -count=1 ./...
just build
```

All tests green, binary builds.

- [ ] **Step 5: Commit**

```
refactor(net): extract narrow ConnSender interface for engine hot path

pkg/net/conn_sender.go defines the subset of *ConnManager methods
that the engine game loop actually needs (Send, SendReliable,
InjectInput, DrainInput, DrainOpInput). engine.Engine.ConnMgr is
now typed as net.ConnSender so a VirtualConnManager can be plugged
in on nodes in T3.

Gateway-side methods (HandleWebSocket, AddTransport, Events,
ActiveConnIDs, Remove, Unregister, byte counters) stay on the
concrete *ConnManager held by the Coordinator for its gateway
routing + metrics work.
```

---

### Task 2: `sessionRoutes` type + replace `c.connIndex`

**Files:** `pkg/universe/session_routes.go` (new), `pkg/universe/coordinator.go`

- [ ] **Step 1: Create `pkg/universe/session_routes.go`** with `SessionRoute`, `sessionRoutes` struct, and methods:

```go
func newSessionRoutes() *sessionRoutes

func (r *sessionRoutes) Set(route *SessionRoute)            // whole-entry write
func (r *sessionRoutes) Get(connID uint32) (*SessionRoute, bool) // returns copy
func (r *sessionRoutes) Remove(connID uint32)
func (r *sessionRoutes) Migrate(connID uint32, newHostID, newCellID string) (uint64, bool)
  // atomically bumps epoch, returns (newEpoch, existed). The coordinator
  // calls this on PlayerMigrated.
```

Guard with a dedicated `sync.RWMutex`. All `Get` returns are deep copies (the struct is small and routes are rewritten often).

- [ ] **Step 2: Replace `c.connIndex`** with `c.sessionRoutes *sessionRoutes` on `Coordinator`. Initialize in `NewCoordinator`. Update every read/write site:
  - `setPlayerNode(connID, nodeID)` → `sessionRoutes.Set(&SessionRoute{...})` (first assignment: epoch=1)
  - `removePlayerNode(connID)` → `sessionRoutes.Remove(connID)`
  - `getPlayerNode(connID)` → `sessionRoutes.Get(connID)` and return `HostID`

- [ ] **Step 3: Verify**

```bash
go vet ./... && go test -count=1 ./...
```

No test regressions. This is a pure rename + type extension; semantic behavior unchanged.

- [ ] **Step 4: Commit**

```
refactor(universe): typed sessionRoutes replace c.connIndex

SessionRoute carries {ConnID, Username, HostID, CellID, Epoch}
alongside the connID → hostID mapping the old c.connIndex held.
The new Epoch field is required in T6 for atomic handoff — every
cross-host migration bumps it so virtual conn managers on nodes
can discriminate stale routing state.

sessionRoutes guards its map with a dedicated RWMutex so the
gateway proxy's per-tick inbound drain doesn't contend with
control-plane writes on the broader Coordinator mu.

No semantic change from the caller's perspective; every existing
connIndex read/write is rewritten to the new method set.
```

---

### Task 3: `VirtualConnManager` on nodes

**Files:** `pkg/universe/virtual_conn_manager.go` (new), `pkg/universe/coordinator.go` (node-mode wiring), `pkg/universe/mesh_data_server.go` (inbound dispatch)

- [ ] **Step 1: Create `VirtualConnManager`** implementing `net.ConnSender`.

```go
package universe

import (
    "sync"

    meshpb "github.com/zenion/mmoserver/gen/go/meshpb"
    "github.com/zenion/mmoserver/pkg/logger"
    "github.com/zenion/mmoserver/pkg/net"
)

// VirtualConnManager is the node-side ConnSender implementation. It
// looks identical to *net.ConnManager from the engine's perspective
// but routes outbound Send/SendReliable through MeshData.ClientFrame
// back to the coordinator, and accepts inbound bytes via InjectInput
// from meshDataServer's ClientInput dispatch.
//
// Thread safety: the sessions map is guarded by a dedicated mutex.
// Send/SendReliable are hot-path (called from game loop systems per
// tick per visible entity) and must not block — encode + enqueue
// into HostNetwork outbound queues, no synchronous waits.
type VirtualConnManager struct {
    coord *Coordinator
    log   *logger.Logger

    mu       sync.RWMutex
    sessions map[uint32]*virtualSession
}

type virtualSession struct {
    connID   uint32
    username string
    epoch    uint64

    // Per-session input buffers. Mirrors *net.Conn's per-connection
    // queues. input_router drains these every tick via the
    // ConnSender.DrainInput path.
    inputMu  sync.Mutex
    input    [][]byte
    opInput  [][]byte
}

func NewVirtualConnManager(coord *Coordinator) *VirtualConnManager

var _ net.ConnSender = (*VirtualConnManager)(nil)

// ConnSender interface implementations:
func (v *VirtualConnManager) Send(connID uint32, data []byte)
func (v *VirtualConnManager) SendReliable(connID uint32, data []byte)
func (v *VirtualConnManager) InjectInput(connID uint32, data []byte)
func (v *VirtualConnManager) DrainInput(connID uint32) [][]byte
func (v *VirtualConnManager) DrainOpInput(connID uint32) [][]byte

// Session management:
func (v *VirtualConnManager) RegisterSession(connID uint32, username string, epoch uint64)
func (v *VirtualConnManager) DropSession(connID uint32)
func (v *VirtualConnManager) HasSession(connID uint32) bool

// Called from mesh_control_client on UpstreamSwitch dispatch:
func (v *VirtualConnManager) OnUpstreamSwitch(connID uint32, newHostID string)
```

**`Send`/`SendReliable`** encode a `MeshFrame.ClientFrame{conn_id, data}` and call `coord.localHost().Network.SendLossy("coordinator", frame)`. The coordinator is treated as a special peer — its host ID is `"coordinator"` (new constant) and it's added to the node's `HostNetwork.peers` map during registration in T5 below.

**`InjectInput`** appends to `sessions[connID].input` under the per-session mutex. Called from `meshDataServer` when it receives `MeshFrame.ClientInput`.

**`DrainInput`/`DrainOpInput`** swap out the per-session slice under lock, return the drained bytes. Called from `input_router.go` every tick per active player.

**`RegisterSession`** is called when the node receives a `PlayerAssignment` MeshFrame — creates a `virtualSession` entry.

**`DropSession`** removes the entry. Called from `OnUpstreamSwitch` when the session has moved to another node, or from `MsgPlayerDisconnected` handling when the client disconnected.

- [ ] **Step 2: Wire into node-mode Build().** In `coordinator.go` where node mode currently creates its local Host + HostNetwork (around line 461), also construct `VirtualConnManager` and set `c.ConnMgr = vcm`. Since `c.ConnMgr` is still declared as `*net.ConnManager` for the gateway side, this is the awkward part — we either split the field into `c.ConnMgr *net.ConnManager` (gateway only, nil on nodes) and `c.engineConnSender net.ConnSender` (passed to `engine.New`), or we change `c.ConnMgr`'s type to `net.ConnSender`. Pick the first during implementation to avoid touching gateway-side code.

- [ ] **Step 3: Route inbound `MeshFrame.ClientInput`** in `mesh_data_server.go`. Existing handlers live in `routeInboundFrame`. Add:

```go
case *meshpb.MeshFrame_ClientInput:
    ci := v.ClientInput
    if ci == nil { return nil }
    if vcm := s.coord.virtualConnMgr(); vcm != nil {
        vcm.InjectInput(ci.ConnId, ci.Data)
    }
```

The node-side `meshDataServer` hangs off the `HostNetwork`, and the coordinator exposes a `virtualConnMgr()` accessor that returns `*VirtualConnManager` or nil (nil in gateway mode).

- [ ] **Step 4: Verify**

```bash
go vet ./... && go test -count=1 ./...
```

All tests green. Note: this task does NOT wire gateway inbound (Task 4) — a node-mode process built at this point can RECEIVE ClientInput but nobody's SENDING them yet. That's fine; the test for this lands in T9.

- [ ] **Step 5: Commit**

```
feat(universe): VirtualConnManager — node-side ConnSender implementation

VirtualConnManager implements net.ConnSender on node-mode
processes so the engine game loop has identical Send/InjectInput/
DrainInput semantics whether running all-in-one or distributed.

Outbound Send and SendReliable encode MeshFrame.ClientFrame and
forward to the coordinator peer via HostNetwork.SendLossy. Inbound
bytes arrive via meshDataServer's ClientInput dispatch and land
in per-session input buffers that input_router drains every tick.

Session registration happens via RegisterSession on PlayerAssignment
receive (T5 wires the call). DropSession is called on UpstreamSwitch
(T6) and ClientDisconnect (T7).

Node-mode coordinator now holds both *net.ConnManager (gateway,
nil on pure nodes) and net.ConnSender (passed to engine.New, set
to *VirtualConnManager on nodes).
```

---

### Task 4: Gateway proxy on coordinator

**Files:** `pkg/universe/gateway_proxy.go` (new), `pkg/universe/coordinator.go`, `pkg/universe/mesh_data_server.go`

- [ ] **Step 1: `coordinator` as a named peer in node `HostNetwork`.**

The node's VirtualConnManager calls `HostNetwork.SendLossy("coordinator", ...)`. Today, `HostNetwork.peers` is populated by `applyPeerList` from the coordinator's PeerList broadcast. Extend that broadcast (not the proto — the construction) so the coordinator includes *itself* as a peer with host id `"coordinator"` and the MeshData listen address. Nodes add it to `peers` during reconcilation.

Alternative: each node opens a MeshData client stream TO the coordinator as part of registration, not via peer list. Pick whichever is cleaner during implementation.

The coordinator also needs a `HostNetwork` if it didn't have one already. Check: in coordinator mode today, is there a local `HostNetwork`? Read `Build()` to confirm. If not, construct one as part of this task.

- [ ] **Step 2: `gatewayProxy` type.**

```go
// pkg/universe/gateway_proxy.go
package universe

// gatewayProxy drives the coordinator-side inbound path: drain the
// WebSocket input queues, consult sessionRoutes, encode as
// MeshFrame.ClientInput, forward to the authoritative host via
// HostNetwork.SendLossy.
//
// One goroutine per active session, spawned at login completion,
// reaped on disconnect.
type gatewayProxy struct {
    coord *Coordinator
}

func newGatewayProxy(coord *Coordinator) *gatewayProxy

// StartSession is called from routeAuthenticatedPlayer after the
// login handler returns a successful username. Spawns the drain
// goroutine that pumps input for this connID.
func (g *gatewayProxy) StartSession(connID uint32)

// StopSession is called on disconnect. Signals the drain goroutine
// to exit.
func (g *gatewayProxy) StopSession(connID uint32)
```

The drain goroutine:
```go
for {
    select {
    case <-done:
        return
    default:
    }
    msgs := g.coord.ConnMgr.DrainInput(connID)
    ops  := g.coord.ConnMgr.DrainOpInput(connID)
    if len(msgs) == 0 && len(ops) == 0 {
        time.Sleep(time.Millisecond)  // yield; tune if needed
        continue
    }
    route, ok := g.coord.sessionRoutes.Get(connID)
    if !ok { continue }
    if g.coord.isLocalShortcut(route.HostID) {
        continue  // local game loop's input_router drains directly
    }
    for _, m := range msgs {
        frame := &meshpb.MeshFrame{
            Msg: &meshpb.MeshFrame_ClientInput{
                ClientInput: &meshpb.ClientInput{ConnId: connID, Data: m},
            },
        }
        _ = g.coord.getHostNetwork().SendLossy(route.HostID, frame)
    }
    // ops use the reliable path
    for _, m := range ops {
        frame := &meshpb.MeshFrame{
            Msg: &meshpb.MeshFrame_ClientInput{
                ClientInput: &meshpb.ClientInput{ConnId: connID, Data: m},
            },
        }
        _ = g.coord.getHostNetwork().SendReliable(route.HostID, frame)
    }
}
```

Polling with a 1ms sleep is crude. A cleaner design is a channel-per-session that `net.Conn.readPump` writes into. Decide during implementation; polling is fine for v1 if it works.

Note: `isLocalShortcut(hostID)` returns true when `cfg.GatewayMode == "local-shortcut"` AND the hostID matches the coordinator's own local host. Under `always-proxy`, always returns false.

- [ ] **Step 3: Outbound path — `MeshFrame.ClientFrame` inbound on coordinator.**

In `mesh_data_server.go`, add a case to `routeInboundFrame`:

```go
case *meshpb.MeshFrame_ClientFrame:
    cf := v.ClientFrame
    if cf == nil { return nil }
    if s.coord.ConnMgr != nil {
        s.coord.ConnMgr.Send(cf.ConnId, cf.Data)
    }
```

The coordinator-side `ConnMgr` is the real `*net.ConnManager` that owns the WebSocket. `Send` forwards to the right transport.

- [ ] **Step 4: Wire into login flow.**

In `routeAuthenticatedPlayer`, after setting `sessionRoutes[connID]`, call `c.gatewayProxy.StartSession(connID)`.

In `routeEvents` disconnect handler, call `c.gatewayProxy.StopSession(connID)` before removing from `sessionRoutes`.

- [ ] **Step 5: Verify**

```bash
go vet ./... && go test -count=1 ./...
```

Existing tests unchanged. The gateway proxy has no dedicated test yet (that's T9).

- [ ] **Step 6: Commit**

```
feat(universe): gateway proxy routes client IO through MeshData

gatewayProxy spawns a per-session drain goroutine on login that
pumps ConnMgr.DrainInput → sessionRoutes lookup → MeshFrame.
ClientInput → HostNetwork.SendLossy(targetHost). Ops channel uses
SendReliable.

Reverse path: meshDataServer routes inbound MeshFrame.ClientFrame
directly to ConnMgr.Send(connId, data) — the real WebSocket
delivers it to the client.

local-shortcut (default): if the session's HostID matches the
coordinator's own local host, the gateway skips forwarding
entirely; the local game loop's input_router drains directly from
the same queue. always-proxy forces the codec path for testing.

The coordinator now participates in its own HostNetwork.peers map
under the special host ID "coordinator" so nodes can address
outbound ClientFrame traffic back to it.
```

---

### Task 5: `PlayerAssignment` flow over MeshData

**Files:** `pkg/universe/coordinator.go` (send), `pkg/universe/mesh_data_server.go` (receive + session registration), `pkg/universe/virtual_conn_manager.go` (RegisterSession already exists from T3)

- [ ] **Step 1: Send side.**

In `routeAuthenticatedPlayer`, after `sessionRoutes.Set(...)` and `gatewayProxy.StartSession(...)`, construct:

```go
assign := &meshpb.MeshFrame{
    Msg: &meshpb.MeshFrame_PlayerAssignment{
        PlayerAssignment: &meshpb.PlayerAssignment{
            ConnId:      connID,
            Username:    username,
            IsReconnect: false,
            // Data carries login-handler-returned serialized state if any
        },
    },
}
_ = c.getHostNetwork().SendReliable(targetHostID, assign)
```

The existing all-in-one path passes `PlayerAssignment` directly to the cell's inbox via `MsgPlayerAssignment`. Keep that path intact for local-shortcut mode. Under `always-proxy` or multi-process, go through MeshData.

- [ ] **Step 2: Receive side.**

`mesh_data_server.go` routes inbound `PlayerAssignment`:

```go
case *meshpb.MeshFrame_PlayerAssignment:
    pa := v.PlayerAssignment
    if pa == nil { return nil }
    // Register the session in the virtual conn manager FIRST so
    // subsequent ClientInput frames have a place to land.
    if vcm := s.coord.virtualConnMgr(); vcm != nil {
        vcm.RegisterSession(pa.ConnId, pa.Username, /*epoch*/ 1)
    }
    // Then push into the target cell's inbox as MsgPlayerAssignment.
    return s.routePlayerAssignmentToCell(pa)
```

`routePlayerAssignmentToCell` is a new helper that finds the destination cell by looking up the session's target cell from… hmm, we don't know it yet on the node side. One option: the `PlayerAssignment` message needs a `cell_id` field. Check the existing proto — if it has `from_cell_id` that's the wrong direction. Add `to_cell_id` or reuse `from_cell_id` with different semantics, OR the node looks up the cell via its own sessionRoutes lookup (but nodes don't have a sessionRoutes). **Simplest:** add `to_cell_id` to the proto message. This IS a schema change, so bump the proto in T5 explicitly.

Actually wait — let me re-read the existing PlayerAssignment proto. It has `from_cell_id` — that's for CROSS-CELL transfer where the source cell identifies itself. For coordinator-initiated login assignment, we want the coordinator to tell the node which cell to spawn in. Either add a new variant or add a field.

**Decision:** add a `to_cell_id string` field to the existing PlayerAssignment proto. Coordinator always sets it; legacy in-process path can set it too (it's already implicit there). Field number: `to_cell_id = 6;` since the last one is `data = 5;`.

- [ ] **Step 3: Proto change + regenerate.**

Edit `proto/meshpb/mesh.proto`. Add `to_cell_id = 6;`. Run `just proto`. Confirm generated Go has the new field.

- [ ] **Step 4: Verify**

```bash
go vet ./... && go test -count=1 ./...
```

- [ ] **Step 5: Commit**

```
feat(universe): PlayerAssignment flow over MeshData

After successful login the coordinator sends MeshFrame.
PlayerAssignment{conn_id, username, to_cell_id} to the authoritative
host via HostNetwork.SendReliable. The target node's meshDataServer
calls VirtualConnManager.RegisterSession then routes the frame to
the destination cell's inbox as the existing MsgPlayerAssignment.

Adds to_cell_id field (#6) to PlayerAssignment proto so the
coordinator can tell the node which cell to spawn the player in
without the node needing its own routing table.

In local-shortcut mode (cfg.GatewayMode == "local-shortcut" and
target host is the coordinator's local host), the coordinator
short-circuits via the existing direct-to-cell-inbox path.
```

---

### Task 6: `UpstreamSwitch` broadcast + `PlayerMigrated` notification

**Files:** `proto/meshpb/mesh.proto`, `pkg/universe/handoff_driver.go`, `pkg/universe/cell_bridge_impl.go`, `pkg/universe/grpc_bridge.go`, `pkg/universe/mesh_control_server.go`, `pkg/universe/mesh_control_client.go`

- [ ] **Step 1: Proto change.**

Add `PlayerMigrated` variant to `HostMessage.msg`:

```protobuf
message PlayerMigrated {
    uint32 conn_id  = 1;
    string from_host = 2;
    string to_host   = 3;
    string to_cell   = 4;
}
```

Assign the next free field number on `HostMessage.msg`.

Run `just proto`.

- [ ] **Step 2: Handoff driver sends PlayerMigrated.**

In `handoff_driver.go`, the commit path currently calls `bridge.OnPlayerTransfer`. Extend that path so after `OnPlayerTransfer` updates local state, the driver also sends `HostMessage.PlayerMigrated` via the node's `meshControlClient` (which has an active stream to the coordinator):

```go
if evt.ConnID != 0 {
    bridge.OnPlayerTransfer(evt.ConnID, evt.DestCellID)
    // S6: notify the coordinator so it can update sessionRoutes
    // and broadcast UpstreamSwitch. Only meaningful in node mode;
    // in all-in-one mode OnPlayerTransfer already updated the
    // local coordinator's tables directly.
    if client := hd.base.coord.controlClient; client != nil {
        msg := &meshpb.HostMessage{Msg: &meshpb.HostMessage_PlayerMigrated{
            PlayerMigrated: &meshpb.PlayerMigrated{
                ConnId:   evt.ConnID,
                FromHost: client.hostID,
                ToHost:   /* looked up via coord.cellToHostMap[destCellID] */,
                ToCell:   evt.DestCellID,
            },
        }}
        _ = client.send(msg)
    }
}
```

The `ToHost` lookup is done against the node's cached `cellToHostMap` (populated by S4.5 PeerList broadcasts).

- [ ] **Step 3: Coordinator receives PlayerMigrated.**

In `mesh_control_server.go` `Control()` recv loop, add:

```go
case *meshpb.HostMessage_PlayerMigrated:
    pm := v.PlayerMigrated
    if pm == nil { continue }
    newEpoch, ok := s.coord.sessionRoutes.Migrate(pm.ConnId, pm.ToHost, pm.ToCell)
    if !ok {
        s.log.Log(CatMeshCell, "coordinator: PlayerMigrated for unknown conn %d", pm.ConnId)
        continue
    }
    s.log.Log(CatMeshCell, "coordinator: session %d migrated %s -> %s (epoch %d)",
        pm.ConnId, pm.FromHost, pm.ToHost, newEpoch)
    if s.engine != nil {
        s.engine.broadcastUpstreamSwitch(pm.ConnId, pm.ToHost)
    }
```

- [ ] **Step 4: `broadcastUpstreamSwitch` on assignmentEngine.**

Sends `CoordMessage.UpstreamSwitch{session_id, new_host_id}` to every registered host. Each host receives the message in its `meshControlClient.dispatch`.

- [ ] **Step 5: Node dispatches UpstreamSwitch.**

In `mesh_control_client.go` `dispatch`:

```go
case *meshpb.CoordMessage_UpstreamSwitch:
    us := v.UpstreamSwitch
    if us == nil { return }
    if us.NewHostId == c.hostID {
        // This session now belongs to me. VirtualConnManager may
        // already have the session registered (from a prior
        // PlayerAssignment) — no-op.
        return
    }
    // The session has moved to a different host. Drop the local
    // virtual session so stale inputs don't accumulate.
    if vcm := c.coord.virtualConnMgr(); vcm != nil {
        vcm.DropSession(us.SessionId)
    }
```

- [ ] **Step 6: Verify**

```bash
go vet ./... && go test -count=1 ./...
```

All existing handoff tests still pass. New behavior isn't exercised until T9's integration test.

- [ ] **Step 7: Commit**

```
feat(universe): UpstreamSwitch broadcast on cross-host handoff

After HandoffDriver commits a cross-host entity promotion, the
source node sends HostMessage.PlayerMigrated to the coordinator.
The coordinator atomically bumps the session's epoch in
sessionRoutes, updates HostID + CellID, then broadcasts
CoordMessage.UpstreamSwitch to every registered host.

The losing node's VirtualConnManager drops the session so
subsequent stale inputs are logged and discarded. The winning
node already has the session registered via prior PlayerAssignment
(landing during HandoffPrepare flow).

The risk window between the routing flip and the new node's first
authoritative frame is 1-2 ticks. The client's dead-reckoning
interpolator bridges the gap invisibly — matches the S2 handoff
design extended across processes.
```

---

### Task 7: Disconnect propagation

**Files:** `proto/meshpb/mesh.proto`, `pkg/universe/message.go`, `pkg/universe/cell.go`, `pkg/universe/mesh_data_server.go`, `pkg/universe/coordinator.go`

- [ ] **Step 1: Proto change.**

Add `ClientDisconnect` variant to `MeshFrame.msg`:

```protobuf
message ClientDisconnect {
    uint32 conn_id = 1;
    string reason  = 2;
}
```

Run `just proto`.

- [ ] **Step 2: CellMessage type.**

In `pkg/universe/message.go`, add:

```go
MsgPlayerDisconnected MsgType = 107 // or next free number

type PlayerDisconnectPayload struct {
    ConnID uint32
    Reason string
}
```

Add the field to `CellMessage`:

```go
Disconnect *PlayerDisconnectPayload
```

- [ ] **Step 3: Coordinator sends on WebSocket close.**

In `routeEvents` disconnect handler:

```go
// Get the session's host before removing the route.
route, ok := c.sessionRoutes.Get(ev.ConnID)
if ok {
    if c.isLocalShortcut(route.HostID) {
        // direct path — existing code in-process
    } else {
        disc := &meshpb.MeshFrame{
            Msg: &meshpb.MeshFrame_ClientDisconnect{
                ClientDisconnect: &meshpb.ClientDisconnect{ConnId: ev.ConnID},
            },
        }
        _ = c.getHostNetwork().SendReliable(route.HostID, disc)
    }
}
c.sessionRoutes.Remove(ev.ConnID)
c.gatewayProxy.StopSession(ev.ConnID)
```

- [ ] **Step 4: Node receives and routes.**

In `mesh_data_server.go`:

```go
case *meshpb.MeshFrame_ClientDisconnect:
    cd := v.ClientDisconnect
    if cd == nil { return nil }
    if vcm := s.coord.virtualConnMgr(); vcm != nil {
        vcm.DropSession(cd.ConnId)
    }
    return s.routeDisconnectToCell(cd.ConnId)
```

Where `routeDisconnectToCell` pushes `CellMessage{Type: MsgPlayerDisconnected, Disconnect: &PlayerDisconnectPayload{ConnID: cd.ConnId, Reason: cd.Reason}}` to every cell that might hold the player. Simplest impl: iterate `c.Cells` and push to each; they'll ignore if they don't know the connID. A more optimized path looks up the cell from the session; defer to a follow-up if it becomes a bottleneck.

- [ ] **Step 5: Cell handles MsgPlayerDisconnected.**

In `cell.go` `DrainInbox`, add:

```go
case MsgPlayerDisconnected:
    if msg.Disconnect == nil { continue }
    // Delegate to the player manager — it already has the grace
    // period logic from earlier phases.
    c.Engine.Players.OnDisconnect(msg.Disconnect.ConnID)
```

- [ ] **Step 6: Verify + commit**

```bash
go vet ./... && go test -count=1 ./...
```

```
feat(universe): client disconnect propagation via MeshData.ClientDisconnect

WebSocket close on the coordinator now emits a MeshFrame.
ClientDisconnect to the authoritative host. The host's
VirtualConnManager drops its session entry and the target cell
transitions the player session to the grace-period state via
the existing OnDisconnect hook.

Adds MeshFrame.ClientDisconnect proto variant and the
corresponding MsgPlayerDisconnected CellMessage variant for the
in-cell handoff from meshDataServer to the game loop.
```

---

### Task 8: `--gateway-mode=local-shortcut` + `always-proxy`

**Files:** `pkg/universe/coordinator.go`, `cmd/server/main.go`, `examples/4node-basic/main.go`

- [ ] **Step 1: Config wiring.**

`Config.GatewayMode` already exists as a string field (inherited from S3). Valid values: `"local-shortcut"` (default), `"always-proxy"`. Add the `isLocalShortcut(hostID)` helper on Coordinator:

```go
func (c *Coordinator) isLocalShortcut(hostID string) bool {
    if c.cfg.GatewayMode == "always-proxy" {
        return false
    }
    if host := c.localHost(); host != nil && host.ID == hostID {
        return true
    }
    return false
}
```

- [ ] **Step 2: Call sites.**

Gateway proxy's inbound drain loop, PlayerAssignment send path, and ClientDisconnect send path all consult `isLocalShortcut` to pick the local vs. proxy path.

- [ ] **Step 3: Smoke test interactively.**

```bash
just db-up
./bin/4node-basic --mode=all-in-one --two-hosts --gateway-mode=always-proxy --log mesh:grpc &
# open web client, click around, verify everything still works
```

Should produce the same gameplay as the default `local-shortcut` mode but with `mesh:grpc` logs showing inbound ClientInput and outbound ClientFrame on every tick.

- [ ] **Step 4: Commit**

```
feat(universe): --gateway-mode local-shortcut vs always-proxy

Coordinator.isLocalShortcut returns true when the session's
authoritative host is the coordinator's own local host AND
GatewayMode is "local-shortcut" (default). In that case the
gateway proxy skips MeshData forwarding and the local game
loop's input_router drains directly from the real ConnManager.

--gateway-mode=always-proxy forces every session through the
MeshData codec path regardless of colocation. Intended for
integration tests and CI so the proxy path gets exercised
continuously — per the research-driven guidance that behavioral
parity between local and proxy paths is only verifiable by
running tests on the proxy path.
```

---

### Task 9: Handoff-across-nodes integration test

**Files:** `pkg/universe/s6_gateway_test.go` (new)

- [ ] **Step 1: Write `TestS6HandoffAcrossNodes`.**

The test shape:

1. Stand up coordinator + 2 nodes in-process (reusing S4.5 pattern).
2. Use `always-proxy` mode to exercise the codec path regardless.
3. Pick host IDs that produce a 2-2 cell split (same trick as `TestS45CrossNodeBorderFrameAndHandoff`).
4. Create a fake WebSocket client that connects to the coordinator. The easiest way: implement a `net.Transport` test double that implements the `Transport` interface and add it via `coord.ConnManager().AddTransport(transport)` directly, bypassing the real HTTP upgrade.
5. Drive a login through the fake transport (send the login message bytes; wait for `routeAuthenticatedPlayer` to complete — look for the session in `sessionRoutes`).
6. Assert the session is on the expected host (whichever host owns the cell at the login spawn position).
7. Drive an input that moves the player entity across the cell boundary toward the other host.
8. Wait for HandoffDriver to fire (poll `coord.sessionRoutes.Get(connID).HostID` until it flips to the new host, or poll `SessionRoute.Epoch` until it bumps).
9. Assert no frames were lost: the fake transport's outbound byte log should show continuous server frames before and after the flip, with monotonic sequence numbers (or at least no gap > 2 ticks).
10. Drive a disconnect. Assert `sessionRoutes` no longer has the connID.

The `Transport` interface from `pkg/net/conn.go` is what you implement — a mock with `SendUnreliable`/`SendReliable` that capture outbound frames into a test-visible slice, and `InjectInput` that simulates inbound WebSocket data.

- [ ] **Step 2: Run**

```bash
just db-up
go test -count=1 -timeout 120s -run TestS6HandoffAcrossNodes ./pkg/universe/
```

Test runtime is dominated by settle window (~5s) + a few handoff round trips (~500ms). Budget: ~10s total.

- [ ] **Step 3: Debug any failures.**

Common failure modes to watch for:
- Fake transport's `InjectInput` bytes don't reach the target host → check gateway proxy drain loop is actually spawning the per-session goroutine
- Frames arrive at the wrong host → check sessionRoutes epoch discrimination
- Disconnect doesn't propagate → check T7 wiring

- [ ] **Step 4: Commit**

```
test(universe): handoff-across-nodes integration test

TestS6HandoffAcrossNodes stands up coordinator + 2 nodes in-process
(with --gateway-mode=always-proxy so the MeshData codec path is
exercised even though the test is single-process), drives a fake
client through login + cross-host handoff + disconnect, and
verifies the session survives the host boundary crossing with no
frame gap larger than 2 ticks.

Uses the Transport interface directly to inject and capture frames,
bypassing the real WebSocket upgrade — the same pattern S4.5's
cross-node MeshData routing test uses to avoid needing a real TCP
client.

The S6 capstone test: if this passes, multi-process playable
gameplay across cell boundaries works end-to-end.
```

---

### Task 10: 4node-basic multi-process demo + CLAUDE.md docs

**Files:** `examples/4node-basic/main.go`, `CLAUDE.md`

- [ ] **Step 1: 4node-basic in node mode listens to the coordinator's proxy.**

The current behavior (see `examples/4node-basic/main.go:117`) is that node mode skips the HTTP listener entirely. Now node mode must NOT accept client connections directly, but also must NOT explicitly skip them — the `VirtualConnManager` wires into the engine automatically via the changes in T3. The example's main.go needs no real changes for node mode, other than removing the "Node mode doesn't accept client connections" comment.

In coordinator mode, the HTTP listener stays. The `ConnManager()` getter returns the real `*net.ConnManager`, and it now serves both the login flow and the gateway proxy.

- [ ] **Step 2: Update the explanatory comment in main.go.**

Replace the comment that says multi-process gameplay is deferred to S6 with one that says it works and describes the 3-process setup.

- [ ] **Step 3: Interactive smoke test.**

```bash
just db-up
./bin/4node-basic --mode=coordinator --log mesh &
./bin/4node-basic --mode=node --coordinator-addr=localhost:9100 --host-id=alpha --log mesh &
./bin/4node-basic --mode=node --coordinator-addr=localhost:9100 --host-id=beta --log mesh &
# open http://localhost:8080
```

Click around. Verify:
- Player connects through coordinator
- Movement across cell boundaries fires handoffs (watch logs)
- Cell list on the coordinator console shows ownership distribution
- Killing one node causes the session to either disconnect cleanly or reassign (depending on which host owned the session's cell)

- [ ] **Step 4: CLAUDE.md rewrite.**

Replace the "Multi-process gameplay (client proxying from coordinator to the authoritative node) is deferred to S6" paragraph with the new paragraph explaining:
- Clients connect to the coordinator's WebSocket
- `VirtualConnManager` on nodes looks like `ConnManager` to game systems
- `SessionRoutes` on the coordinator tracks `{connID → hostID + epoch}`
- Handoffs across hosts bump the epoch and broadcast `UpstreamSwitch`
- `--gateway-mode=local-shortcut` (default) skips MeshData for colocated sessions; `always-proxy` forces the codec path

Remove any lingering references to deferred functionality.

- [ ] **Step 5: Commit**

```
docs: S6 gateway + multi-process gameplay in CLAUDE.md

Rewrites the Multi-process mode section to describe the gateway
proxy flow: clients → coordinator WebSocket → sessionRoutes
lookup → MeshData.ClientInput → authoritative node. Reverse path:
node VirtualConnManager.Send → MeshData.ClientFrame → coordinator
ConnManager.Send → client WebSocket.

Handoffs across hosts explained: PlayerMigrated notification from
source node → sessionRoutes.Migrate bumps the epoch → coordinator
broadcasts UpstreamSwitch → losing node drops its virtual session.
Client sees a 1-2 tick gap bridged by dead-reckoning interpolation.

4node-basic now demonstrates the 3-process setup: coordinator
owns the WebSocket + admin console, two nodes register and get
cells assigned via rendezvous, clients play end-to-end across
host boundaries.
```

---

## Verification checklist

- [ ] `go vet ./...` clean
- [ ] `go test -count=1 ./...` all pass (without Postgres)
- [ ] `just test-pg` all pass (with Postgres, S5 regression check)
- [ ] `go test -count=1 -run S6 ./pkg/universe/` passes (T9 integration test)
- [ ] `just build` produces `bin/server`
- [ ] `net.ConnSender` interface extracted; `*net.ConnManager` satisfies it via compile-time assertion
- [ ] `VirtualConnManager` implements `net.ConnSender`; every method tested via the T9 integration
- [ ] `sessionRoutes` replaces `c.connIndex` with typed routes + epoch field
- [ ] `gatewayProxy` drains WebSocket input per-session and forwards via MeshData
- [ ] `MeshFrame.ClientInput` / `ClientFrame` / `PlayerAssignment` / `ClientDisconnect` all routed bidirectionally
- [ ] `HostMessage.PlayerMigrated` sent from handoff driver on cross-host commits
- [ ] `CoordMessage.UpstreamSwitch` broadcast after `sessionRoutes.Migrate`
- [ ] `--gateway-mode=local-shortcut` (default) bypasses MeshData for colocated sessions
- [ ] `--gateway-mode=always-proxy` forces MeshData codec
- [ ] `examples/4node-basic` is playable in 3-process setup (coord + 2 nodes) via a browser client
- [ ] `CLAUDE.md` "Multi-process gameplay" section updated to reflect reality

## Out of scope (deferred past S6)

- **Session tokens + coordinator crash recovery** — S6.5 or S7. Requires token issuance, client-side storage, reconnect handler, and coordinator-side token validation. Adds ~4-5 tasks; scope-cut for now.
- **`--mode=gateway` separate process** — the interface this plan defines is compatible with a future split, but the process boundary is deferred.
- **Input rate limiting per session** — recommended by the research but hardening not capstone.
- **UDP proxying** — WebSocket only for S6.
- **Cross-region gateway failover** — operational concern.
- **Distributed cell splits/merges** — S7.

---

## Risk notes

- **Gateway proxy drain loop polling.** The T4 draft uses a 1ms sleep in the drain goroutine because a channel-per-session approach requires wiring into `net.Conn.readPump` which is more invasive. If CPU cost becomes a problem (N goroutines × 1000 wakes/sec = noticeable), move to a channel-driven design.

- **Epoch races.** The `sessionRoutes.Migrate` call is atomic under write lock, but between the lock release and the `UpstreamSwitch` broadcast there's a window where client input with the NEW epoch arrives at the OLD host before the OLD host knows it's lost the session. The old host's `VirtualConnManager.InjectInput` will accept it and the input will be processed on a cell that no longer owns the entity. Mitigation: include the session epoch in `MeshFrame.ClientInput` and have the receiving host's `VirtualConnManager.InjectInput` compare against the current local epoch, discarding stale ones. Add this as a follow-up in T6 if the integration test surfaces the issue.

- **Coordinator as `HostNetwork` peer.** The coordinator needs to be addressable by nodes for the reverse `ClientFrame` path. In S4.5, `PeerList` broadcasts populate each node's peer map with other nodes. The coordinator needs to be in that map too, under a stable ID like `"coordinator"`. Decide in T4 whether to extend the PeerList construction to include the coordinator OR have each node open a MeshData stream to the coordinator at registration time. Both work; the first is cleaner.

- **`PlayerAssignment` routing on the node.** The coordinator knows which cell the player belongs to; nodes don't have their own sessionRoutes. Adding `to_cell_id` to the proto is the minimal intervention. If the schema gets cluttered over time, consider a `MeshFrame.SessionControl` variant that subsumes PlayerAssignment + ClientDisconnect into one oneof.

- **`Broadcast` on CoordMessage_UpstreamSwitch.** The broadcast fires to every registered host, even ones that don't have the session. Most will no-op. For small clusters (<10 hosts) this is fine; for large clusters a targeted send to just the affected hosts is worth designing.

- **Local-shortcut correctness testing.** By the research guidance, `always-proxy` should be the CI default. The T9 integration test uses `always-proxy` explicitly. Long-running smoke tests under `local-shortcut` are the user's responsibility.

- **Engine `ConnMgr` type change ripple.** T1 changes `engine.Engine.ConnMgr` from `*net.ConnManager` to `net.ConnSender`. Any code outside engine that accesses `engine.ConnMgr.<method>` where method is gateway-only breaks. Grep for every hit and decide: does it need the narrow interface or the full type? Most hits will be the narrow interface (hot-path game loop code); gateway-only hits are in `pkg/universe/coordinator.go` which holds the concrete type separately.

---

## Approval gate

Before executing, the user reviews this plan and confirms:

1. **Approved as-is** → proceed to T1.
2. **Approved with changes** → list the changes, update the plan, then proceed.
3. **Defer or redesign** → revisit.

If approved, this plan replaces the S6 portion of `2026-04-13-S4.5-S5-S6-distributed-mesh-continuation.md`. The master plan stays for historical context.
