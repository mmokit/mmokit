package universe

import (
	"github.com/mlange-42/ark/ecs"

	"github.com/zenion/mmoserver/pkg/component"
	"github.com/zenion/mmoserver/pkg/coords"
	"github.com/zenion/mmoserver/pkg/engine"
)

// HandoffDriver orchestrates entity handoff across cell boundaries.
// Runs each tick in PostSystems after BorderDispatcher, drains the
// WorldBase crossing-event queue, and emits a single Handoff message
// per crossing to the destination cell via the Bridge.
//
// G2 intermediate state: the driver still fires a single Handoff per
// crossing with CommitTick = currentTick and demotes the source
// immediately (v1-like same-tick flip). Phase H1 rewires this to the
// hard-cut protocol with a lead-tick commit queue.
type HandoffDriver struct {
	base   *WorldBase
	bridge Bridge
	netMap *ecs.Map1[component.NetworkID]
	posMap *ecs.Map1[component.Position]
	cellMap *ecs.Map1[component.CellCoord]
}

// NewHandoffDriver creates a driver bound to the given WorldBase and
// Bridge. The bridge is used for sending Handoff messages to destination
// cells (may be a cellBridge or grpcBridge).
func NewHandoffDriver(base *WorldBase, bridge Bridge) *HandoffDriver {
	w := base.ECSWorld()
	return &HandoffDriver{
		base:    base,
		bridge:  bridge,
		netMap:  ecs.NewMap1[component.NetworkID](w),
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

// handleCrossing processes a single CrossingEvent. G2 intermediate
// behavior: fire a single Handoff message with CommitTick = currentTick
// and demote the source immediately. The destination, on receipt, will
// spawn + promote in the same processMessage call (see cell.go). Phase
// H1 replaces this with the hard-cut protocol: compute a lead tick,
// queue a demote for CommitTick on the source, and the destination
// queues a promote for the same CommitTick.
func (hd *HandoffDriver) handleCrossing(evt CrossingEvent, currentTick uint64) {
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

	// Fire a single Handoff message to the destination. If the destination
	// cell no longer exists on this process (concurrent merge commit), the
	// bridge returns false — bail out, roll back the epoch bump, and let
	// BoundarySystem re-detect the crossing next tick.
	ok := hd.bridge.SendHandoff(evt.DestCellID, &HandoffPayload{
		NetID:        evt.NetID,
		Epoch:        newEpoch,
		CommitTick:   currentTick,
		TransferBlob: data,
		ConnID:       evt.ConnID,
	})
	if !ok {
		if hd.netMap.HasAll(evt.Entity) {
			hd.netMap.Get(evt.Entity).Epoch = oldEpoch
		}
		hd.base.eng.Log.Log(CatMeshTransfer,
			"[%s] handoff aborted (dest %s gone): netID=%d will retry next tick",
			hd.base.cellID, evt.DestCellID, evt.NetID)
		return
	}

	// v1-like same-tick demote. Phase H1 replaces this with a queued
	// demote keyed on CommitTick.
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

	hd.base.eng.Log.Log(CatMeshTransfer,
		"[%s] handoff committed: netID=%d -> %s tick=%d epoch=%d",
		hd.base.cellID, evt.NetID, evt.DestCellID, currentTick, newEpoch)
}
