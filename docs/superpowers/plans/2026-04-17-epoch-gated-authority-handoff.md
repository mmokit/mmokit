# Epoch-Gated Authority Handoff Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Eliminate client jitter during cross-host cell migration by adding epoch validation to the frame routing layer — the gateway drops stale frames from hosts that have lost authority, and hosts reject stale input routed during the handoff window.

**Architecture:** Add an `epoch` field to both `ClientFrame` (host→gateway→client) and `ClientInput` (client→gateway→host) proto messages. Hosts stamp their session epoch on every outbound frame. The gateway validates: frames with stale epochs are silently dropped. After migrate commit, the coordinator sends `CellRelease` to the remote source host to shut down the now-empty cell. Unused pre-S7 proto messages are deleted.

**Tech Stack:** Go, protobuf (buf generate), `pkg/universe/` (gateway, VCM, host_network, cell_transfer_commit), `proto/meshpb/mesh.proto`

---

## File Structure

**Modify:**
- `proto/meshpb/mesh.proto` — add `epoch` to `ClientFrame` + `ClientInput`; delete 5 unused pre-S7 messages
- `pkg/universe/virtual_conn_manager.go` — stamp epoch on outbound `ClientFrame`; validate epoch on inbound `ClientInput`
- `pkg/universe/gateway.go` — validate epoch on received `ClientFrame`; stamp epoch on outbound `ClientInput`
- `pkg/universe/host_network.go` — pass epoch through `ClientInput` routing to VCM
- `pkg/universe/cell_transfer_commit.go` — send `CellRelease` to remote source host after migrate commit
- `pkg/universe/coordinator.go` — wire CellRelease dispatch for remote hosts; `sendCellRelease` helper

**Create:**
- `pkg/universe/s7_migrate_epoch_test.go` — E2E test: cross-host migrate with epoch validation

---

## Task 1: Proto Changes — Add Epoch Fields + Delete Unused Messages

**Files:**
- Modify: `proto/meshpb/mesh.proto`
- Regenerate: `gen/go/meshpb/`, `gen/csharp/`, `gen/es/`

- [ ] **Step 1: Add epoch to ClientFrame and ClientInput**

In `proto/meshpb/mesh.proto`, find `ClientInput` (lines 499-503) and `ClientFrame` (lines 506-510). Add `uint64 epoch = 4;` to both:

```protobuf
message ClientInput {
  uint32 conn_id    = 1;
  bytes  data       = 2;
  string gateway_id = 3;
  uint64 epoch      = 4;
}

message ClientFrame {
  uint32 conn_id    = 1;
  bytes  data       = 2;
  string gateway_id = 3;
  uint64 epoch      = 4;
}
```

- [ ] **Step 2: Delete unused pre-S7 messages**

Delete these message definitions and their `oneof` entries:

From `CoordMessage` oneof (lines 41-60): remove entries for:
- `CellSplit cell_split = 4;`
- `CellMerge cell_merge = 5;`
- `CellMigrate cell_migrate = 6;`
- `CellMigrateCommit migrate_commit = 7;`

From `HostMessage` oneof (lines 17-35): remove entry for:
- `MigrateReady migrate_ready = 7;`

Delete the message definitions themselves:
- `CellSplit` (lines 157-160)
- `CellMerge` (lines 167-171)
- `CellMigrate` (lines 173-176)
- `CellMigrateCommit` (lines 178-180)
- `MigrateReady` (lines 99-102)

**Before deleting:** Verify no Go code references these types:

```bash
grep -rn 'CellSplit\b\|CellMerge\b\|CellMigrate\b\|CellMigrateCommit\|MigrateReady' pkg/ --include='*.go' | grep -v '_test.go' | grep -v '.pb.go'
```

Expected: no matches (all replaced by unified `CellTransfer` in S7).

- [ ] **Step 3: Regenerate proto outputs**

```bash
just proto
```

Verify no compilation errors:

```bash
go vet ./...
```

- [ ] **Step 4: Commit**

```bash
git add proto/ gen/
git commit -m "proto(meshpb): add epoch to ClientFrame/ClientInput; delete pre-S7 stubs

ClientFrame and ClientInput gain a uint64 epoch field for authority
validation during cross-host handoff. Five unused pre-S7 messages
removed: CellSplit, CellMerge, CellMigrate, CellMigrateCommit,
MigrateReady — all replaced by the unified CellTransfer protocol.

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>"
```

