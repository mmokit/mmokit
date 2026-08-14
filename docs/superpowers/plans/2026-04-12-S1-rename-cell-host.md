# S1: Rename Node→Cell + 1:N Cell Hosting — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Rename `Node` to `Cell` throughout the codebase and introduce a `Host` container that owns N cells, establishing the vocabulary and structural foundation for distributed multi-host meshing.

**Architecture:** Purely structural refactor — no new features, no network, no Postgres. Every existing `Node` becomes a `Cell` (the minimal simulation unit: one CellID, one ecs.World, one goroutine). A new `Host` struct holds `map[CellID]*Cell` and shared resources (logger, metrics registry). The `Coordinator` manages `Host` records instead of `Cell` records directly. All existing behavior is preserved.

**Tech Stack:** Go, Ark ECS v0.7.1, existing mmokit infrastructure. No new dependencies.

**Spec:** [docs/superpowers/specs/2026-04-12-distributed-mesh-design.md](../specs/2026-04-12-distributed-mesh-design.md) — Section 1 (Vocabulary), Section 2 (Topology)

---

## File Structure

### Files to rename (git mv)

| From | To | Reason |
|---|---|---|
| `pkg/universe/node.go` | `pkg/universe/cell.go` | Struct rename Node→Cell |
| `pkg/universe/node_bridge_impl.go` | `pkg/universe/cell_bridge_impl.go` | Bridge implementation for cells |

### Files to create

| Path | Responsibility |
|---|---|
| `pkg/universe/host.go` | `Host` struct: owns N cells, shared logger, metrics, future gRPC endpoint |

### Files to modify (core — compile-breaking rename)

| Path | What changes |
|---|---|
| `pkg/universe/bridge.go` | `NodeBridge` → `Bridge`, `NoopNodeBridge` → `NoopBridge` |
| `pkg/universe/message.go` | `NodeMessage` → `CellMessage`, `FromNodeID` → `FromCellID` |
| `pkg/universe/coordinator.go` | `Nodes map[string]*Node` → manage via `Host`, cell-addressing |
| `pkg/universe/partition.go` | All `*Node` references → `*Cell` |
| `pkg/universe/border_viewer.go` | `NodeViewer` → `CellViewer`, `NewNodeViewer` → `NewCellViewer`, `NodeViewerID` → `CellViewerID` |
| `pkg/universe/border_replication.go` | `NodeViewer` → `CellViewer` parameter types |
| `pkg/universe/topology.go` | `MeshNodeID` → `MeshCellID` |
| `pkg/universe/world_base.go` | `nodeID` field/params, bridge types, log categories |
| `pkg/universe/handoff.go` | `NeighborID` field docs (minor) |
| `pkg/universe/loopback_bridge.go` | `NodeMessage` → `CellMessage`, param names |
| `pkg/universe/cell_id.go` | `NodeID()` method stays (generates "cell_X_Y" strings now) |

### Files to modify (facades + callers)

| Path | What changes |
|---|---|
| `pkg/mmokit/mmokit.go` | Facade type aliases: `Node` → `Cell`, `NodeBridge` → `Bridge`, `NoopNodeBridge` → `NoopBridge` |
| `pkg/metrics/node_metrics.go` | `NodeMetrics` → `CellMetrics`, `SetNodeID` → `SetCellID` |

### Files to modify (tests)

| Path | What changes |
|---|---|
| `pkg/universe/border_viewer_test.go` | `NodeViewer` → `CellViewer`, string literals |
| `pkg/universe/border_replication_stub_test.go` | `NodeViewer` → `CellViewer` |
| `pkg/universe/handoff_test.go` | String literals "node_1_0" → "cell_1_0" |
| `pkg/universe/loopback_bridge_test.go` | `NodeMessage` → `CellMessage` |
| `pkg/universe/universe_test.go` | `Node` → `Cell` references |
| `pkg/universe/partition_test.go` | `Nodes` map access |
| `pkg/universe/cell_id_test.go` | `NodeID()` test expectations |
| `pkg/metrics/node_metrics_test.go` | `NodeMetrics` → `CellMetrics` |
| `internal/game/node_test.go` → rename to `internal/game/cell_test.go` | All `Node` refs + string literals |
| `internal/game/testutil_test.go` | `newTestNode` → `newTestCell` |
| `internal/game/topology_test.go` | String literals |

