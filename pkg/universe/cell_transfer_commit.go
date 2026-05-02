package universe

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
//     Stage bounds are updated on the game loop, the three donor
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
			delete(c.Control.cellToHostMap, MeshCellID(k))
		}
		for k, v := range req.mutation.add {
			c.Control.cellToHostMap[MeshCellID(k)] = v
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
			out[k] = c.Control.cellToHostMap[MeshCellID(k)]
		}
	}
	for k := range req.mutation.add {
		if _, ok := out[k]; !ok {
			out[k] = c.Control.cellToHostMap[MeshCellID(k)]
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
	if err := c.ExecuteCommitPlan(buildMigratePlan(c, req)); err != nil {
		c.Log.Log(CatMeshCell, "applyMigrateCommit: %v", err)
	}
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