---

## Task 2: VCM Stamps Epoch on Outbound ClientFrame

**Files:**
- Modify: `pkg/universe/virtual_conn_manager.go`
- Test: `pkg/universe/virtual_conn_manager_test.go` (create or extend)

- [ ] **Step 1: Write failing test**

Create `pkg/universe/virtual_conn_manager_test.go` (or extend if it exists). The test verifies that `forwardToGateway` stamps the session's epoch on the outgoing `ClientFrame`.

First, read `pkg/universe/virtual_conn_manager.go` to understand:
- How `forwardToGateway()` builds the `ClientFrame` (lines 238-271)
- The `virtualSession` struct (lines 42-52) — it already has an `epoch uint64` field
- How `RegisterSession()` sets the epoch (line 75-99)

Write a test that:
1. Creates a VCM with a mock `HostNetwork` that captures sent frames
2. Registers a session with epoch=5
3. Calls `Send(localID, data)`
4. Asserts the captured `ClientFrame` has `Epoch: 5`

The exact test shape depends on the VCM's constructor and whether `HostNetwork` is an interface or concrete type. Read the file first. If testing requires too much infrastructure, skip the unit test and cover this in the E2E test (Task 7). In that case, proceed directly to Step 3.

- [ ] **Step 2: Run test, verify failure**

```bash
go test ./pkg/universe/ -run TestVCMStampsEpoch -count=1 -v
```

Expected: FAIL — `ClientFrame.Epoch` is 0 (not stamped).

- [ ] **Step 3: Stamp epoch in `forwardToGateway`**

In `pkg/universe/virtual_conn_manager.go`, find `forwardToGateway()` (lines 238-271). Locate where the `ClientFrame` proto is built (around line 252-260):

```go
ClientFrame: &meshpb.ClientFrame{
    GatewayId: sess.key.GatewayID,
    ConnId:    sess.key.ConnID,
    Data:      data,
},
```

Add the `Epoch` field:

```go
ClientFrame: &meshpb.ClientFrame{
    GatewayId: sess.key.GatewayID,
    ConnId:    sess.key.ConnID,
    Data:      data,
    Epoch:     sess.epoch,
},
```

- [ ] **Step 4: Verify**

```bash
go vet ./...
go test ./pkg/universe/... -count=1 -short -timeout 3m
```

- [ ] **Step 5: Commit**

```bash
git add pkg/universe/virtual_conn_manager.go
git commit -m "feat(universe): VCM stamps session epoch on outbound ClientFrame

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>"
```

---

## Task 3: Gateway Validates Epoch on Inbound ClientFrame

**Files:**
- Modify: `pkg/universe/gateway.go`

The gateway receives `ClientFrame` messages from hosts (via MeshData streams or direct dispatch). Before forwarding the frame's data to the client WebSocket, validate that the frame's epoch is current.

- [ ] **Step 1: Find the gateway's ClientFrame receive path**

Read `pkg/universe/gateway.go` thoroughly. Search for where `ClientFrame` is handled. There are two paths:

**Path A — Standalone gateway:** The gateway's `HostNetwork` receives MeshData frames from hosts. `routeInboundFrame()` in the gateway's context dispatches `ClientFrame` to the client connection. Find this handler.

**Path B — Embedded gateway (always-proxy mode):** Similar MeshData path but in-process.

The handler likely does something like:
```go
if cf := frame.GetClientFrame(); cf != nil {
    conn := g.connMgr.Get(cf.ConnId)
    conn.Send(cf.Data)
}
```

Find the EXACT code. If the dispatch is in `host_network.go` rather than `gateway.go`, follow the call chain.

- [ ] **Step 2: Add epoch validation**

At the point where the gateway forwards `ClientFrame.Data` to the client, add a check:

```go
if cf := frame.GetClientFrame(); cf != nil {
    // Validate epoch — drop stale frames from hosts that lost authority.
    g.mu.RLock()
    sess, ok := g.sessions[cf.ConnId]
    g.mu.RUnlock()
    if !ok {
        return nil // session gone
    }
    if cf.Epoch > 0 && cf.Epoch < sess.epoch {
        return nil // stale frame from old host
    }
    // ... forward to client as before
}
```

