package universe

import (
	"context"
	"fmt"
	"time"
)

// ═══════════════════════════════════════════════════════════════════════════
// Process.applyCellTransferCommit (S7-T9 atomic topology commit)
//
// Atomic-commit hook the orchestrator calls after all Ready responses
// arrive. Every commit variant reconciles cellToHostMap, HostRegistry.OwnedCells,
// Topology.Neighbors, the per-cell BorderDispatcher wiring, the sessionRoutes
// table, and finally broadcasts a fresh PeerList to every registered host
// and gateway:
//
//   - split: the parent cell is deleted from c.Cells / c.CellOwner and
//     shut down; c.Control.Topology.UpdateAfterSplit + RebuildNeighborsFor rewires
//     neighbors incrementally; partition cooldowns are primed on each child;
//     OnTopologyChanged fires after the write lock is released.
//   - merge: the survivor sibling (req.commands[*].DestCellID, all
//     commands share it) is renamed in place to the parent cell ID, its
//     WorldBase bounds are updated on the game loop, the three donor
//     cells are removed and shut down, c.Control.Topology.UpdateAfterMerge +
//     RebuildNeighborsFor rewires neighbors incrementally, in-flight
//     session routes pointed at the old siblings are remapped to the
//     parent, per-session UpstreamSwitch notifications fire, partition
//     cooldowns are primed on the parent and cleared on the siblings,
//     and OnTopologyChanged fires after the write lock is released.
//   - migrate: the source cell is torn down on its old host (Host.Cells
//     entry removed, game loop shut down, netID range released), all
//     in-flight sessions for that cell are atomically remapped to the
//     new host via sessionRoutes.remapHostCell, and per-session
//     UpstreamSwitch notifications fire so every client's gateway
//     starts routing input to the new authoritative host. Topology
//     neighbor adjacency doesn't change (same CellID, same depth) so
//     we skip the border-dispatcher rewire entirely.
//
// Orchestrator unit tests that don't go through Build() have empty
// c.Cells / c.Control.Topology.Neighbors — the split/merge/migrate paths degrade
// gracefully to the cellToHostMap-only behavior they used to depend on.
// ═══════════════════════════════════════════════════════════════════════════

// applyCellTransferCommit dispatches to the per-kind commit helper.
func (c *Process) applyCellTransferCommit(req *CellTransferRequest) {
	switch req.Kind {
	case CellTransferSplit:
		c.applySplitCommit(req)
	case CellTransferMerge:
		c.applyMergeCommit(req)
	case CellTransferMigrate:
		c.applyMigrateCommit(req)
	default:
		// Unknown kind — just mutate cellToHostMap defensively. No
		// live-state reconciliation is possible without knowing the
		// intended semantics.
		c.mu.Lock()
		c.Control.mu.Lock()
		for _, k := range req.mutation.remove {
			delete(c.Control.cellToHostMap, k)
		}
		for k, v := range req.mutation.add {
			c.Control.cellToHostMap[k] = v
		}
		c.Control.mu.Unlock()
		c.mu.Unlock()
	}
}

// snapshotOwnershipLocked captures the pre-mutation owner of every cell the
// request touches. The primary source of truth is req.commands[].SrcHostID,
// which BeginSplit/BeginMerge/BeginMigrate populate via HostForCellID (which
// unifies hostRegistry + cellToHostMap). cellToHostMap alone is insufficient
// on pure-coordinator processes where remote-host cell ownership lives only
// in hostRegistry and cellToHostMap is never populated for those cells.
//
// Caller must hold c.mu. Used by commit helpers to pass pre-mutation
// ownership to applyRegistryDelta (and, for migrate, to find the source
// host for CellRelease dispatch).
func (c *Process) snapshotOwnershipLocked(req *CellTransferRequest) map[string]string {
	out := make(map[string]string, len(req.mutation.remove)+len(req.mutation.add))
	// Authoritative source: whatever the orchestrator recorded at Begin*
	// time. Overwrites on repeated commands are fine — every command
	// targeting the same SrcCellID carries the same SrcHostID by
	// construction (SPLIT: one parent; MERGE: one donor per command;
	// MIGRATE: one cell total).
	for _, cmd := range req.commands {
		if cmd.SrcCellID != "" && cmd.SrcHostID != "" {
			out[cmd.SrcCellID] = cmd.SrcHostID
		}
	}
	// Fallback to cellToHostMap for any mutation key not covered by
	// commands (e.g. destination cells in SPLIT/MERGE, which have no
	// pre-mutation owner but are still listed in mutation.add).
	c.Control.mu.RLock()
	defer c.Control.mu.RUnlock()
	for _, k := range req.mutation.remove {
		if _, ok := out[k]; !ok {
			out[k] = c.Control.cellToHostMap[k]
		}
	}
	for k := range req.mutation.add {
		if _, ok := out[k]; !ok {
			out[k] = c.Control.cellToHostMap[k]
		}
	}
	return out
}

