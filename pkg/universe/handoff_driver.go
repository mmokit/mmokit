package universe

import (
	"github.com/mlange-42/ark/ecs"

	"github.com/zenion/mmoserver/pkg/component"
	"github.com/zenion/mmoserver/pkg/coords"
	"github.com/zenion/mmoserver/pkg/engine"
)

// HandoffDriver orchestrates entity handoff across cell boundaries.
// Runs each tick in PostSystems after BorderDispatcher, drains the
// WorldBase crossing-event queue, and emits Prepare/Commit messages
// to destination cells via the Bridge.
//
// v1 simplification: Prepare and Commit fire together at crossing
// time (no separate warmup window). Promote-radius early detection is
// deferred to v1.1. The HandoffStateMachine is still used for cooldown
// tracking to prevent re-crossing thrash.
type HandoffDriver struct {
	base     *WorldBase
	sm       *HandoffStateMachine
	bridge   Bridge
	netMap   *ecs.Map1[component.NetworkID]
	kindMap  *ecs.Map1[component.EntityKind]
	posMap   *ecs.Map1[component.Position]
	cellMap  *ecs.Map1[component.CellCoord]
}

// NewHandoffDriver creates a driver bound to the given WorldBase and
// Bridge. The bridge is used for sending Prepare/Commit messages to
// destination cells (may be a localBridge or grpcBridge in the future).
func NewHandoffDriver(base *WorldBase, bridge Bridge) *HandoffDriver {
	w := base.ECSWorld()
	return &HandoffDriver{
		base:    base,
		sm:      NewHandoffStateMachine(),
		bridge:  bridge,
		netMap:  ecs.NewMap1[component.NetworkID](w),
		kindMap: ecs.NewMap1[component.EntityKind](w),
		posMap:  ecs.NewMap1[component.Position](w),
		cellMap: ecs.NewMap1[component.CellCoord](w),
	}
}

// Tick runs one pass of the handoff driver. Called from
// cellBridge.PostSystems after BorderDispatcher.Tick on every game tick.
func (hd *HandoffDriver) Tick(currentTick uint64) {
	events := hd.base.DrainCrossingQueue()
	for _, evt := range events {
		hd.handleCrossing(evt, currentTick)
	}
}

