# S3: Proto Schema + gRPC Bridge — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Define the `meshpb` proto schema and ship a `grpcBridge` data-plane implementation so cells on different `Host` instances can exchange `BorderFrame`, handoff, and cross-cell-action messages over a real network transport. End state: `4node-basic` can run with 2 `Host` instances in the same process, communicating over a gRPC loopback, with all boundary crossings and border replication working end-to-end.

**Architecture:** A new `pkg/universe` dispatch layer resolves each destination cell to its owning `Host` and picks a bridge. Local destinations use `localBridge` (existing direct-channel path, renamed for clarity). Remote destinations use `grpcBridge`, which serializes `CellMessage` envelopes into `meshpb.MeshFrame` protos and sends them on a long-lived bidi stream to the peer `Host`'s `MeshData` server. The receiving `MeshData` server converts frames back into `CellMessage` envelopes and delivers them to the target cell's inbox. `MeshControl` proto is defined but **not implemented** in S3 — the in-process `Coordinator` continues to own control-plane responsibilities until S4.

**S3 simplifications (deliberate):**
- `MeshControl` server is not implemented. S3 only ships the proto definitions for S4 to consume.
- Host-to-host peer lists are static, computed at coordinator build time from a test-only `TestHosts` config field. No registration handshake, no rendezvous hashing, no heartbeats — those ship in S4.
- `--gateway-mode=always-proxy` forces gRPC even for local destinations (for testing). `local-shortcut` is the default.
- Reconnect logic is minimal: on stream error, log and drop the frame (no retry loop). S4 adds exponential backoff + reconnect.
- Everything still runs in one OS process. S3 proves the wire format + serialization path; multi-process execution lands in S8.

