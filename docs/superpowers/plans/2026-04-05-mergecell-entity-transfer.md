# MergeCell Entity Transfer Fix

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix MergeCell so entities from non-survivor nodes are transferred to the survivor, and the survivor's WorldBase is updated to reflect the parent cell identity.

**Architecture:** Mirror SplitCell's entity drain pattern for non-survivor nodes. Fix UpdateCellBounds to not incorrectly remap positions for subcell→parent transitions (entities use base-cell coordinates). Schedule the cell identity update on the survivor's game loop.

**Tech Stack:** Go, Ark ECS, existing universe package patterns

---

### Task 1: Fix UpdateCellBounds to handle base-cell coordinates correctly

The current `UpdateCellBounds` computes position offsets using subcell origins, which is wrong because entities always use base-cell (depth-0) coordinates. Position remapping should only happen when the root cell changes.

**Files:**
- Modify: `pkg/universe/world_base.go:414-465`
- Test: `pkg/universe/world_base_test.go` (new)

- [ ] **Step 1: Write failing test for UpdateCellBounds with subcell→parent**

Create `pkg/universe/world_base_test.go`:

```go
package universe

import (
	"testing"

	"github.com/mlange-42/ark/ecs"
	"github.com/mmokit/mmokit/pkg/component"
	"github.com/mmokit/mmokit/pkg/coords"
	"github.com/mmokit/mmokit/pkg/engine"
	"github.com/mmokit/mmokit/pkg/logger"
	"github.com/mmokit/mmokit/pkg/net"
)

func TestUpdateCellBounds_SubcellToParent_NoPositionShift(t *testing.T) {
	coords.SetCellSize(8192)
	log := logger.New()
	eng := engine.New(engine.DefaultConfig(), net.NewConnManager(), log)

	// Survivor is subcell {X:1, Y:0, Depth:1} — the RIGHT half of root {0,0}.
	// Entities have base-cell positions: e.g. (5000, 3000) which is valid
	// in the right subcell's LocalBounds [4096, 8192) x [0, 4096).
	subcell := CellID{X: 1, Y: 0, Depth: 1}
	base := NewWorldBase(eng, subcell, 3000, nil)

	// Spawn a test entity with a known position
	posMap := ecs.NewMap2[component.Position, component.CellCoord](eng.ECS)
	entity := posMap.NewEntity(&component.Position{X: 5000, Y: 3000}, &component.CellCoord{CellX: 0, CellY: 0})

	// Merge subcell into parent {X:0, Y:0, Depth:0}
	parent := CellID{X: 0, Y: 0, Depth: 0}
	base.UpdateCellBounds(parent, coords.CellSize)

	// Cell identity should be updated
	if base.Cell() != parent {
		t.Errorf("cell = %v, want %v", base.Cell(), parent)
	}
	if base.NodeID() != MeshNodeID(parent) {
		t.Errorf("nodeID = %s, want %s", base.NodeID(), MeshNodeID(parent))
	}

	// Position should NOT have changed — entities use base-cell coords
	pos := posMap.Get(entity)
	if pos.X != 5000 || pos.Y != 3000 {
		t.Errorf("position = (%.0f, %.0f), want (5000, 3000) — positions should not shift during same-root-cell merge", pos.X, pos.Y)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd . && go test ./pkg/universe/ -run TestUpdateCellBounds_SubcellToParent -v`

Expected: FAIL — position will be (9096, 3000) because the old code incorrectly shifts by subcell origin delta.

- [ ] **Step 3: Fix UpdateCellBounds to skip position remapping for same-root-cell changes**

In `pkg/universe/world_base.go`, replace the `UpdateCellBounds` method (lines 414-465):

