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
// Overlap protocol (two-phase): handleCrossing fires Prepare and
// transitions the source entity to HandoffPromoted — the source stays
// Live and ticks normally. tickPromoted walks all Promoted entries
// each game tick, increments their warmup counter, and fires Commit +
// DemoteLiveToReplica once MinWarmupTicks have elapsed. Both source
// and destination are authoritative during warmup; shared border
// neighbors receive border frames from both. The commit flip is
// client-invisible.
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
//
// Short-circuits when the cell is draining for a merge — the donor's
// entities have been (or are about to be) serialized for shipping to
// the survivor, and emitting Prepare+Commit messages from here would
// race with the merge populate and produce duplicate netIDs on the
// destination cell. Pending crossings that accumulate during drain
// are discarded: the cell is about to be torn down by
// stepMergeReleaseDonors, so the source entity was already captured
// by serializeAllEntities and will land on the survivor via merge
// populate (or drain-donor-residuals). Both phases are frozen so that
// a Promoted entity cannot commit while drain is in progress.
func (hd *HandoffDriver) Tick(currentTick uint64) {
	if hd.base.IsDrainingForMerge() {
		hd.base.DrainCrossingQueue() // drop pending events; see docstring
		return                       // freeze both phases during merge drain
	}
	events := hd.base.DrainCrossingQueue()
	for _, evt := range events {
		hd.handleCrossing(evt, currentTick)
	}
	hd.tickPromoted(currentTick)
}

// handleCrossing processes a single CrossingEvent. Fires only Prepare
// and transitions the entity to HandoffPromoted — the source stays
// Live. tickPromoted drives the second phase (Commit) once the warmup
// floor has been met.
func (hd *HandoffDriver) handleCrossing(evt CrossingEvent, currentTick uint64) {
	k := HandoffKey{EntityNetID: evt.NetID, NeighborID: evt.DestCellID}

	// Idempotent: already prepared for this neighbor, skip.
	if hd.sm.State(k) == HandoffPromoted {
		return
	}

	// Skip if already in cooldown (anti-thrash).
	if hd.sm.InCooldown(k, currentTick) {
		return
	}

	if !hd.base.eng.ECS.Alive(evt.Entity) {
		return
	}

	// Bump epoch on the source entity before serializing so the
	// destination shadow carries a higher epoch than any stale replica.
	// The epoch is bumped now; the source continues ticking with the new
	// epoch until Commit demotes it to Replica.
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
	// out-of-bounds local position and immediately re-queue a crossing.
	//
	// The source entity stays Live after Prepare, so we do NOT restore
	// the original values — the entity's canonical position has shifted
	// into the destination frame and border frames from here forward will
	// carry the wrapped coordinates.
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
	// destination spawns the shadow at the correct local position.
	data, err := hd.base.SerializeEntity(evt.Entity)
	if err != nil {
		hd.base.eng.Log.Log(CatMeshTransfer,
			"[%s] handoff serialize failed: netID=%d err=%v",
			hd.base.cellID, evt.NetID, err)
		return
	}

	var kind uint16
	if hd.kindMap.HasAll(evt.Entity) {
		kind = uint16(hd.kindMap.Get(evt.Entity).Type)
	}

	// Emit Prepare to the destination. If the destination cell no longer
	// exists (e.g. a concurrent merge commit just removed it), the bridge
	// returns false. Bail out — the source entity stays Live and the next
	// BoundarySystem tick will re-detect the crossing to the new owner.
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
		// Roll back the epoch bump so the next retry produces a fresh
		// epoch rather than a stale duplicate. Source entity is untouched.
		if hd.netMap.HasAll(evt.Entity) {
			hd.netMap.Get(evt.Entity).Epoch = oldEpoch
		}
		hd.base.eng.Log.Log(CatMeshTransfer,
			"[%s] handoff aborted (dest %s gone): netID=%d will retry next tick",
			hd.base.cellID, evt.DestCellID, evt.NetID)
		return
	}

	// Handle player session transfer at Prepare time so the player's
	// input is routed to the destination during the warmup window.
	if evt.ConnID != 0 {
		hd.bridge.OnPlayerTransfer(evt.ConnID, evt.DestCellID)
		if sess := hd.base.eng.Players.ByConnID(evt.ConnID); sess != nil {
			_ = hd.base.eng.Players.Transition(sess, engine.StateTransferring)
			hd.base.eng.Players.Remove(sess)
		}
	}

	// Transition to Promoted; tickPromoted will fire Commit once the
	// warmup floor is met.
	hd.sm.SetState(k, HandoffPromoted)

	hd.base.eng.Log.Log(CatMeshTransfer,
		"[%s] handoff prepared: netID=%d -> %s tick=%d epoch=%d",
		hd.base.cellID, evt.NetID, evt.DestCellID, currentTick, newEpoch)
}