// applySplitCommit reconciles Process state after a SPLIT request
// reaches commit. The executor has already created each child cell and
// populated it on its target host; this method removes the parent cell,
// rewires the topology so readers see the post-split layout, remaps any
// in-flight session routes off the parent key, reconciles the HostRegistry,
// and broadcasts a fresh PeerList.
//
// The work itself is expressed as a CommitPlan in buildSplitPlan; this
// method is a thin dispatcher. Entry/exit CheckInvariants are provided
// by ExecuteCommitPlan.
func (c *Process) applySplitCommit(req *CellTransferRequest) {
	if err := c.ExecuteCommitPlan(buildSplitPlan(c, req)); err != nil {
		c.Log.Log(CatMeshCell, "applySplitCommit: %v", err)
	}
}

// applyMigrateCommit reconciles Process state after a MIGRATE request
// reaches commit. The executor has already created a fresh *Cell on the
// destination host and populated it with every live entity; this method
// removes the source cell from the old host's Host.Cells + coord.Cells
// maps, shuts down its game loop, releases its NetID range, atomically
// remaps every session route pointing at the cell to the new host via
// a single bulk remapHostCell call (bumping epochs in one shot), and
// fires targeted UpstreamSwitch notifications so clients' gateways
// route subsequent input to the new authoritative host.
//
// Fixes the T6 TODO-S7-T9 leak: prior to this helper, the migrate commit
// only flipped cellToHostMap and left the source cell's game loop
// running forever on the leaving host. Graceful-leave drains and admin
// `cell migrate` now both free the full 20Hz loop + NetID range on commit.
func (c *Process) applyMigrateCommit(req *CellTransferRequest) {
	c.CheckInvariants(defaultInvariants, fmt.Sprintf("commit %d entry (%s)", req.ID, req.Kind))
	srcCellID := req.SrcCell
	srcCellKey := MeshCellID(srcCellID)

	// The migrate mutation has exactly one add entry for srcCellKey
	// pointing at destHost, and no removes (migrate is in-place).
	destHost := req.mutation.add[srcCellKey]

	c.mu.Lock()
	preOwnership := c.snapshotOwnershipLocked(req)
	srcHost := preOwnership[srcCellKey]

	// Apply the ownership flip first so readers see the post-migrate
	// state consistently with the tear-down that follows.
	c.Control.mu.Lock()
	c.Control.cellToHostMap[srcCellKey] = destHost
	c.Control.mu.Unlock()

	// Capture the source *Cell (if this process owns the host locally) so
	// we can release its netID range after teardown. The actual Host.Cells
	// entry removal + Shutdown() is owned by hostProxy.ReleaseCell below:
	// pre-removing the host entry here would cause localHostOps.ReleaseCell
	// to fail its CellByID lookup with "unknown cell", silently skipping
	// cell.Shutdown() and leaking a zombie 20Hz game loop. Remote hosts
	// have no entry in c.Hosts, so srcCell stays nil and the netID release
	// path is correctly skipped (the remote host owns its own netID alloc).
	var srcCell *Cell
	if oldHost, ok := c.Hosts[srcHost]; ok && oldHost != nil {
		srcCell = oldHost.CellByCellID(srcCellID)
	}
	// If coord.Cells[srcCellKey] still resolves to the OLD cell (e.g.
	// because Receive's createNode hasn't overwritten it yet), swap in
	// the new one from the destination host so external readers see
	// the post-commit cell.
	if newHostObj, ok := c.Hosts[destHost]; ok && newHostObj != nil {
		if newCell := newHostObj.CellByCellID(srcCellID); newCell != nil {
			c.Cells[srcCellKey] = newCell
			c.CellOwner[srcCellID] = srcCellKey
		}
	}
	// Neighbor topology doesn't change on migrate — same CellID, same
	// depth, same adjacency. We intentionally do NOT rewire Node.Neighbors
	// here: the cell's game loop reads that map every PostSystems tick
	// without the coord lock, and rewriting it under c.mu would race with
	// ensureBorderDispatcher. The destination cell picks up its neighbor
	// wiring via createNode -> reconcileCellNeighbors on Receive, and the
	// post-commit PeerList broadcast gets the new ownership to every
	// remote peer.
	c.mu.Unlock()

	// Atomically remap every session route on the source cell to the
	// new host. remapHostCell bumps epoch per route in one lock
	// acquisition; each affected key then gets a targeted UpstreamSwitch.
	remapResults := c.sessionRoutes.remapHostCell(func(cellID string) bool {
		return cellID == srcCellKey
	}, destHost, srcCellKey)
	for _, r := range remapResults {
		// Migrate keeps the same cellID (cell moves hosts, not ID).
		c.dispatchUpstreamSwitch(r.Key, destHost, srcCellKey, r.Epoch)
		// Register the session on the destination host's VCM so it can
		// stamp the correct epoch on outbound frames.
		c.dispatchSessionRegister(destHost, r.Key, r.Epoch, srcCellKey)
	}

	// Reconcile HostRegistry bookkeeping.
	c.applyRegistryDelta(req.mutation, preOwnership)

	// Unified teardown via hostProxy: local == direct call; remote ==
	// MeshControl with blocking HostOpAck. Holds the caller's ctx for
	// deadline control. netIDAlloc.Release happens unconditionally
	// after teardown completes.
	releaseCtx, releaseCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer releaseCancel()
	if err := c.Control.hostProxy(srcHost).ReleaseCell(releaseCtx, srcCellKey); err != nil {
		c.Log.Log(CatMeshCell, "applyMigrateCommit: ReleaseCell %s -> %s failed: %v", srcCellKey, srcHost, err)
		// Failure is logged but we don't roll back — the migrate's
		// ownership flip has already succeeded on the coord side. The
		// source host may have a leaked cell; next host restart or
		// manual intervention cleans it up. See Stage-2 TODO for
		// tighter rollback semantics if needed.
	}
	if srcCell != nil {
		c.netIDAlloc.Release(srcCell.Engine.NetIDBase())
	}

	c.broadcastPeerListIfReady()
	c.CheckInvariants(defaultInvariants, fmt.Sprintf("commit %d exit (%s)", req.ID, req.Kind))
}

// applyMergeCommit reconciles Process state after a MERGE request
// reaches commit. The executor has drained entities from the three
// donor siblings and (tried to) deliver them to the survivor; this
// method renames the survivor cell to the parent ID, tears down the
// donors, rewires topology incrementally, reconciles the HostRegistry,
// remaps in-flight session routes, fires targeted UpstreamSwitch
// notifications, and broadcasts a fresh PeerList.
//
// The work itself is expressed as a CommitPlan in buildMergePlan; this
// method is a thin dispatcher. Entry/exit CheckInvariants are provided
// by ExecuteCommitPlan.
func (c *Process) applyMergeCommit(req *CellTransferRequest) {
	if err := c.ExecuteCommitPlan(buildMergePlan(c, req)); err != nil {
		c.Log.Log(CatMeshCell, "applyMergeCommit: %v", err)
	}
}
