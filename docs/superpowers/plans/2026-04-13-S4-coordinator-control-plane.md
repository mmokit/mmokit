# S4: Coordinator as Control-Plane Service — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Split the Coordinator into a standalone control-plane service. A process started with `--mode=coordinator` runs the `meshpb.MeshControl` gRPC server, tracks registered remote hosts, computes cell→host assignments via rendezvous hashing, and pushes `CellAssign` / `CellRelease` messages to the appropriate node. A process started with `--mode=node` dials the coordinator, sends `RegisterHost`, receives a stream of `CoordMessage` commands, and manages its local cells on demand. The existing `--mode=all-in-one` (default) path keeps the S3 semantics intact: one process owns everything, optionally with `TestHosts` for in-process multi-host loopback.

**S4 scope decision (confirmed with user):** Option A — **strict control-plane only**. Multi-process mode (`coordinator` + `node` processes) is validated interactively via console commands and log output, NOT through playable gameplay. Client proxying, `VirtualConnManager`, and `UpstreamSwitch` handling are explicitly deferred to S6 (Gateway). The `--two-hosts` in-process mode from S3 remains the only interactive playable multi-host path until S6 lands.

**Architecture:** The monolithic `Coordinator` keeps its existing responsibilities (topology, cell creation helpers, console, metrics, connection proxy). Two new subsystems are grafted onto it, both gated on `Config.Mode`:

- **coordinator mode:** A new `meshControlServer` accepts bidi streams from remote nodes. A new `HostRegistry` tracks `{hostID, grpcAddr, lastHeartbeat, ownedCells, state}`. An `assignmentEngine` runs a settle window after the first registration, computes rendezvous hashes, and dispatches `CellAssign` / `CellRelease` via each host's stream. Cell CREATION still happens on the node process that receives the assignment — the coordinator never holds a local `*Cell`.

- **node mode:** A new `meshControlClient` dials the coordinator, opens a `Control` bidi stream, sends `RegisterHost`, and runs a receive loop that interprets `CoordMessage` variants. On `CellAssign`, the node calls the existing `createNode` helper to spin up a cell locally, wires it into the local `HostNetwork` for MeshData peer traffic, and reports back via `CellReady`. On `CellRelease`, the node stops the cell and reports `CellStopped`. Every second the node sends a `Heartbeat`. The node does NOT accept WebSocket connections in S4.

Fencing is handled by a monotonically-increasing `coordEpoch uint64` that the coordinator bumps on every restart. Every `CoordMessage` sent to a node carries the current epoch; nodes track the highest-seen value and reject anything lower. This prevents a restarted coordinator's stale state from interfering with a fresh assignment run.

**Research-driven design choices (synthesis from the S4 research pass):**
- **Rendezvous hashing, not consistent hashing.** Simpler code, better load distribution at our scale (<100 hosts). See `pkg/universe/rendezvous.go`.
- **Heartbeat = 1s interval, 3s dead threshold.** Industry standard 3-5× ratio; fast detection without false positives under normal load. gRPC keepalive (60s) handles the TCP-level dead-connection case.
- **Fencing tokens / coordinator epoch**, not quorum/Raft. Single-coordinator SPOF is explicit in the spec; epoch prevents stale state from a restarted coordinator without adding consensus complexity.
- **Settle window = 5s** after first host registration. Matches spec; prevents assignment churn if hosts come up in rapid succession.
- **NetID range allocation over the wire.** `NetIDRangeGrant` sent alongside `CellAssign`. Each grant carries ~10M IDs (`netIDRangeSize`) so nodes never need another grant during normal operation.
- **Graceful leave via per-cell `CellStopped` + stream close**, no dedicated `GracefulLeave` message. Keeps the proto schema stable and makes graceful vs. crash distinguishable by whether the stream saw every owned cell reported stopped before EOF.

**Tech Stack:** Go, `google.golang.org/grpc` (already in go.mod), `meshpb.MeshControl` service (already defined in S3), existing `pkg/universe/` types.

**Spec:** [docs/superpowers/specs/2026-04-12-distributed-mesh-design.md](../specs/2026-04-12-distributed-mesh-design.md) — §10.S4, §5 (MeshControl proto), §4 (cell lifecycle)

---

## File Structure

### Files to create

| Path | Responsibility |
|---|---|
| `pkg/universe/mesh_control_server.go` | `meshControlServer` — implements `meshpb.MeshControl`, accepts bidi streams, dispatches to handlers |
| `pkg/universe/mesh_control_client.go` | `meshControlClient` — node-side dial + register + receive loop |
| `pkg/universe/host_registry.go` | `HostRegistry` + `RemoteHost` — coordinator's view of registered nodes, liveness, owned cells |
| `pkg/universe/rendezvous.go` | `AssignCellToHost(cellID, hosts)` — rendezvous hashing helper |
| `pkg/universe/rendezvous_test.go` | Unit tests for rendezvous hashing (stability, distribution, weight handling) |
| `pkg/universe/coord_assignment.go` | `assignmentEngine` — settle window, rebalance loop, `dispatchCellAssign` / `dispatchCellRelease` |
| `pkg/universe/coord_control_plane_test.go` | In-process coordinator + node integration test over MeshControl loopback |

### Files to modify

| Path | What changes |
|---|---|
| `pkg/universe/coordinator.go` | Add `Mode`, `CoordinatorAddr`, `ControlListen` to `Config`. Add `coordEpoch`, `hostRegistry`, `controlServer`, `controlClient` to `Coordinator`. Branch `Build()` on mode. Extend `Shutdown()` to tear down the control plane. |
| `pkg/universe/console.go` (or wherever admin commands live) | Add `host list`, `host kill <id>`, `cell list` commands for S4 interactive validation |
| `pkg/mmokit/mmokit.go` | No direct edits needed (Config is a type alias) — verify `Mode`, `CoordinatorAddr`, `ControlListen` are reachable via `mmokit.Config` |
| `examples/4node-basic/main.go` | Add `--mode`, `--coordinator-addr`, `--control-listen` flags; pass them through `mmokit.Config` |
| `CLAUDE.md` | Brief note about `--mode` and the coordinator/node split; point at S4 plan for details |

### Files to leave alone

- `pkg/universe/host_network.go` — the MeshData data plane is unchanged
- `pkg/universe/grpc_bridge.go` — multi-host routing still works via cellToHostMap
- `pkg/universe/handoff_driver.go` — handoff protocol is orthogonal to control plane
- `proto/meshpb/mesh.proto` — all MeshControl messages were already defined in S3 Task 2

---

## Task Breakdown

### Task 1: Config + coordinator epoch + mode branching

**Files:**
- Modify: `pkg/universe/coordinator.go`

- [ ] **Step 1: Config fields**

