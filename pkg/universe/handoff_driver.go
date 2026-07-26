package universe

import (
	"sync"

	"github.com/mlange-42/ark/ecs"

	"github.com/zenion/mmoserver/pkg/component"
	"github.com/zenion/mmoserver/pkg/coords"
	"github.com/zenion/mmoserver/pkg/engine"
)

// HandoffLeadTicks is how far ahead of the current cluster-tick the
// CommitTick is set. At 20 Hz (50 ms/tick), 2 ticks = 100 ms — a
// conservative LAN round-trip plus destination processing margin.
// Both ends derive CommitTick = currentClusterTick + HandoffLeadTicks
// from their shared ClusterClock so the hard-cut commit is
// cluster-coherent even when cells tick asynchronously.
const HandoffLeadTicks = 2

// HandoffCooldownTicks is the minimum cluster-tick gap between
// successive handoffs of the same (netID, destCellID) pair. 20 ticks
// (1 s at 20 Hz) prevents thrash for entities hovering on a boundary.
const HandoffCooldownTicks = 20

// HandoffInputForwardTicks bounds the source-side grace window that drains
// event inputs already routed to the old host while the gateway applies an
// upstream switch. At 20 Hz this is one second. Movement inputs are idempotent
// and sequence-gated, so a duplicate delivery is safe while a lost final click
// would otherwise remain unacknowledged indefinitely.
const HandoffInputForwardTicks = 20

// pendingDemote is a source-side demote queued for a specific
// cluster-tick. HandoffDriver.Tick drains due demotes at tick start
// (before handling new crossings), flipping the local Live entity
// into a Replica of the destination cell — the hard-cut commit on
// the source side.
//
// No ecs.Entity is stored: re-looking the netID up in the netIDIdx
// at commit time survives removal races (merge drain, death, etc.).
type pendingDemote struct {
	netID      uint32
	destCellID MeshCellID
	connID     uint32
}

type pendingInputForward struct {
	destCellID  MeshCellID
	expiresTick uint64
	// backlog owns frames already drained from the source connection but not
	// yet accepted by the destination path. Preserve order and retry from the
	// first failed frame instead of converting enqueue pressure into input loss.
	backlog [][]byte
}

type inputForwardingBridge interface {
	RequiresInputForwarding(destCellID MeshCellID) bool
}

// HandoffDriver orchestrates entity handoff across cell boundaries
// via the hard-cut protocol.
//
// Each tick (driven from cellBridge.PostSystems):
//  1. Drain pending demotes whose CommitTick <= currentClusterTick,
//     flipping the local Live entity to a Replica of the destination.
//  2. Drain the Stage crossing-event queue. For each crossing,
//     bump the source entity's epoch, serialize, send a single
//     Handoff message with CommitTick = currentClusterTick +
//     HandoffLeadTicks, and queue a pendingDemote for that CommitTick.
//
// The destination-side mirror is in pkg/universe/cell.go: the
// MsgHandoff handler queues a pendingPromote for the same CommitTick,
// drained at tick start by Cell.drainPendingPromotes.
type HandoffDriver struct {
	base    *Stage
	bridge  Bridge
	netMap  *ecs.Map1[component.NetworkID]
	posMap  *ecs.Map1[component.Position]
	cellMap *ecs.Map1[component.CellCoord]

	// mu guards pendingDemotes ONLY. Most accesses happen on the cell's
	// game loop (Tick, handleCrossing) and don't need locking against
	// each other. The lock exists so CancelPendingDemotesTo can run
	// off-loop (from the orchestrator's BeginMerge) and remove entries
	// without racing with a concurrent loop-side drain in Tick.
	mu sync.Mutex

	// pendingDemotes is keyed by the cluster-tick at which the demote
	// should fire. Tick() drains every entry with key <=
	// currentClusterTick, so a slipped commit-tick (the driver did
	// not run on the exact tick the source intended) still commits
	// on the next pass.
	pendingDemotes map[uint64][]pendingDemote

	// lastHandoff[netID][destCellID] records the CommitTick of the
	// most recent successful handoff. Anti-thrash: a new crossing is
	// dropped when currentClusterTick - last < HandoffCooldownTicks.
	lastHandoff map[uint32]map[MeshCellID]uint64

	// inputForwards exists only for real cross-host handoffs. Same-host cells
	// share a connection queue and must let the destination drain it directly.
	inputForwards map[uint32]pendingInputForward
}