```go
// UpdateCellBounds updates the cell identity and coordinate bounds for this world.
// Called from the game loop during dynamic cell split/merge operations.
//
// Entities always use base-cell (depth-0) coordinates, so position remapping
// is only needed when the root cell changes (cross-root transfers). For subcell
// depth changes within the same root cell (split/merge), only the cell identity
// and node ID are updated.
func (b *WorldBase) UpdateCellBounds(cell CellID, cellSize float32) {
	oldCell := b.cell
	b.cell = cell
	b.nodeID = MeshNodeID(cell)

	// Check if root cell changed — only then do positions need remapping.
	oldRoot := oldCell
	for oldRoot.Depth > 0 {
		oldRoot = oldRoot.Parent()
	}
	newRoot := cell
	for newRoot.Depth > 0 {
		newRoot = newRoot.Parent()
	}

	if oldRoot != newRoot {
		dx := float32(oldRoot.X-newRoot.X) * cellSize
		dy := float32(oldRoot.Y-newRoot.Y) * cellSize

		if dx != 0 || dy != 0 {
			filter := ecs.NewFilter1[component.Position](b.eng.ECS).
				Without(ecs.C[component.Ghost](), ecs.C[component.Replica](), ecs.C[component.Proxy]())
			query := filter.Query()
			for query.Next() {
				entity := query.Entity()
				pos := b.posMap.Get(entity)
				pos.X += dx
				pos.Y += dy
				if b.cellMap.HasAll(entity) {
					cc := b.cellMap.Get(entity)
					cc.CellX = newRoot.X
					cc.CellY = newRoot.Y
				}
			}
		}
	}

	// Notify connected players about the cell change
	if b.onCellBoundsChanged != nil {
		playerFilter := ecs.NewFilter1[component.PlayerConn](b.eng.ECS).
			Without(ecs.C[component.Ghost](), ecs.C[component.Replica]())
		pq := playerFilter.Query()
		for pq.Next() {
			pc := b.playerMap.Get(pq.Entity())
			if pc.ConnID != 0 {
				b.onCellBoundsChanged(pc.ConnID)
			}
		}
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd . && go test ./pkg/universe/ -run TestUpdateCellBounds_SubcellToParent -v`

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add pkg/universe/world_base.go pkg/universe/world_base_test.go
git commit -m "fix: UpdateCellBounds skips position remap for same-root-cell changes"
```

---

### Task 2: Add entity drain from non-survivor nodes in MergeCell

Before shutting down non-survivor nodes, serialize all their entities on their game loops and collect transfer data. This mirrors the `SplitCell` drain pattern.

**Files:**
- Modify: `pkg/universe/partition.go:297-410`

- [ ] **Step 1: Add entity drain before topology update in MergeCell**

In `pkg/universe/partition.go`, replace the `MergeCell` function (lines 297-410) with:

```go
func (c *Coordinator) MergeCell(cell CellID, bypassCooldown bool) error {
	pc := c.cfg.DynamicPartitioning
	if pc == nil {
		return fmt.Errorf("dynamic partitioning is not enabled")
	}

	if cell.Depth == 0 {
		return fmt.Errorf("cannot merge depth-0 cells")
	}

	siblings := cell.Siblings()

	// Validate under read lock
	c.mu.RLock()
	for _, s := range siblings {
		if _, ok := c.NodeOwner[s]; !ok {
			c.mu.RUnlock()
			return fmt.Errorf("sibling cell %s does not exist — cannot merge partial split", s)
		}
	}
	c.mu.RUnlock()

	if !bypassCooldown && c.partState != nil {
		for _, s := range siblings {
			if c.partState.onCooldown(s) {
				return fmt.Errorf("cell %s is on cooldown", s)
			}
		}
	}

	parent := cell.Parent()
	c.Log.Log(CatMeshNode, "coordinator: merging cells %v into parent %s", siblings, parent)

	// Find survivor (most entities)
	survivorIdx := 0
	maxEntities := 0
	for i, s := range siblings {
		nID := c.getNodeOwner(s)
		if snap, ok := c.NodeLoad(nID); ok {
			total := snap.Entities.Real + snap.Entities.Players
			if total > maxEntities {
				maxEntities = total
				survivorIdx = i
			}
		}
	}

	// Step 2: Drain entities from non-survivor nodes.
	// Run serialization closures on each non-survivor's game loop.
	type entityTransfer struct {
		data   []byte
		netID  uint32
		connID uint32
	}

	nonSurvivorIDs := make([]string, 0, 3)
	for i, s := range siblings {
		if i == survivorIdx {
			continue
		}
		nonSurvivorIDs = append(nonSurvivorIDs, c.getNodeOwner(s))
	}

	allTransfers := make([]entityTransfer, 0)

	for _, nID := range nonSurvivorIDs {
		node := c.Nodes[nID]
		if node == nil {
			continue
		}

		transfersCh := make(chan []entityTransfer, 1)
		node.Engine.PendingAdminCmds <- func() {
			netIDMap := ecs.NewMap1[component.NetworkID](node.Engine.ECS)
			playerMap := ecs.NewMap1[component.PlayerConn](node.Engine.ECS)

			filter := ecs.NewFilter1[component.Position](node.Engine.ECS).
				Without(ecs.C[component.Ghost](), ecs.C[component.Replica](), ecs.C[component.Proxy]())

			var transfers []entityTransfer
			query := filter.Query()
			for query.Next() {
				entity := query.Entity()

				data, err := node.World.SerializeEntity(entity)
				if err != nil {
					continue
				}

				var netID uint32
				if netIDMap.HasAll(entity) {
					netID = netIDMap.Get(entity).ID
				}
				var connID uint32
				if playerMap.HasAll(entity) {
					connID = playerMap.Get(entity).ConnID
				}

				transfers = append(transfers, entityTransfer{
					data: data, netID: netID, connID: connID,
				})

				c.Log.Log(CatMeshNode, "  merge drain: netID=%d from %s", netID, nID)
			}

			// Migrate player sessions on source node
			for _, t := range transfers {
				if t.connID != 0 {
					if sess := node.Engine.Players.ByConnID(t.connID); sess != nil {
						node.Engine.Players.Transition(sess, engine.StateTransferring)
						node.Engine.Players.Remove(sess)
					}
				}
			}

			transfersCh <- transfers
		}

		select {
		case transfers := <-transfersCh:
			allTransfers = append(allTransfers, transfers...)
		case <-time.After(5 * time.Second):
			c.Log.Log(CatMeshNode, "coordinator: timeout draining entities from %s during merge", nID)
		}
	}

	// Step 3: Update topology and routing under write lock.
	c.mu.Lock()

	survivor := c.Nodes[c.NodeOwner[siblings[survivorIdx]]]
	oldSurvivorID := survivor.ID
	newSurvivorID := MeshNodeID(parent)

	// Rename survivor to parent
	delete(c.Nodes, oldSurvivorID)
	delete(c.NodeOwner, siblings[survivorIdx])
	survivor.ID = newSurvivorID
	survivor.Cell = parent
	c.Nodes[newSurvivorID] = survivor
	c.NodeOwner[parent] = newSurvivorID

	if survivor.Metrics != nil {
		survivor.Metrics.SetNodeID(newSurvivorID)
	}

	// Collect non-survivor nodes and remove from maps
	nonSurvivorNodes := make([]*Node, 0, 3)
	for i, s := range siblings {
		if i == survivorIdx {
			continue
		}
		nID := c.NodeOwner[s]
		if node, ok := c.Nodes[nID]; ok {
			nonSurvivorNodes = append(nonSurvivorNodes, node)
			delete(c.Nodes, nID)
		}
		delete(c.NodeOwner, s)
	}

	c.Topology.UpdateAfterMerge(siblings, parent, coords.CellSize)
	c.rewireNeighbors()

	// Remap player routing — survivor's old ID AND all non-survivor players
	for connID, nID := range c.playerNode {
		if nID == oldSurvivorID {
			c.playerNode[connID] = newSurvivorID
			continue
		}
		for _, nsID := range nonSurvivorIDs {
			if nID == nsID {
				c.playerNode[connID] = newSurvivorID
				break
			}
		}
	}

	c.mu.Unlock()

	// Step 4: Update survivor's WorldBase cell identity on its game loop.
	doneCh := make(chan struct{}, 1)
	survivor.Engine.PendingAdminCmds <- func() {
		survivor.World.UpdateCellBounds(parent, coords.CellSize)
		doneCh <- struct{}{}
	}

	select {
	case <-doneCh:
	case <-time.After(5 * time.Second):
		c.Log.Log(CatMeshNode, "coordinator: timeout updating cell bounds on survivor %s", newSurvivorID)
	}

	// Step 5: Deliver drained entities to survivor's inbox.
	for _, t := range allTransfers {
		survivor.Inbox <- NodeMessage{
			Type:          MsgTransfer,
			FromNodeID:    "merge",
			TransferNetID: t.netID,
			Transfer:      t.data,
		}
	}

	// Remap player connections to survivor node
	for _, t := range allTransfers {
		if t.connID != 0 {
			c.playerNode[t.connID] = newSurvivorID
		}
	}

	// Step 6: Shut down non-survivor nodes and release resources.
	for _, node := range nonSurvivorNodes {
		node.Shutdown()
		c.netIDAlloc.Release(node.Engine.NetIDBase())
		c.Log.Log(CatMeshNode, "coordinator: shut down merged node %s", node.ID)
	}

	if c.partState != nil {
		c.partState.setCooldown(parent, pc.Cooldown)
		for _, s := range siblings {
			c.partState.clearCooldown(s)
		}
	}

	c.Log.Log(CatMeshNode, "coordinator: merge complete — %v -> %s (transferred %d entities)", siblings, parent, len(allTransfers))

	if pc.OnTopologyChanged != nil {
		pc.OnTopologyChanged()
	}

	return nil
}
```

- [ ] **Step 2: Verify compilation**

Run: `cd . && go vet ./pkg/universe/`

Expected: No errors.

- [ ] **Step 3: Run existing partition tests**

Run: `cd . && go test ./pkg/universe/ -run "TestMerge|TestSplitMerge" -v`

Expected: All existing tests pass.

- [ ] **Step 4: Commit**

```bash
git add pkg/universe/partition.go
git commit -m "fix: MergeCell drains entities from non-survivor nodes and updates survivor cell identity"
```

---

### Task 3: Run full test suite

- [ ] **Step 1: Run all universe package tests**

Run: `cd . && go test ./pkg/universe/ -v`

Expected: All tests pass.

- [ ] **Step 2: Run go vet on the full project**

Run: `cd . && go vet ./...`

Expected: No errors.
