package universe

import (
	"github.com/mlange-42/ark/ecs"

	"github.com/zenion/mmoserver/pkg/component"
	"github.com/zenion/mmoserver/pkg/coords"
	"github.com/zenion/mmoserver/pkg/engine"
)

// HandoffDriver orchestrates entity handoff across cell boundaries.
// Runs each tick in PostSystems after BorderDispatcher, drains the
// WorldBase crossing-event queue, and emits Prepare+Commit messages
// to destination cells via the Bridge.
//
// v1 protocol (pre-Replication-Timeline-Redesign): Prepare and Commit
// fire on the same tick for each detected crossing. No warmup window,
// no overlap smoothness — the destination shadow is promoted as soon
// as the source sends Commit, and the source entity is demoted to a
// Replica in place. Phases G/H of the timeline redesign replace this
// with a hard-cut handoff coordinated by per-entity produced-at-ms
// stamps and the ClusterClock.
type HandoffDriver struct {
	base    *WorldBase
	sm      *HandoffStateMachine
	bridge  Bridge
	netMap  *ecs.Map1[component.NetworkID]
	kindMap *ecs.Map1[component.EntityKind]
	posMap  *ecs.Map1[component.Position]
	cellMap *ecs.Map1[component.CellCoord]
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
// populate (or drain-donor-residuals).
func (hd *HandoffDriver) Tick(currentTick uint64) {
	if hd.base.IsDrainingForMerge() {
		hd.base.DrainCrossingQueue() // drop pending events; see docstring
		return
	}
	events := hd.base.DrainCrossingQueue()
	for _, evt := range events {
		hd.handleCrossing(evt, currentTick)
	}
}

// handleCrossing processes a single CrossingEvent. Fires Prepare
// immediately followed by Commit on the same tick (v1-style) and
// demotes the source entity from Live to Replica.
func (hd *HandoffDriver) handleCrossing(evt CrossingEvent, currentTick uint64) {
	k := HandoffKey{EntityNetID: evt.NetID, NeighborID: evt.DestCellID}

	if !hd.base.eng.ECS.Alive(evt.Entity) {
		return
	}

	// Bump epoch on the source entity before serializing so the
	// destination shadow carries a higher epoch than any stale replica.
	var oldEpoch uint32
	if hd.netMap.HasAll(evt.Entity) {
		nid := hd.netMap.Get(evt.Entity)
		oldEpoch = nid.Epoch
		nid.Epoch++
	}
	newEpoch := oldEpoch + 1

	// Compute normalized (destination-frame) coords for the serialized
	// TransferBlob WITHOUT modifying the live source entity.
	var normPosX, normPosY float32
	var normCellX, normCellY int32
	normalizedAvailable := hd.posMap.HasAll(evt.Entity) && hd.cellMap.HasAll(evt.Entity)
	if normalizedAvailable {
		pos := hd.posMap.Get(evt.Entity)
		cc := hd.cellMap.Get(evt.Entity)
		normPosX, normPosY = pos.X, pos.Y
		normCellX, normCellY = cc.CellX, cc.CellY
		cellSize := coords.CellSize
		for normPosX >= cellSize {
			normPosX -= cellSize
			normCellX++
		}
		for normPosX < 0 {
			normPosX += cellSize
			normCellX--
		}
		for normPosY >= cellSize {
			normPosY -= cellSize
			normCellY++
		}
		for normPosY < 0 {
			normPosY += cellSize
			normCellY--
		}
	}

	// Serialize the entity, then overwrite the frame's Position +
	// CellCoord with the normalized values for the destination's frame.
	frame := hd.base.SerializeEntityCore(evt.Entity)
	if normalizedAvailable {
		frame.PosX = normPosX
		frame.PosY = normPosY
		frame.CellX = normCellX
		frame.CellY = normCellY
	}
	// Append registered game components (matches SerializeEntity).
	if reg := hd.base.ReplicationRegistry(); reg != nil {
		for _, rep := range reg.All() {
			if cdata := rep.Scan(evt.Entity); cdata != nil {
				frame.Components = append(frame.Components, ComponentSlice{ID: rep.ID, Data: cdata})
			}
		}
	}
	data, err := MarshalTransferFrame(frame)
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
		ClientBaselines: nil,
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

	// v1-style same-tick Commit: authority flips immediately after
	// Prepare. Demote the source entity to a Replica in place.
	committed := hd.bridge.SendHandoffCommit(evt.DestCellID, &HandoffCommitPayload{
		NetID:      evt.NetID,
		Epoch:      newEpoch,
		CommitTick: currentTick,
	})
	if !committed {
		hd.base.eng.Log.Log(CatMeshTransfer,
			"[%s] handoff commit aborted (dest %s gone): netID=%d — will retry",
			hd.base.cellID, evt.DestCellID, evt.NetID)
		return
	}

	if err := hd.base.DemoteLiveToReplica(evt.NetID, evt.DestCellID); err != nil {
		hd.base.eng.Log.Log(CatMeshTransfer,
			"[%s] handoff DemoteLiveToReplica failed: netID=%d err=%v",
			hd.base.cellID, evt.NetID, err)
		return
	}

	// Transfer player session: update the gateway sessionRoute to the
	// destination cell, then release the source engine's Players entry.
	if evt.ConnID != 0 {
		hd.bridge.OnPlayerTransfer(evt.ConnID, evt.DestCellID)
		if sess := hd.base.eng.Players.ByConnID(evt.ConnID); sess != nil {
			_ = hd.base.eng.Players.Transition(sess, engine.StateTransferring)
			hd.base.eng.Players.Remove(sess)
		}
	}

	// Record the transition in the state machine so duplicate crossings
	// land as no-ops until Forget clears it.
	hd.sm.SetState(k, HandoffUnseen)

	hd.base.eng.Log.Log(CatMeshTransfer,
		"[%s] handoff committed: netID=%d -> %s tick=%d epoch=%d",
		hd.base.cellID, evt.NetID, evt.DestCellID, currentTick, newEpoch)
}