The `cf.Epoch > 0` check handles backward compatibility — frames without epoch (from old code) pass through. Once all hosts stamp epoch, this guard can be removed.

**Important:** Read the gateway's session lookup mechanism. `g.sessions` might be a map, or there might be a lookup method. The `localSession` struct (lines 86-92) has an `epoch uint64` field. Use whatever lookup the existing code uses.

Also check: does `gateway.OnUpstreamSwitch()` update `sess.epoch`? It must — find the method and verify.

- [ ] **Step 3: Verify**

```bash
go vet ./...
go test ./pkg/universe/... -count=1 -short -timeout 3m
```

- [ ] **Step 4: Commit**

```bash
git add pkg/universe/gateway.go
git commit -m "feat(universe): gateway validates epoch on inbound ClientFrame

Drops frames from hosts whose epoch is stale (< session's current
epoch). Prevents cross-host migration jitter where both source and
destination cells replicate to the same client.

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>"
```

---

## Task 4: Gateway Stamps Epoch on Outbound ClientInput

**Files:**
- Modify: `pkg/universe/gateway.go`

When the gateway routes client input to a host, stamp the session's current epoch on the `ClientInput` message.

- [ ] **Step 1: Find the gateway's ClientInput send path**

In `pkg/universe/gateway.go`, find `runSessionPump()` (lines 736-781). This is the standalone gateway path. At lines 751-762, it builds `ClientInput`:

```go
ClientInput: &meshpb.ClientInput{
    GatewayId: g.id,
    ConnId:    connID,
    Data:      raw,
},
```

Also find the embedded gateway input path. In embedded mode, input may go directly to `cell.Inbox` without a `ClientInput` proto (local-shortcut). That path doesn't need epoch stamping — epoch validation only matters for cross-host routing.

- [ ] **Step 2: Stamp epoch on ClientInput**

In `runSessionPump()`, add the epoch:

```go
ClientInput: &meshpb.ClientInput{
    GatewayId: g.id,
    ConnId:    connID,
    Data:      raw,
    Epoch:     sess.epoch,
},
```

The `sess` variable should already be available in the pump's scope. If not, look up the session by connID.

Also check if there are other code paths that build `ClientInput` — search for `ClientInput{` in the file and update each.

- [ ] **Step 3: Verify**

```bash
go vet ./...
go test ./pkg/universe/... -count=1 -short -timeout 3m
```

- [ ] **Step 4: Commit**

```bash
git add pkg/universe/gateway.go
git commit -m "feat(universe): gateway stamps epoch on outbound ClientInput

Hosts can validate that input was routed at the current epoch, rejecting
stale input that arrives during the handoff window.

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>"
```

---

## Task 5: Host Validates Epoch on Inbound ClientInput

**Files:**
- Modify: `pkg/universe/host_network.go`
- Modify: `pkg/universe/virtual_conn_manager.go`

- [ ] **Step 1: Pass epoch through host_network to VCM**

In `pkg/universe/host_network.go`, find the `ClientInput` dispatch in `routeInboundFrame()` (lines 704-718):

```go
if ci := frame.GetClientInput(); ci != nil {
    ...
    n.vcm.InjectInput(localID, ci.Data)
    return nil
}
```

Change `InjectInput` call to pass the epoch:

```go
n.vcm.InjectInputWithEpoch(localID, ci.Data, ci.Epoch)
```

- [ ] **Step 2: Add epoch validation to VCM**

In `pkg/universe/virtual_conn_manager.go`, add `InjectInputWithEpoch`:

```go
func (v *VirtualConnManager) InjectInputWithEpoch(localID uint32, data []byte, epoch uint64) {
    v.mu.RLock()
    sess, ok := v.byLocal[localID]
    v.mu.RUnlock()
    if !ok {
        return
    }
    // Reject stale input from the handoff window. epoch==0 means
    // the gateway hasn't been upgraded yet — allow for compatibility.
    if epoch > 0 && epoch < sess.epoch {
        return
    }
    // Delegate to existing InjectInput logic
    v.injectInputInner(localID, data)
}
```

