package universe

import (
	"context"
	"time"

	"github.com/zenion/mmoserver/pkg/coords"
)

// mergeNoInvariants is a sentinel Invariant slice that disables
// default-invariant checking for a single plan step without disabling
// the whole commit's entry/exit checks. It's non-empty (len > 0) so
// ExecuteCommitPlan uses it instead of defaultInvariants, but its sole
// predicate is a no-op. Used by stepMergeApplyCoordMutation to skip
// the coord-maps-consistent / host-ownership-matches-coord checks
// during the transient state between coord-map rekey and host-side
// rename (see stepMergeApplyCoordMutation's comment for details).
var mergeNoInvariants = []Invariant{{
	Name:  "merge-intermediate-ok",
	Check: func(*Process) error { return nil },
}}

// buildMergePlan translates a MERGE CellTransferRequest into a data-driven
// CommitPlan. The step list mirrors the imperative order preserved from
// the pre-B3 applyMergeCommit body — every conditional, lock acquisition,
// error handler, and log line is carried across verbatim into the step
// functions below. See cell_transfer_commit.go for the rationale comment
// block describing what each commit variant reconciles.
func buildMergePlan(c *Process, req *CellTransferRequest) *CommitPlan {
	parent := req.SrcCell
	parentKey := parent.MeshID()

	// The survivor cell key is the shared DestCellID on every command.
	// Fall back to the mutation if the command list happens to be empty
	// (unit-test paths).
	var survivorKey MeshCellID
	if len(req.commands) > 0 {
		survivorKey = req.commands[0].DestCellID
	}

	ctx := &CommitContext{
		Req:         req,
		Mutation:    req.mutation,
		ParentKey:   parentKey,
		SurvivorKey: survivorKey,
	}

	return &CommitPlan{
		ID:   req.ID,
		Kind: CommitKindMerge,
		Req:  req,
		Ctx:  ctx,
		Steps: []PlanStep{
			// apply-coord-mutation leaves the coord maps in a transient
			// state for a local survivor: c.Cells[parentKey] points at
			// the survivor cell, but the cell's own .Cell field is still
			// the sub-cell ID until rename-survivor-host runs the
			// hostProxy rename (which rewrites cell.Cell on the game
			// loop). Both invCoordMapsConsistent and
			// invHostOwnershipMatchesCoord would flag this intermediate
			// state as a violation. The pre-refactor applyMergeCommit
			// did not check invariants between these two blocks; we
			// preserve that exact behavior by suppressing invariants
			// for this step and letting the post-rename step re-validate.
			{Name: "apply-coord-mutation", Run: stepMergeApplyCoordMutation, Invariants: mergeNoInvariants},
			{Name: "rename-survivor-host", Run: stepMergeRenameSurvivorHost},
			{Name: "apply-rewire-directives", Run: stepMergeApplyRewireDirectives},
			{Name: "remap-sessions", Run: stepMergeRemapSessions},
			{Name: "apply-registry-delta", Run: stepMergeApplyRegistryDelta},
			{Name: "drain-donor-residuals", Run: stepMergeDrainDonorResiduals},
			{Name: "release-donors", Run: stepMergeReleaseDonors},
			{Name: "release-donor-netids", Run: stepMergeReleaseDonorNetIDs},
			{Name: "prime-cooldowns", Run: stepMergePrimeCooldowns},
			{Name: "broadcast-peer-list", Run: stepMergeBroadcastPeerList},
		},
	}
}