### Files to modify (examples + game)

| Path | What changes |
|---|---|
| `internal/game/game.go` | References to bridge/cell types |
| `examples/slither/main.go` | `coord.Nodes` → new access pattern |
| `examples/slither/world.go` | Bridge type |
| `examples/4node-basic/main.go` | Coordinator usage |

### Files to modify (log categories)

| Path | What changes |
|---|---|
| `pkg/universe/world_base.go` | `CatMeshNode` → `CatMeshCell` |

---

## Task Breakdown

### Task 1: Create branch and rename source files

**Files:**
- Rename: `pkg/universe/node.go` → `pkg/universe/cell.go`
- Rename: `pkg/universe/node_bridge_impl.go` → `pkg/universe/cell_bridge_impl.go`
- Rename: `internal/game/node_test.go` → `internal/game/cell_test.go`

- [ ] **Step 1: Create the feature branch**

```bash
git checkout -b feature/S1-rename-cell-host
```

- [ ] **Step 2: Rename files via git mv**

```bash
git mv pkg/universe/node.go pkg/universe/cell.go
git mv pkg/universe/node_bridge_impl.go pkg/universe/cell_bridge_impl.go
git mv internal/game/node_test.go internal/game/cell_test.go
```

- [ ] **Step 3: Commit the renames before content changes**

```bash
git add -A
git commit -m "chore: rename node.go -> cell.go, node_bridge_impl.go -> cell_bridge_impl.go

Pure file renames, no content changes. Establishes the new naming
convention for the Node->Cell vocabulary rename (S1)."
```

This commit is intentionally content-free so git tracks the renames properly (git detects renames by content similarity — if we change content at the same time as the move, git may see it as delete+create instead of rename).

---

### Task 2: Rename core types in pkg/universe

This is the big mechanical rename. All type names, method receivers, field names, and constructor functions in `pkg/universe/` change from Node→Cell vocabulary. The codebase will NOT compile until this task is complete.

**Files:**
- Modify: `pkg/universe/cell.go` (was node.go)
- Modify: `pkg/universe/bridge.go`
- Modify: `pkg/universe/message.go`
- Modify: `pkg/universe/cell_bridge_impl.go` (was node_bridge_impl.go)
- Modify: `pkg/universe/border_viewer.go`
- Modify: `pkg/universe/topology.go`
- Modify: `pkg/universe/handoff.go`
- Modify: `pkg/universe/loopback_bridge.go`
- Modify: `pkg/universe/world_base.go`
- Modify: `pkg/universe/border_replication.go`
- Modify: `pkg/universe/border_components.go`

- [ ] **Step 1: Rename types in cell.go (was node.go)**

In `pkg/universe/cell.go`, apply these renames:
- `type Node struct` → `type Cell struct`
- All comments referencing "Node" in the struct/method context → "Cell"
- `func (n *Node) Run(` → `func (c *Cell) Run(`
- `func (n *Node) Shutdown(` → `func (c *Cell) Shutdown(`
- `func (n *Node) DrainInbox(` → `func (c *Cell) DrainInbox(`
- `func (n *Node) processMessage(` → `func (c *Cell) processMessage(`
- All `n.` references inside methods → `c.`
- `Neighbors map[string]*Node` → `Neighbors map[string]*Cell`
- `Bridge NodeBridge` → `Bridge Bridge`
- `Inbox chan NodeMessage` → `Inbox chan CellMessage`
- Log category: `CatMeshNode` → `CatMeshCell` (update string too)
- Log prefix: keep `[%s]` with `c.ID` — the ID string format changes in a later step

