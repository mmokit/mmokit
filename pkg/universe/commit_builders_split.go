package universe

import (
	"context"
	"time"

	"github.com/zenion/mmoserver/pkg/coords"
)

// buildSplitPlan translates a SPLIT CellTransferRequest into a data-driven
// CommitPlan. The step list mirrors the imperative order preserved from
// the pre-B2 applySplitCommit body — every conditional, lock acquisition,
// error handler, and log line is carried across verbatim into the step
// functions below. See cell_transfer_commit.go for the rationale comment
// block describing what each commit variant reconciles.
func buildSplitPlan(c *Process, req *CellTransferRequest) *CommitPlan {
	parent := req.SrcCell
	children := parent.Children()
	parentKey := parent.MeshID()

	// Each dest host's CellTransferReady carries the usernames whose
	// entities landed on that child. req.adoptedUsers is the aggregated
	// username -> destCellKey map, populated by OnReady as acks arrive.
	// Sessions for adopted users route to their actual child cell.
	// Sessions still pointing at parentKey whose username isn't in the
	// adopted set (e.g. disconnected sessions with no live entity) fall
	// back to children[0] so client input keeps flowing to a real cell;
	// the next boundary crossing fixes it if needed.
	fallbackChildKey := children[0].MeshID()

	ctx := &CommitContext{
		Req:              req,
		Mutation:         req.mutation,
		ParentKey:        parentKey,
		Children:         children,
		FallbackChildKey: fallbackChildKey,
	}

	return &CommitPlan{
		ID:   req.ID,
		Kind: CommitKindSplit,
		Req:  req,
		Ctx:  ctx,
		Steps: []PlanStep{
			{Name: "apply-coord-mutation", Run: stepSplitApplyCoordMutation},
			{Name: "apply-rewire-directives", Run: stepSplitApplyRewireDirectives},
			{Name: "remap-sessions", Run: stepSplitRemapSessions},
			{Name: "apply-registry-delta", Run: stepSplitApplyRegistryDelta},
			{Name: "release-parent-host", Run: stepSplitReleaseParentHost},
			{Name: "prime-cooldowns", Run: stepSplitPrimeCooldowns},
			{Name: "broadcast-peer-list", Run: stepSplitBroadcastPeerList},
		},
	}
}

// stepSplitApplyCoordMutation performs the under-c.mu work in one atomic
// block: snapshot pre-mutation ownership, flip cellToHostMap, detach the
// parent from coord-level maps, advance the topology tree, and compute
// the rewire directives that the next step applies off-lock.
//
// This step intentionally fuses multiple semantic sub-steps (snapshot /
// cell-to-host / detach-parent / topology-update / rewire-compute)
// because they must run under a single c.mu.Lock()/Unlock() pair. Splitting
// them into separate PlanSteps would require multiple re-acquisitions of
// c.mu, which would change observable locking semantics.
func stepSplitApplyCoordMutation(c *Process, ctx *CommitContext) error {
	req := ctx.Req
	parent := req.SrcCell
	children := ctx.Children
	parentKey := ctx.ParentKey

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
	ctx.ParentCell = parentCell
	ctx.HadParent = hadParent

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
	ctx.SplitDirectives = splitDirectives
	return nil
}

// stepSplitApplyRewireDirectives applies the per-cell neighbor rewires via
// PendingAdminCmds so the writes happen on the same goroutine that reads
// node.Neighbors from PostSystems, avoiding a race with the game loop.
func stepSplitApplyRewireDirectives(c *Process, ctx *CommitContext) error {
	c.applyRewireDirectives(ctx.SplitDirectives)
	return nil
}

// splitSessionTarget pairs a session key with its resolved destination cell,
// destination host, and username so the remap loop can call Migrate + dispatch
// without holding sessionRoutes' lock across the dispatch calls.
type splitSessionTarget struct {
	Key      SessionKey
	Username string
	DestCell string
	DestHost string
}