Refactor the existing `InjectInput(localID, data)` to call the same inner function (so both paths share the buffer-append logic):

```go
func (v *VirtualConnManager) InjectInput(localID uint32, data []byte) {
    v.injectInputInner(localID, data)
}

func (v *VirtualConnManager) injectInputInner(localID uint32, data []byte) {
    // ... existing InjectInput body (lines 174-192)
}
```

- [ ] **Step 3: Verify**

```bash
go vet ./...
go test ./pkg/universe/... -count=1 -short -timeout 3m
```

- [ ] **Step 4: Commit**

```bash
git add pkg/universe/host_network.go pkg/universe/virtual_conn_manager.go
git commit -m "feat(universe): host validates epoch on inbound ClientInput

Rejects stale input that was routed to this host during the handoff
window (between destination reporting Ready and gateway receiving
UpstreamSwitch). Prevents the old host from processing phantom input
that could corrupt entity state.

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>"
```

---

## Task 6: applyMigrateCommit Sends CellRelease to Remote Source

**Files:**
- Modify: `pkg/universe/cell_transfer_commit.go`
- Modify: `pkg/universe/coordinator.go` (if `sendCellRelease` helper needed)

- [ ] **Step 1: Read applyMigrateCommit**

Read `pkg/universe/cell_transfer_commit.go`, find `applyMigrateCommit()` (lines 193-270). Focus on lines 264-266 where `srcCell.Shutdown()` is called:

```go
if srcCell != nil {
    srcCell.Shutdown()
    c.netIDAlloc.Release(srcCell.Engine.NetIDBase())
}
```

This only works for in-process cells. For remote hosts, `srcCell` is nil because `c.Hosts[srcHost]` doesn't contain the remote host's cell objects.

- [ ] **Step 2: Add remote CellRelease dispatch**

Replace the shutdown block with logic that handles both local and remote:

```go
if srcCell != nil {
    // In-process cell — shut down directly.
    srcCell.Shutdown()
    c.netIDAlloc.Release(srcCell.Engine.NetIDBase())
} else if c.controlServer != nil {
    // Remote cell — send CellRelease via MeshControl. The remote
    // host's releaseCellOnNode() will shut down the cell and send
    // CellStopped back.
    c.sendCellRelease(srcHost, srcCellKey)
}
```

- [ ] **Step 3: Add `sendCellRelease` helper to coordinator**

In `pkg/universe/coordinator.go`, add:

```go
func (c *Coordinator) sendCellRelease(hostID, cellID string) {
    if c.controlServer == nil {
        return
    }
    msg := &meshpb.CoordMessage{
        CoordEpoch: c.coordEpoch,
        Msg: &meshpb.CoordMessage_CellRelease{
            CellRelease: &meshpb.CellRelease{
                CellId: cellID,
            },
        },
    }
    if err := c.controlServer.sendCoordMessageToHost(hostID, msg); err != nil {
        c.Log.Log(CatMeshCell, "coordinator: CellRelease to %s for %s failed: %v", hostID, cellID, err)
    }
}
```

**Before writing this:** verify:
1. `sendCoordMessageToHost(hostID, msg)` exists on `controlServer` — search for similar sends (e.g., how `PeerList` broadcasts work)
2. The remote host's `mesh_control_client.go` handles `CoordMessage_CellRelease` — search for `CellRelease` in the client's message handler. It should call `releaseCellOnNode()`.

If the handler doesn't exist on the remote host side, add it:

```go
case *meshpb.CoordMessage_CellRelease:
    c.coord.releaseCellOnNode(m.CellRelease.CellId)
```

Find where other `CoordMessage` cases are handled (search for `case *meshpb.CoordMessage_CellAssign` — the release handler goes next to it).

- [ ] **Step 4: Verify**

```bash
go vet ./...
go test ./pkg/universe/... -count=1 -short -timeout 3m
```

- [ ] **Step 5: Commit**

```bash
git add pkg/universe/cell_transfer_commit.go pkg/universe/coordinator.go pkg/universe/mesh_control_client.go
git commit -m "fix(universe): send CellRelease to remote source host after migrate commit

applyMigrateCommit now sends CellRelease to remote source hosts instead
of calling srcCell.Shutdown() on a nil object reference. The remote
host's releaseCellOnNode() shuts down the cell and sends CellStopped
back. In-process cells still shut down directly.

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>"
```