// CancelPendingDemotesTo drops every queued pending demote whose destCellID
// is in the given set. Used by the merge orchestrator to clear stale demotes
// on the survivor cell — they would otherwise fire AFTER the merge populated
// the survivor with a (deduped) Live for the same netID, demote that Live
// to a Replica with newSource = a now-torn-down donor, and the Replica
// would expire via TTL when border replication never refreshed it.
//
// Locks hd.mu so it is safe to call from off the cell's loop (BeginMerge
// invokes it from the orchestrator goroutine, before donor Executes ship).
//
// Returns the number of demotes cancelled (for logging).
func (hd *HandoffDriver) CancelPendingDemotesTo(destCellIDs map[MeshCellID]struct{}) int {
	if len(destCellIDs) == 0 {
		return 0
	}
	hd.mu.Lock()
	defer hd.mu.Unlock()
	if len(hd.pendingDemotes) == 0 {
		return 0
	}
	cancelled := 0
	for commitTick, list := range hd.pendingDemotes {
		kept := list[:0]
		for _, d := range list {
			if _, drop := destCellIDs[d.destCellID]; drop {
				cancelled++
				continue
			}
			kept = append(kept, d)
		}
		if len(kept) == 0 {
			delete(hd.pendingDemotes, commitTick)
		} else {
			hd.pendingDemotes[commitTick] = kept
		}
	}
	return cancelled
}

// hasPendingDemote reports whether a demote for netID is already queued
// for any future commit-tick. handleCrossing uses this to drop redundant
// crossings while a handoff is in flight — preventing duplicate Live
// entities when a player crosses two boundaries within HandoffLeadTicks
// (e.g. a diagonal click that triggers an east crossing and a south
// crossing in the same window).
//
// Locks hd.mu for the same reason CancelPendingDemotesTo does: orchestrator
// goroutines may be mutating pendingDemotes off-loop.
func (hd *HandoffDriver) hasPendingDemote(netID uint32) bool {
	hd.mu.Lock()
	defer hd.mu.Unlock()
	for _, list := range hd.pendingDemotes {
		for _, d := range list {
			if d.netID == netID {
				return true
			}
		}
	}
	return false
}

// NewHandoffDriver creates a driver bound to the given Stage and
// Bridge. The bridge is used for sending Handoff messages to destination
// cells (may be a cellBridge or grpcBridge).
func NewHandoffDriver(base *Stage, bridge Bridge) *HandoffDriver {
	w := base.ECSWorld()
	return &HandoffDriver{
		base:           base,
		bridge:         bridge,
		netMap:         ecs.NewMap1[component.NetworkID](w),
		posMap:         ecs.NewMap1[component.Position](w),
		cellMap:        ecs.NewMap1[component.CellCoord](w),
		pendingDemotes: make(map[uint64][]pendingDemote),
		lastHandoff:    make(map[uint32]map[MeshCellID]uint64),
		inputForwards:  make(map[uint32]pendingInputForward),
	}
}