- [ ] **Step 2: Rename types in bridge.go**

In `pkg/universe/bridge.go`:
- `type NodeBridge interface` → `type Bridge interface`
- `type NoopNodeBridge struct{}` → `type NoopBridge struct{}`
- All `func (NoopNodeBridge)` → `func (NoopBridge)`
- Update comments from "NodeBridge" to "Bridge"

- [ ] **Step 3: Rename types in message.go**

In `pkg/universe/message.go`:
- `type NodeMessage struct` → `type CellMessage struct`
- `FromNodeID string` → `FromCellID string`
- Update all comments referencing "NodeMessage" or "inter-node"

- [ ] **Step 4: Rename types in cell_bridge_impl.go (was node_bridge_impl.go)**

In `pkg/universe/cell_bridge_impl.go`:
- `type nodeBridge struct` → `type cellBridge struct`
- `node *Node` field → `cell *Cell`
- All `b.node.` → `b.cell.`
- All `NodeMessage{` → `CellMessage{`
- `FromNodeID: b.node.ID` → `FromCellID: b.cell.ID`
- All `n.Neighbors` → use updated field paths
- Method receivers: `func (b *nodeBridge)` → `func (b *cellBridge)`
- `neighborInfo` return type uses `NeighborInfo` (unchanged)
- `NodeBridge` references in interface assertions → `Bridge`

- [ ] **Step 5: Rename types in border_viewer.go**