Add to `Config`:

```go
// Mode selects the operating role for this process.
//   - "" or "all-in-one" (default): single-process, owns cells directly.
//     TestHosts is optional and provides in-process multi-host loopback.
//   - "coordinator": runs the MeshControl server and admin console.
//     Holds no local cells; waits for remote nodes to register.
//   - "node": dials CoordinatorAddr, registers via MeshControl, creates
//     cells on demand as CellAssign messages arrive. No local console,
//     no WebSocket listener for clients.
Mode string

// ControlListen is the listen address for the MeshControl gRPC server.
// Only used when Mode == "coordinator". Default ":9100".
ControlListen string

// CoordinatorAddr is the MeshControl server address to dial.
// Only used when Mode == "node". No default — required in node mode.
CoordinatorAddr string
```

- [ ] **Step 2: Coordinator epoch field**

Add to the `Coordinator` struct:

```go
// coordEpoch is a fencing token that monotonically increases on every
// coordinator restart. Every CoordMessage sent to a registered node
// carries the current epoch; nodes track the highest-seen value and
// reject anything lower. Prevents stale state from a restarted
// coordinator from clobbering a fresh assignment run. Initialized
// from the current unix nanoseconds at NewCoordinator time so it
// monotonically advances across restarts without persistence.
coordEpoch uint64
```

Initialize in `NewCoordinator`:

```go
coordEpoch: uint64(time.Now().UnixNano()),
```

- [ ] **Step 3: Build() mode branching**

Resolve `Mode` defaults at the top of `Build()`:

```go
mode := cfg.Mode
if mode == "" {
    mode = "all-in-one"
}
if mode != "all-in-one" && mode != "coordinator" && mode != "node" {
    panic(fmt.Errorf("coordinator: unknown Mode %q", mode))
}
```

Skip local cell creation in `coordinator` and `node` mode. Cell creation in those modes is driven by control-plane events, not static topology:

```go
if mode == "all-in-one" {
    // existing host roster + cell creation + bridge wiring
} else {
    // Phases B-C wire this in later tasks. For now, no-op.
}
```

This leaves `c.Cells`, `c.Hosts`, etc. empty in coordinator and node mode. Later tasks populate them as registration and assignment messages arrive.

- [ ] **Step 4: Verify**

```bash
go vet ./... && go test -count=1 ./pkg/universe/...
```

All existing tests must pass. `--mode=coordinator` and `--mode=node` are not yet functionally useful; default behavior is preserved.

- [ ] **Step 5: Commit**

```
feat(universe): Config.Mode + coordEpoch + Build() mode branching

Three new Config fields (Mode, ControlListen, CoordinatorAddr) plus
a new coordEpoch fencing token seeded from time.Now().UnixNano() at
NewCoordinator time. Build() now panics on unknown mode strings and
skips local cell creation for coordinator/node modes — the control
plane will populate cells dynamically in later tasks.

all-in-one (default) mode is unchanged; existing tests pass without
modification. TestHosts multi-host loopback still works under
all-in-one.
```

---

### Task 2: MeshControl server skeleton

**Files:**
- Create: `pkg/universe/mesh_control_server.go`
- Modify: `pkg/universe/coordinator.go` (start the server when `Mode == "coordinator"`)

- [ ] **Step 1: Define `meshControlServer`**

```go
// meshControlServer implements meshpb.MeshControl. It accepts bidi
// streams from remote nodes, dispatches inbound HostMessage variants
// to the HostRegistry (Task 3), and drives assignment via the
// assignmentEngine (Task 5). One instance per coordinator process.
type meshControlServer struct {
    meshpb.UnimplementedMeshControlServer // forward-compat
    coord  *Coordinator
    log    *logger.Logger

    mu       sync.RWMutex
    streams  map[string]meshpb.MeshControl_ControlServer // hostID -> stream
    streamMu map[string]*sync.Mutex                      // per-stream send mutex
}
```

`Send` on grpc-go ServerStream is not safe for concurrent use either (same constraint as ClientStream). A dedicated sender goroutine per stream is overkill for control traffic (low rate); a per-stream mutex is enough and simpler. Matching the HostNetwork pattern would over-engineer a path that sends <100 msgs/sec per host.

- [ ] **Step 2: Implement `Control(stream) error`**

For this task the method is a minimal skeleton that just accepts the stream, reads the first `HostMessage`, confirms it's a `RegisterHost`, stores the stream in `s.streams`, and then loops reading further messages — logging each type but not acting on them yet. Registration logic lands in Task 5.

```go
func (s *meshControlServer) Control(stream meshpb.MeshControl_ControlServer) error {
    first, err := stream.Recv()
    if err != nil {
        return err
    }
    reg := first.GetRegister()
    if reg == nil {
        return fmt.Errorf("mesh control: first message must be RegisterHost, got %T", first.Msg)
    }
    hostID := reg.HostId
    s.mu.Lock()
    s.streams[hostID] = stream
    s.streamMu[hostID] = &sync.Mutex{}
    s.mu.Unlock()
    s.log.Log(CatMeshCell, "coordinator: host %s registered from %s", hostID, reg.GrpcAddr)

    // Drain subsequent messages (logic added in Task 5, 7, 8).
    for {
        msg, err := stream.Recv()
        if err != nil {
            if errors.Is(err, io.EOF) {
                s.log.Log(CatMeshCell, "coordinator: host %s stream closed", hostID)
                return nil
            }
            s.log.Log(CatMeshCell, "coordinator: host %s recv error: %v", hostID, err)
            return err
        }
        s.log.Log(CatMeshMsg, "coordinator: host %s sent %T", hostID, msg.Msg)
    }
}
```

- [ ] **Step 3: Implement `sendCoordMessage(hostID, msg)` helper**

Picks up the stream from `s.streams`, locks the per-stream mutex, calls `stream.Send`. Returns an error on failure; callers decide whether to drop the host.

```go
func (s *meshControlServer) sendCoordMessage(hostID string, msg *meshpb.CoordMessage) error {
    s.mu.RLock()
    stream := s.streams[hostID]
    smu := s.streamMu[hostID]
    s.mu.RUnlock()
    if stream == nil || smu == nil {
        return fmt.Errorf("no control stream for host %q", hostID)
    }
    smu.Lock()
    defer smu.Unlock()
    return stream.Send(msg)
}
```

- [ ] **Step 4: Wire into `Coordinator.Build()`**

When `mode == "coordinator"`, bind a listener on `cfg.ControlListen`, construct a `grpc.Server` with the same keepalive + 16MB caps as `HostNetwork`, register `meshControlServer`, and start serving in a goroutine. Store the server on the `Coordinator` for Shutdown:

```go
// Default
if cfg.ControlListen == "" {
    cfg.ControlListen = ":9100"
}

listener, err := net.Listen("tcp", cfg.ControlListen)
if err != nil {
    panic(fmt.Errorf("coordinator: MeshControl listen: %w", err))
}
srv := grpc.NewServer(/* same opts as HostNetwork */)
ctrl := &meshControlServer{
    coord:    c,
    log:      c.Log,
    streams:  make(map[string]meshpb.MeshControl_ControlServer),
    streamMu: make(map[string]*sync.Mutex),
}
meshpb.RegisterMeshControlServer(srv, ctrl)
go func() { _ = srv.Serve(listener) }()

c.controlServer = ctrl
c.controlGrpcServer = srv
c.controlListener = listener
c.Log.Log(CatMeshCell, "coordinator: MeshControl listening on %s", listener.Addr())
```

- [ ] **Step 5: Shutdown integration**

`Coordinator.Shutdown()` calls `c.controlGrpcServer.GracefulStop()` (with the same 5s race + hard-stop fallback pattern as `HostNetwork.Shutdown`) when non-nil.

- [ ] **Step 6: Verify**

```bash
go vet ./... && go test -count=1 ./pkg/universe/...
```

- [ ] **Step 7: Commit**

```
feat(universe): meshControlServer skeleton

New meshControlServer implements meshpb.MeshControl's Control bidi
stream RPC. Accepts connections, expects RegisterHost as the first
message, stores the stream + per-stream send mutex, then logs
subsequent messages. Registered into Coordinator.Build() when
Mode == "coordinator"; the grpc.Server is bound to Config.ControlListen
with the same keepalive + 16MB caps as the MeshData servers in S3.
Shutdown races GracefulStop against a 5s deadline with Stop() fallback.

No message handlers are wired yet — this task is pure scaffolding.
Tasks 5/7/8 land the registration, cell lifecycle, and heartbeat
handlers on top of this skeleton.
```

---

### Task 3: HostRegistry — tracking remote hosts

**Files:**
- Create: `pkg/universe/host_registry.go`

`HostRegistry` is the coordinator's authoritative state for remote nodes. Not to be confused with `Coordinator.Hosts` which is the in-process local-host map from S3. In coordinator mode, `Coordinator.Hosts` stays empty and `HostRegistry` is the source of truth.

- [ ] **Step 1: Define types**

```go
// RemoteHostState tracks the lifecycle of a registered remote node.
type RemoteHostState int

const (
    RemoteHostUnknown RemoteHostState = iota
    RemoteHostRegistered
    RemoteHostLive
    RemoteHostDead
    RemoteHostLeaving
)

// RemoteHost holds coordinator-side state for one registered node.
type RemoteHost struct {
    ID            string
    GrpcAddr      string // MeshData peer address (for PeerList distribution)
    RegisteredAt  time.Time
    LastHeartbeat time.Time
    State         RemoteHostState
    OwnedCells    map[string]bool // cell string IDs currently assigned to this host
}

// HostRegistry is the coordinator's view of all registered remote nodes.
// Guarded by its own RWMutex so registration and liveness updates don't
// contend with the coordinator's main mu.
type HostRegistry struct {
    mu    sync.RWMutex
    hosts map[string]*RemoteHost
    log   *logger.Logger
}
```

- [ ] **Step 2: Methods**

```go
func NewHostRegistry(log *logger.Logger) *HostRegistry
func (r *HostRegistry) Register(hostID, grpcAddr string) *RemoteHost
func (r *HostRegistry) Touch(hostID string)                    // updates LastHeartbeat
func (r *HostRegistry) MarkDead(hostID string)                 // state transition for reassignment
func (r *HostRegistry) Remove(hostID string)                   // removes after reassignment completes
func (r *HostRegistry) LiveHosts() []*RemoteHost                // snapshot for rendezvous input
func (r *HostRegistry) Get(hostID string) *RemoteHost           // nil if unknown
func (r *HostRegistry) AssignCell(hostID, cellID string) error  // adds to OwnedCells
func (r *HostRegistry) ReleaseCell(hostID, cellID string)       // removes from OwnedCells
func (r *HostRegistry) HostForCell(cellID string) string        // reverse lookup
```

Each method takes/releases the registry mutex explicitly. `LiveHosts` returns a fresh slice of `*RemoteHost` copies (not pointers into the map) to avoid races with concurrent Touch / MarkDead calls.

- [ ] **Step 3: Tests**

Minimal unit test file `host_registry_test.go` covering:
- `Register` creates a new entry with `State == RemoteHostRegistered`
- `Touch` advances `LastHeartbeat` and transitions to `RemoteHostLive` on first touch
- `MarkDead` transitions state without deleting the entry (reassignment needs the OwnedCells set)
- `Remove` is idempotent
- `AssignCell` / `ReleaseCell` keep `OwnedCells` consistent
- `HostForCell` returns `""` for unknown cells

- [ ] **Step 4: Verify**

```bash
go vet ./... && go test -count=1 ./pkg/universe/ -run HostRegistry
```

- [ ] **Step 5: Commit**

```
feat(universe): HostRegistry tracks registered remote nodes

HostRegistry is the coordinator's authoritative view of nodes that
have registered via MeshControl. Distinct from Coordinator.Hosts
(which stays empty in coordinator mode — S3's in-process host roster).
Stores per-host grpc address, last heartbeat, lifecycle state, and
the set of owned cell IDs. Guarded by its own RWMutex so registration
and liveness updates don't contend with Coordinator.mu. Snapshot
helper LiveHosts() returns copies for safe iteration by the
assignment engine.
```

---

### Task 4: Rendezvous hashing helper

**Files:**
- Create: `pkg/universe/rendezvous.go`
- Create: `pkg/universe/rendezvous_test.go`

- [ ] **Step 1: Implement `AssignCellToHost`**

```go
// AssignCellToHost picks the highest-scoring host for a given cell
// via rendezvous (HRW) hashing. Each host's score is fnv64(cellID||hostID);
// the host with the highest score wins. Stable under restart: given
// the same (cellID, hostIDs) input the same host always wins.
// Returns "" if hosts is empty.
func AssignCellToHost(cellID string, hosts []string) string {
    if len(hosts) == 0 {
        return ""
    }
    var bestHost string
    var bestScore uint64
    for _, h := range hosts {
        hash := fnv.New64a()
        hash.Write([]byte(cellID))
        hash.Write([]byte{0}) // separator
        hash.Write([]byte(h))
        score := hash.Sum64()
        if score > bestScore || bestHost == "" {
            bestScore = score
            bestHost = h
        }
    }
    return bestHost
}

// AssignCellsAcrossHosts runs AssignCellToHost for every cell ID and
// returns a (cellID -> hostID) map. Used by the assignment engine
// after the settle window closes or when the host roster changes.
func AssignCellsAcrossHosts(cellIDs, hostIDs []string) map[string]string {
    out := make(map[string]string, len(cellIDs))
    for _, cid := range cellIDs {
        out[cid] = AssignCellToHost(cid, hostIDs)
    }
    return out
}
```