// stepSplitRemapSessions remaps in-flight session routes off the parent
// key onto the appropriate child cell. For each affected session it:
//   - calls sessionRoutes.Migrate (bumps Epoch, updates HostID + CellID)
//   - dispatches SessionRegister to the destination host's VCM
//   - dispatches UpstreamSwitch to the owning gateway
//   - updates c.players[username].HostID via notifySessionActive
//
// This mirrors the full notifyPlayerMigrated / stepMigrateRemapSessions
// treatment so a cross-host split doesn't leave stale HostIDs or a
// mismatched epoch between gateway and the destination host's VCM.
func stepSplitRemapSessions(c *Process, ctx *CommitContext) error {
	req := ctx.Req
	parentKey := ctx.ParentKey
	fallbackChildKey := ctx.FallbackChildKey
	fallbackHost := req.mutation.add[fallbackChildKey]

	// Collect targets under a single read pass; Migrate is called
	// afterwards so we don't hold sessionRoutes' lock during dispatch.
	var targets []splitSessionTarget
	c.sessionRoutes.ForEach(func(route *SessionRoute) bool {
		if route.CellID != parentKey {
			return true
		}
		destCell := fallbackChildKey
		if k, ok := req.adoptedUsers[route.Username]; ok {
			destCell = k
		}
		destHost := req.mutation.add[destCell]
		if destHost == "" {
			destHost = fallbackHost
		}
		targets = append(targets, splitSessionTarget{
			Key:      route.Key,
			Username: route.Username,
			DestCell: destCell,
			DestHost: destHost,
		})
		return true
	})

	for _, t := range targets {
		// Migrate atomically bumps Epoch and updates HostID + CellID.
		newEpoch, ok := c.sessionRoutes.Migrate(t.Key, t.DestHost, t.DestCell)
		if !ok {
			continue
		}
		// Keep the coordinator's username→host index consistent (same as
		// notifyPlayerMigrated does for normal cross-host handoffs).
		if t.Username != "" {
			c.notifySessionActive(t.Username, t.DestHost, t.DestCell)
		}
		// Tell the destination host's VCM the authoritative epoch so it
		// stamps outbound ClientFrames with the value the gateway expects.
		c.dispatchSessionRegister(t.DestHost, t.Key, newEpoch, t.DestCell)
		// Tell the gateway the new upstream host + bumped epoch.
		c.dispatchUpstreamSwitch(t.Key, t.DestHost, t.DestCell, newEpoch)
	}
	return nil
}

// stepSplitApplyRegistryDelta reconciles HostRegistry bookkeeping against
// the pre-mutation ownership snapshot.
func stepSplitApplyRegistryDelta(c *Process, ctx *CommitContext) error {
	req := ctx.Req
	c.applyRegistryDelta(req.mutation, ctx.PreOwnership)
	return nil
}

// stepSplitReleaseParentHost tears down the parent cell on its host via
// hostProxy.ReleaseCell and, if the parent cell was local, releases its
// NetID range.
func stepSplitReleaseParentHost(c *Process, ctx *CommitContext) error {
	req := ctx.Req
	parentKey := ctx.ParentKey

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
	if ctx.HadParent {
		c.netIDAlloc.Release(ctx.ParentCell.Engine.NetIDBase())
	}
	return nil
}

// stepSplitPrimeCooldowns primes per-child split cooldowns (to debounce
// rapid re-splits) and fires the game's OnTopologyChanged hook.
func stepSplitPrimeCooldowns(c *Process, ctx *CommitContext) error {
	children := ctx.Children
	if c.partState != nil && c.cfg.DynamicPartitioning != nil {
		cooldown := c.cfg.DynamicPartitioning.Cooldown
		for _, ch := range children {
			c.partState.setCooldown(ch, cooldown)
		}
	}
	if pc := c.cfg.DynamicPartitioning; pc != nil && pc.OnTopologyChanged != nil {
		pc.OnTopologyChanged()
	}
	return nil
}

// stepSplitBroadcastPeerList pushes a fresh PeerList to every registered
// host and gateway so they observe the new cell-to-host ownership.
func stepSplitBroadcastPeerList(c *Process, ctx *CommitContext) error {
	c.broadcastPeerListIfReady()
	return nil
}
