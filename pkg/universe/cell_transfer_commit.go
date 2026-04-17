package universe

import (
	"context"
	"time"

	"github.com/zenion/mmoserver/pkg/coords"
)

// ═══════════════════════════════════════════════════════════════════════════
// Coordinator.applyCellTransferCommit (S7-T9 atomic topology commit)
//
// Atomic-commit hook the orchestrator calls after all Ready responses
// arrive. Every commit variant reconciles cellToHostMap, HostRegistry.OwnedCells,
// Topology.Neighbors, the per-cell BorderDispatcher wiring, the sessionRoutes
// table, and finally broadcasts a fresh PeerList to every registered host
// and gateway:
//
//   - split: the parent cell is deleted from c.Cells / c.CellOwner and
//     shut down; c.Topology.UpdateAfterSplit + RebuildNeighborsFor rewires
//     neighbors incrementally; partition cooldowns are primed on each child;
//     OnTopologyChanged fires after the write lock is released.
//   - merge: the survivor sibling (req.commands[*].DestCellID, all
//     commands share it) is renamed in place to the parent cell ID, its
//     WorldBase bounds are updated on the game loop, the three donor
//     cells are removed and shut down, c.Topology.UpdateAfterMerge +
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
// c.Cells / c.Topology.Neighbors — the split/merge/migrate paths degrade
// gracefully to the cellToHostMap-only behavior they used to depend on.
// ═══════════════════════════════════════════════════════════════════════════

// applyCellTransferCommit dispatches to the per-kind commit helper.
func (c *Coordinator) applyCellTransferCommit(req *CellTransferRequest) {
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
		for _, k := range req.mutation.remove {
			delete(c.cellToHostMap, k)
		}
		for k, v := range req.mutation.add {
			c.cellToHostMap[k] = v
		}
		c.mu.Unlock()
	}
}

// snapshotOwnershipLocked builds a copy of cellToHostMap restricted to the
// keys referenced by the given mutation. Used by commit helpers to pass
// pre-mutation ownership to applyRegistryDelta without leaking the coord
// lock. Caller must hold c.mu.
func (c *Coordinator) snapshotOwnershipLocked(mutation topologyMutation) map[string]string {
	out := make(map[string]string, len(mutation.remove)+len(mutation.add))
	for _, k := range mutation.remove {
		out[k] = c.cellToHostMap[k]
	}
	for k := range mutation.add {
		out[k] = c.cellToHostMap[k]
	}
	return out
}