- [ ] **Step 2: Tests**

`rendezvous_test.go` covers:

1. **Stability:** `AssignCellToHost("cell_0_0", {"h1","h2","h3"})` returns the same host regardless of input order.
2. **Distribution:** Run 1000 synthetic cell IDs across 4 hosts; each host's assignment count should be within ±15% of 250.
3. **Minimal rebalance:** Assign 100 cells across 4 hosts; remove one host; assert the 3 surviving hosts keep approximately 3/4 of their prior assignments (cells that had the dead host as winner migrate; others stay).
4. **Deterministic across restarts:** Call `AssignCellsAcrossHosts` twice with the same inputs; assert byte-identical maps.
5. **Empty host list:** Returns empty string (no panic).

- [ ] **Step 3: Verify**

```bash
go vet ./... && go test -count=1 ./pkg/universe/ -run Rendezvous
```

- [ ] **Step 4: Commit**

```
feat(universe): rendezvous hashing helper for cell assignment

AssignCellToHost returns the highest-scoring host for a given cell
ID via rendezvous (HRW) hashing. AssignCellsAcrossHosts applies it
to a full cell set. Stable under restart, minimal rebalancing when
hosts join/leave, and simpler than consistent hashing with virtual
nodes for our target scale (<100 hosts).

Tests verify stability, distribution (±15% of even for 1000 cells
across 4 hosts), minimal rebalance on host removal, and deterministic
output across invocations.
```

---

### Task 5: Registration handshake + initial assignment

**Files:**
- Create: `pkg/universe/coord_assignment.go`
- Modify: `pkg/universe/mesh_control_server.go` — replace the Task 2 skeleton's "log-and-ignore" loop with real handlers

`assignmentEngine` owns the settle-window timer and rebalance logic.

- [ ] **Step 1: Settle window state machine**

```go
type assignmentEngine struct {
    coord    *Coordinator
    registry *HostRegistry
    ctrl     *meshControlServer
    log      *logger.Logger

    mu              sync.Mutex
    firstRegistered bool
    settleDeadline  time.Time
    settled         bool
}

const settleWindow = 5 * time.Second

// onHostRegistered is called by meshControlServer.Control after it
// inserts a newly-registered host into the registry. Starts or
// resets the settle timer on the first registration; subsequent
// registrations extend the deadline by settleWindow (the spec
// describes a fixed 5s window, but extending keeps rapid bursts
// on a single bootstrap pass).
func (e *assignmentEngine) onHostRegistered(host *RemoteHost) { ... }

// settle fires when the settleDeadline elapses. Runs the first
// rendezvous pass over all configured cells (c.Topology.Cells()
// or similar) and dispatches CellAssign messages.
func (e *assignmentEngine) settle() { ... }
```

Settle is fired by a background goroutine per coordinator. Use a ticker or a timer; either works. Keep it simple: a goroutine that wakes every 200ms, checks if settleDeadline has passed, and if so runs the first assignment.

- [ ] **Step 2: `dispatchCellAssign(host, cellID)`**

Builds a `CoordMessage` with:
- `CellAssign { cell_id: cellID }`
- A companion `NetIDRangeGrant { host_id: host.ID, start: <nextRange>, count: netIDRangeSize }` sent BEFORE the CellAssign so the node has ID space ready when it spawns the cell

Send both via `ctrl.sendCoordMessage(host.ID, ...)`. On error, log and give up on that host for this pass (the next rebalance will retry).

Track the assignment in `registry.AssignCell(host.ID, cellID)` so reassignment on host death has the right owned-cell set.

- [ ] **Step 3: Wire into `Control` loop**

Replace the Task 2 skeleton's log-and-ignore case for `RegisterHost`:

```go
case reg := first.GetRegister(); reg != nil:
    host := s.coord.hostRegistry.Register(reg.HostId, reg.GrpcAddr)

    // Send RegisterAck with the current coordinator epoch.
    ack := &meshpb.CoordMessage{
        Msg: &meshpb.CoordMessage_RegisterAck{
            RegisterAck: &meshpb.RegisterAck{
                Ok:       true,
                HostIdx:  uint32(host.RegisteredAt.UnixNano() & 0xffffffff),
            },
        },
    }
    if err := s.sendCoordMessage(reg.HostId, ack); err != nil { ... }

    s.coord.assignmentEngine.onHostRegistered(host)
```

Note: `RegisterAck` doesn't currently carry an epoch field. **Add one:** edit `proto/meshpb/mesh.proto` to add `uint64 coord_epoch = 4;` to `RegisterAck`, regenerate. Every CoordMessage variant that matters (CellAssign, CellRelease, NetIDRangeGrant) similarly needs an epoch — either add an epoch field to each, or (cleaner) add `uint64 coord_epoch = 200;` at the top level of `CoordMessage` outside the oneof, same pattern as S3's `dest_cell_id = 100` on `MeshFrame`. **Go with the top-level field** — simpler.

- [ ] **Step 4: First-pass cell enumeration**

When `settle` fires, the engine needs a list of cells to assign. In S4 we keep the static topology from `cfg.CellsX * cfg.CellsY`:

```go
cells := make([]string, 0, int(cfg.CellsX)*int(cfg.CellsY))
for sy := uint32(0); sy < cfg.CellsY; sy++ {
    for sx := uint32(0); sx < cfg.CellsX; sx++ {
        cells = append(cells, MeshCellID(CellID{X: int32(sx), Y: int32(sy)}))
    }
}
```

Rendezvous-assign them across live hosts, dispatch each.

- [ ] **Step 5: Update MeshFrame codec if needed**

The MeshFrame codec (Task 3 of S3) doesn't touch CoordMessage — skip this step unless the proto change broke the build.

- [ ] **Step 6: Verify**

```bash
go vet ./... && just proto && go vet ./... && go test -count=1 ./pkg/universe/...
```

- [ ] **Step 7: Commit**

```
feat(universe): registration handshake + settle-window assignment

When a node registers via RegisterHost, coordinator inserts it into
HostRegistry, replies with RegisterAck carrying the current epoch,
and starts or extends a 5s settle window. When the window closes,
assignmentEngine enumerates the static cell grid, runs rendezvous
hashing across live hosts, and dispatches (NetIDRangeGrant, CellAssign)
pairs per cell via meshControlServer.sendCoordMessage.

Proto update: MeshFrame already has dest_cell_id at field 100;
CoordMessage now has coord_epoch at field 200, carried on every
command so nodes can fence stale coordinator state. RegisterAck
gains the same epoch for bootstrap.
```