// tickPromoted advances the warmup counter for all Promoted entries
// and fires Commit for any that have met the MinWarmupTicks floor.
// Called each game tick from Tick, after draining the crossing queue.
//
// Order: check CanCommit before ticking so that MinWarmupTicks
// represents the number of complete tickPromoted calls that must pass
// after Prepare before Commit fires. The prepare tick itself does not
// count toward the warmup floor.
func (hd *HandoffDriver) tickPromoted(currentTick uint64) {
	var ready []HandoffKey
	for k, e := range hd.sm.entries {
		if e.phase != HandoffPromoted {
			continue
		}
		if hd.sm.CanCommit(k) {
			ready = append(ready, k)
		} else {
			hd.sm.TickWarmup(k)
		}
	}
	// Fire commits in a separate loop to avoid mutating the map while
	// iterating over it.
	for _, k := range ready {
		entity, pres, ok := hd.base.LookupNetID(k.EntityNetID)
		if !ok || pres != PresenceLive || !hd.base.eng.ECS.Alive(entity) {
			hd.sm.Forget(k)
			continue
		}
		hd.fireCommit(k, entity, currentTick)
	}
}

// OnCancelFromDest is called when this cell (as source) receives a
// MsgHandoffCancel from a destination cell — typically because the
// destination's Shadow watchdog timed out. Releases the stuck
// HandoffStateMachine entry for the (entity, neighbor) pair so the
// next Tick does not re-fire a Commit into a destination that already
// tore down the Shadow.
func (hd *HandoffDriver) OnCancelFromDest(netID uint32, fromCellID string) {
	hd.sm.Forget(HandoffKey{EntityNetID: netID, NeighborID: fromCellID})
}

// fireCommit sends HandoffCommit to the destination and demotes the
// source entity from Live to Replica. On any failure the entity stays
// Live and the warmup counter keeps advancing so the next tick retries.
func (hd *HandoffDriver) fireCommit(k HandoffKey, entity ecs.Entity, currentTick uint64) {
	netID := k.EntityNetID
	var epoch uint32
	if hd.netMap.HasAll(entity) {
		epoch = hd.netMap.Get(entity).Epoch
	}

	committed := hd.bridge.SendHandoffCommit(k.NeighborID, &HandoffCommitPayload{
		NetID:      netID,
		Epoch:      epoch,
		CommitTick: currentTick,
	})
	if !committed {
		hd.base.eng.Log.Log(CatMeshTransfer,
			"[%s] handoff commit aborted (dest %s gone): netID=%d — will retry",
			hd.base.cellID, k.NeighborID, netID)
		return
	}

	if err := hd.base.DemoteLiveToReplica(netID, k.NeighborID); err != nil {
		hd.base.eng.Log.Log(CatMeshTransfer,
			"[%s] handoff DemoteLiveToReplica failed: netID=%d err=%v",
			hd.base.cellID, netID, err)
		return
	}

	hd.sm.SetState(k, HandoffHandoff)
	hd.sm.EnterCooldown(k, currentTick)

	// Cancel any other Promoted states for this entity on different
	// neighbors — the entity committed to exactly one destination.
	for _, otherNeighbor := range hd.sm.PromotedNeighborsFor(netID) {
		if otherNeighbor == k.NeighborID {
			continue
		}
		hd.bridge.SendHandoffCancel(otherNeighbor, &HandoffCancelPayload{
			NetID: netID,
			Epoch: epoch,
		})
		otherKey := HandoffKey{EntityNetID: netID, NeighborID: otherNeighbor}
		hd.sm.Forget(otherKey)
	}

	hd.base.eng.Log.Log(CatMeshTransfer,
		"[%s] handoff committed: netID=%d -> %s tick=%d epoch=%d",
		hd.base.cellID, netID, k.NeighborID, currentTick, epoch)
}
