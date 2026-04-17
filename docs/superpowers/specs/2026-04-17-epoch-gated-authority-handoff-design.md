# Epoch-Gated Authority Handoff

## Problem

After cross-host cell migration, the source cell's game loop continues running on the remote host and replicating to the client. The destination cell also starts replicating. The client receives conflicting position updates from both hosts on alternating ticks, causing the player's ship to jitter wildly until reconnect.

Root cause: the gateway forwards ALL `ClientFrame` messages from ANY host to the matching client connection without validating which host is currently authoritative. There is no mechanism to reject stale frames from a host that has lost authority.

Secondary cause: after migrate commit, the coordinator calls `srcCell.Shutdown()` directly on the source cell object — which only works for in-process cells. Remote hosts never receive a shutdown signal, so the source cell runs indefinitely.

## Design

### Core Principle: Gateway as Authority Enforcer

The gateway becomes the single enforcement point for client-facing traffic, analogous to SpatialOS's Runtime. The coordinator is the single authority mint (only it can bump session epochs). Hosts stamp their epoch on all traffic. The gateway validates both directions:

- **Outbound (host → client):** Only forward `ClientFrame` messages at current epoch
- **Inbound (client → host):** Only route `ClientInput` messages at current epoch

This is a single uint64 comparison per message on the hot path. No allocations, no locks beyond what already exists.

### Session Epoch Lifecycle

Every client session has a monotonically increasing epoch managed by `sessionRoutes` on the coordinator. The epoch is bumped atomically on every authority change (migrate, split, merge). The lifecycle:

```
Session created (login)         → epoch = 1, host = H1
Cell migrate commit             → epoch = 2, host = H2 (UpstreamSwitch dispatched)
Cell split transfers player     → epoch = 3, host = H3 (UpstreamSwitch dispatched)
Player disconnects              → session removed
```

Each host learns its epoch for a session at the moment the session is created on that host:
- **Login:** coordinator assigns epoch 1 via `SessionAnnounce`
- **Transfer:** destination host receives epoch N+1 via `sessionRoutes.Migrate()` during `CellTransfer.Populate()`

The `VirtualConnManager.virtualSession` struct already has an `epoch uint64` field — it's just never used. This design wires it.

### Protocol Changes

**Add epoch to `ClientFrame`:**
```protobuf
message ClientFrame {
  string gateway_id = 1;
  uint32 conn_id    = 2;
  bytes  data       = 3;
  uint64 epoch      = 4;  // session epoch at send time
}
```

**Add epoch to `ClientInput`:**
```protobuf
message ClientInput {
  string gateway_id = 1;
  uint32 conn_id    = 2;
  bytes  data       = 3;
  uint64 epoch      = 4;  // session epoch at route time
}
```

### Gateway Outbound Validation

When the gateway receives a `ClientFrame` from a host (via MeshData stream):

```
frame = received ClientFrame
session = sessionRoutes.Get(frame.GatewayID, frame.ConnID)
if session == nil:
    drop (session expired or never existed)
if frame.epoch < session.Epoch:
    drop (stale frame from old host — authority transferred)
forward frame.data to client WebSocket
```

For embedded gateway (`local-shortcut` mode), the same validation applies. Frames arriving via `cell.Inbox` direct dispatch carry the same epoch field.

### Gateway Inbound Validation

When the gateway routes client input to a host:

```
session = sessionRoutes.Get(gatewayID, connID)
input = ClientInput{
    GatewayID: gatewayID,
    ConnID:    connID,
    Data:      rawInput,
    Epoch:     session.Epoch,  // stamp current epoch
}
route input to session.HostID via MeshData or cell.Inbox
```

On the host side, the VCM validates:

```
func (v *VirtualConnManager) InjectInput(frame ClientInput):
    sess = v.byKey[{frame.GatewayID, frame.ConnID}]
    if sess == nil:
        drop
    if frame.epoch != sess.epoch:
        drop (stale input routed to wrong host during handoff window)
    sess.inputBuf.append(frame.data)
```

### Host-Side Epoch Stamping

`VirtualConnManager.Send()` stamps the session epoch on every outbound ClientFrame:

```go
func (v *VirtualConnManager) Send(connID uint32, data []byte) {
    sess := v.byLocal[connID]
    frame := &meshpb.ClientFrame{
        GatewayId: sess.key.GatewayID,
        ConnId:    sess.key.ConnID,
        Data:      data,
        Epoch:     sess.epoch,
    }
    // ... encode into MeshFrame and send via HostNetwork
}
```