---

### Task 6: Node-side MeshControl client

**Files:**
- Create: `pkg/universe/mesh_control_client.go`
- Modify: `pkg/universe/coordinator.go` — start the client when `Mode == "node"`

`meshControlClient` is the node's long-lived connection to the coordinator. It dials, opens the bidi stream, sends `RegisterHost` as the first message, and runs a receive loop that dispatches `CoordMessage` variants to handlers (Task 7 fills in cell lifecycle; Task 8 adds heartbeat).

- [ ] **Step 1: Define `meshControlClient`**

```go
type meshControlClient struct {
    coord    *Coordinator
    log      *logger.Logger
    hostID   string
    coordAddr string

    conn   *grpc.ClientConn
    stream meshpb.MeshControl_ControlClient
    cancel context.CancelFunc

    mu          sync.Mutex
    highestEpoch uint64
}
```

Connection setup mirrors `HostNetwork.ConnectPeer`: same dial opts, same keepalive, `insecure.NewCredentials` with a TODO(S4+) comment for mTLS.

- [ ] **Step 2: Registration flow**

```go
func (c *meshControlClient) Start(ctx context.Context) error {
    conn, err := grpc.NewClient(c.coordAddr, /* same opts */)
    if err != nil { return err }
    c.conn = conn

    client := meshpb.NewMeshControlClient(conn)
    streamCtx, cancel := context.WithCancel(ctx)
    c.cancel = cancel
    stream, err := client.Control(streamCtx)
    if err != nil { cancel(); _ = conn.Close(); return err }
    c.stream = stream

    // First message MUST be RegisterHost — the server rejects anything else.
    reg := &meshpb.HostMessage{
        Msg: &meshpb.HostMessage_Register{
            Register: &meshpb.RegisterHost{
                HostId:   c.hostID,
                GrpcAddr: c.coord.host().Network.Addr(),
            },
        },
    }
    if err := stream.Send(reg); err != nil { /* cleanup */ return err }

    // Receive loop in a goroutine.
    go c.runRecvLoop()
    return nil
}
```

- [ ] **Step 3: Receive loop**

```go
func (c *meshControlClient) runRecvLoop() {
    for {
        msg, err := c.stream.Recv()
        if err != nil {
            if errors.Is(err, io.EOF) { return }
            c.log.Log(CatMeshCell, "node: control recv error: %v", err)
            return
        }
        // Epoch check — reject stale commands.
        if msg.CoordEpoch > 0 {
            c.mu.Lock()
            if msg.CoordEpoch < c.highestEpoch {
                c.mu.Unlock()
                c.log.Log(CatMeshCell, "node: dropping stale CoordMessage epoch=%d (highest=%d)", msg.CoordEpoch, c.highestEpoch)
                continue
            }
            if msg.CoordEpoch > c.highestEpoch {
                c.highestEpoch = msg.CoordEpoch
            }
            c.mu.Unlock()
        }
        c.dispatch(msg)
    }
}

func (c *meshControlClient) dispatch(msg *meshpb.CoordMessage) {
    switch v := msg.Msg.(type) {
    case *meshpb.CoordMessage_RegisterAck:
        c.log.Log(CatMeshCell, "node: registered (epoch=%d)", msg.CoordEpoch)
    case *meshpb.CoordMessage_CellAssign:
        // Task 7 fills this in.
    case *meshpb.CoordMessage_CellRelease:
        // Task 7 fills this in.
    case *meshpb.CoordMessage_NetidRange:
        // Task 7 fills this in.
    default:
        c.log.Log(CatMeshMsg, "node: received %T (handler not wired)", v)
    }
}
```

- [ ] **Step 4: Send helper (for Heartbeat, CellReady, CellStopped in later tasks)**

```go
func (c *meshControlClient) send(msg *meshpb.HostMessage) error {
    c.mu.Lock()
    defer c.mu.Unlock()
    if c.stream == nil { return fmt.Errorf("control stream not ready") }
    return c.stream.Send(msg)
}
```

- [ ] **Step 5: Wire into `Coordinator.Build()`**

When `mode == "node"`, construct a local Host for this node, boot its HostNetwork, construct the meshControlClient, call `Start`:

```go
if mode == "node" {
    if cfg.CoordinatorAddr == "" {
        panic("node mode requires --coordinator-addr")
    }
    hostID := cfg.HostID
    if hostID == "" { hostID = generatedHostID() /* uuid-ish */ }

    host := NewHost(hostID)
    host.Log = c.Log
    c.Hosts[hostID] = host

    hn, err := NewHostNetwork(host, ":0", c.Log)
    if err != nil { panic(err) }
    host.Network = hn

    c.controlClient = &meshControlClient{
        coord:     c,
        log:       c.Log,
        hostID:    hostID,
        coordAddr: cfg.CoordinatorAddr,
    }
    if err := c.controlClient.Start(context.Background()); err != nil {
        panic(fmt.Errorf("node: meshControlClient start: %w", err))
    }
}
```

Add `HostID` to `Config` (optional, with UUID fallback) so tests can set deterministic host IDs.

- [ ] **Step 6: Shutdown**

`Coordinator.Shutdown` calls `controlClient.cancel()` and `controlClient.conn.Close()` when non-nil. Order: cells first (none in node mode until Task 7), host networks second, control client third.

- [ ] **Step 7: Verify**

```bash
go vet ./... && go test -count=1 ./pkg/universe/...
```

- [ ] **Step 8: Commit**

```
feat(universe): meshControlClient — node-side registration

Node mode now dials the coordinator at CoordinatorAddr, opens a
MeshControl.Control bidi stream, sends RegisterHost as the first
message (hostID + its local MeshData grpc addr), and runs a recv
loop that dispatches CoordMessage variants. Receive loop enforces
the monotonic coord_epoch: stale commands are dropped with a warning.

CellAssign / CellRelease / NetIDRangeGrant dispatch is stubbed;
Task 7 wires cell lifecycle. Heartbeat is stubbed; Task 8 adds it.

Adds Config.HostID for deterministic test host IDs (UUID fallback
otherwise).
```

---

### Task 7: Node-side cell lifecycle + CellReady / CellStopped

**Files:**
- Modify: `pkg/universe/mesh_control_client.go` — wire the real CellAssign / CellRelease / NetIDRangeGrant handlers
- Modify: `pkg/universe/coordinator.go` — extend the node-mode Build() to hold a reference to the createNode helper so the client can spawn cells

- [ ] **Step 1: Handle NetIDRangeGrant**

Node stores the grant so the next `createNode` call for the granted host consumes from the right base:

```go
case *meshpb.CoordMessage_NetidRange:
    grant := v.NetidRange
    c.coord.netIDAlloc.SetBase(grant.Start)
    c.log.Log(CatMeshCell, "node: NetIDRange grant [%d..%d]", grant.Start, grant.Start+grant.Count)
```

Check `NetIDAllocator`'s current API. If it doesn't have `SetBase`, add one. This replaces the in-process monotonic allocator for node mode.

- [ ] **Step 2: Handle CellAssign**

```go
case *meshpb.CoordMessage_CellAssign:
    assign := v.CellAssign
    cellID := parseCellID(assign.CellId) // "cell_X_Y" -> CellID struct
    c.coord.assignCellOnNode(cellID)
```

`assignCellOnNode` is a new method on Coordinator that wraps the existing `createNode` helper plus bridge wiring. In node mode it's the first path that ever creates a Cell. Call `node.Run(ctx)` to start the game loop, then send `CellReady` back to the coordinator.

```go
func (c *Coordinator) assignCellOnNode(cell CellID) {
    spatialCellSize := c.resolveSpatialCellSize()
    node, systems := c.createNode(cell, spatialCellSize)
    host := c.localHost() // the node's single Host
    host.AddCell(cell, node)

    // Wire bridge — for S4 the node has no peers initially so plain cellBridge
    // is fine. Task 7.5 (deferred) extends this to wrap in grpcBridge once the
    // node learns about remote peers via PeerList.
    //
    // For now: single-host mode equivalent. Cross-host border frames don't
    // flow yet — that's a known S4 limitation, resolved in S4.5 or S5.

    node.World.Init()
    initSystems(systems)
    go node.Run(context.Background())

    c.controlClient.send(&meshpb.HostMessage{
        Msg: &meshpb.HostMessage_CellReady{
            CellReady: &meshpb.CellReady{
                HostId: c.controlClient.hostID,
                CellId: MeshCellID(cell),
            },
        },
    })
}
```

- [ ] **Step 3: Handle CellRelease**

Stop the cell's game loop, remove from the host, send `CellStopped`:

```go
case *meshpb.CoordMessage_CellRelease:
    rel := v.CellRelease
    c.coord.releaseCellOnNode(rel.CellId)
```