// handleCrossing processes a single CrossingEvent. In the v1 model,
// every crossing triggers an immediate Prepare+Commit sequence — no
// warmup window. Cooldown checking prevents thrash for entities
// oscillating on a boundary.
func (hd *HandoffDriver) handleCrossing(evt CrossingEvent, currentTick uint64) {
	k := HandoffKey{EntityNetID: evt.NetID, NeighborID: evt.DestCellID}

	// Skip if already in cooldown (anti-thrash).
	if hd.sm.InCooldown(k, currentTick) {
		return
	}

	if !hd.base.eng.ECS.Alive(evt.Entity) {
		return
	}

	// Bump epoch on the source entity before serializing (commit
	// semantics). The source's highestSeenEpoch[netID] advances to
	// newEpoch at removal time.
	var oldEpoch uint32
	if hd.netMap.HasAll(evt.Entity) {
		nid := hd.netMap.Get(evt.Entity)
		oldEpoch = nid.Epoch
		nid.Epoch++
	}
	newEpoch := oldEpoch + 1

	// Normalize the source entity's Position + CellCoord into the
	// destination cell's local frame before serializing. The entity
	// crossed into a neighbor cell, so its Position.X/Y is outside
	// [0, cellSize) relative to the current (source) cell root. If we
	// serialized as-is, the destination would spawn the entity at an
	// out-of-bounds local position and immediately re-queue a crossing
	// (or clamp to the wrong edge).
	//
	// Wrap Position into [0, cellSize) and adjust CellCoord to track
	// which base-cell the entity is now in. Mirrors the old
	// BoundarySystem.Update logic that was removed in S2 Task 2.
	//
	// The entity is being removed at the end of this tick (MarkForRemoval
	// below), so we do not restore the original values.
	cellSize := coords.CellSize
	if hd.posMap.HasAll(evt.Entity) && hd.cellMap.HasAll(evt.Entity) {
		pos := hd.posMap.Get(evt.Entity)
		cc := hd.cellMap.Get(evt.Entity)
		for pos.X >= cellSize {
			pos.X -= cellSize
			cc.CellX++
		}
		for pos.X < 0 {
			pos.X += cellSize
			cc.CellX--
		}
		for pos.Y >= cellSize {
			pos.Y -= cellSize
			cc.CellY++
		}
		for pos.Y < 0 {
			pos.Y += cellSize
			cc.CellY--
		}
	}

	// Serialize the entity using the existing TransferFrame format.
	// The blob now carries the normalized Position + CellCoord so the
	// destination spawns the entity at the correct local position.
	data, err := hd.base.SerializeEntity(evt.Entity)
	if err != nil {
		hd.base.eng.Log.Log(CatMeshTransfer,
			"[%s] handoff serialize failed: netID=%d err=%v",
			hd.base.nodeID, evt.NetID, err)
		return
	}

	var kind uint16
	if hd.kindMap.HasAll(evt.Entity) {
		kind = uint16(hd.kindMap.Get(evt.Entity).Type)
	}

	// Emit Prepare to the destination. If the destination cell no longer
	// exists on this process (e.g. a concurrent merge commit just removed
	// it from coord.Cells), the bridge returns false. Bail out without
	// MarkForRemoval — the source entity stays put and the next
	// BoundarySystem tick will re-detect the crossing and route to the new
	// owner of the position. CRUCIAL for cross-cell handoffs that race
	// against split/merge commits — without this the source is deleted
	// while the payload silently drops.
	prepared := hd.bridge.SendHandoffPrepare(evt.DestCellID, &HandoffPreparePayload{
		NetID:           evt.NetID,
		Epoch:           newEpoch,
		Kind:            kind,
		TransferBlob:    data,
		ClientBaselines: nil, // baseline handover deferred to v1.1
		ExpectedTick:    currentTick,
		OldEpoch:        oldEpoch,
	})
	if !prepared {
		// Roll back the epoch bump so the next retry on the next tick
		// produces a fresh epoch instead of a stale duplicate. Source
		// entity is untouched.
		if hd.netMap.HasAll(evt.Entity) {
			hd.netMap.Get(evt.Entity).Epoch = oldEpoch
		}
		hd.base.eng.Log.Log(CatMeshTransfer,
			"[%s] handoff aborted (dest %s gone): netID=%d will retry next tick",
			hd.base.nodeID, evt.DestCellID, evt.NetID)
		return
	}

	// Emit Commit immediately (v1: no warmup window).
	committed := hd.bridge.SendHandoffCommit(evt.DestCellID, &HandoffCommitPayload{
		NetID:      evt.NetID,
		Epoch:      newEpoch,
		CommitTick: currentTick,
	})
	if !committed {
		// Same recovery as above — Prepare reached the dest but Commit
		// didn't. The dest will see a stale Shadow with TTL; we leave
		// the source in place so the next tick re-handoffs cleanly.
		if hd.netMap.HasAll(evt.Entity) {
			hd.netMap.Get(evt.Entity).Epoch = oldEpoch
		}
		hd.base.eng.Log.Log(CatMeshTransfer,
			"[%s] handoff commit aborted (dest %s gone): netID=%d",
			hd.base.nodeID, evt.DestCellID, evt.NetID)
		return
	}

	// Update state machine: transition straight to Handoff phase,
	// enter cooldown to prevent thrash.
	hd.sm.SetState(k, HandoffPromoted)
	hd.sm.SetState(k, HandoffHandoff)

	// Cancel any pending Promoted states for this entity on OTHER neighbors.
	// This handles the corner case where an entity was near 3 cell boundaries
	// simultaneously and entered Promoted phase for multiple neighbors; it
	// commits to exactly one, the others get their shadows cleaned up.
	for _, otherNeighbor := range hd.sm.PromotedNeighborsFor(evt.NetID) {
		if otherNeighbor == evt.DestCellID {
			continue
		}
		hd.bridge.SendHandoffCancel(otherNeighbor, &HandoffCancelPayload{
			NetID: evt.NetID,
			Epoch: newEpoch,
		})
		otherKey := HandoffKey{EntityNetID: evt.NetID, NeighborID: otherNeighbor}
		hd.sm.Forget(otherKey)
	}

	hd.sm.EnterCooldown(k, currentTick)

	// Handle player session transfer.
	if evt.ConnID != 0 {
		hd.bridge.OnPlayerTransfer(evt.ConnID, evt.DestCellID)
		if sess := hd.base.eng.Players.ByConnID(evt.ConnID); sess != nil {
			_ = hd.base.eng.Players.Transition(sess, engine.StateTransferring)
			hd.base.eng.Players.Remove(sess)
		}
	}

	// Mark source entity for removal — it now lives on the destination.
	hd.base.eng.MarkForRemoval(evt.Entity)

	hd.base.eng.Log.Log(CatMeshTransfer,
		"[%s] handoff: netID=%d -> %s tick=%d epoch=%d",
		hd.base.nodeID, evt.NetID, evt.DestCellID, currentTick, newEpoch)
}
