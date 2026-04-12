package universe

import (
	"github.com/mlange-42/ark/ecs"

	"github.com/zenion/mmoserver/pkg/component"
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
	base    *WorldBase
	sm      *HandoffStateMachine
	bridge  Bridge
	netMap  *ecs.Map1[component.NetworkID]
	kindMap *ecs.Map1[component.EntityKind]
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

	// Serialize the entity using the existing TransferFrame format.
	// SerializeEntity normalizes position inside base-cell-local coords.
	data, err := hd.base.SerializeEntity(evt.Entity)
	if err != nil {
		hd.base.eng.Log.Log(CatMeshTransfer,
			"[%s] handoff serialize failed: netID=%d err=%v",
			hd.base.nodeID, evt.NetID, err)
		return
	}

	// Bump epoch on the source entity (commit semantics).
	var oldEpoch uint32
	if hd.netMap.HasAll(evt.Entity) {
		nid := hd.netMap.Get(evt.Entity)
		oldEpoch = nid.Epoch
		nid.Epoch++
	}
	newEpoch := oldEpoch + 1

	var kind uint16
	if hd.kindMap.HasAll(evt.Entity) {
		kind = uint16(hd.kindMap.Get(evt.Entity).Type)
	}

	// Emit Prepare to the destination.
	hd.bridge.SendHandoffPrepare(evt.DestCellID, &HandoffPreparePayload{
		NetID:           evt.NetID,
		Epoch:           newEpoch,
		Kind:            kind,
		TransferBlob:    data,
		ClientBaselines: nil, // baseline handover deferred to v1.1
		ExpectedTick:    currentTick,
		OldEpoch:        oldEpoch,
	})

	// Emit Commit immediately (v1: no warmup window).
	hd.bridge.SendHandoffCommit(evt.DestCellID, &HandoffCommitPayload{
		NetID:      evt.NetID,
		Epoch:      newEpoch,
		CommitTick: currentTick,
	})

	// Update state machine: transition straight to Handoff phase,
	// enter cooldown to prevent thrash.
	hd.sm.SetState(k, HandoffPromoted)
	hd.sm.SetState(k, HandoffHandoff)
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