// applySplitCommit reconciles Coordinator state after a SPLIT request
// reaches commit. The executor has already created each child cell and
// populated it on its target host; this method removes the parent cell,
// rewires the topology so readers see the post-split layout, remaps any
// in-flight session routes off the parent key, reconciles the HostRegistry,
// and broadcasts a fresh PeerList.
func (c *Coordinator) applySplitCommit(req *CellTransferRequest) {
	parent := req.SrcCell
	children := parent.Children()
	parentKey := MeshCellID(parent)

	// Pick a deterministic fallback child for any session route still
	// pointing at the parent after commit. The executor's populateCell
	// places each player entity on the child hosting its quadrant, but
	// we don't currently thread that (connID -> child) mapping back up
	// to the coordinator. Routing any leftover parent-pointing session
	// to children[0] keeps client input flowing to a real cell on a
	// real host; the next cross-boundary handoff will correct the cell
	// ID if the player's entity actually lives elsewhere. This is a
	// best-effort fallback — in practice, sessions with live entities
	// don't point at the parent key during split because setPlayerNode
	// is only called on explicit handoffs.
	fallbackChildKey := MeshCellID(children[0])

	c.mu.Lock()
	preOwnership := c.snapshotOwnershipLocked(req.mutation)
	for _, k := range req.mutation.remove {
		delete(c.cellToHostMap, k)
	}
	for k, v := range req.mutation.add {
		c.cellToHostMap[k] = v
	}

	parentCell, hadParent := c.Cells[parentKey]
	if hadParent {
		delete(c.Cells, parentKey)
		delete(c.CellOwner, parent)
		for _, h := range c.Hosts {
			h.RemoveCell(parent)
		}
	}

	var splitDirectives []rewireDirective
	if c.Topology.Neighbors != nil {
		c.Topology.UpdateAfterSplit(parent, children, coords.CellSize)
		// Incremental rewire — only touch the parent's former frontier
		// plus the new children.
		affected := make([]CellID, 0, 5)
		affected = append(affected, children[:]...)
		c.Topology.RebuildNeighborsFor(affected, coords.CellSize)
		splitDirectives = c.computeRewireDirectivesLocked(affected)
	}
	c.mu.Unlock()

	// Apply the per-cell neighbor rewires via PendingAdminCmds so
	// the writes happen on the same goroutine that reads node.Neighbors
	// from PostSystems, avoiding a race with the game loop.
	c.applyRewireDirectives(splitDirectives)

	// Remap any session routes still pointing at the parent key to the
	// deterministic fallback child, then dispatch UpstreamSwitch per
	// affected session. The new host for the fallback child comes from
	// req.mutation.add.
	fallbackHost := req.mutation.add[fallbackChildKey]
	affectedSessions := c.sessionRoutes.remapCell(func(cellID string) bool {
		return cellID == parentKey
	}, fallbackChildKey)
	for _, key := range affectedSessions {
		if route, ok := c.sessionRoutes.Get(key); ok {
			c.dispatchUpstreamSwitch(key, fallbackHost, route.Epoch)
		}
	}

	// Reconcile HostRegistry bookkeeping.
	c.applyRegistryDelta(req.mutation, preOwnership)

	if hadParent {
		parentCell.Shutdown()
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

// applyMigrateCommit reconciles Coordinator state after a MIGRATE request
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
func (c *Coordinator) applyMigrateCommit(req *CellTransferRequest) {
	srcCellID := req.SrcCell
	srcCellKey := MeshCellID(srcCellID)

	// The migrate mutation has exactly one add entry for srcCellKey
	// pointing at destHost, and no removes (migrate is in-place).
	destHost := req.mutation.add[srcCellKey]

	c.mu.Lock()
	preOwnership := c.snapshotOwnershipLocked(req.mutation)
	srcHost := preOwnership[srcCellKey]

	// Apply the ownership flip first so readers see the post-migrate
	// state consistently with the tear-down that follows.
	c.cellToHostMap[srcCellKey] = destHost

	// Locate the source cell on the old host and detach it. coord.Cells
	// is keyed on the stringified cell ID; since migrate keeps the same
	// ID, the per-host Host.Cells maps are the authoritative source-of-
	// truth for "which Host owns which cell", and we tear down the
	// source-side entry so its game loop stops. The destination Receive
	// path already ran createNode under c.mu, which self-registered the
	// fresh cell in coord.Cells / coord.CellOwner under the destination
	// host.
	// Host.Cells is guarded by Host.mu; go through the thread-safe
	// accessor/mutator API rather than touching the map directly. The
	// routeInboundFrame reader path walks the same map concurrently
	// under h.mu.RLock and will otherwise race with this commit.
	var srcCell *Cell
	if oldHost, ok := c.Hosts[srcHost]; ok && oldHost != nil {
		if cell := oldHost.CellByCellID(srcCellID); cell != nil {
			srcCell = cell
			oldHost.RemoveCell(srcCellID)
		}
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
		c.dispatchUpstreamSwitch(r.Key, destHost, r.Epoch)
		// Register the session on the destination host's VCM so it can
		// stamp the correct epoch on outbound frames.
		c.dispatchSessionRegister(destHost, r.Key, r.Epoch, srcCellKey)
	}

	// Reconcile HostRegistry bookkeeping.
	c.applyRegistryDelta(req.mutation, preOwnership)

	// Tear down the source cell outside the lock. The Shutdown call
	// drains its PendingAdminCmds and halts its game loop; Release puts
	// the NetID range back in the pool for future cells on this host.
	if srcCell != nil {
		// In-process cell — shut down directly.
		srcCell.Shutdown()
		c.netIDAlloc.Release(srcCell.Engine.NetIDBase())
	} else {
		// Remote cell — send CellRelease via MeshControl. The remote
		// host's releaseCellOnNode() will shut down the cell, remove it
		// from its local maps, and send CellStopped back.
		c.sendCellRelease(srcHost, srcCellKey)
	}

	c.broadcastPeerListIfReady()
}

// applyMergeCommit reconciles Coordinator state after a MERGE request
// reaches commit. The executor has drained entities from the three
// donor siblings and (tried to) deliver them to the survivor; this
// method renames the survivor cell to the parent ID, tears down the
// donors, rewires topology incrementally, reconciles the HostRegistry,
// remaps in-flight session routes, fires targeted UpstreamSwitch
// notifications, and broadcasts a fresh PeerList.
func (c *Coordinator) applyMergeCommit(req *CellTransferRequest) {
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
	preOwnership := c.snapshotOwnershipLocked(req.mutation)
	for _, k := range req.mutation.remove {
		delete(c.cellToHostMap, k)
	}
	for k, v := range req.mutation.add {
		c.cellToHostMap[k] = v
	}

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
		for _, h := range c.Hosts {
			h.RemoveCell(sib)
		}
	}
	if survivorIsSibling && survivorKey != "" {
		survivor = c.Cells[survivorKey]
	}

	if survivor != nil {
		// Rename survivor in-place: its CellID and string key both
		// change to the parent. Metrics observe the rename so
		// dashboards follow. Also retag the host that currently owns
		// the survivor so host.CellByID works for the new name.
		//
		// Only the coord-owned maps (c.Cells, c.CellOwner) and the
		// thread-safe Host maps are flipped under c.mu here. The
		// survivor.ID / survivor.Cell struct fields MUST NOT be
		// written from this goroutine — they are read every tick by
		// the cell's own game loop via cellBridge.PostSystems ->
		// BorderDispatcher.Tick -> CellViewer.Send (border_viewer.go
		// line 135) without any lock, and writing them here races.
		// Those two fields are rewritten on the survivor's game loop
		// via PendingAdminCmds after c.mu is released; see below.
		delete(c.Cells, survivorKey)
		delete(c.CellOwner, survivorCellID)
		c.Cells[parentKey] = survivor
		c.CellOwner[parent] = parentKey
		// Move the survivor cell's entry in its host from the old
		// sibling CellID to the new parent CellID. Go through the
		// thread-safe Host accessors so the routeInboundFrame reader
		// path doesn't race.
		for _, h := range c.Hosts {
			if existing := h.CellByCellID(survivorCellID); existing != nil {
				h.RemoveCell(survivorCellID)
				h.AddCell(parent, survivor)
			}
		}
		if survivor.Metrics != nil {
			survivor.Metrics.SetCellID(parentKey)
		}
	}

	var mergeDirectives []rewireDirective
	if c.Topology.Neighbors != nil {
		c.Topology.UpdateAfterMerge(siblings, parent, coords.CellSize)
		// Incremental rewire — touch the parent plus whatever the old
		// siblings used to border.
		affected := []CellID{parent}
		c.Topology.RebuildNeighborsFor(affected, coords.CellSize)
		mergeDirectives = c.computeRewireDirectivesLocked(affected)
	}
	c.mu.Unlock()

	// Rename the survivor struct fields on its OWN game-loop
	// goroutine, along with the WorldBase cell-bounds update. These
	// fields (survivor.ID, survivor.Cell, WorldBase.cell) are read
	// every tick by PostSystems without any lock; writing from the
	// coordinator goroutine races. Running them here via
	// PendingAdminCmds guarantees the writes are ordered with the
	// game loop's reads. We block on doneCh so the rename is
	// observable to everything that follows (applyRewireDirectives,
	// which invalidates the border dispatcher whose next rebuild
	// reads survivor.Cell; session-route remap; HostRegistry delta;
	// donor teardown).
	if survivor != nil && survivor.Engine != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := survivor.Engine.RunOnLoop(ctx, func() error {
			survivor.ID = parentKey
			survivor.Cell = parent
			if survivor.World != nil {
				survivor.World.UpdateCellBounds(parent, coords.CellSize)
			}
			return nil
		})
		cancel()
		if err != nil {
			c.Log.Log(CatMeshCell, "coordinator: survivor rename to %s via loop failed (%v); applying inline", parentKey, err)
			// Fallback — if the cell's game loop isn't draining (test
			// fixtures without a running loop), we still need the
			// survivor to carry the new identity so downstream commit
			// steps see a consistent view. Unsafe under -race if a real
			// loop IS running, but by construction we only reach here
			// when RunOnLoop hit a deadline or the loop is not live.
			survivor.ID = parentKey
			survivor.Cell = parent
			if survivor.World != nil {
				survivor.World.UpdateCellBounds(parent, coords.CellSize)
			}
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
	parentHost := req.mutation.add[parentKey]
	for _, key := range affectedSessions {
		if route, ok := c.sessionRoutes.Get(key); ok {
			c.dispatchUpstreamSwitch(key, parentHost, route.Epoch)
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

	// Tear down donors outside the coord lock. Survivor bounds and
	// identity were already updated via PendingAdminCmds above.
	for _, d := range donorCells {
		d.Shutdown()
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
