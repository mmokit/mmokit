package universe

import (
	"context"
	"time"

	"github.com/zenion/mmoserver/pkg/coords"
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
func (c *Process) applySplitCommit(req *CellTransferRequest) {
	parent := req.SrcCell
	children := parent.Children()
	parentKey := MeshCellID(parent)

	// Each dest host's CellTransferReady carries the usernames whose
	// entities landed on that child. req.adoptedUsers is the aggregated
	// username -> destCellKey map, populated by OnReady as acks arrive.
	// Sessions for adopted users route to their actual child cell.
	// Sessions still pointing at parentKey whose username isn't in the
	// adopted set (e.g. disconnected sessions with no live entity) fall
	// back to children[0] so client input keeps flowing to a real cell;
	// the next boundary crossing fixes it if needed.
	fallbackChildKey := MeshCellID(children[0])

	c.mu.Lock()
	preOwnership := c.snapshotOwnershipLocked(req)
	c.Control.mu.Lock()
	for _, k := range req.mutation.remove {
		delete(c.Control.cellToHostMap, k)
	}
	for k, v := range req.mutation.add {
		c.Control.cellToHostMap[k] = v
	}
	c.Control.mu.Unlock()

	// Remove the parent from coord-level maps under the lock; the actual
	// Host.Cells entry + game-loop teardown is owned by hostProxy.ReleaseCell
	// below. Pre-removing the host entry here would cause the subsequent
	// localHostOps.ReleaseCell lookup to fail with "unknown cell", silently
	// skipping cell.Shutdown() and leaking a zombie 20Hz game loop that keeps
	// replicating alongside the real children.
	parentCell, hadParent := c.Cells[parentKey]
	if hadParent {
		delete(c.Cells, parentKey)
		delete(c.CellOwner, parent)
	}

	var splitDirectives []rewireDirective
	if c.Control.Topology.Neighbors != nil {
		c.Control.Topology.UpdateAfterSplit(parent, children, coords.CellSize)
		// Incremental rewire — only touch the parent's former frontier
		// plus the new children.
		affected := make([]CellID, 0, 5)
		affected = append(affected, children[:]...)
		c.Control.Topology.RebuildNeighborsFor(affected, coords.CellSize)
		splitDirectives = c.computeRewireDirectivesLocked(affected)
	}
	c.mu.Unlock()

	// Apply the per-cell neighbor rewires via PendingAdminCmds so
	// the writes happen on the same goroutine that reads node.Neighbors
	// from PostSystems, avoiding a race with the game loop.
	c.applyRewireDirectives(splitDirectives)

	// Remap sessions per-player: use the adopted-users map from the Ready
	// acks to route each session to the child that received its entity.
	// Sessions whose username isn't in the adopted set fall back to
	// children[0] (typical for disconnected sessions with no live entity).
	fallbackHost := req.mutation.add[fallbackChildKey]
	affectedSessions := c.sessionRoutes.remapCellPerRoute(func(route *SessionRoute) (string, bool) {
		if route.CellID != parentKey {
			return "", false
		}
		if destKey, ok := req.adoptedUsers[route.Username]; ok {
			return destKey, true
		}
		return fallbackChildKey, true
	})
	for _, key := range affectedSessions {
		if route, ok := c.sessionRoutes.Get(key); ok {
			destHost := req.mutation.add[route.CellID]
			if destHost == "" {
				destHost = fallbackHost
			}
			c.dispatchUpstreamSwitch(key, destHost, route.CellID, route.Epoch)
		}
	}

	// Reconcile HostRegistry bookkeeping.
	c.applyRegistryDelta(req.mutation, preOwnership)

	// Unified parent teardown via hostProxy. All split commands share
	// the same parent and SrcHostID by construction; any command's
	// SrcHostID is the parent's host.
	if len(req.commands) > 0 {
		if srcHost := req.commands[0].SrcHostID; srcHost != "" {
			releaseCtx, releaseCancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer releaseCancel()
			if err := c.Control.hostProxy(srcHost).ReleaseCell(releaseCtx, parentKey); err != nil {
				c.Log.Log(CatMeshCell, "applySplitCommit: ReleaseCell parent %s -> %s failed: %v", parentKey, srcHost, err)
			}
		}
	}
	if hadParent {
		c.netIDAlloc.Release(parentCell.Engine.NetIDBase())
	}

	if c.partState != nil && c.cfg.DynamicPartitioning != nil {
		cooldown := c.cfg.DynamicPartitioning.Cooldown
		for _, ch := range children {
			c.partState.setCooldown(ch, cooldown)
		}
	}
	if pc := c.cfg.DynamicPartitioning; pc != nil && pc.OnTopologyChanged != nil {
		pc.OnTopologyChanged()
	}

	c.broadcastPeerListIfReady()
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
}

// applyMergeCommit reconciles Process state after a MERGE request
// reaches commit. The executor has drained entities from the three
// donor siblings and (tried to) deliver them to the survivor; this
// method renames the survivor cell to the parent ID, tears down the
// donors, rewires topology incrementally, reconciles the HostRegistry,
// remaps in-flight session routes, fires targeted UpstreamSwitch
// notifications, and broadcasts a fresh PeerList.
func (c *Process) applyMergeCommit(req *CellTransferRequest) {
	parent := req.SrcCell
	siblings := parent.Children()
	parentKey := MeshCellID(parent)

	// The survivor cell key is the shared DestCellID on every command.
	// Fall back to the mutation if the command list happens to be empty
	// (unit-test paths).
	var survivorKey string
	if len(req.commands) > 0 {
		survivorKey = req.commands[0].DestCellID
	}

	c.mu.Lock()
	preOwnership := c.snapshotOwnershipLocked(req)
	c.Control.mu.Lock()
	for _, k := range req.mutation.remove {
		delete(c.Control.cellToHostMap, k)
	}
	for k, v := range req.mutation.add {
		c.Control.cellToHostMap[k] = v
	}
	c.Control.mu.Unlock()

	// Remove donors from coord-level maps under the lock; the Host.Cells
	// entry + game-loop teardown is owned by hostProxy.ReleaseCell below.
	// Pre-removing the host entry here would cause localHostOps.ReleaseCell
	// to fail its CellByID lookup with "unknown cell", silently skipping
	// cell.Shutdown() and leaking a zombie game loop.
	var survivor *Cell
	var survivorCellID CellID
	var survivorIsSibling bool
	var donorCells []*Cell
	var donorIDs []string
	for _, sib := range siblings {
		sibKey := MeshCellID(sib)
		if sibKey == survivorKey {
			survivorCellID = sib
			survivorIsSibling = true
			continue
		}
		if cell, ok := c.Cells[sibKey]; ok {
			donorCells = append(donorCells, cell)
			delete(c.Cells, sibKey)
			donorIDs = append(donorIDs, sibKey)
		}
		delete(c.CellOwner, sib)
	}
	if survivorIsSibling && survivorKey != "" {
		survivor = c.Cells[survivorKey]
	}

	// Rekey coord-level maps for a local survivor BEFORE computing rewire
	// directives below. computeRewireDirectivesLocked consults c.CellOwner
	// / c.Cells to resolve each affected CellID to a *Cell; if the parent
	// key isn't in those maps yet, both the parent's own directive and
	// every external neighbor's reference to the parent drop silently.
	// The result is a cell with an empty runtime Neighbors map — no AoI
	// replication across borders, no BoundarySystem handoffs to adjacent
	// top-level cells. hostProxy.RenameCell below re-applies the same
	// rekey (idempotent) once the Host.Cells rename lands.
	if local, ok := c.Cells[survivorKey]; ok {
		delete(c.Cells, survivorKey)
		delete(c.CellOwner, survivorCellID)
		c.Cells[parentKey] = local
		c.CellOwner[parent] = parentKey
	}

	var mergeDirectives []rewireDirective
	if c.Control.Topology.Neighbors != nil {
		c.Control.Topology.UpdateAfterMerge(siblings, parent, coords.CellSize)
		// Incremental rewire — touch the parent plus whatever the old
		// siblings used to border.
		affected := []CellID{parent}
		c.Control.Topology.RebuildNeighborsFor(affected, coords.CellSize)
		mergeDirectives = c.computeRewireDirectivesLocked(affected)
	}
	c.mu.Unlock()

	// Unified survivor rename via hostProxy. For local survivors, this
	// delegates to renameCellOnNode directly; for remote survivors, it
	// dispatches CellRename via MeshControl and blocks on HostOpAck.
	// Fixes the CRITICAL Stage-1 bug where remote survivors' identity
	// was never rewritten.
	if survivorKey != "" && survivorKey != parentKey {
		survivorHost := req.mutation.add[parentKey] // after merge, parent lives here
		if survivorHost != "" {
			renameCtx, renameCancel := context.WithTimeout(context.Background(), 5*time.Second)
			if err := c.Control.hostProxy(survivorHost).RenameCell(renameCtx, survivorKey, parentKey); err != nil {
				c.Log.Log(CatMeshCell, "applyMergeCommit: RenameCell %s -> %s on %s failed: %v", survivorKey, parentKey, survivorHost, err)
				// Log but don't fail commit — ownership flip has already
				// happened on the coord side. Future hardening could add
				// rollback here.
			}
			renameCancel()
		}
	}

	// Apply the per-cell neighbor rewires off the coord lock and on
	// each target cell's own game loop — see applyRewireDirectives.
	c.applyRewireDirectives(mergeDirectives)

	// Remap in-flight player routes for any session still pointing at
	// the survivor's old key or at one of the donor keys. Collect the
	// affected keys so we can target UpstreamSwitch dispatches without
	// a second lookup.
	var affectedSessions []SessionKey
	if len(donorIDs) > 0 || survivorKey != "" {
		affectedSessions = c.sessionRoutes.remapCell(func(cellID string) bool {
			if cellID == survivorKey {
				return true
			}
			for _, d := range donorIDs {
				if cellID == d {
					return true
				}
			}
			return false
		}, parentKey)
	}
	// After merge the parent lives on survivorHost (carried in mutation.add).
	// Sessions on any sibling/donor or the parent itself now belong on the
	// merged parent cell.
	parentHost := req.mutation.add[parentKey]
	for _, key := range affectedSessions {
		if route, ok := c.sessionRoutes.Get(key); ok {
			c.dispatchUpstreamSwitch(key, parentHost, parentKey, route.Epoch)
		}
	}

	// Reconcile HostRegistry bookkeeping.
	c.applyRegistryDelta(req.mutation, preOwnership)

	// Drain residual entities from donors to the survivor before tearing
	// them down. The initial donor serialize (via the executor) captured
	// a snapshot at one point in time; between snapshot and commit, more
	// entities may have flowed into the donor via cross-sibling handoffs.
	// Without this drain those entities die with the donor when it shuts
	// down. Run in two passes to converge: pass 1 ships any new arrivals,
	// pass 2 catches anything that arrived during pass 1.
	if survivor != nil && len(donorCells) > 0 {
		for pass := 0; pass < 2; pass++ {
			c.drainDonorResidualsToSurvivor(donorCells, survivor)
		}
	}

	// Unified donor teardown via hostProxy — local donors dispatch to
	// localHostOps (RemoveCell + cell.Shutdown synchronously); remote
	// donors go via MeshControl CellRelease. Each merge command carries
	// the donor's SrcHostID + SrcCellID. Running this before the netID
	// release ensures the local Host.Cells entry is still present when
	// localHostOps looks it up.
	for _, cmd := range req.commands {
		if cmd.Kind != CellTransferMerge || cmd.SrcCellID == "" || cmd.SrcHostID == "" {
			continue
		}
		releaseCtx, releaseCancel := context.WithTimeout(context.Background(), 10*time.Second)
		if err := c.Control.hostProxy(cmd.SrcHostID).ReleaseCell(releaseCtx, cmd.SrcCellID); err != nil {
			c.Log.Log(CatMeshCell, "applyMergeCommit: ReleaseCell donor %s -> %s failed: %v", cmd.SrcCellID, cmd.SrcHostID, err)
		}
		releaseCancel()
	}

	// Release NetID ranges for local donors (captured above before
	// hostProxy.ReleaseCell tore the cell down). Remote donors own their
	// own netID allocator and don't need release here.
	for _, d := range donorCells {
		c.netIDAlloc.Release(d.Engine.NetIDBase())
	}

	if c.partState != nil && c.cfg.DynamicPartitioning != nil {
		c.partState.setCooldown(parent, c.cfg.DynamicPartitioning.Cooldown)
		for _, sib := range siblings {
			c.partState.clearCooldown(sib)
		}
	}
	if pc := c.cfg.DynamicPartitioning; pc != nil && pc.OnTopologyChanged != nil {
		pc.OnTopologyChanged()
	}

	c.broadcastPeerListIfReady()
}
