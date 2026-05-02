package universe

import (
	"context"
	"time"
)

// migrateNoInvariants is a sentinel Invariant slice that disables
// default-invariant checking for a single plan step without disabling
// the whole commit's entry/exit checks. It's non-empty (len > 0) so
// ExecuteCommitPlan uses it instead of defaultInvariants, but its sole
// predicate is a no-op. Used by stepMigrateApplyCoordMutation to skip
// the host-ownership-matches-coord check during the transient state
// between coord-map flip (which now points srcCellKey at destHost) and
// the later release-src-host step that actually tears down the cell on
// the source host. In that window both srcHost and destHost briefly
// reference the cell, which would otherwise trip
// invHostOwnershipMatchesCoord on the intermediate snapshot.
var migrateNoInvariants = []Invariant{{
	Name:  "migrate-intermediate-ok",
	Check: func(*Process) error { return nil },
}}

// buildMigratePlan translates a MIGRATE CellTransferRequest into a
// data-driven CommitPlan. The step list mirrors the imperative order
// preserved from the pre-B4 applyMigrateCommit body — every conditional,
// lock acquisition, error handler, and log line is carried across verbatim
// into the step functions below. See cell_transfer_commit.go for the
// rationale comment block describing what each commit variant reconciles.
func buildMigratePlan(c *Process, req *CellTransferRequest) *CommitPlan {
	srcCellID := req.SrcCell
	srcCellKey := srcCellID.MeshID()

	// The migrate mutation has exactly one add entry for srcCellKey
	// pointing at destHost, and no removes (migrate is in-place).
	destHost := req.mutation.add[srcCellKey]

	ctx := &CommitContext{
		Req:        req,
		Mutation:   req.mutation,
		SrcCellKey: srcCellKey,
		DestHost:   destHost,
	}

	return &CommitPlan{
		ID:   req.ID,
		Kind: CommitKindMigrate,
		Req:  req,
		Ctx:  ctx,
		Steps: []PlanStep{
			// apply-coord-mutation flips cellToHostMap so srcCellKey points
			// at destHost and (for local dest hosts) swaps c.Cells to the
			// new cell. The src host still owns its old cell until
			// release-src-host runs below, so both hosts briefly reference
			// srcCellKey. invHostOwnershipMatchesCoord would flag that
			// overlap — we suppress its check for this single step and let
			// the post-release step re-validate. The pre-refactor
			// applyMigrateCommit performed no mid-step invariant check
			// between these blocks; we preserve that exact behavior.
			{Name: "apply-coord-mutation", Run: stepMigrateApplyCoordMutation, Invariants: migrateNoInvariants},
			{Name: "remap-sessions", Run: stepMigrateRemapSessions},
			{Name: "apply-registry-delta", Run: stepMigrateApplyRegistryDelta},
			{Name: "release-src-host", Run: stepMigrateReleaseSrcHost},
			{Name: "release-src-netid", Run: stepMigrateReleaseSrcNetID},
			{Name: "broadcast-peer-list", Run: stepMigrateBroadcastPeerList},
		},
	}
}

// stepMigrateApplyCoordMutation performs the under-c.mu work in one atomic
// block: snapshot pre-mutation ownership, flip cellToHostMap, capture the
// local source *Cell (for later netID release), and swap c.Cells /
// c.CellOwner to the destination's new cell when this process hosts it.
//
// This step intentionally fuses multiple semantic sub-steps because they
// must run under a single c.mu.Lock()/Unlock() pair — splitting them into
// separate PlanSteps would change observable locking semantics.
func stepMigrateApplyCoordMutation(c *Process, ctx *CommitContext) error {
	req := ctx.Req
	srcCellID := req.SrcCell
	srcCellKey := ctx.SrcCellKey
	destHost := ctx.DestHost

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

	ctx.PreOwnership = preOwnership
	ctx.SrcHost = srcHost
	ctx.SrcCell = srcCell
	return nil
}

// stepMigrateRemapSessions atomically remaps every session route on the
// source cell to the new host. remapHostCell bumps epoch per route in one
// lock acquisition; each affected key then gets a targeted UpstreamSwitch
// and a SessionRegister on the destination host's VCM so it can stamp the
// correct epoch on outbound frames.
func stepMigrateRemapSessions(c *Process, ctx *CommitContext) error {
	srcCellKey := ctx.SrcCellKey
	destHost := ctx.DestHost

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
	return nil
}

// stepMigrateApplyRegistryDelta reconciles HostRegistry bookkeeping against
// the pre-mutation ownership snapshot.
func stepMigrateApplyRegistryDelta(c *Process, ctx *CommitContext) error {
	req := ctx.Req
	c.applyRegistryDelta(req.mutation, ctx.PreOwnership)
	return nil
}

// stepMigrateReleaseSrcHost tears down the source cell on the old host via
// hostProxy: local == direct call; remote == MeshControl with blocking
// HostOpAck. Holds the caller's ctx for deadline control.
func stepMigrateReleaseSrcHost(c *Process, ctx *CommitContext) error {
	srcHost := ctx.SrcHost
	srcCellKey := ctx.SrcCellKey

	// Unified teardown via hostProxy: local == direct call; remote ==
	// MeshControl with blocking HostOpAck. Holds the caller's ctx for
	// deadline control. netIDAlloc.Release happens unconditionally
	// after teardown completes.
	releaseCtx, releaseCancel := context.WithTimeout(context.Background(), 10*time.Second)
	if err := c.Control.hostProxy(srcHost).ReleaseCell(releaseCtx, srcCellKey); err != nil {
		c.Log.Log(CatMeshCell, "applyMigrateCommit: ReleaseCell %s -> %s failed: %v", srcCellKey, srcHost, err)
		// Failure is logged but we don't roll back — the migrate's
		// ownership flip has already succeeded on the coord side. The
		// source host may have a leaked cell; next host restart or
		// manual intervention cleans it up. See Stage-2 TODO for
		// tighter rollback semantics if needed.
	}
	releaseCancel()
	return nil
}

// stepMigrateReleaseSrcNetID releases the source cell's NetID range when
// the source host was local to this process (captured by
// stepMigrateApplyCoordMutation before hostProxy.ReleaseCell tore it
// down). Remote source hosts own their own netID allocator and don't need
// release here.
func stepMigrateReleaseSrcNetID(c *Process, ctx *CommitContext) error {
	if ctx.SrcCell != nil {
		c.netIDAlloc.Release(ctx.SrcCell.Engine.NetIDBase())
	}
	return nil
}

// stepMigrateBroadcastPeerList pushes a fresh PeerList to every registered
// host and gateway so they observe the new cell-to-host ownership.
func stepMigrateBroadcastPeerList(c *Process, ctx *CommitContext) error {
	c.broadcastPeerListIfReady()
	return nil
}