The epoch is set at session creation time:
- `RegisterSessionTransfer()` receives the epoch from `sessionRoutes.Migrate()`
- `RegisterSession()` (login path) receives epoch from `SessionAnnounce`

### Migrate Commit: CellRelease for Source Host

After `applyMigrateCommit` updates topology and remaps sessions:

**For ALL source cells (both local and remote):**
1. Call `releaseCellOnNode(cellID)` for in-process cells (existing function, synchronous)
2. Send `CellRelease` message to remote source hosts (existing message, already handled by `releaseCellOnNode` on the remote side)

This replaces the current pattern of calling `srcCell.Shutdown()` directly on a cell object reference, which only works for in-process cells.

The CellRelease is for **resource cleanup** (stop the game loop, free memory, release NetID range), not for correctness. The epoch gating already prevents stale frames from reaching the client before CellRelease arrives.

### Abort Path

If the transfer aborts before commit:
- No epoch change occurred — the source host keeps replicating normally
- No freeze/unfreeze state machine needed
- The destination cell (if created) is torn down by the executor's abort handler
- Zero complexity on the source side

### Proto Cleanup

Remove unused message types that were stubbed but never wired:

- `CellMigrateCommit` message — `CellRelease` covers the use case
- `HostMessage.migrate_ready` (field 7) — replaced by `cell_transfer_ready` in S7
- `CoordMessage.migrate_commit` (field 7) — send side of unused CellMigrateCommit
- `CoordMessage.cell_split` (field 4) — pre-S7, replaced by unified `cell_transfer`
- `CoordMessage.cell_merge` (field 5) — same
- `CoordMessage.cell_migrate` (field 6) — same

Per project convention: don't reserve old field numbers. Delete the messages and their oneof variants. Protobuf wire compatibility is maintained by field number (gaps in oneof are fine).

Verify no handler code references deleted types (grep for message names in Go code). Remove any dead handler code found.

Regenerate with `just proto`.

### Applicability Beyond Migrate

This design handles ALL authority transfers uniformly:
- **Cell migrate:** source cell moves to new host. Epoch bump + CellRelease.
- **Cell split:** parent cell splits into children across hosts. Each transferred session gets an epoch bump. Parent cell released.
- **Cell merge:** donor cells merge into survivor. Transferred sessions get epoch bumps. Donors released.
- **Player handoff:** player crosses cell boundary. Session rerouted with epoch bump.

Every case flows through `sessionRoutes.remapHostCell()` → epoch bump → `UpstreamSwitch` → gateway validation. The authority enforcement is one code path for all topology changes.

## Files

| File | Change |
|------|--------|
| `proto/meshpb/mesh.proto` | Add `epoch` to ClientFrame + ClientInput; delete unused messages |
| `gen/go/meshpb/` | Regenerated |
| `gen/csharp/` | Regenerated (Unity client) |
| `gen/es/` | Regenerated (web client) |
| `pkg/universe/virtual_conn_manager.go` | Stamp epoch on Send/SendReliable; validate epoch on InjectInput |
| `pkg/universe/gateway.go` | Validate epoch on inbound ClientFrame before forwarding |
| `pkg/universe/cell_transfer_commit.go` | Send CellRelease for remote source cells; unify local/remote cleanup |
| `pkg/universe/coordinator.go` | Remove direct srcCell.Shutdown(); use releaseCellOnNode uniformly |
| `pkg/universe/host_network.go` | Pass epoch through ClientInput routing |
| `pkg/universe/session_routes.go` | Ensure Migrate() returns new epoch for VCM to learn |

## Verification

1. `just proto` — regenerate all proto outputs
2. `go vet ./...` — clean
3. `go test ./pkg/universe/... -count=1` — full E2E suite green (46s)
4. `just build` — binary compiles
5. `just distributed-space` — run 5-process distributed setup:
   - `cell migrate 0_0 space-host-0` — player inside cell sees seamless transition (no jitter, no pause)
   - `perf` — shows data from all hosts
   - `cell list` — shows migrated cell on new host
   - Source host console: cell's game loop stopped after CellRelease
6. Single-process `./bin/server --mode=all`:
   - All existing gameplay unaffected
   - Cell split/merge still seamless