**Research-driven choices (grpc-go + HTTP/2):**
- Bidi streaming (`stream MeshFrame returns stream MeshFrame`) — one long-lived connection per peer pair, naturally ordered, cheap to multiplex. Matches the spec's "Bridge interface" and the existing `BorderDispatcher`'s fire-and-forget semantics.
- `oneof` envelope inside `MeshFrame` — one stream to manage per peer; Prepare always arrives before Commit because gRPC preserves intra-stream order.
- `bytes` for `BorderFrame.data` — the existing `pkg/replication.Frame` binary encoding is wrapped without double-encoding. Same treatment for `ClientInput`/`ClientFrame`.
- `insecure.NewCredentials()` for S3 — this is a loopback test transport. S4+ adds mTLS.
- **Per-peer outbound channel + dedicated sender goroutine** — grpc-go `ClientStream.Send` is not safe for concurrent use from multiple goroutines. The recommended pattern (per the grpc-go concurrency doc) is to funnel all sends through a single goroutine via a channel. This also decouples cell-loop goroutines from stream backpressure so one slow peer can't stall unrelated cells on the same host.
- **Keepalive pings on both sides** — without keepalive the HTTP/2 connection will never detect a dead peer during long idle periods (border frames can be near-silent if no entities are near a boundary). Symmetric `Time=60s, Timeout=20s, PermitWithoutStream=true` on both server and client; server `MinTime=30s` to reject runaway pings. 60s > 30s avoids `ENHANCE_YOUR_CALM` GOAWAYs.
- **Explicit 16MB message-size cap** — default is 4MB Recv/Send. Not an S3 problem, but S7 `CellMigratePayload` will blow past it silently. Setting `16 << 20` now makes the limit grep-able and prevents a future silent bug.
- **Forward-compat: keep `require_unimplemented_servers=true` (the default)** — when `MeshControl` gains new methods in S4, we want the compile-time break, not a runtime panic. Test fakes embed `UnimplementedMeshControlServer` — one line each.
- **Lossy vs. reliable send policies** — border frames may drop (30-tick resync recovers); handoff messages must not drop. `HostNetwork` exposes two send entrypoints (`SendLossy` / `SendReliable`) so `grpcBridge` can pick per message type. `SendLossy` is non-blocking on the per-peer queue; `SendReliable` is blocking with a deadline and returns an error the caller can log.
- **Shutdown has a deadline + hard-stop fallback** — `server.GracefulStop()` hangs indefinitely if bidi streams are still open ([grpc-go#2888](https://github.com/grpc/grpc-go/issues/2888), [#5930](https://github.com/grpc/grpc-go/issues/5930)). We cancel outbound streams first, then race `GracefulStop` against a 5s timer and fall back to `Stop()`.

**Tech Stack:** Go, `google.golang.org/grpc` (new), `google.golang.org/protobuf` (already present), `buf` codegen (already wired).

**Spec:** [docs/superpowers/specs/2026-04-12-distributed-mesh-design.md](../specs/2026-04-12-distributed-mesh-design.md) — §5 (Proto & transport layer)

---

## File Structure

### Files to create

| Path | Responsibility |
|---|---|
| `proto/meshpb/mesh.proto` | `MeshControl` + `MeshData` service definitions, all envelope messages |
| `gen/go/meshpb/mesh.pb.go` | Protobuf-generated (buf) |
| `gen/go/meshpb/mesh_grpc.pb.go` | gRPC service stubs (buf, new plugin) |
| `pkg/universe/grpc_bridge.go` | `grpcBridge` client — serializes `CellMessage` → `MeshFrame`, sends on bidi stream |
| `pkg/universe/grpc_bridge_test.go` | Round-trip unit tests for frame conversion |
| `pkg/universe/mesh_data_server.go` | `meshDataServer` — receives `MeshFrame` bidi stream, routes to local cell inboxes |
| `pkg/universe/mesh_data_server_test.go` | Server-side test fixtures |
| `pkg/universe/host_network.go` | `HostNetwork` — owns the gRPC server, peer client stubs, and the dispatch lookup table |
| `pkg/universe/two_host_integration_test.go` | End-to-end test: 2 hosts, gRPC transport, boundary crossing works |

### Files to modify

| Path | What changes |
|---|---|
| `go.mod` / `go.sum` | Add `google.golang.org/grpc` |
| `buf.gen.yaml` | Add `buf.build/grpc/go` plugin for service stubs |
| `pkg/universe/cell_bridge_impl.go` | Split: rename `cellBridge` → `localBridge` (direct-channel intra-host path) OR turn it into a dispatching shim that delegates to either local or grpc per destination |
| `pkg/universe/bridge.go` | No interface changes expected; internal routing only |
| `pkg/universe/host.go` | Add `Network *HostNetwork` field; `Network` is nil in single-host colocated mode |
| `pkg/universe/coordinator.go` | Accept `TestHosts []string` config; distribute cells across multiple `Host` instances; wire each Host's `HostNetwork` and exchange peer addresses during `Build()` |
| `pkg/universe/config.go` (or wherever `Config` lives) | `TestHosts []string`, `GatewayMode string` fields |
| `cmd/server/main.go` / `examples/4node-basic/main.go` | Add `--two-hosts` dev flag that populates `TestHosts` |
| `pkg/mmokit/mmokit.go` | Export `HostNetwork` if consumers need it (probably not) |

### Files to leave alone

- `pkg/universe/loopback_bridge.go` — stays as-is, it's the test-only synchronous helper for unit tests that don't want real networking.
- `pkg/universe/handoff_driver.go` — no changes; the `Bridge` interface it holds is unchanged.

---

## Task Breakdown

### Task 1: Wire gRPC codegen + dependency

**Files:**
- Modify: `go.mod`, `buf.gen.yaml`, `justfile`

- [ ] **Step 1: Add grpc-go dependency**

```bash
go get google.golang.org/grpc@latest
go mod tidy
```

- [ ] **Step 2: Add buf gRPC plugin**

Edit `buf.gen.yaml`:

```yaml
version: v2
inputs:
  - directory: proto
plugins:
  - remote: buf.build/protocolbuffers/go
    out: gen/go
    opt: paths=source_relative
  - remote: buf.build/grpc/go
    out: gen/go
    opt: paths=source_relative
  - remote: buf.build/protocolbuffers/csharp
    out: gen/csharp
  - remote: buf.build/bufbuild/es
    out: gen/es
```

Note: we deliberately **keep `require_unimplemented_servers` at its default (true)**. Real server impls and test fakes both embed `UnimplementedMeshControlServer` / `UnimplementedMeshDataServer`. When S4 adds new methods to `MeshControl`, this gives us a compile-time break instead of a runtime panic. One line per fake; worth the safety.

- [ ] **Step 3: Verify buf + go tooling still run clean**

```bash
just proto   # may be a no-op until Task 2 adds mesh.proto
go vet ./...
```

- [ ] **Step 4: Commit**

```bash
git commit -m "build(proto): add grpc-go plugin + grpc-go dependency

Wires buf.build/grpc/go into buf.gen.yaml so service stubs land in
gen/go/ alongside the existing message codegen. Adds google.golang.org/grpc
to go.mod as a direct dependency. Keeps require_unimplemented_servers
at its default (true) so forward-compat breaks show up at compile
time when S4 adds new MeshControl methods — test fakes embed
UnimplementedXServer (one line each)."
```

---

### Task 2: Define `proto/meshpb/mesh.proto`

**Files:**
- Create: `proto/meshpb/mesh.proto`
- Generated: `gen/go/meshpb/mesh.pb.go`, `gen/go/meshpb/mesh_grpc.pb.go`

- [ ] **Step 1: Write `proto/meshpb/mesh.proto`**

Full schema — both services and every envelope message. Even though `MeshControl` is not implemented in S3, defining it now means S4 doesn't have to touch generated code layout. Base on spec §5.

Key messages to include:
- `MeshControl` service + `HostMessage` / `CoordMessage` oneof envelopes (each with all nested types stubbed — `RegisterHost`, `Heartbeat`, `CellReady`, `CellStopped`, `SplitComplete`, `MergeComplete`, `MigrateReady`, `PersistBatch`, `PlayerHandoff`, `RegisterAck`, `CellAssign`, `CellRelease`, `CellSplit`, `CellMerge`, `CellMigrate`, `CellMigrateCommit`, `PeerList`, `NetIDRangeGrant`, `PersistResult`, `UpstreamSwitch`)
- `MeshData` service + `MeshFrame` oneof envelope with `BorderFrame`, `HandoffPrepare`, `HandoffCommit`, `HandoffCancel`, `ForwardInput`, `CellSpawnTransfer`, `CellMergeTransfer`, `CellMigratePayload`, `ClientInput`, `ClientFrame`, `ChatRelay`, `CrossNodeAction`, `ActionResult`, `PlayerAssignment`, `SessionTransfer`, `SpawnTransfer`

Header:

```protobuf
syntax = "proto3";
package meshpb;
option go_package = "github.com/zenion/mmoserver/gen/go/meshpb";
```

S3 only requires the `MeshData` messages and service to be fully wired — `MeshControl` message bodies can be defined with the minimum fields the spec describes, but the server/client stubs are generated and not used until S4.

Each message must round-trip from its `CellMessage`/`HandoffPreparePayload`/etc. counterpart. Map fields one-to-one:

- `BorderFrame { string from_cell_id = 1; bytes data = 2; }` — `data` is the encoded `replication.Frame`.
- `HandoffPrepare { uint32 net_id = 1; uint32 epoch = 2; uint32 kind = 3; bytes transfer_blob = 4; repeated ClientBaseline baselines = 5; uint64 expected_tick = 6; uint32 old_epoch = 7; string from_cell_id = 8; }` — `ClientBaseline` is a nested message matching `ClientBaselineEntry`.
- `HandoffCommit { uint32 net_id = 1; uint32 epoch = 2; uint64 commit_tick = 3; string from_cell_id = 4; }`
- `HandoffCancel { uint32 net_id = 1; uint32 epoch = 2; string from_cell_id = 3; }`
- `ForwardInput { uint32 conn_id = 1; bytes input_blob = 2; string from_cell_id = 3; }`
- `CrossNodeAction` / `ActionResult` — match the existing Go struct field-for-field. Read `pkg/universe/action.go` and `cell.go`.
- `ChatRelay { string username = 1; string text = 2; string from_cell_id = 3; }`
- `PlayerAssignment { uint32 conn_id = 1; string username = 2; bool is_reconnect = 3; bytes data = 4; }` — `data` is `bytes` because per-game session data is opaque.
- `SessionTransfer { uint32 conn_id = 1; string username = 2; string state_tag = 3; bytes data = 4; }`
- `SpawnTransfer { uint32 conn_id = 1; string username = 2; }`
- `MeshFrame` wraps all of the above in a single `oneof msg`.

Every envelope must carry `from_cell_id` (or `from_host_id`, pick one convention and stick with it) — the receiver needs to know which cell the frame came from to look up the `Cell` instance in its local `Host.Cells` map. Prefer `from_cell_id` for symmetry with the existing `CellMessage.FromCellID` field.

- [ ] **Step 2: Generate Go code**

```bash
just proto
```

Must emit `gen/go/meshpb/mesh.pb.go` and `gen/go/meshpb/mesh_grpc.pb.go`.

- [ ] **Step 3: Verify**

```bash
go vet ./gen/go/meshpb/
```

- [ ] **Step 4: Commit**

```bash
git commit -m "feat(meshpb): define MeshControl + MeshData proto services

Full envelope schema for distributed mesh data plane. MeshData wires
all border/handoff/client envelopes into a single bidi stream.
MeshControl is fully typed but not yet implemented — S4 lands the
control-plane server on top of this schema.

generated: gen/go/meshpb/mesh.pb.go, mesh_grpc.pb.go"
```

---

### Task 3: Frame converter — `CellMessage` ↔ `meshpb.MeshFrame`

**Files:**
- Create: `pkg/universe/mesh_frame_codec.go`
- Create: `pkg/universe/mesh_frame_codec_test.go`

The converter is a pure function pair:

```go
func encodeCellMessage(msg CellMessage, destCellID string) (*meshpb.MeshFrame, error)
func decodeMeshFrame(frame *meshpb.MeshFrame) (CellMessage, error)
```

`destCellID` goes into the `MeshFrame.dest_cell_id` field defined in Task 2 — the receiver reads it to look up the target cell before dispatching the `oneof` payload.

- [ ] **Step 1: Implement `encodeCellMessage`**

Switch on `msg.Type`. For each supported type, build the corresponding `meshpb.MeshFrame` variant. Unsupported types (e.g. `MsgType` values that are purely intra-host) return an error — `grpcBridge.send` will refuse to dispatch them and log a warning.

Supported over the wire: `MsgBorderFrame`, `MsgHandoffPrepare`, `MsgHandoffCommit`, `MsgHandoffCancel`, `MsgForwardInput`, `MsgChat`, `MsgCrossNodeAction`, `MsgActionResult`, `MsgPlayerAssignment`, `MsgSessionTransfer`, `MsgSpawnTransfer`.

Not supported (should not cross hosts): none currently — everything the `Bridge` interface sends should serialize. If a type is missing, add it explicitly; do not silently drop.

- [ ] **Step 2: Implement `decodeMeshFrame`**

Switch on `frame.Msg` oneof. Reverse every conversion from Step 1. Set `CellMessage.FromCellID` from the envelope's `from_cell_id` field.

- [ ] **Step 3: Table-driven round-trip test**

```go
func TestMeshFrameRoundTrip(t *testing.T) {
    cases := []CellMessage{
        { Type: MsgBorderFrame, FromCellID: "cell_0_0", BorderFrame: []byte{1,2,3} },
        { Type: MsgHandoffPrepare, FromCellID: "cell_0_0", HandoffPrepare: &HandoffPreparePayload{NetID:42, Epoch:3, ...} },
        // ... one case per supported MsgType
    }
    for _, orig := range cases {
        frame, err := encodeCellMessage(orig)
        require.NoError(t, err)
        decoded, err := decodeMeshFrame(frame)
        require.NoError(t, err)
        require.Equal(t, orig, decoded)  // deep-equal the CellMessage
    }
}
```

Important: `PlayerAssignment.Data` / `SessionTransfer.Data` are `any` in Go but `bytes` on the wire. For S3, the codec assumes these fields are `[]byte` when non-nil and returns an error otherwise — game code must already be serializing before dispatching across hosts. Document this clearly in a comment.

- [ ] **Step 4: Verify**

```bash
go vet ./... && go test -count=1 ./pkg/universe/ -run MeshFrame
```

- [ ] **Step 5: Commit**

```bash
git commit -m "feat(universe): MeshFrame codec for CellMessage wire format

Pure function pair that converts between the internal CellMessage
envelope and the meshpb.MeshFrame proto. Table-driven round-trip test
covers every MsgType that crosses host boundaries."
```

---

### Task 4: `HostNetwork` — gRPC server + peer client stubs

**Files:**
- Create: `pkg/universe/host_network.go`
- Create: `pkg/universe/mesh_data_server.go`
- Modify: `pkg/universe/host.go` — add `Network *HostNetwork` field

`HostNetwork` is the per-process container for the gRPC server (inbound `MeshData`) and the set of open client streams to peer hosts (outbound `MeshData`). Each peer has a **dedicated sender goroutine** that drains an outbound queue — all `Send()` calls on a given stream funnel through this one goroutine, which is the pattern grpc-go explicitly recommends and also decouples cell-loop goroutines from stream backpressure.

- [ ] **Step 1: Define `HostNetwork` + `hostPeer` with outbound queue**

```go
// Tunables — exported so tests can override.
const (
    peerOutQueueSize  = 256              // depth of per-peer outbound channel
    peerSendDeadline  = 250 * time.Millisecond // reliable send block cap
    meshMaxMsgBytes   = 16 << 20         // 16MB send/recv cap
)

// HostNetwork owns the gRPC server and peer client streams for one Host.
// Nil in single-host colocated mode.
type HostNetwork struct {
    hostID   string
    grpcAddr string        // listen address, e.g. ":0" for ephemeral
    server   *grpc.Server
    listener net.Listener  // so the bound address is recoverable for test peers
    log      *logger.Logger
    host     *Host          // back-pointer for inbound frame routing

    ctx    context.Context
    cancel context.CancelFunc

    mu    sync.RWMutex
    peers map[string]*hostPeer // hostID -> live client stream
}

type hostPeer struct {
    hostID   string
    grpcAddr string

    conn    *grpc.ClientConn
    stream  meshpb.MeshData_DataClient
    cancel  context.CancelFunc // cancels the stream context on shutdown / error

    outQ chan outboundFrame // buffered; sender goroutine drains it
    done chan struct{}      // closed when sender goroutine exits
}

type outboundFrame struct {
    frame *meshpb.MeshFrame
    // reliable sends get a result channel so the caller can block + observe errors.
    // lossy sends leave this nil and treat Send as fire-and-forget.
    result chan<- error
}
```

- [ ] **Step 2: Implement `NewHostNetwork(host *Host, grpcAddr string, log *logger.Logger) (*HostNetwork, error)`**

```go
func NewHostNetwork(host *Host, grpcAddr string, log *logger.Logger) (*HostNetwork, error) {
    listener, err := net.Listen("tcp", grpcAddr)
    if err != nil { return nil, fmt.Errorf("mesh listen: %w", err) }

    ctx, cancel := context.WithCancel(context.Background())
    n := &HostNetwork{
        hostID:   host.ID,
        grpcAddr: listener.Addr().String(),
        listener: listener,
        log:      log,
        host:     host,
        ctx:      ctx,
        cancel:   cancel,
        peers:    make(map[string]*hostPeer),
    }

    n.server = grpc.NewServer(
        grpc.MaxRecvMsgSize(meshMaxMsgBytes),
        grpc.MaxSendMsgSize(meshMaxMsgBytes),
        grpc.KeepaliveParams(keepalive.ServerParameters{
            Time:    60 * time.Second,
            Timeout: 20 * time.Second,
        }),
        grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
            MinTime:             30 * time.Second, // reject client pings faster than this
            PermitWithoutStream: true,
        }),
    )
    meshpb.RegisterMeshDataServer(n.server, &meshDataServer{net: n})

    go func() {
        if err := n.server.Serve(listener); err != nil && !errors.Is(err, grpc.ErrServerStopped) {
            log.Log(CatMeshMsg, "[%s] mesh grpc serve: %v", host.ID, err)
        }
    }()

    return n, nil
}

// Addr returns the actual bound address (useful when grpcAddr was ":0").
func (n *HostNetwork) Addr() string { return n.grpcAddr }
```

- [ ] **Step 3: Implement `ConnectPeer(hostID, grpcAddr string) error`**

Opens the stream, starts the dedicated sender goroutine, and starts the dedicated recv goroutine. Both goroutines exit cleanly on context cancel.

```go
func (n *HostNetwork) ConnectPeer(hostID, grpcAddr string) error {
    conn, err := grpc.NewClient(grpcAddr,
        grpc.WithTransportCredentials(insecure.NewCredentials()),
        grpc.WithKeepaliveParams(keepalive.ClientParameters{
            Time:                60 * time.Second,
            Timeout:             20 * time.Second,
            PermitWithoutStream: true,
        }),
        grpc.WithDefaultCallOptions(
            grpc.MaxCallSendMsgSize(meshMaxMsgBytes),
            grpc.MaxCallRecvMsgSize(meshMaxMsgBytes),
        ),
    )
    if err != nil { return fmt.Errorf("mesh dial %s: %w", grpcAddr, err) }

    streamCtx, cancel := context.WithCancel(n.ctx)
    client := meshpb.NewMeshDataClient(conn)
    stream, err := client.Data(streamCtx)
    if err != nil {
        cancel()
        conn.Close()
        return fmt.Errorf("mesh open stream %s: %w", hostID, err)
    }

    peer := &hostPeer{
        hostID:   hostID,
        grpcAddr: grpcAddr,
        conn:     conn,
        stream:   stream,
        cancel:   cancel,
        outQ:     make(chan outboundFrame, peerOutQueueSize),
        done:     make(chan struct{}),
    }

    n.mu.Lock()
    if old, ok := n.peers[hostID]; ok {
        old.cancel()
    }
    n.peers[hostID] = peer
    n.mu.Unlock()

    go n.runPeerSender(peer)
    go n.runPeerReceiver(peer)
    return nil
}

func (n *HostNetwork) runPeerSender(p *hostPeer) {
    defer close(p.done)
    for {
        select {
        case <-n.ctx.Done():
            return
        case of, ok := <-p.outQ:
            if !ok { return }
            err := p.stream.Send(of.frame)
            if of.result != nil {
                of.result <- err
            }
            if err != nil {
                n.log.Log(CatMeshMsg, "[%s] peer %s send error: %v", n.hostID, p.hostID, err)
                n.dropPeer(p.hostID)
                return
            }
        }
    }
}

func (n *HostNetwork) runPeerReceiver(p *hostPeer) {
    for {
        frame, err := p.stream.Recv()
        if err != nil {
            if !errors.Is(err, io.EOF) && n.ctx.Err() == nil {
                n.log.Log(CatMeshMsg, "[%s] peer %s recv error: %v", n.hostID, p.hostID, err)
            }
            n.dropPeer(p.hostID)
            return
        }
        if err := n.routeInboundFrame(frame); err != nil {
            n.log.Log(CatMeshMsg, "[%s] peer %s route error: %v", n.hostID, p.hostID, err)
        }
    }
}

func (n *HostNetwork) dropPeer(hostID string) {
    n.mu.Lock()
    p, ok := n.peers[hostID]
    if ok { delete(n.peers, hostID) }
    n.mu.Unlock()
    if !ok { return }
    p.cancel()
    _ = p.conn.Close()
}
```

- [ ] **Step 4: Implement `SendLossy` and `SendReliable`**

Two entrypoints with different backpressure semantics. `grpcBridge` picks per message type.

```go
// SendLossy enqueues a frame for fire-and-forget delivery. If the peer's
// outbound queue is full (slow peer or dead stream), the frame is dropped
// and false is returned. Used for border frames: the 30-tick forced resync
// recovers the receiver.
func (n *HostNetwork) SendLossy(hostID string, frame *meshpb.MeshFrame) bool {
    n.mu.RLock()
    p, ok := n.peers[hostID]
    n.mu.RUnlock()
    if !ok { return false }

    select {
    case p.outQ <- outboundFrame{frame: frame}:
        return true
    default:
        // Queue full — drop. Upper layer logs its own metric.
        return false
    }
}

// SendReliable enqueues a frame and waits for the sender goroutine to
// push it on the stream. Returns an error if the peer is unknown, the
// queue is full past peerSendDeadline, or the Send call itself failed.
// Used for handoff, action, chat, and player assignment frames which
// must not drop.
func (n *HostNetwork) SendReliable(hostID string, frame *meshpb.MeshFrame) error {
    n.mu.RLock()
    p, ok := n.peers[hostID]
    n.mu.RUnlock()
    if !ok { return fmt.Errorf("no peer %q", hostID) }

    result := make(chan error, 1)
    deadline := time.NewTimer(peerSendDeadline)
    defer deadline.Stop()

    select {
    case p.outQ <- outboundFrame{frame: frame, result: result}:
        // enqueued; now wait for the sender goroutine to push it
    case <-deadline.C:
        return fmt.Errorf("peer %q queue backpressure", hostID)
    case <-n.ctx.Done():
        return n.ctx.Err()
    }

    select {
    case err := <-result:
        return err
    case <-deadline.C:
        return fmt.Errorf("peer %q send deadline exceeded", hostID)
    case <-n.ctx.Done():
        return n.ctx.Err()
    }
}
```

- [ ] **Step 5: Implement `Shutdown()` with deadline + hard-stop fallback**

`server.GracefulStop()` hangs indefinitely when bidi streams are open — we cancel outbound streams first, then race GracefulStop against a 5s timer and fall back to `Stop()`.

```go
func (n *HostNetwork) Shutdown() error {
    // 1. Cancel the root context — sender goroutines exit on their next
    //    select, receiver goroutines see stream Recv error.
    n.cancel()

    // 2. Cancel every outbound client stream + close its conn.
    n.mu.Lock()
    peers := n.peers
    n.peers = nil
    n.mu.Unlock()
    for _, p := range peers {
        p.cancel()
        _ = p.conn.Close()
    }

    // 3. Wait for sender goroutines to exit (bounded).
    for _, p := range peers {
        select {
        case <-p.done:
        case <-time.After(500 * time.Millisecond):
            // sender wedged; the server.Stop below will force it
        }
    }

    // 4. Race GracefulStop against a 5s deadline; hard-stop on timeout.
    stopped := make(chan struct{})
    go func() { n.server.GracefulStop(); close(stopped) }()
    select {
    case <-stopped:
    case <-time.After(5 * time.Second):
        n.log.Log(CatMeshMsg, "[%s] mesh grpc hard-stop after GracefulStop timeout", n.hostID)
        n.server.Stop()
        <-stopped
    }

    return nil // listener is closed by server.Stop
}
```

- [ ] **Step 6: Wire `Host.Network`**

Add `Network *HostNetwork` to `pkg/universe/host.go`. Zero value means colocated mode; non-nil means distributed.

- [ ] **Step 7: `WaitPeersReady()` barrier**

Integration tests (Task 8) need deterministic startup. Add a simple helper that blocks until every configured peer has an entry in `n.peers`:

```go
func (n *HostNetwork) WaitPeersReady(expected []string, timeout time.Duration) error {
    deadline := time.Now().Add(timeout)
    for {
        n.mu.RLock()
        ready := true
        for _, id := range expected {
            if _, ok := n.peers[id]; !ok { ready = false; break }
        }
        n.mu.RUnlock()
        if ready { return nil }
        if time.Now().After(deadline) {
            return fmt.Errorf("peers not ready within %s", timeout)
        }
        time.Sleep(5 * time.Millisecond)
    }
}
```

- [ ] **Step 8: Verify**

```bash
go vet ./... && go test -count=1 ./pkg/universe/ -run HostNetwork
```

- [ ] **Step 9: Commit**

```bash
git commit -m "feat(universe): HostNetwork — gRPC server + peer sender goroutines

HostNetwork owns the MeshData gRPC server and bidi client streams to
peer hosts. Each peer has a dedicated sender goroutine draining a
buffered outbound channel — the pattern grpc-go recommends because
ClientStream.Send is not safe for concurrent use, and this also
decouples cell loops from stream backpressure so one slow peer
cannot stall unrelated cells on the same host.

- Keepalive on both server and client (Time=60s, Timeout=20s,
  PermitWithoutStream=true, server MinTime=30s) so dead peers are
  detected during idle periods.
- 16MB explicit MaxRecv/MaxSendMsgSize caps so future cell-migration
  payloads (S7) do not silently hit the 4MB default.
- SendLossy (non-blocking, drop-on-full) for border frames.
- SendReliable (blocking with 250ms deadline) for handoff, action,
  chat, and assignment frames that must not drop.
- Shutdown cancels peer streams first, races GracefulStop against
  a 5s deadline, then falls back to Stop — works around grpc-go#2888.

Nil in single-host colocated mode; hosts gracefully skip it."
```

---

### Task 4.5: Backpressure + lifecycle unit tests for `HostNetwork`

**Files:**
- Create: `pkg/universe/host_network_test.go`

- [ ] **Step 1: Set up a 2-peer fixture**

Stand up two `HostNetwork` instances in-process (`grpcAddr: ":0"`), cross-connect them via `ConnectPeer`, and wait for `WaitPeersReady`. Each test gets `t.Cleanup(net.Shutdown)` to verify Shutdown terminates cleanly.

- [ ] **Step 2: SendLossy drops on full queue**

```go
// Block the peer's sender goroutine by making Recv never progress — easier:
// construct a HostNetwork whose peer stream is real but whose receiver never
// reads, then fill the channel past peerOutQueueSize.
func TestSendLossyDropsOnFullQueue(t *testing.T) {
    // Fill the queue deterministically; assert SendLossy returns false
    // once the buffer is full, and the drop count metric (if any) advances.
}
```

- [ ] **Step 3: SendReliable blocks then times out**

Same setup. Call `SendReliable` in a goroutine; assert it returns an error matching `"queue backpressure"` or `"send deadline exceeded"` after ~`peerSendDeadline`.

- [ ] **Step 4: Shutdown terminates with active stream**

Start two peers, have one mid-stream Send, then call `Shutdown`. Assert it returns within ~1s (well under the 5s GracefulStop cap) and leaks no goroutines (use `goleak` or runtime.NumGoroutine snapshot).

- [ ] **Step 5: Dead peer drops from map**

Cancel peer A's underlying `grpc.ClientConn` from the outside. Call `SendReliable("peer-a", ...)`; assert the error, then assert the peer was removed from `n.peers`.

- [ ] **Step 6: Verify**

```bash
go test -count=1 -race ./pkg/universe/ -run HostNetwork
```

The `-race` flag is important here — the sender/receiver/shutdown interactions are the most likely places for a data race to sneak in.

- [ ] **Step 7: Commit**

```bash
git commit -m "test(universe): HostNetwork backpressure + shutdown lifecycle

Unit tests for SendLossy drop-on-full, SendReliable deadline,
Shutdown during active streams, and dead-peer cleanup. Run under
-race to catch sender/receiver/shutdown interactions."
```

---

### Task 5: `meshDataServer` — inbound frame routing

**Files:**
- Create: `pkg/universe/mesh_data_server.go`

- [ ] **Step 1: Implement the service**

```go
type meshDataServer struct {
    meshpb.UnimplementedMeshDataServer  // embed to satisfy forward-compat check
    net *HostNetwork
}

func (s *meshDataServer) Data(stream meshpb.MeshData_DataServer) error {
    for {
        frame, err := stream.Recv()
        if err != nil {
            if errors.Is(err, io.EOF) { return nil }
            return err
        }
        if err := s.net.routeInboundFrame(frame); err != nil {
            s.net.log.Log(CatMeshMsg, "[%s] mesh data recv error: %v", s.net.hostID, err)
        }
    }
}
```

- [ ] **Step 2: Implement `HostNetwork.routeInboundFrame(frame *meshpb.MeshFrame) error`**

```go
func (n *HostNetwork) routeInboundFrame(frame *meshpb.MeshFrame) error {
    msg, err := decodeMeshFrame(frame)
    if err != nil { return err }
    destCellID := resolveDestCellID(msg)  // see below
    cell := n.host.CellByID(destCellID)   // new helper on Host
    if cell == nil {
        return fmt.Errorf("no cell %q on host %s", destCellID, n.hostID)
    }
    select {
    case cell.Inbox <- msg:
    default:
        n.log.Log(CatMeshMsg, "[%s] inbox full for %s, dropping %v", n.hostID, destCellID, msg.Type)
    }
    return nil
}
```

`resolveDestCellID`: the destination is **not** in the envelope — the sender knows it from the `SendX(destCellID, ...)` call site. The receiver currently has to infer it. Two options:

1. **Add `dest_cell_id` to every envelope in `MeshFrame`.** Cleanest; envelope becomes self-describing. Requires a `dest_cell_id` field at the `MeshFrame` level (outside the oneof).
2. **Per-peer dest table.** The sender tells the server which cell it's targeting via gRPC metadata. Fragile.

Pick option 1. Add `string dest_cell_id = 100;` to `MeshFrame` in Task 2 (**go back and amend Task 2's proto**). Every encoder sets it; every decoder reads it before touching the oneof payload. This is a small proto change — do it now rather than shoehorning it in later.

- [ ] **Step 3: Add `Host.CellByID(cellID string) *Cell` helper**

The existing `Host.Cells map[CellID]*Cell` uses `CellID` (struct). Add a string lookup because envelopes carry string IDs. Can iterate the map or add a parallel `map[string]*Cell`.

- [ ] **Step 4: Verify**

```bash
go vet ./... && go test -count=1 ./pkg/universe/ -run MeshDataServer
```

- [ ] **Step 5: Commit**

```bash
git commit -m "feat(universe): meshDataServer routes inbound frames to local cells

Implements meshpb.MeshData service. Recv loop decodes MeshFrame,
looks up the destination cell by string ID on the local Host, and
delivers into its Inbox. Full envelope drops on inbox backpressure
to mirror the existing BorderDispatcher drop semantics. dest_cell_id
is carried outside the oneof on MeshFrame so routing happens before
variant dispatch."
```

---

### Task 6: `grpcBridge` — outbound bridge implementation

**Files:**
- Create: `pkg/universe/grpc_bridge.go`

`grpcBridge` is the `Bridge` implementation for **remote** destinations. It holds a reference to the local `Host` (for shortcut detection) and the local `HostNetwork` (for sending). It also holds a `cellToHost func(string) string` resolver — given a destCellID, which hostID owns it? Every `Send*` method picks between the colocated `cellBridge` shortcut, `HostNetwork.SendLossy`, and `HostNetwork.SendReliable` based on message semantics.

- [ ] **Step 1: Define `grpcBridge` + the lossy/reliable split**

```go
type grpcBridge struct {
    cell        *Cell               // source cell
    host        *Host               // local host (for IsLocal shortcut)
    coord       *Coordinator        // for control-plane lookups (CellOwner etc.)
    cellToHost  func(string) string // destCellID -> hostID
    local       *cellBridge         // fallback for colocated cells
    gatewayMode string              // "local-shortcut" | "always-proxy"
}
```

Policy matrix — dictates which `HostNetwork` entrypoint each `Bridge` method uses:

| Bridge method | Policy | Reason |
|---|---|---|
| `SendBorderFrame` | **Lossy** | Tick-driven; 30-tick forced resync recovers |
| `SendHandoffPrepare` | **Reliable** | Must arrive before Commit |
| `SendHandoffCommit` | **Reliable** | Authority flip — cannot drop |
| `SendHandoffCancel` | **Reliable** | Shadow cleanup — dropping leaks entities |
| `SendForwardInput` | **Reliable** | Rare safety-path input; dropping loses a player action |
| `SendAction` | **Reliable** | Cross-cell action — cannot drop |
| `SendActionResult` | **Reliable** | Cross-cell action — cannot drop |
| `RelayChatToOtherNodes` | **Reliable** | Dropping would lose user-visible chat |
| `OnPlayerTransfer` | (control plane, stays in-process via Coordinator in S3) | No wire format yet |
| `RequestRespawn` | (control plane, stays in-process via Coordinator in S3) | No wire format yet |

- [ ] **Step 2: Implement `shouldUseLocal` + shared dispatch helper**

```go
// sendViaGrpc is the shared remote-dispatch helper. reliable=true blocks
// with deadline; false is fire-and-forget drop-on-full.
func (b *grpcBridge) sendViaGrpc(destCellID string, msg CellMessage, reliable bool) {
    destHostID := b.cellToHost(destCellID)
    if destHostID == "" {
        b.cell.Log.Log(CatMeshMsg, "[%s] grpc send: no host for cell %s", b.cell.ID, destCellID)
        return
    }
    frame, err := encodeCellMessage(msg, destCellID)
    if err != nil {
        b.cell.Log.Log(CatMeshMsg, "[%s] grpc encode %v failed: %v", b.cell.ID, msg.Type, err)
        return
    }
    if reliable {
        if err := b.host.Network.SendReliable(destHostID, frame); err != nil {
            b.cell.Log.Log(CatMeshMsg, "[%s] grpc reliable send to %s failed: %v", b.cell.ID, destHostID, err)
        }
        return
    }
    if ok := b.host.Network.SendLossy(destHostID, frame); !ok {
        // Drop metric already counted by HostNetwork; just debug-log.
        b.cell.Log.Log(CatMeshMsg, "[%s] grpc lossy drop to %s (%v)", b.cell.ID, destHostID, msg.Type)
    }
}

func (b *grpcBridge) shouldUseLocal(destCellID string) bool {
    if b.gatewayMode == "always-proxy" {
        return false // force gRPC even for colocated cells
    }
    destHostID := b.cellToHost(destCellID)
    return destHostID == "" || b.host.IsLocal(destHostID)
}
```

Then each `Bridge` method becomes a one-liner. Example:

```go
func (b *grpcBridge) SendHandoffPrepare(destCellID string, payload *HandoffPreparePayload) {
    if b.shouldUseLocal(destCellID) {
        b.local.SendHandoffPrepare(destCellID, payload)
        return
    }
    b.sendViaGrpc(destCellID, CellMessage{
        Type: MsgHandoffPrepare,
        FromCellID: b.cell.ID,
        HandoffPrepare: payload,
    }, true) // reliable
}

func (b *grpcBridge) SendBorderFrame(destCellID string, frame []byte) {
    if b.shouldUseLocal(destCellID) {
        b.local.SendBorderFrame(destCellID, frame)
        return
    }
    b.sendViaGrpc(destCellID, CellMessage{
        Type: MsgBorderFrame,
        FromCellID: b.cell.ID,
        BorderFrame: frame,
    }, false) // lossy
}
```

Repeat the pattern for every `Bridge.Send*` method, picking reliable/lossy per the policy matrix above. Control-plane methods (`OnPlayerTransfer`, `RequestRespawn`) delegate to `b.local` unchanged in S3 — the Coordinator is still in-process.

- [ ] **Step 3: Wire PreTick/PostSystems passthrough**

`grpcBridge.PreTick` and `PostSystems` just delegate to the wrapped `local *cellBridge` — the tick-driven scan and drain happen on the local host regardless of which bridge is in use.

- [ ] **Step 4: Verify**

```bash
go vet ./... && go test -count=1 ./pkg/universe/
```

(Existing tests must keep passing; the new grpcBridge is not yet exercised by them.)

- [ ] **Step 5: Commit**

```bash
git commit -m "feat(universe): grpcBridge — outbound Bridge impl with lossy/reliable split

grpcBridge wraps a local cellBridge and picks per destination: shortcut
local if the destCellID maps to this host, otherwise encode and dispatch
via HostNetwork. --gateway-mode=always-proxy forces gRPC for every
destination.

Border frames use SendLossy (tick-driven, drop-on-full). Handoff,
action, chat, and assignment frames use SendReliable (blocking with
250ms deadline). Control-plane methods (OnPlayerTransfer, RequestRespawn)
delegate to the in-process Coordinator in S3.

PreTick/PostSystems tick hooks delegate to the wrapped cellBridge."
```

```bash
git commit -m "feat(universe): grpcBridge — outbound Bridge impl over MeshData

grpcBridge wraps a local cellBridge and picks the transport per
destination: shortcut local if the destCellID maps to this host,
otherwise serialize and send via HostNetwork. --gateway-mode=always-proxy
forces gRPC for every destination, for testing. PreTick/PostSystems
tick hooks delegate unchanged."
```

---

### Task 7: Coordinator — multi-host build with static peer list

**Files:**
- Modify: `pkg/universe/coordinator.go`
- Modify: wherever `Config` lives (likely `pkg/universe/coordinator.go` or a sibling)

- [ ] **Step 1: Add `TestHosts` + `GatewayMode` to `Config`**

```go
// TestHosts distributes cells across N in-process Host instances when
// non-empty. Each entry is a host ID. Cells are assigned to hosts via
// round-robin at Build() time. Each host gets its own HostNetwork
// bound to an ephemeral port; peer addresses are exchanged before the
// cells start their game loops. Colocated mode (the default, empty
// slice) creates a single "local" host.
TestHosts []string

// GatewayMode selects bridge behavior for colocated destinations.
// "local-shortcut" (default) uses the direct-channel cellBridge path
// for cells on the same host. "always-proxy" forces grpcBridge even
// for local destinations, exercising the gRPC serialization path in
// tests.
GatewayMode string
```

- [ ] **Step 2: Multi-host cell distribution in `Build()`**

Find the `Create the default host that owns all cells in colocated mode` block near line 319. Refactor to:

```go
hostIDs := cfg.TestHosts
if len(hostIDs) == 0 {
    hostIDs = []string{"local"}
}

hosts := make([]*Host, 0, len(hostIDs))
for _, hid := range hostIDs {
    h := NewHost(hid)
    h.Log = c.Log
    c.Hosts[hid] = h
    hosts = append(hosts, h)
    if len(hostIDs) > 1 {
        // Boot a HostNetwork per host so they can exchange frames.
        hn, err := NewHostNetwork(h, ":0", c.Log)
        if err != nil {
            panic(fmt.Errorf("host network bind: %w", err))
        }
        h.Network = hn
    }
}

// Round-robin cells across hosts.
hostIdx := 0
for _, cell := range cells {
    // ... existing cell creation ...
    targetHost := hosts[hostIdx % len(hosts)]
    targetHost.AddCell(cell2.Cell, cell2)
    hostIdx++
}
```

Populate a `cellToHost` map on the coordinator: `map[string]string` (stringCellID → hostID) built as cells are added.

After all hosts exist, iterate pairs and call `hostA.Network.ConnectPeer(hostB.ID, hostB.Network.Addr())` so every host has a live stream to every other host. In S4 this handshake is replaced with `PeerList` broadcasts from the coordinator.

- [ ] **Step 3: Install the right bridge per cell**

The existing code creates one `cellBridge` per cell. Extend it: in multi-host mode, wrap each `cellBridge` in a `grpcBridge` whose `cellToHost` closure reads from `coord.cellToHost`. In single-host colocated mode, keep the plain `cellBridge` (zero overhead).

- [ ] **Step 4: Shutdown path**

On coordinator shutdown, call `Network.Shutdown()` on every multi-host `Host`. Add to the existing shutdown sequence.

- [ ] **Step 5: Verify**

```bash
go vet ./... && go test -count=1 ./...
```

All existing tests must still pass in single-host mode.

- [ ] **Step 6: Commit**

```bash
git commit -m "feat(universe): coordinator supports TestHosts multi-host build

Config.TestHosts distributes cells across N in-process Host instances
via round-robin assignment. Each multi-host Host gets a HostNetwork
bound to an ephemeral port; peers are exchanged at build time. Cells
on multi-host coordinators get a grpcBridge wrapping the existing
cellBridge for per-destination shortcut vs. grpc dispatch.

Single-host colocated mode (the default) is unchanged — zero bridge
overhead, no HostNetwork created."
```

---

### Task 8: Integration test — 2 hosts over gRPC loopback

**Files:**
- Create: `pkg/universe/two_host_integration_test.go`

Goal: stand up a 2x2 coordinator with 2 hosts, verify a player entity can cross a cell boundary where source and destination cells live on **different** hosts. This exercises the full path: `BoundarySystem` → crossing queue → `HandoffDriver` → `grpcBridge.SendHandoffPrepare` → `HostNetwork.SendToPeer` → encoded `MeshFrame` → peer stream → `meshDataServer.Data` → `routeInboundFrame` → `decodeMeshFrame` → destination cell inbox → `SpawnShadow` → `PromoteShadow`.

- [ ] **Step 1: Write the test**

```go
func TestTwoHostBoundaryCrossing(t *testing.T) {
    // 2x2 grid, 2 hosts -> 2 cells per host. Round-robin means
    // cell_0_0 + cell_1_0 are on different hosts (good — the test
    // exercises a cross-host crossing).
    coord := NewCoordinator(Config{
        CellsX: 2, CellsY: 2,
        Headless: true,
        TestHosts: []string{"host-a", "host-b"},
        GatewayMode: "local-shortcut",
        // ... minimal world factory ...
    })
    coord.SetWorld(newTestWorldFactory())
    coord.Build()
    defer coord.Shutdown()

    // Drive a few ticks on every cell goroutine to ensure streams are live.
    // Spawn a test entity near the cell_0_0 / cell_1_0 boundary.
    // Nudge it across.
    // Tick the source cell, then the destination cell.
    // Assert: destination cell has an entity with the same NetID,
    // and the source cell no longer has it (Shadow promoted).
}
```

Use existing test utilities (see `pkg/universe/universe_test.go` for how two-cell setups are constructed today).

- [ ] **Step 2: Add a variant with `GatewayMode: "always-proxy"`**

Verifies that even the always-proxy path works end-to-end for **colocated** cells (both on host-a). Same assertion structure.

- [ ] **Step 3: Verify**

```bash
go test -count=1 ./pkg/universe/ -run TwoHost
```

- [ ] **Step 4: Commit**

```bash
git commit -m "test(universe): two-host boundary crossing over gRPC loopback

Integration test stands up a 2x2 coordinator distributed across
two in-process Host instances. Validates:
- MeshFrame serialization round-trips end-to-end
- grpcBridge shortcut decision routes correctly
- HandoffPrepare/Commit arrive in order on the remote inbox
- Shadow entity promotes on the destination

always-proxy variant forces gRPC even for colocated cells and
verifies the serialization path is exercised."
```

---

### Task 9: `--two-hosts` dev flag on examples/4node-basic

**Files:**
- Modify: `examples/4node-basic/main.go` (or wherever the flag parsing lives)

- [ ] **Step 1: Add the flag**

```go
var twoHosts = flag.Bool("two-hosts", false, "split the 2x2 grid across two in-process Host instances over gRPC loopback (dev-only)")
var gatewayMode = flag.String("gateway-mode", "local-shortcut", "local-shortcut | always-proxy")
```

Populate `Config.TestHosts` with `[]string{"host-a","host-b"}` when the flag is set; pass `GatewayMode` through unconditionally.

- [ ] **Step 2: Smoke test manually**

```bash
cd examples/4node-basic && just build && ./4node-basic --two-hosts --gateway-mode=always-proxy
```

Connect two web clients. Walk them across cell boundaries. Verify they stay visible to each other across the boundary (regression test for Bug #2 from S2).

- [ ] **Step 3: Commit**

```bash
git commit -m "feat(4node-basic): --two-hosts dev flag for gRPC loopback testing

Lets operators run the example as two in-process Host instances so
the gRPC data plane gets exercised by real interactive traffic. Pairs
with --gateway-mode=always-proxy to force the serialization path even
for colocated cells."
```

---

### Task 10: Verification + docs

- [ ] **Step 1: Full build + test**

```bash
go vet ./... && go test -count=1 ./... && just build
```

- [ ] **Step 2: Build all examples**

```bash
go build -o /tmp/_s3_check ./examples/4node-basic/ && rm /tmp/_s3_check
go build -o /tmp/_s3_check ./examples/simple/ && rm /tmp/_s3_check
```

- [ ] **Step 3: Update CLAUDE.md if anything user-facing changed**

Add a short "Distributed mode" bullet under Architecture describing the `--two-hosts` flag and the `meshpb` proto package.

- [ ] **Step 4: Commit**

```bash
git commit -m "docs: note --two-hosts dev flag + meshpb package in CLAUDE.md"
```

---

## Verification Checklist

After completing all tasks, verify:

- [ ] `go vet ./...` clean
- [ ] `go test -count=1 -race ./...` all pass (race detector is important for the sender/receiver goroutine interactions)
- [ ] `just build` produces `bin/server`
- [ ] `proto/meshpb/mesh.proto` defines both `MeshControl` and `MeshData` services
- [ ] `gen/go/meshpb/mesh_grpc.pb.go` exists and compiles
- [ ] `MeshFrame` has a top-level `dest_cell_id` field outside the oneof
- [ ] `HostNetwork` uses per-peer outbound channel + dedicated sender goroutine (no `sendMu` on `hostPeer`)
- [ ] Server and client both configure keepalive (`Time=60s`, `Timeout=20s`, `PermitWithoutStream=true`, server `MinTime=30s`)
- [ ] Both server and client configure `16MB` max send/recv message size
- [ ] `buf.gen.yaml` uses default `require_unimplemented_servers=true` (we rely on the compile-time forward-compat break)
- [ ] `SendLossy` drops on full queue (non-blocking); `SendReliable` blocks with 250ms deadline
- [ ] `grpcBridge` picks lossy vs. reliable per the Task 6 policy matrix
- [ ] `HostNetwork.Shutdown` terminates within 5s even with active bidi streams (`GracefulStop` → `Stop` fallback)
- [ ] Unit tests cover backpressure drop, reliable deadline, shutdown during active stream, dead-peer cleanup — all pass under `-race`
- [ ] `TwoHostBoundaryCrossing` test passes in both `local-shortcut` and `always-proxy` modes
- [ ] `examples/4node-basic --two-hosts` runs and a player can cross between cells owned by different hosts
- [ ] Single-host colocated mode is still the default and has zero gRPC overhead (no listener, no goroutines)
- [ ] `MeshControl` server is NOT implemented — it stays stubbed until S4
- [ ] No references to `MsgTransfer` / `MsgArrivalConfirm` remain (they were already retired in S2 but double-check)

---

## Out of scope for S3 (defer to later subprojects)

- **MeshControl server, registration, heartbeats, rendezvous assignment** → S4
- **Graceful reconnect with exponential backoff** → S4
- **mTLS** → S4
- **Postgres persistence** → S5
- **Gateway process + `VirtualConnManager`** → S6
- **Distributed cell splits/merges over the wire** → S7
- **Multi-process demo and CI recipes** → S8

---

## Risk notes

- **Proto has `dest_cell_id` at the top level.** Task 2 defines `MeshFrame.dest_cell_id` outside the oneof so receivers can route before dispatching on the variant. Get it right on first definition — it's a wire-format decision, painful to retrofit.
- **Concurrent Send on a bidi stream.** grpc-go documents that `ClientStream.Send` is not safe for concurrent use. We don't use a mutex — we funnel all sends through a per-peer channel + dedicated sender goroutine (Task 4 Step 1). This is the pattern grpc-go docs explicitly recommend and it also decouples cell-loop goroutines from stream backpressure. `Recv` runs concurrently on its own goroutine — grpc-go explicitly permits `Send` and `Recv` on the same stream from different goroutines.
- **Keepalive asymmetry causes `ENHANCE_YOUR_CALM`.** Both sides must set matching keepalive settings. Client `Time=60s` is greater than server `MinTime=30s`, so the server accepts the pings. If a future operator tunes client `Time` down below server `MinTime`, the server will send GOAWAY `too_many_pings`. Call this out in code comments near the keepalive config.
- **`GracefulStop` hangs with open bidi streams.** Known grpc-go bug ([#2888](https://github.com/grpc/grpc-go/issues/2888), [#5930](https://github.com/grpc/grpc-go/issues/5930)). Task 4 Step 5 works around it by cancelling outbound streams first, then racing `GracefulStop` against a 5s timer with `Stop()` fallback. Tested in Task 4.5.
- **Stream recv goroutine leaks.** Per [grpc-go#2015](https://github.com/grpc/grpc-go/issues/2015), recv goroutines must drain to EOF. Our receiver goroutine exits on `stream.Recv` error (EOF or context cancellation) and calls `dropPeer` to remove the peer from the map. No locks are held during `Recv`.
- **Backpressure policy is per message type, not per peer.** Border frames are lossy (drop on full queue — 30-tick resync recovers). Handoff/action/chat/assignment are reliable (block with 250ms deadline). Getting this wrong either drops critical game state or stalls the source tick. The policy matrix in Task 6 is load-bearing — don't "simplify" it to a single send mode.
- **4MB default message size is a landmine for S7.** We're not hitting it in S3 (border frames are ~KB, handoff blobs ~10KB), but cell-migration payloads in S7 will blow through it. Set explicit 16MB caps now (Task 4 Step 2) so the limit is visible and grep-able.
- **Test flakiness from goroutine startup.** The integration test (Task 8) needs deterministic ordering: peer streams must be open before the first boundary crossing. Use `HostNetwork.WaitPeersReady` (defined in Task 4 Step 7).
- **`-race` is mandatory for Task 4.5.** The sender/receiver/shutdown interactions are the highest-risk place for data races. CI should run `go test -race` against at least `./pkg/universe/...`.
- **IDE stale diagnostics.** VS Code's Go extension has been consistently behind during S1/S2. Trust `go vet` and `go test` output; ignore the IDE until the final verification step.