---

## Task 7: E2E Distributed Migrate Epoch Test

**Files:**
- Create: `pkg/universe/s7_migrate_epoch_test.go`

- [ ] **Step 1: Read existing S7 test harness**

Read the test harness used by `pkg/universe/s7_migrate_test.go` and `pkg/universe/s7_perf_test.go`. Understand:
- How a multi-host coord is constructed (Config with TestHosts)
- How cells are distributed across hosts
- How to invoke `cell.migrate` via the dispatcher
- How to verify a cell's host changed

- [ ] **Step 2: Write the test**

Create `pkg/universe/s7_migrate_epoch_test.go`:

```go
package universe

import (
    "context"
    "testing"
    "time"

    "github.com/mmokit/mmokit/pkg/cmdsys"
)

// TestMigrateEpochNoJitter verifies that after a cross-host cell migrate:
// 1. The source cell's game loop stops (CellRelease processed)
// 2. Frames from the old host are dropped by epoch validation
// 3. The destination cell is reachable
func TestMigrateEpochNoJitter(t *testing.T) {
    // Use the same harness as s7_migrate_test.go.
    // 2 hosts, 2x1 cells: cell 0_0 on host-a, cell 1_0 on host-b.
    // Migrate cell 0_0 from host-a to host-b.
    // ... (adapt from existing S7 test setup)

    // After migrate:
    // 1. Verify cell 0_0 is now on host-b (via cell list or cellToHostMap)
    // 2. Verify host-a no longer has cell 0_0 (Host.CellCount decreased)
    // 3. Verify session epoch was bumped (sessionRoutes epoch > 1)
    // 4. Let a few ticks pass — no panics, no stale frame errors
}
```

Model this test on the existing `TestS7MigrateAcrossHosts` test (in `s7_migrate_test.go`). The key additions:
- After migrate, verify the source host's cell count decreased by 1
- Verify the session epoch was bumped via `sessionRoutes.Get()`
- Sleep for a few ticks and verify no errors (the epoch validation silently drops stale frames rather than erroring)

- [ ] **Step 3: Run the test**

```bash
go test ./pkg/universe/ -run TestMigrateEpochNoJitter -count=1 -v -timeout 2m
```

Expected: PASS. If it fails, debug — the most likely issue is the CellRelease handler not being wired on the remote host side.

- [ ] **Step 4: Commit**

```bash
git add pkg/universe/s7_migrate_epoch_test.go
git commit -m "test(universe): E2E test for epoch-gated cross-host migrate

Verifies source cell shuts down via CellRelease, session epoch is bumped,
and no stale frames reach the client after cross-host cell migration.

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>"
```

---

## Self-Review

**1. Spec coverage:**
- ✅ Add epoch to ClientFrame — Task 1
- ✅ Add epoch to ClientInput — Task 1
- ✅ VCM stamps epoch on outbound — Task 2
- ✅ Gateway validates epoch on inbound ClientFrame — Task 3
- ✅ Gateway stamps epoch on outbound ClientInput — Task 4
- ✅ Host validates epoch on inbound ClientInput — Task 5
- ✅ CellRelease for remote source — Task 6
- ✅ Proto cleanup (delete unused messages) — Task 1
- ✅ E2E test — Task 7
- ✅ SessionAnnounce epoch — not needed for initial login (epoch=1 hardcoded everywhere for login; only migrate/split bump it). Can be added as a follow-up if needed.

**2. Placeholder scan:** No TBDs or TODOs. Step 1 of Tasks 3, 4, and 7 say "read the code first" with specific guidance on what to look for — this is intentional (the gateway's frame routing has multiple paths that the implementer must trace).

**3. Type consistency:**
- `ClientFrame.Epoch` / `ClientInput.Epoch` — uint64, consistent across proto + Go
- `sess.epoch` — uint64 on both `virtualSession` (VCM) and `localSession` (gateway)
- `InjectInputWithEpoch(localID uint32, data []byte, epoch uint64)` — consistent with existing `InjectInput` signature + epoch param
- `sendCellRelease(hostID, cellID string)` — consistent with existing `CellRelease{CellId: string}` proto