`releaseCellOnNode` looks up the cell by string ID (the node's Host.CellByID helper), stops its loop, removes it from the map, sends CellStopped.

- [ ] **Step 4: Coordinator-side CellReady / CellStopped handlers**

In `meshControlServer.Control`, the receive loop gains handlers for `CellReady` (update `RemoteHost.OwnedCells`, transition to `RemoteHostLive` if first `CellReady`) and `CellStopped` (remove from `OwnedCells`, potentially finish a graceful leave).

- [ ] **Step 5: Verify**

```bash
go vet ./... && go test -count=1 ./pkg/universe/...
```

- [ ] **Step 6: Commit**

```
feat(universe): node-side CellAssign/CellRelease handling

CellAssign triggers createNode + local host wiring + game loop start;
node replies with CellReady. CellRelease stops the loop and replies
with CellStopped. NetIDRangeGrant seeds the node's NetIDAllocator so
entity IDs stay unique cluster-wide without per-spawn coordination.

Cross-host MeshData bridge wiring in node mode is deferred: this
task spawns plain cellBridge-wrapped cells (no grpcBridge) so
border frames and handoffs between remote-hosted cells don't route
correctly yet. S4.5 or S5 will add grpcBridge installation via
PeerList broadcasts.
```

---

### Task 8: Heartbeat + crash detection + reassignment

**Files:**
- Modify: `pkg/universe/mesh_control_client.go` — heartbeat loop
- Modify: `pkg/universe/coord_assignment.go` — liveness watcher + reassignment trigger
- Modify: `pkg/universe/mesh_control_server.go` — Heartbeat handler

- [ ] **Step 1: Node heartbeat loop**

```go
const heartbeatInterval = 1 * time.Second

func (c *meshControlClient) runHeartbeatLoop(ctx context.Context) {
    tick := time.NewTicker(heartbeatInterval)
    defer tick.Stop()
    for {
        select {
        case <-ctx.Done():
            return
        case <-tick.C:
            _ = c.send(&meshpb.HostMessage{
                Msg: &meshpb.HostMessage_Heartbeat{
                    Heartbeat: &meshpb.Heartbeat{HostId: c.hostID, Tick: c.currentTick()},
                },
            })
        }
    }
}
```

Started from `meshControlClient.Start` after successful registration.

- [ ] **Step 2: Server heartbeat handler**

```go
case v := hostMsg.GetHeartbeat(); v != nil:
    s.coord.hostRegistry.Touch(v.HostId)
```

`Touch` updates `LastHeartbeat` and transitions `State` from `Registered` to `Live` if it's the first heartbeat.

- [ ] **Step 3: Liveness watcher**

A goroutine on the coordinator that wakes every 500ms, iterates `hostRegistry.hosts`, and marks any host with `LastHeartbeat` older than `deadThreshold` (3s) as `RemoteHostDead`. On transition to dead, trigger reassignment:

```go
const deadThreshold = 3 * time.Second

func (e *assignmentEngine) runLivenessWatcher(ctx context.Context) {
    tick := time.NewTicker(500 * time.Millisecond)
    defer tick.Stop()
    for {
        select {
        case <-ctx.Done():
            return
        case <-tick.C:
            now := time.Now()
            for _, host := range e.registry.LiveHosts() {
                if host.State == RemoteHostLive && now.Sub(host.LastHeartbeat) > deadThreshold {
                    e.registry.MarkDead(host.ID)
                    e.log.Log(CatMeshCell, "coordinator: host %s DEAD (no heartbeat for %s)", host.ID, now.Sub(host.LastHeartbeat))
                    e.reassignOrphanedCells(host)
                }
            }
        }
    }
}
```

- [ ] **Step 4: Reassignment on death**

`reassignOrphanedCells` takes the dead host's `OwnedCells`, removes the dead host from the live set, runs rendezvous hashing over the survivors, and dispatches new `CellAssign` messages. If no live hosts remain, the cells are orphaned and logged; they'll be reassigned when a new host registers.

```go
func (e *assignmentEngine) reassignOrphanedCells(dead *RemoteHost) {
    live := e.registry.LiveHosts()
    liveIDs := make([]string, 0, len(live))
    for _, h := range live {
        if h.State == RemoteHostLive {
            liveIDs = append(liveIDs, h.ID)
        }
    }
    if len(liveIDs) == 0 {
        e.log.Log(CatMeshCell, "coordinator: no live hosts, cells orphaned (%d)", len(dead.OwnedCells))
        return
    }
    for cellID := range dead.OwnedCells {
        newHost := AssignCellToHost(cellID, liveIDs)
        e.log.Log(CatMeshCell, "coordinator: reassign cell %s -> %s", cellID, newHost)
        e.dispatchCellAssign(newHost, cellID)
    }
    // Don't call Remove here — the test/console may want to see the dead
    // entry until the next host churn. Remove on re-registration.
}
```

- [ ] **Step 5: Verify**

```bash
go vet ./... && go test -count=1 ./pkg/universe/...
```

- [ ] **Step 6: Commit**

```
feat(universe): heartbeat + crash detection + reassignment

Node sends Heartbeat every 1s via the MeshControl stream. Server
handler calls HostRegistry.Touch, advancing LastHeartbeat. A new
assignmentEngine.runLivenessWatcher goroutine wakes every 500ms
and marks any Live host whose heartbeat is older than 3s as Dead.
On dead transition, rendezvous hashing runs over the surviving
live hosts and CellAssign is dispatched for each orphaned cell.
If no live hosts remain the cells are logged as orphaned and
reassigned when the next host registers.

Orphaned cell entries stay in HostRegistry until the next churn
event for console observability; they're removed on re-registration.
```

---

### Task 9: Graceful shutdown flow

**Files:**
- Modify: `pkg/universe/mesh_control_client.go` — shutdown path sends CellStopped before closing
- Modify: `pkg/universe/mesh_control_server.go` — distinguish graceful leave from crash

- [ ] **Step 1: Node-side graceful shutdown**

`Coordinator.Shutdown` in node mode: for each cell in the local host, send `CellStopped`, then close the control stream:

```go
if c.controlClient != nil {
    host := c.localHost()
    for _, cell := range host.Cells {
        _ = c.controlClient.send(&meshpb.HostMessage{
            Msg: &meshpb.HostMessage_CellStopped{
                CellStopped: &meshpb.CellStopped{
                    HostId: c.controlClient.hostID,
                    CellId: cell.ID,
                },
            },
        })
    }
    c.controlClient.cancel()
    _ = c.controlClient.conn.Close()
}
```

- [ ] **Step 2: Server-side graceful-leave recognition**

When the `Control` recv loop sees stream EOF, check the registry entry's state. If all `OwnedCells` were previously reported as stopped (`OwnedCells` is empty), treat as graceful leave — log it, set state to `RemoteHostLeaving`, remove the entry. If there are still un-stopped cells, treat as crash — mark dead, trigger reassignment per Task 8.

- [ ] **Step 3: Verify**

```bash
go vet ./... && go test -count=1 ./pkg/universe/...
```

- [ ] **Step 4: Commit**

```
feat(universe): graceful node leave via CellStopped + stream close

Node Shutdown now sends a CellStopped per owned cell before closing
the MeshControl stream. Server recv-loop treats EOF differently
based on the OwnedCells state: empty means graceful leave (log +
remove entry, no reassignment), non-empty means crash (mark dead,
trigger reassignment per Task 8).
```

---

### Task 10: Admin console commands

**Files:**
- Modify: wherever the existing `cell list` / `node list` commands are declared (likely `pkg/universe/partition_console.go` or a sibling)

Add three new commands under the existing `host` command group (create the group if it doesn't exist):

- [ ] **Step 1: `host list`**

Dumps the HostRegistry state. One line per host with columns: ID, state, last heartbeat age, grpc addr, owned cell count.

```
host  state       hb-age  grpc-addr       cells
h-1   Live        0.8s    127.0.0.1:9101  [cell_0_0, cell_0_1]
h-2   Live        0.3s    127.0.0.1:9102  [cell_1_0, cell_1_1]
```

- [ ] **Step 2: `host kill <id>`**

Forcibly cancels the MeshControl stream for the given host ID, simulating a crash. Triggers the liveness watcher's reassignment path. Used for interactive demo: `host kill h-1` then watch `host list` show the surviving host pick up all four cells.

```go
func hostKillCmd(c *Coordinator, hostID string) error {
    s := c.controlServer.streamFor(hostID)
    if s == nil { return fmt.Errorf("no stream for %q", hostID) }
    // Force-close via context cancel. grpc stream observes it on next Recv.
    c.controlServer.cancelStream(hostID)
    return nil
}
```

- [ ] **Step 3: `cell list` (extend existing)**

If the existing `cell list` command already exists, extend it to show the owning host ID alongside each cell. If not, create it.

- [ ] **Step 4: Verify**

Manual smoke: start the coordinator + a node interactively, type `host list` / `cell list` / `host kill h-1`, watch reassignment happen.

- [ ] **Step 5: Commit**

```
feat(universe): host/cell admin console commands for S4

Three new commands:
- `host list`: HostRegistry state — id, state, heartbeat age,
  grpc addr, owned cells.
- `host kill <id>`: force-cancel a remote host's control stream,
  simulating a crash. Triggers the liveness watcher's reassignment.
- `cell list`: extended to show owning host id alongside each cell.

Used for interactive validation of registration / heartbeat /
crash detection / reassignment without needing playable gameplay
in multi-process mode.
```

---

### Task 11: Integration test + docs + verification

**Files:**
- Create: `pkg/universe/coord_control_plane_test.go`
- Modify: `CLAUDE.md` — brief note about `--mode` and the coordinator/node split
- Modify: `examples/4node-basic/main.go` — add `--mode`, `--coordinator-addr`, `--control-listen`, `--host-id` flags

- [ ] **Step 1: In-process integration test**

`TestS4CoordNodeRegistrationAndAssignment` — stand up a coordinator `Coordinator` with `Mode: "coordinator"` on `:0`, capture its actual listen address, then stand up a node `Coordinator` with `Mode: "node"` + `CoordinatorAddr: coord.controlListener.Addr().String()`. Wait for registration → settle → first CellAssign → CellReady. Assert:

- Coordinator's `HostRegistry` contains one host with state `Live`
- Node's `Host.Cells` contains every cell from the configured grid
- `coord.hostRegistry.Get(nodeID).OwnedCells` matches the node's local cells

Then simulate a crash by calling `coord.controlServer.cancelStream(nodeID)`. Wait ~4 seconds. Assert:

- Host state transitions to `Dead`
- With no other live hosts, cells are orphaned (log check, not state)

Start a second node. Assert reassignment: the second node's `Host.Cells` now contains all the cells.

- [ ] **Step 2: `TestS4GracefulShutdown`**

Start coordinator + node + wait for assignment. Call `node.Shutdown()`. Assert:
- Coordinator sees `CellStopped` for every cell first
- Then stream EOF
- Registry removes the entry (graceful, no reassignment log)

- [ ] **Step 3: CLAUDE.md update**

Brief paragraph under Architecture:

> **Multi-process mode (S4+):** `--mode=coordinator` runs only the MeshControl server + admin console; no local cells. `--mode=node --coordinator-addr=host:9100` runs a node process that registers with the coordinator and hosts cells assigned by rendezvous hashing. `--mode=all-in-one` (default) is unchanged: single-process with optional `--two-hosts` in-process loopback. Multi-process gameplay (client proxying through the coordinator to the authoritative node) is deferred to S6.

- [ ] **Step 4: 4node-basic flag wiring**

```go
mode := flag.String("mode", "all-in-one", "all-in-one | coordinator | node")
controlListen := flag.String("control-listen", ":9100", "MeshControl listen addr (coordinator mode)")
coordinatorAddr := flag.String("coordinator-addr", "", "MeshControl dial addr (node mode)")
hostID := flag.String("host-id", "", "stable host identifier for node mode (empty = auto)")
```

Thread into `mmokit.Config`.

- [ ] **Step 5: Full verify**

```bash
go vet ./... && go test -count=1 -race ./pkg/universe/ -run S4 && go test -count=1 ./... && just build
```

Build the examples too:

```bash
go build -o bin/4node-basic ./examples/4node-basic
go build -o bin/simple ./examples/simple
go build -o bin/server ./cmd/server
```

- [ ] **Step 6: Commit**

```
test(universe): S4 in-process coord+node integration tests

TestS4CoordNodeRegistrationAndAssignment spawns a coordinator
Coordinator and a node Coordinator in the same test process
connected via real gRPC loopback. Asserts registration, settle
window, rendezvous assignment, CellReady confirmation, then force-
closes the node stream and asserts crash detection + reassignment
to a second node.

TestS4GracefulShutdown verifies CellStopped per owned cell on
node Shutdown followed by clean stream EOF, without triggering
the reassignment path.

examples/4node-basic gains --mode, --control-listen, --coordinator-addr,
--host-id flags. Multi-process gameplay remains deferred to S6;
S4 validation goes through admin console commands + log output.

CLAUDE.md gets a brief paragraph pointing at the S4 plan.
```

---

## Verification Checklist

After completing all tasks, verify:

- [ ] `go vet ./...` clean
- [ ] `go test -count=1 ./pkg/universe/...` green
- [ ] `go test -count=1 -race ./pkg/universe/ -run S4` green (S4 integration tests under race detector)
- [ ] `just build` produces `bin/server`
- [ ] All existing S3 tests (`-run TwoHost`, `-run HostNetwork`, `-run MeshFrame`) still pass
- [ ] `Config.Mode` is `"all-in-one"` | `"coordinator"` | `"node"`; any other value panics at Build()
- [ ] `coordEpoch` is seeded from `time.Now().UnixNano()` and present on every CoordMessage
- [ ] Rendezvous hashing produces stable, evenly-distributed assignments (see rendezvous_test.go)
- [ ] Settle window waits 5s after first registration before first assignment
- [ ] Heartbeat interval 1s, dead threshold 3s, reassignment fires within ~4s of stream force-close
- [ ] Graceful shutdown distinguishes from crash via CellStopped accounting
- [ ] `host list` / `cell list` / `host kill <id>` admin commands work
- [ ] CLAUDE.md documents the `--mode` split

---

## Out of scope for S4 (explicit deferrals)

- **Client proxying through coordinator to node** (`VirtualConnManager`, `UpstreamSwitch`) → S6 Gateway
- **Playable gameplay in multi-process mode** → S6 (relies on client proxying)
- **Cross-host MeshData bridge wiring on nodes** (making grpcBridge route to remote peers in node mode via PeerList) → S4.5 or S5. In S4, nodes run plain cellBridge; border frames and handoffs between cells on different nodes don't route correctly yet. Validation relies on console commands, not interactive boundary crossings.
- **PeerList broadcasts when hosts join/leave** → S4.5
- **Persistence, crash recovery from Postgres state** → S5
- **Distributed cell splits/merges** → S7
- **Real subprocess integration test via os/exec** → optional S4.5, in-process loopback is sufficient for S4 success
- **mTLS on MeshControl** → S4.5 or later; S4 uses `insecure.NewCredentials` with a `TODO(mTLS)` comment

---

## Risk notes

- **The MeshControl epoch field needs a proto edit.** Task 5 Step 3 adds `uint64 coord_epoch = 200;` to `CoordMessage` outside the oneof. Do this at the start of Task 5; don't sneak it in later. Regenerate with `just proto` and verify the Go types have the new field.
- **In coordinator mode the admin console and the assignment engine run on different goroutines.** Console commands that inspect `HostRegistry` must take the registry's RLock; commands that mutate (e.g., `host kill`) must ensure they don't deadlock against an in-flight assignment. Keep the registry mutex narrow.
- **Node mode starts cells at arbitrary wall-clock times** — after the settle window closes on the coordinator side, the node receives CellAssign and calls `createNode` + `World.Init` + `initSystems` + `go node.Run(ctx)`. This is a very different code path from the existing all-in-one `Build()` which creates everything synchronously. The existing two-phase init (World.Init then system Init) must still happen in the right order per cell. `assignCellOnNode` captures this ordering explicitly — don't "optimize" by spawning the goroutine before Init.
- **Graceful shutdown race on node side.** If a CellRelease arrives while Coordinator.Shutdown is draining cells, the CellStopped counter can double-fire. Make `releaseCellOnNode` and the Shutdown loop coordinate via the host.Cells map (each cell is stopped at most once).
- **Test ordering:** `TestS4GracefulShutdown` must NOT run concurrently with `TestS4CoordNodeRegistrationAndAssignment` because they share ephemeral-port allocation and timing assumptions. Use `t.Run` subtests within a single test function or rely on `go test`'s default sequential execution within a package.
- **Coordinator epoch seed collision** is theoretically possible if two coordinators start at the same Unix nanosecond. Probability is negligible in practice; if it matters, add a random 16-bit suffix in S4.5.
- **`coord_epoch = 200` proto field number:** 200 is above the `oneof` variants (1-11) and leaves plenty of room. Don't pick something in the low single digits that could collide with future oneof variants.

---

## Dependency ordering

Tasks must execute in order — each builds on the previous:

```
1 (config/epoch) → 2 (server skeleton) → 3 (registry) → 4 (rendezvous)
                                                              ↓
                                              5 (registration + assign)
                                                              ↓
6 (node client) → 7 (cell lifecycle) → 8 (heartbeat) → 9 (graceful leave)
                                                              ↓
                                          10 (admin console)
                                                              ↓
                                          11 (integration test + docs)
```

Tasks 3 and 4 can theoretically run in parallel (pure data-structure work with no inter-dependencies), but for subagent-driven execution they should be serialized to keep the review surface focused.