// stepMergeApplyCoordMutation performs the under-c.mu work in one atomic
// block: snapshot pre-mutation ownership, flip cellToHostMap, collect the
// donor cells and remove them from coord-level maps, resolve/rekey the
// local survivor when present, advance the topology tree, and compute the
// rewire directives that a later step applies off-lock.
//
// This step intentionally fuses multiple semantic sub-steps because they
// must run under a single c.mu.Lock()/Unlock() pair — splitting them into
// separate PlanSteps would change observable locking semantics.
func stepMergeApplyCoordMutation(c *Process, ctx *CommitContext) error {
	req := ctx.Req
	parent := req.SrcCell
	siblings := parent.Children()
	parentKey := ctx.ParentKey
	survivorKey := ctx.SurvivorKey

	c.mu.Lock()
	ctx.PreOwnership = c.snapshotOwnershipLocked(req)
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
	var donorIDs []MeshCellID
	for _, sib := range siblings {
		sibKey := sib.MeshID()
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

	ctx.Survivor = survivor
	ctx.SurvivorCellID = survivorCellID
	ctx.SurvivorIsSibling = survivorIsSibling
	ctx.DonorCells = donorCells
	ctx.DonorIDs = donorIDs
	ctx.MergeDirectives = mergeDirectives
	return nil
}

// stepMergeRenameSurvivorHost renames the survivor cell to the parent key
// on its owning host via hostProxy. For local survivors this delegates to
// renameCellOnNode directly; for remote survivors it dispatches CellRename
// via MeshControl and blocks on HostOpAck. Fixes the CRITICAL Stage-1 bug
// where remote survivors' identity was never rewritten.
func stepMergeRenameSurvivorHost(c *Process, ctx *CommitContext) error {
	req := ctx.Req
	parentKey := ctx.ParentKey
	survivorKey := ctx.SurvivorKey

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
	return nil
}

// stepMergeApplyRewireDirectives applies per-cell neighbor snapshots off the
// caller's coord lock and at each target cell's next tick boundary.
func stepMergeApplyRewireDirectives(c *Process, ctx *CommitContext) error {
	c.applyRewireDirectives(ctx.MergeDirectives)
	return nil
}

// stepMergeRemapSessions remaps in-flight player routes for any session
// still pointing at the survivor's old key or at one of the donor keys to
// the parent key + parent host. Mirrors the full split/migrate treatment:
//   - sessionRoutes.Migrate bumps Epoch + updates HostID + CellID atomically
//   - notifySessionActive keeps the coordinator's username→host index fresh
//   - dispatchSessionRegister tells the parent host's VCM the new epoch so
//     outbound ClientFrames stamp the value the gateway expects
//   - dispatchUpstreamSwitch tells the gateway about the new upstream
//
// The pre-fix path used remapCell, which only changed CellID. For a
// same-host merge that worked because HostID didn't actually change. For a
// cross-host merge (donor on a different host than survivor) the gateway
// kept routing client input to the donor's old host and the parent host's
// VCM never learned the session — outbound replication frames never
// reached the client and the connection stalled.
func stepMergeRemapSessions(c *Process, ctx *CommitContext) error {
	req := ctx.Req
	parentKey := ctx.ParentKey
	survivorKey := ctx.SurvivorKey
	parentHost := req.mutation.add[parentKey]

	// Build the set of cell keys whose sessions need to migrate to the
	// parent. Use req.commands[].SrcCellID (the orchestrator's authoritative
	// donor list) rather than ctx.DonorIDs — the latter is built from
	// stepMergeApplyCoordMutation's local-cells loop and is empty on a
	// pure --mode=coordinator process where no donor cells live locally.
	// Without the full donor list, sessions on cross-host donors stay
	// pinned to the donor's old host + cell ID forever and the gateway
	// keeps routing input to a torn-down cell — manifesting as the player
	// losing connectivity on a cross-host MERGE.
	affected := make(map[MeshCellID]struct{}, 1+len(req.commands))
	if survivorKey != "" {
		affected[survivorKey] = struct{}{}
	}
	for _, cmd := range req.commands {
		if cmd.Kind == CellTransferMerge && cmd.SrcCellID != "" {
			affected[cmd.SrcCellID] = struct{}{}
		}
	}
	if len(affected) == 0 {
		return nil
	}

	// Collect targets in a single read pass so Migrate (write lock) and
	// the dispatch calls happen outside sessionRoutes' lock.
	type target struct {
		Key      SessionKey
		Username string
	}
	var targets []target
	c.sessionRoutes.ForEach(func(route *SessionRoute) bool {
		if _, ok := affected[route.CellID]; !ok {
			return true
		}
		targets = append(targets, target{Key: route.Key, Username: route.Username})
		return true
	})

	for _, t := range targets {
		newEpoch, ok := c.sessionRoutes.Migrate(t.Key, parentHost, parentKey)
		if !ok {
			continue
		}
		if t.Username != "" {
			c.notifySessionActive(t.Username, parentHost, parentKey)
		}
		c.dispatchSessionRegister(parentHost, t.Key, newEpoch, parentKey)
		c.dispatchUpstreamSwitch(t.Key, parentHost, parentKey, newEpoch)
	}
	return nil
}

// stepMergeApplyRegistryDelta reconciles HostRegistry bookkeeping against
// the pre-mutation ownership snapshot.
func stepMergeApplyRegistryDelta(c *Process, ctx *CommitContext) error {
	req := ctx.Req
	c.applyRegistryDelta(req.mutation, ctx.PreOwnership)
	return nil
}

// stepMergeDrainDonorResiduals drains residual entities from donors to the
// survivor before tearing them down. The initial donor serialize (via the
// executor) captured a snapshot at one point in time; between snapshot and
// commit, more entities may have flowed into the donor via cross-sibling
// handoffs. Without this drain those entities die with the donor when it
// shuts down. Run in two passes to converge: pass 1 ships any new arrivals,
// pass 2 catches anything that arrived during pass 1.
func stepMergeDrainDonorResiduals(c *Process, ctx *CommitContext) error {
	survivor := ctx.Survivor
	donorCells := ctx.DonorCells
	if survivor != nil && len(donorCells) > 0 {
		for pass := 0; pass < 2; pass++ {
			c.drainDonorResidualsToSurvivor(donorCells, survivor)
		}
	}
	return nil
}

// survivorHandoffDriver pulls the HandoffDriver out of the survivor's
// bridge. Returns nil if the bridge doesn't expose one (defensive — every
// configured bridge does). Used by the merge executor's Receive path to
// cancel stale pending demotes on the survivor before populate runs.
func survivorHandoffDriver(survivor *Cell) *HandoffDriver {
	return survivor.handoffDriver()
}

// cancelStaleDemotesOnSurvivor drops every pending demote on the survivor
// cell whose destCellID is one of the doomed donor siblings of survivorKey.
// The siblings are derived from the parent of survivorKey (always at depth
// >= 1; a depth-0 cell can never be a merge survivor) excluding survivorKey
// itself.
//
// Must be called from the survivor's game loop (held by RunOnLoop in the
// MERGE executor's Receive). The call itself takes the HandoffDriver mutex,
// so it's also safe to call off-loop — but inside RunOnLoop it's atomic
// with the subsequent populate, which is what eliminates the post-populate
// stale-demote race.
//
// See cellTransferExecutor.Receive's MERGE branch for the why.
func cancelStaleDemotesOnSurvivor(survivor *Cell, survivorKey MeshCellID) {
	hd := survivorHandoffDriver(survivor)
	if hd == nil {
		return
	}
	cellID, err := ParseCellID(string(survivorKey))
	if err != nil || cellID.Depth == 0 {
		return
	}
	parent := cellID.Parent()
	doomed := make(map[MeshCellID]struct{}, 3)
	for _, sib := range parent.Children() {
		sibKey := sib.MeshID()
		if sibKey == survivorKey {
			continue
		}
		doomed[sibKey] = struct{}{}
	}
	if n := hd.CancelPendingDemotesTo(doomed); n > 0 {
		survivor.Engine.Log.Log(CatMeshCell,
			"executor: cancelled %d stale pending demote(s) on survivor %s before merge populate",
			n, survivorKey)
	}
}

// stepMergeReleaseDonors tears down every donor cell on its owning host via
// hostProxy — local donors dispatch to localHostOps (RemoveCell +
// cell.Shutdown synchronously); remote donors go via MeshControl CellRelease.
// Each merge command carries the donor's SrcHostID + SrcCellID. Running this
// before the netID release ensures the local Host.Cells entry is still
// present when localHostOps looks it up.
func stepMergeReleaseDonors(c *Process, ctx *CommitContext) error {
	req := ctx.Req
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
	return nil
}

// stepMergeReleaseDonorNetIDs releases NetID ranges for local donors
// (captured by stepMergeApplyCoordMutation before hostProxy.ReleaseCell tore
// the cell down). Remote donors own their own netID allocator and don't
// need release here.
func stepMergeReleaseDonorNetIDs(c *Process, ctx *CommitContext) error {
	for _, d := range ctx.DonorCells {
		c.netIDAlloc.Release(d.Engine.NetIDBase())
	}
	return nil
}

// stepMergePrimeCooldowns primes the per-parent merge cooldown, clears the
// cooldowns on each former sibling, and fires the game's OnTopologyChanged
// hook.
func stepMergePrimeCooldowns(c *Process, ctx *CommitContext) error {
	req := ctx.Req
	parent := req.SrcCell
	siblings := parent.Children()
	if c.partState != nil && c.cfg.DynamicPartitioning != nil {
		c.partState.setCooldown(parent, c.cfg.DynamicPartitioning.Cooldown)
		for _, sib := range siblings {
			c.partState.clearCooldown(sib)
		}
	}
	if pc := c.cfg.DynamicPartitioning; pc != nil && pc.OnTopologyChanged != nil {
		pc.OnTopologyChanged()
	}
	return nil
}

// stepMergeBroadcastPeerList pushes a fresh PeerList to every registered
// host and gateway so they observe the new cell-to-host ownership.
func stepMergeBroadcastPeerList(c *Process, ctx *CommitContext) error {
	c.broadcastPeerListIfReady()
	return nil
}