// Tick runs one pass of the handoff driver. Called from
// cellBridge.PostSystems after BorderDispatcher.Tick on every game tick.
//
// currentClusterTick is the cluster-coherent tick index derived from
// ClusterClock.ClusterTick(tickIntervalMs); both source and destination
// of a handoff compute CommitTick relative to this shared axis.
//
// Order of operations:
//  1. Drain pending demotes due now or earlier (hard-cut commit on
//     source: Live → Replica of destination cell).
//  2. Handle new crossings (bump epoch, send Handoff, queue demote).
//
// Short-circuits when the cell is draining for a merge — the donor's
// entities have been (or are about to be) serialized for shipping to
// the survivor; emitting Handoff messages from here would race with
// the merge populate and produce duplicate netIDs on the survivor cell.
// Pending crossings that accumulate during drain are discarded. Pending
// demotes are also skipped — the cell is about to be torn down.
func (hd *HandoffDriver) Tick(currentClusterTick uint64) {
	hd.forwardPendingInputs(currentClusterTick)
	if hd.base.IsDrainingForMerge() {
		hd.base.DrainCrossingQueue() // drop pending events; see docstring
		return
	}

	// 1. Drain due demotes FIRST. Use <=, not ==, so a missed commit
	//    tick still commits on the next pass. Snapshot under hd.mu so a
	//    concurrent CancelPendingDemotesTo (off-loop, from BeginMerge)
	//    can't mutate the map mid-iteration.
	hd.mu.Lock()
	type dueDemote struct {
		commitTick uint64
		demote     pendingDemote
	}
	var due []dueDemote
	for commitTick, list := range hd.pendingDemotes {
		if commitTick > currentClusterTick {
			continue
		}
		for _, d := range list {
			due = append(due, dueDemote{commitTick: commitTick, demote: d})
		}
		delete(hd.pendingDemotes, commitTick)
	}
	hd.mu.Unlock()
	for _, x := range due {
		d := x.demote
		commitTick := x.commitTick
		if err := hd.base.DemoteLiveToReplica(d.netID, string(d.destCellID)); err != nil {
			hd.base.eng.Log.Log(CatMeshTransfer,
				"[%s] commit-tick demote failed: netID=%d dest=%s err=%v",
				hd.base.cellID, d.netID, d.destCellID, err)
		}
		// Player-session side effects fire AT the commit tick, not
		// at crossing time — matches the authoritative flip.
		if d.connID != 0 {
			hd.bridge.OnPlayerTransfer(d.connID, d.destCellID)
			if forwarding, ok := hd.bridge.(inputForwardingBridge); ok && forwarding.RequiresInputForwarding(d.destCellID) {
				route := pendingInputForward{
					destCellID:  d.destCellID,
					expiresTick: currentClusterTick + HandoffInputForwardTicks,
				}
				hd.forwardConnectionInputs(d.connID, &route)
				hd.inputForwards[d.connID] = route
			}
			if sess := hd.base.eng.Players.ByConnID(d.connID); sess != nil {
				_ = hd.base.eng.Players.Transition(sess, engine.StateTransferring)
				// RemoveTransferred (not Remove) so onSessionRemoved
				// doesn't fire — see player_manager.go for why a
				// transfer-out must NOT scrub coord.activeUsers
				// (destination cell takes over the entry).
				hd.base.eng.Players.RemoveTransferred(sess)
			}
		}
		hd.base.eng.Log.Log(CatMeshTransfer,
			"[%s] handoff committed (source demoted): netID=%d dest=%s commitTick=%d",
			hd.base.cellID, d.netID, d.destCellID, commitTick)
	}

	// 2. Handle new crossings.
	events := hd.base.DrainCrossingQueue()
	for _, evt := range events {
		hd.handleCrossing(evt, currentClusterTick)
	}
}

func (hd *HandoffDriver) forwardPendingInputs(currentClusterTick uint64) {
	for connID, route := range hd.inputForwards {
		hd.forwardConnectionInputs(connID, &route)
		if currentClusterTick >= route.expiresTick && len(route.backlog) == 0 {
			delete(hd.inputForwards, connID)
		} else {
			hd.inputForwards[connID] = route
		}
	}
}

func (hd *HandoffDriver) forwardConnectionInputs(connID uint32, route *pendingInputForward) {
	if hd.base.eng.ConnMgr != nil {
		route.backlog = append(route.backlog, hd.base.eng.ConnMgr.DrainInput(connID)...)
	}
	sent := 0
	for _, frame := range route.backlog {
		if !hd.bridge.SendForwardInput(route.destCellID, &ForwardInputPayload{
			ConnID:    connID,
			InputBlob: frame,
		}) {
			break
		}
		sent++
	}
	if sent == len(route.backlog) {
		route.backlog = nil
	} else if sent > 0 {
		route.backlog = route.backlog[sent:]
	}
}