In `pkg/universe/border_viewer.go`:
- `type NodeViewer struct` → `type CellViewer struct`
- `sourceNode *Node` → `sourceCell *Cell`
- `destNode *Node` → `destCell *Cell`
- `func NewNodeViewer(` → `func NewCellViewer(`
- `func NodeViewerID(` → `func CellViewerID(`
- `v.sourceNode` → `v.sourceCell`, `v.destNode` → `v.destCell`
- `nodeID string` field/param → `cellID string` (the viewer's target cell ID)
- Update all comments

- [ ] **Step 6: Rename in topology.go**

In `pkg/universe/topology.go`:
- `func MeshNodeID(cell CellID) string` → `func MeshCellID(cell CellID) string`
- The returned string format changes from `cell.NodeID()` which returns `"node_X_Y"` — we'll update `CellID.NodeID()` in a later step to return `"cell_X_Y"`.

- [ ] **Step 7: Rename in remaining universe files**

In `pkg/universe/loopback_bridge.go`:
- `func (lb *LoopbackBridge) Send(sourceNode, destNode string, msg NodeMessage)` → `Send(sourceCell, destCell string, msg CellMessage)`
- `receivers map[string]func(NodeMessage)` → `receivers map[string]func(CellMessage)`
- `func (lb *LoopbackBridge) SetReceiver(nodeID string, fn func(NodeMessage))` → `SetReceiver(cellID string, fn func(CellMessage))`

In `pkg/universe/world_base.go`:
- `nodeID` field → `cellID`
- `CatMeshNode` references → `CatMeshCell`
- Update log format strings
- `NodeBridge` type references → `Bridge`

In `pkg/universe/border_replication.go`:
- `map[string]*NodeViewer` → `map[string]*CellViewer`
- `nv *NodeViewer` params → `nv *CellViewer`

In `pkg/universe/border_components.go`:
- No type renames needed (uses `WorldBase` methods, no direct Node/Cell refs)

In `pkg/universe/handoff.go`:
- `NeighborID string` field — keep the name (it's a cell ID for the neighbor, not a node ID)
- Update comment: "neighbor node" → "neighbor cell"

- [ ] **Step 8: Verify the universe package compiles in isolation**

```bash
go build ./pkg/universe/
```

Expected: compile errors from callers outside `pkg/universe/` (mmokit, metrics, game, examples) but the universe package itself should compile.

Note: This will likely fail because `coordinator.go` and `partition.go` reference the old type names heavily. Those are addressed in Step 9.

- [ ] **Step 9: Rename in coordinator.go and partition.go**

These are the two largest files and require careful attention.

In `pkg/universe/coordinator.go`:
- `Nodes map[string]*Node` → `Cells map[string]*Cell`
- `NodeOwner map[CellID]string` → `CellOwner map[CellID]string`
- All `c.Nodes[` → `c.Cells[`
- All `c.NodeOwner[` → `c.CellOwner[`
- All `for _, node := range c.Nodes` → `for _, cell := range c.Cells`
- All `node.` references inside loops → `cell.`
- `getNode(id)` → `getCell(id)` (private helper)
- `NodeBridge` → `Bridge` in constructor calls
- `NodeMessage{` → `CellMessage{`
- `FromNodeID` → `FromCellID`
- Update all comments

In `pkg/universe/partition.go`:
- Same pattern: `*Node` → `*Cell`, `Nodes` → `Cells`, `NodeOwner` → `CellOwner`
- `NodeMessage{` → `CellMessage{`
- `FromNodeID` → `FromCellID`
- All loop variable names `node` → `cell`

- [ ] **Step 10: Verify pkg/universe compiles**

```bash
go build ./pkg/universe/
```

Expected: SUCCESS for the universe package. Callers still broken.

- [ ] **Step 11: Commit the universe rename**

```bash
git add -A
git commit -m "refactor(universe): rename Node->Cell, NodeBridge->Bridge, NodeMessage->CellMessage

Mechanical type rename throughout pkg/universe/ establishing the
distributed mesh vocabulary from the S1 spec. Cell is the minimal
simulation unit (one CellID, one ECS world, one goroutine). Host
container comes in the next commit.

Renames: Node->Cell, NodeBridge->Bridge, NoopNodeBridge->NoopBridge,
NodeMessage->CellMessage, NodeViewer->CellViewer, NodeViewerID->CellViewerID,
MeshNodeID->MeshCellID. FromNodeID->FromCellID in message struct.
Coordinator.Nodes->Cells, NodeOwner->CellOwner."
```

---

### Task 3: Rename in pkg/metrics and pkg/mmokit facades

**Files:**
- Modify: `pkg/metrics/node_metrics.go`
- Modify: `pkg/metrics/node_metrics_test.go`
- Modify: `pkg/mmokit/mmokit.go`

- [ ] **Step 1: Rename NodeMetrics → CellMetrics**

In `pkg/metrics/node_metrics.go`:
- `type NodeMetrics struct` → `type CellMetrics struct`
- `func NewNodeMetrics(` → `func NewCellMetrics(`
- All receiver `func (m *NodeMetrics)` → `func (m *CellMetrics)`
- `SetNodeID` → `SetCellID`
- `NodeID()` → `CellID()`
- `nodeID string` field → `cellID string`
- `NodeSnapshot` → `CellSnapshot` (if this type exists)
- Update comments

In `pkg/metrics/node_metrics_test.go`:
- `NewNodeMetrics` → `NewCellMetrics`
- `NodeMetrics` → `CellMetrics`
- String literals with "node" → "cell" where they are IDs

- [ ] **Step 2: Update mmokit facade exports**

In `pkg/mmokit/mmokit.go`, update these type aliases:
- `type Node = universe.Node` → `type Cell = universe.Cell`
- `type NodeBridge = universe.NodeBridge` → `type Bridge = universe.Bridge`
- `type NoopNodeBridge = universe.NoopNodeBridge` → `type NoopBridge = universe.NoopBridge`
- `type NodeMetrics = metrics.NodeMetrics` → `type CellMetrics = metrics.CellMetrics`
- `type NodeMessage = universe.NodeMessage` → `type CellMessage = universe.CellMessage`
- Any `NewNodeMetrics` → `NewCellMetrics`
- Search for ALL references to the old type names and update callers within mmokit.go

- [ ] **Step 3: Verify metrics and mmokit compile**

```bash
go build ./pkg/metrics/ ./pkg/mmokit/
```

Expected: SUCCESS for these packages. Game code and examples still broken.

- [ ] **Step 4: Commit**

```bash
git add -A
git commit -m "refactor(metrics,mmokit): rename NodeMetrics->CellMetrics, update facade aliases

Propagates the Cell vocabulary rename into pkg/metrics and pkg/mmokit
facade layer. CellMetrics, CellViewer, etc. are the public API names
game code uses."
```

---

### Task 4: Rename in internal/game and examples

**Files:**
- Modify: `internal/game/game.go`
- Modify: `internal/game/cell_test.go` (was node_test.go)
- Modify: `internal/game/testutil_test.go`
- Modify: `internal/game/topology_test.go`
- Modify: `internal/game/system_network.go`
- Modify: `internal/game/transfer.go`
- Modify: `internal/game/transfer_test.go`
- Modify: `examples/slither/main.go`
- Modify: `examples/slither/world.go`
- Modify: `examples/4node-basic/main.go`
- Modify: `examples/simple/main.go`

- [ ] **Step 1: Update internal/game**

Search all `.go` files in `internal/game/` for:
- `mmokit.Node` → `mmokit.Cell`
- `mmokit.NodeBridge` → `mmokit.Bridge`
- `mmokit.NoopNodeBridge` → `mmokit.NoopBridge`
- `mmokit.NodeMetrics` → `mmokit.CellMetrics`
- `universe.Node` → `universe.Cell` (if directly imported)
- `universe.NodeBridge` → `universe.Bridge`
- `universe.NodeMessage` → `universe.CellMessage`
- `"node_` string literals → `"cell_` (test fixtures)
- `newTestNode` → `newTestCell` (in testutil_test.go)
- `FromNodeID` → `FromCellID`

- [ ] **Step 2: Update examples**

In `examples/slither/main.go`:
- `coord.Nodes` → `coord.Cells`
- Any `*universe.Node` or `*mmokit.Node` → `*Cell`

In `examples/slither/world.go`:
- `NodeBridge` → `Bridge`
- Any `*Node` → `*Cell`

In `examples/4node-basic/main.go`:
- `coord.Nodes` → `coord.Cells` (if accessed)
- Any type references

In `examples/simple/main.go`:
- Likely no changes (uses `OnInit` pattern, no direct Node access)

- [ ] **Step 3: Full compile check**

```bash
go vet ./...
```

Expected: CLEAN. Every reference to the old names should now be updated. If any compile errors remain, fix them — they indicate a missed rename.

- [ ] **Step 4: Run full test suite**

```bash
go test -count=1 ./...
```

Expected: ALL PASS. This is a mechanical rename — zero behavioral changes.

- [ ] **Step 5: Commit**

```bash
git add -A
git commit -m "refactor(game,examples): propagate Cell vocabulary to game code and examples

Updates internal/game/, examples/slither/, examples/4node-basic/, and
examples/simple/ to use the Cell vocabulary. All compile errors from the
Node->Cell rename are now resolved."
```

---

### Task 5: Update CellID string format and test fixtures

**Files:**
- Modify: `pkg/universe/cell_id.go`
- Modify: `pkg/universe/cell_id_test.go`
- Modify: `pkg/universe/topology.go`
- Modify: Various test files with "node_X_Y" string literals

- [ ] **Step 1: Change CellID.NodeID() → CellID.String() output format**

In `pkg/universe/cell_id.go`, the `NodeID()` method currently returns `"node_X_Y"` or `"node_dD_X_Y"`. Two changes:

1. Rename the method from `NodeID()` to keep it but change the output prefix from `"node_"` to `"cell_"`:
   - `"node_0_0"` → `"cell_0_0"`
   - `"node_d1_2_3"` → `"cell_d1_2_3"`

2. If there's a `String()` method that delegates, update it too.

Also update `MeshCellID()` in `topology.go` to call the renamed method.

Also update `parseNodeIndex` in `pkg/universe/border_viewer.go` (or wherever it lives) — it parses `"node_X_Y"` format strings. Change the `Sscanf` patterns:
- `"node_d%*d_%d_%d"` → `"cell_d%*d_%d_%d"`
- `"node_%d_%d"` → `"cell_%d_%d"`

Rename the function to `parseCellIndex`.

- [ ] **Step 2: Update all string literal fixtures**

Search the entire codebase for `"node_` in `.go` files and update to `"cell_`:

Files known to contain these:
- `pkg/universe/border_viewer_test.go`: `"node_1_0"`, `"node_0_1"` etc.
- `pkg/universe/handoff_test.go`: `"node_1_0"` in HandoffKey literals
- `pkg/universe/cell_id_test.go`: expected output strings
- `pkg/universe/universe_test.go`: fixture IDs
- `pkg/universe/border_replication_stub_test.go`: `"neighbor"` (this one is fine, not a node_ literal)
- `internal/game/cell_test.go` (was node_test.go): `"node_1_1"` etc.
- `internal/game/topology_test.go`: `"node_0_0"` etc.

- [ ] **Step 3: Run full test suite**

```bash
go test -count=1 ./...
```

Expected: ALL PASS. String format change is tested by the cell_id_test.go expectations.

- [ ] **Step 4: Commit**

```bash
git add -A
git commit -m "refactor(universe): cell ID strings now use 'cell_X_Y' prefix

CellID.NodeID() output changes from 'node_0_0' to 'cell_0_0' and
'node_dD_X_Y' to 'cell_dD_X_Y'. parseCellIndex updated to match.
All test fixtures updated."
```

---

### Task 6: Update log categories and documentation

**Files:**
- Modify: `pkg/universe/world_base.go` (log category constants)
- Modify: `CLAUDE.md`
- Modify: `pkg/mmokit/README.md`
- Modify: `internal/game/README.md`

- [ ] **Step 1: Update log category strings**

In `pkg/universe/world_base.go` (or wherever `CatMeshNode` is defined):
- `CatMeshNode = "mesh:node"` → `CatMeshCell = "mesh:cell"`

Search for all references to `CatMeshNode` and update to `CatMeshCell`:
- `pkg/universe/cell.go` (was node.go)
- `pkg/universe/coordinator.go`
- `pkg/universe/partition.go`
- `pkg/universe/partition_monitor.go`
- `pkg/universe/cell_bridge_impl.go`

- [ ] **Step 2: Update CLAUDE.md**

In `CLAUDE.md`, update references to the old vocabulary:
- "Node" → "Cell" where it refers to the simulation unit
- `NodeBridge` → `Bridge`
- `NodeMessage` → `CellMessage`
- `coord.Nodes` → `coord.Cells`
- `NodeViewer` → `CellViewer`
- Add `Host` to the vocabulary where `Node` was used as a process-level concept
- The `### Server Meshing` section needs the most attention

Do NOT update references to "Node" that refer to the future distributed `Host` concept — those should say "Host".

- [ ] **Step 3: Update package READMEs**

In `pkg/mmokit/README.md`:
- `Node` → `Cell` in struct/type references
- `NodeBridge` → `Bridge`
- `Coordinator.Nodes` → `Coordinator.Cells`

In `internal/game/README.md`:
- Any `Node` references in the meshing context

- [ ] **Step 4: Run tests + vet**

```bash
go vet ./... && go test -count=1 ./...
```

Expected: ALL PASS.

- [ ] **Step 5: Commit**

```bash
git add -A
git commit -m "docs: update CLAUDE.md and READMEs for Cell vocabulary

Log category CatMeshNode -> CatMeshCell. Documentation updated to
reflect the Cell/Host/Coordinator vocabulary from the distributed
mesh spec."
```

---

### Task 7: Introduce Host struct

This is the only *new code* in S1. Everything before was rename; this task adds the `Host` container.

**Files:**
- Create: `pkg/universe/host.go`
- Modify: `pkg/universe/coordinator.go`
- Test: `pkg/universe/host_test.go`

- [ ] **Step 1: Write the Host struct and test**

Create `pkg/universe/host_test.go`:

```go
package universe

import "testing"

func TestHost_AddRemoveCell(t *testing.T) {
	h := NewHost("host-01")
	if h.ID != "host-01" {
		t.Fatalf("ID = %q, want host-01", h.ID)
	}
	if len(h.Cells) != 0 {
		t.Fatal("new host should have zero cells")
	}

	cell := &Cell{ID: "cell_0_0"}
	h.AddCell(CellID{X: 0, Y: 0}, cell)
	if len(h.Cells) != 1 {
		t.Fatalf("after AddCell: len = %d, want 1", len(h.Cells))
	}
	if h.Cells[CellID{X: 0, Y: 0}] != cell {
		t.Fatal("cell not found by CellID key")
	}

	h.RemoveCell(CellID{X: 0, Y: 0})
	if len(h.Cells) != 0 {
		t.Fatal("after RemoveCell: should be empty")
	}
}

func TestHost_IsLocal(t *testing.T) {
	h := NewHost("host-01")
	if !h.IsLocal("host-01") {
		t.Fatal("should be local for own ID")
	}
	if h.IsLocal("host-02") {
		t.Fatal("should not be local for other ID")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test -run TestHost ./pkg/universe/ -v
```

Expected: FAIL — `NewHost` undefined.

- [ ] **Step 3: Implement Host struct**

Create `pkg/universe/host.go`:

```go
package universe

import "github.com/zenion/mmokit/pkg/logger"

// Host is a process-level container that owns N Cells. In a distributed
// mesh, each OS process runs one Host with one or more Cells. In the
// default colocated mode, one Host owns all cells.
//
// Host holds shared resources (logger, future gRPC endpoint, metrics
// registry) and provides cell lookup by CellID.
type Host struct {
	// ID is a stable process-scoped identifier. Set via --host-id flag
	// or auto-generated UUID. Does not change across cell migrations.
	ID string

	// Cells maps CellID to the Cell running on this host.
	Cells map[CellID]*Cell

	// Log is the shared logger for all cells on this host.
	Log *logger.Logger
}

// NewHost creates a Host with the given ID and no cells.
func NewHost(id string) *Host {
	return &Host{
		ID:    id,
		Cells: make(map[CellID]*Cell),
	}
}

// AddCell registers a cell on this host.
func (h *Host) AddCell(cellID CellID, cell *Cell) {
	h.Cells[cellID] = cell
}

// RemoveCell unregisters a cell from this host.
func (h *Host) RemoveCell(cellID CellID) {
	delete(h.Cells, cellID)
}

// IsLocal reports whether the given hostID matches this host.
func (h *Host) IsLocal(hostID string) bool {
	return h.ID == hostID
}

// CellCount returns the number of cells on this host.
func (h *Host) CellCount() int {
	return len(h.Cells)
}
```

- [ ] **Step 4: Run test to verify it passes**

```bash
go test -run TestHost ./pkg/universe/ -v
```

Expected: PASS.

- [ ] **Step 5: Wire Host into Coordinator**

In `pkg/universe/coordinator.go`, add a `Hosts` map alongside the existing `Cells` map:

```go
type Coordinator struct {
	Cells     map[string]*Cell    // cellID string -> Cell (existing, renamed from Nodes)
	CellOwner map[CellID]string   // cell -> hostID (renamed from NodeOwner)
	Hosts     map[string]*Host    // hostID -> Host (NEW)
	Topology  Topology
	// ... rest unchanged
}
```

In `NewCoordinator`, initialize: `Hosts: make(map[string]*Host)`.

In `Build()` (or wherever cells are created), create a default Host:

```go
// Create default host for colocated mode.
defaultHost := NewHost("local")
defaultHost.Log = c.Log
c.Hosts["local"] = defaultHost
```

After creating each cell, register it on the default host:

```go
defaultHost.AddCell(cell.CellID, cell)
```

This is additive — the existing `c.Cells` map continues to work. The Host is just a new layer on top.

- [ ] **Step 6: Run full test suite**

```bash
go vet ./... && go test -count=1 ./...
```

Expected: ALL PASS. Host is additive, no existing behavior changed.

- [ ] **Step 7: Commit**

```bash
git add -A
git commit -m "feat(universe): introduce Host struct for 1:N cell hosting

Host is a process-level container that owns N Cells. In colocated mode
(default), one Host named 'local' owns all cells. Coordinator.Hosts
map tracks registered hosts. This is the foundation for distributed
multi-host meshing — future work adds gRPC-backed Hosts that own cells
on remote processes."
```

---

### Task 8: Build + examples smoke test

**Files:** none (verification only)

- [ ] **Step 1: Build server binary**

```bash
just build
```

Expected: SUCCESS.

- [ ] **Step 2: Build examples**

```bash
go build ./examples/4node-basic/
go build ./examples/simple/
go build ./examples/slither/
```

Expected: SUCCESS for all.

- [ ] **Step 3: Run 4node-basic briefly**

```bash
cd examples/4node-basic && go run . &
sleep 3 && kill %1
```

Expected: server starts, logs show "cell_0_0", "cell_1_0", "cell_0_1", "cell_1_1" in log prefixes (not "node_"). No panics.

- [ ] **Step 4: Run space game briefly**

```bash
just build && timeout 5 ./bin/server || true
```

Expected: server starts, logs show cell_ prefixes. Exit after 5s timeout.

Note: this may fail if the BoltDB lock is held by another process. Kill any running servers first.

- [ ] **Step 5: Run full test suite one final time**

```bash
go vet ./... && go test -count=1 ./...
```

Expected: ALL PASS.

- [ ] **Step 6: Final commit (if any stragglers)**

If Steps 1-5 revealed any missed renames or issues, fix and commit:

```bash
git add -A
git commit -m "fix: address remaining Node->Cell rename stragglers found during smoke test"
```

---

### Task 9: Update memory files and merge

**Files:**
- Modify: memory files if any reference Node vocabulary

- [ ] **Step 1: Check memory files for stale Node references**

```bash
grep -r "Node" ~/.claude/projects/-home-josh-projects-zenion-mmoserver/memory/ 2>/dev/null | grep -v "NodeHost\|CellNode" || echo "No stale refs"
```

Update any memory files that reference the old vocabulary.

- [ ] **Step 2: Merge to main**

```bash
git checkout main
git merge --no-ff feature/S1-rename-cell-host -m "Merge branch 'feature/S1-rename-cell-host'

S1: Rename Node->Cell + introduce Host container. Establishes the
distributed mesh vocabulary: Cell is the minimal simulation unit
(one CellID, one ECS world, one goroutine), Host is the process-level
container (owns N Cells). Zero behavioral changes — pure structural
refactor that is the foundation for S2 (handoff wiring) and beyond."
```

- [ ] **Step 3: Delete feature branch**

```bash
git branch -d feature/S1-rename-cell-host
```

- [ ] **Step 4: Post-merge verification**

```bash
go vet ./... && go test -count=1 ./... && just build
```

Expected: ALL GREEN on main.

---

## Verification Checklist

After completing all tasks, verify:

- [ ] `go vet ./...` clean
- [ ] `go test -count=1 ./...` all pass
- [ ] `just build` produces `bin/server`
- [ ] `go build ./examples/4node-basic/` succeeds
- [ ] `go build ./examples/simple/` succeeds
- [ ] No remaining references to `Node` as a type name in `pkg/universe/` (except comments about the rename)
- [ ] No remaining `"node_X_Y"` string literals in source code (except docs/specs/plans)
- [ ] Log output shows `"cell_0_0"` style prefixes, not `"node_0_0"`
- [ ] `Coordinator.Hosts` map exists and is populated in `Build()`
- [ ] `Host.IsLocal()` works correctly