// handleCrossing processes a single CrossingEvent under the hard-cut
// protocol:
//  1. Anti-thrash: drop if we handed this (netID, dest) off within
//     the cooldown window.
//  2. Bump epoch on the source entity so the destination's post-commit
//     frames carry a higher epoch than any stale replica.
//  3. Serialize + send a single Handoff message with
//     CommitTick = currentClusterTick + HandoffLeadTicks.
//  4. Queue a pendingDemote for that CommitTick. The demote does NOT
//     fire here — Tick() drains it when currentClusterTick catches up
//     to CommitTick.
//
// The source entity stays Live between now and CommitTick; its
// border-dispatcher push continues to carry samples toward the
// destination for the lead-time window. The client sees the last
// authoritative sample from the source at tick N, the first from the
// destination at tick N+1 or later, and the lerp through the seam is
// invisible.
func (hd *HandoffDriver) handleCrossing(evt CrossingEvent, currentClusterTick uint64) {
	if !hd.base.eng.ECS.Alive(evt.Entity) {
		return
	}

	// Entity must still be Live on this cell. Two stale-state cases this
	// catches together with the hasPendingDemote check below:
	//
	//   1. Crossing queued by BoundarySystem on tick T while a previous
	//      handoff for the same netID is still in flight (commit not yet
	//      fired). The entity is still Live but a demote is pending —
	//      caught by hasPendingDemote.
	//
	//   2. Crossing queued by BoundarySystem on tick T (entity still
	//      Live), then HandoffDriver.Tick(T+lead) runs at end-of-tick:
	//      first it drains the pending demote (entity becomes Replica),
	//      then it processes new crossings. hasPendingDemote now returns
	//      false (just drained!), so this presence check is the second
	//      net — handing off a Replica would leave a Live copy on the
	//      previous destination AND spawn another Live on the new one,
	//      and the player session ends up routed to whichever destination
	//      commits last while the original Live is orphaned.
	if _, pres, ok := hd.base.LookupNetID(evt.NetID); !ok || pres != PresenceLive {
		return
	}

	// Drop redundant crossings while a handoff for this netID is already
	// in flight. The per-(netID, destCellID) cooldown below only blocks
	// repeats to the SAME destination — a diagonal cross that triggers an
	// east-crossing and a south-crossing within HandoffLeadTicks targets
	// two different destinations, slips past that cooldown, and ends up
	// sending two Handoff messages for the same netID.
	if hd.hasPendingDemote(evt.NetID) {
		return
	}

	// Anti-thrash cooldown — bypassed for explicit teleports.
	if !evt.BypassCooldown {
		if dst, ok := hd.lastHandoff[evt.NetID]; ok {
			if last, ok := dst[evt.DestCellID]; ok {
				if currentClusterTick < last+HandoffCooldownTicks {
					return
				}
			}
		}
	}

	// Bump epoch on the source entity.
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
	// A destination cell owns a fresh per-viewer replication stream whose
	// sequence restarts at one. Advance only the transferred copy so the
	// source session remains pinned to its current generation throughout the
	// lead window and any failed-send retry serializes the same N+1 value.
	if frame.ConnID != 0 {
		frame.StreamGeneration++
	}
	if normalizedAvailable {
		frame.PosX = normPosX
		frame.PosY = normPosY
		frame.CellX = normCellX
		frame.CellY = normCellY
	}
	// Append registered game components (matches SerializeEntity).
	// Skip IsTransferCore replicators — their values are already carried by the
	// dedicated frame fields (PosX/PosY, VelX/VelY, etc.) and normalized for the
	// destination. Including them would overwrite the normalized values in
	// SpawnFromTransferCore with the raw source-frame values.
	if reg := hd.base.ReplicationRegistry(); reg != nil {
		for _, rep := range reg.All() {
			if rep.IsTransferCore {
				continue
			}
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
		// Roll back epoch so a retry next tick re-arms it.
		if hd.netMap.HasAll(evt.Entity) {
			hd.netMap.Get(evt.Entity).Epoch = oldEpoch
		}
		return
	}

	commitTick := currentClusterTick + HandoffLeadTicks

	// Fire a single Handoff message to the destination. If the destination
	// cell no longer exists on this process (concurrent merge commit), the
	// bridge returns false — bail out, roll back the epoch bump, and let
	// BoundarySystem re-detect the crossing next tick.
	ok := hd.bridge.SendHandoff(evt.DestCellID, &HandoffPayload{
		NetID:        evt.NetID,
		Epoch:        newEpoch,
		CommitTick:   commitTick,
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

	// Queue demote for commit-tick. The source entity stays Live between
	// now and commitTick; Tick() drains the queue when the cluster clock
	// catches up. Lock guards against a concurrent off-loop
	// CancelPendingDemotesTo (the merge orchestrator).
	hd.mu.Lock()
	hd.pendingDemotes[commitTick] = append(hd.pendingDemotes[commitTick], pendingDemote{
		netID:      evt.NetID,
		destCellID: evt.DestCellID,
		connID:     evt.ConnID,
	})
	hd.mu.Unlock()

	// Record cooldown.
	if hd.lastHandoff[evt.NetID] == nil {
		hd.lastHandoff[evt.NetID] = make(map[MeshCellID]uint64)
	}
	hd.lastHandoff[evt.NetID][evt.DestCellID] = commitTick

	hd.base.eng.Log.Log(CatMeshTransfer,
		"[%s] handoff sent: netID=%d dest=%s commitTick=%d epoch=%d (lead=%d)",
		hd.base.cellID, evt.NetID, evt.DestCellID, commitTick, newEpoch, HandoffLeadTicks)
}
