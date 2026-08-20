package universe

import (
	"sync"

	"github.com/mlange-42/ark/ecs"

	"github.com/mmokit/mmokit/pkg/component"
	"github.com/mmokit/mmokit/pkg/engine"
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

// HandoffAcceptRetryTicks is how often an unaccepted handoff is re-sent.
// 4 ticks (200 ms at 20 Hz) is comfortably longer than the 2-tick lead the
// ack normally needs. The destination dedups on (NetID, Epoch) and re-acks,
// so re-sending the identical payload is always safe.
const HandoffAcceptRetryTicks = 4

// HandoffAcceptWarnAttempts is how many unacknowledged attempts pass before
// the driver escalates to an error-level log. Retries continue after it —
// abandoning a handoff the destination may already have committed would
// leave two Live copies forever.
const HandoffAcceptWarnAttempts = 5

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

// inflightHandoff is a Handoff that has been sent to a destination cell but
// not yet acknowledged by it. It is the record that keeps the source Live:
// the demote is queued only when a matching HandoffAckPayload arrives.
//
// payload is retained verbatim so a retry re-sends byte-identical bytes —
// same Epoch, same CommitTick, same TransferBlob — which is what makes the
// destination's (NetID, Epoch) dedup work.
type inflightHandoff struct {
	destCellID    MeshCellID
	connID        uint32
	epoch         uint32
	commitTick    uint64
	payload       *HandoffPayload
	attempts      int
	nextRetryTick uint64
	warned        bool
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
//  1. Retry any handoff still awaiting destination acceptance.
//  2. Drain pending demotes whose CommitTick <= currentClusterTick,
//     flipping the local Live entity to a Replica of the destination.
//  3. Drain the Stage crossing-event queue. For each crossing,
//     bump the source entity's epoch, serialize, send a single
//     Handoff message with CommitTick = currentClusterTick +
//     HandoffLeadTicks, and record it as INFLIGHT.
//
// The send -> accept -> arm sequence is load-bearing. SendHandoff returning
// true means only that the message was enqueued; for a remote destination
// that is a local stream write, not delivery. Arming the demote on the
// enqueue (which is what this driver used to do) means a dropped Handoff
// demotes the source anyway and leaves the entity with ZERO authoritative
// holders, permanently — a frozen player. Only OnHandoffAccepted, driven by
// the destination's MsgHandoffAccepted, moves an inflight entry into
// pendingDemotes.
//
// The residual exposure is a bounded DOUBLE-authority window: if the
// destination receives the Handoff but its ack is lost, the destination
// promotes at CommitTick while the source stays Live until a retry ack
// lands (~HandoffAcceptRetryTicks). That is strictly better than the
// zero-authority orphan it replaces — zero-authority is unrecoverable,
// double-authority is transient and self-healing, and the destination drops
// the source's border frames for the netID anyway because its own slot is
// Live (see upsertBorderReplica). Driving the window to exactly zero
// requires the destination to gate its promote on a source-sent Commit,
// i.e. a three-phase protocol. That is deliberately deferred.
//
// The destination-side mirror is in pkg/universe/cell.go: the
// MsgHandoff handler dedups on (NetID, Epoch), acks, and queues a
// pendingPromote for the same CommitTick, drained at tick start by
// Cell.drainPendingPromotes.
type HandoffDriver struct {
	base    *Stage
	bridge  Bridge
	netMap  *ecs.Map1[component.NetworkID]
	posMap  *ecs.Map1[component.Position]
	cellMap *ecs.Map1[component.CellCoord]

	// mu guards pendingDemotes AND inflight. Most accesses happen on the
	// cell's game loop (Tick, handleCrossing) and don't need locking against
	// each other. The lock exists so CancelPendingDemotesTo can run
	// off-loop (from the orchestrator's BeginMerge) and remove entries
	// without racing with a concurrent loop-side drain in Tick.
	mu sync.Mutex

	// inflight holds handoffs sent but not yet accepted by the destination,
	// keyed by netID. An entry here means the source is still authoritative
	// and no demote is queued. Guarded by hd.mu because
	// CancelPendingDemotesTo runs off-loop.
	inflight map[uint32]*inflightHandoff

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

// CancelPendingDemotesTo drops every queued pending demote AND every
// unaccepted inflight handoff whose destCellID is in the given set. Used by
// the merge orchestrator to clear stale demotes on the survivor cell — they
// would otherwise fire AFTER the merge populated the survivor with a
// (deduped) Live for the same netID, demote that Live to a Replica with
// newSource = a now-torn-down donor, and the Replica would expire via TTL
// when border replication never refreshed it.
//
// The inflight sweep matters for the same reason: without it a donor's ack
// arriving after BeginMerge cancelled the pending demotes would re-arm a
// demote toward a torn-down donor, which is precisely the failure this
// helper exists to prevent.
//
// Locks hd.mu so it is safe to call from off the cell's loop (BeginMerge
// invokes it from the orchestrator goroutine, before donor Executes ship).
//
// Returns the number of demotes and inflight handoffs cancelled (for logging).
func (hd *HandoffDriver) CancelPendingDemotesTo(destCellIDs map[MeshCellID]struct{}) int {
	if len(destCellIDs) == 0 {
		return 0
	}
	hd.mu.Lock()
	defer hd.mu.Unlock()
	cancelled := 0
	for netID, h := range hd.inflight {
		if _, drop := destCellIDs[h.destCellID]; drop {
			delete(hd.inflight, netID)
			cancelled++
		}
	}
	if len(hd.pendingDemotes) == 0 {
		return cancelled
	}
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

// hasHandoffInFlight reports whether a handoff for netID is already
// outstanding — either sent and awaiting destination acceptance, or accepted
// and queued for a future commit-tick. handleCrossing uses this to drop
// redundant crossings while a handoff is in flight, preventing duplicate
// Live entities when a player crosses two boundaries within HandoffLeadTicks
// (e.g. a diagonal click that triggers an east crossing and a south
// crossing in the same window).
//
// The inflight half is what covers the pre-acceptance window this driver now
// spends waiting; the pendingDemotes half covers the post-acceptance
// lead-time window as before.
//
// Locks hd.mu for the same reason CancelPendingDemotesTo does: orchestrator
// goroutines may be mutating both maps off-loop.
func (hd *HandoffDriver) hasHandoffInFlight(netID uint32) bool {
	hd.mu.Lock()
	defer hd.mu.Unlock()
	if _, ok := hd.inflight[netID]; ok {
		return true
	}
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
		inflight:       make(map[uint32]*inflightHandoff),
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
//  1. Re-send handoffs still awaiting destination acceptance.
//  2. Drain pending demotes due now or earlier (hard-cut commit on
//     source: Live → Replica of destination cell).
//  3. Handle new crossings (bump epoch, send Handoff, record inflight).
//
// Short-circuits when the cell is draining for a merge — the donor's
// entities have been (or are about to be) serialized for shipping to
// the survivor; emitting Handoff messages from here would race with
// the merge populate and produce duplicate netIDs on the survivor cell.
// Pending crossings that accumulate during drain are discarded. Pending
// demotes are also skipped — the cell is about to be torn down. Retries are
// placed AFTER that short-circuit for the same reason: re-sending a Handoff
// from a draining donor would race the merge populate exactly as a fresh
// crossing would.
func (hd *HandoffDriver) Tick(currentClusterTick uint64) {
	hd.forwardPendingInputs(currentClusterTick)
	if hd.base.IsDrainingForMerge() {
		hd.base.DrainCrossingQueue() // drop pending events; see docstring
		return
	}

	hd.retryInflight(currentClusterTick)

	// 2. Drain due demotes. Use <=, not ==, so a missed commit
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

	// 3. Handle new crossings.
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
	// catches together with the hasHandoffInFlight check below:
	//
	//   1. Crossing queued by BoundarySystem on tick T while a previous
	//      handoff for the same netID is still outstanding (unaccepted, or
	//      accepted with the commit not yet fired). The entity is still
	//      Live — caught by hasHandoffInFlight.
	//
	//   2. Crossing queued by BoundarySystem on tick T (entity still
	//      Live), then HandoffDriver.Tick(T+lead) runs at end-of-tick:
	//      first it drains the pending demote (entity becomes Replica),
	//      then it processes new crossings. hasHandoffInFlight now returns
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
	if hd.hasHandoffInFlight(evt.NetID) {
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
		cellSize := hd.base.CellSize()
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
		// PosZ is deliberately NOT normalized and NOT wrapped. Partitioning is
		// horizontal-only by project decision (§7.4), so there is no vertical
		// cell boundary for Z to cross — it passes through untouched, and a
		// Z-only motion must never trigger a handoff. SerializeEntityCore is
		// what puts the authoritative value on the frame; until phase 2 it did
		// not, and this comment asserted otherwise.
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

	payload := &HandoffPayload{
		NetID:        evt.NetID,
		Epoch:        newEpoch,
		CommitTick:   commitTick,
		TransferBlob: data,
		ConnID:       evt.ConnID,
	}

	// Record the inflight entry BEFORE the send. A same-process bridge
	// delivers synchronously, so the destination can call back into
	// OnHandoffAccepted from inside SendHandoff; inserting first means that
	// ack finds the entry instead of being discarded as unmatched.
	hd.mu.Lock()
	hd.inflight[evt.NetID] = &inflightHandoff{
		destCellID:    evt.DestCellID,
		connID:        evt.ConnID,
		epoch:         newEpoch,
		commitTick:    commitTick,
		payload:       payload,
		attempts:      1,
		nextRetryTick: currentClusterTick + HandoffAcceptRetryTicks,
	}
	hd.mu.Unlock()

	// Fire a single Handoff message to the destination. If the destination
	// cell no longer exists on this process (concurrent merge commit), the
	// bridge returns false — bail out, roll back the epoch bump, and let
	// BoundarySystem re-detect the crossing next tick.
	if !hd.bridge.SendHandoff(evt.DestCellID, payload) {
		hd.mu.Lock()
		delete(hd.inflight, evt.NetID)
		hd.mu.Unlock()
		if hd.netMap.HasAll(evt.Entity) {
			hd.netMap.Get(evt.Entity).Epoch = oldEpoch
		}
		hd.base.eng.Log.Log(CatMeshTransfer,
			"[%s] handoff aborted (dest %s gone): netID=%d will retry next tick",
			hd.base.cellID, evt.DestCellID, evt.NetID)
		return
	}

	// NOTHING is queued here. The demote — and with it OnPlayerTransfer, the
	// gateway upstream switch, StateTransferring and RemoveTransferred — is
	// armed only by OnHandoffAccepted, so a lost Handoff can never leave the
	// entity with zero authoritative holders. The anti-thrash cooldown is
	// recorded there too, so an abandoned attempt does not block the retry.
	hd.base.eng.Log.Log(CatMeshTransfer,
		"[%s] handoff sent: netID=%d dest=%s commitTick=%d epoch=%d (lead=%d) — awaiting destination acceptance",
		hd.base.cellID, evt.NetID, evt.DestCellID, commitTick, newEpoch, HandoffLeadTicks)
}

// OnHandoffAccepted is the source-side ingest of a destination's
// MsgHandoffAccepted. It is the ONLY thing that arms a demote.
//
// Fences on the full (netID, epoch, commitTick, destCellID) tuple. An ack
// that does not match the current inflight entry is a no-op: it is either a
// late ack for a superseded attempt, or an ack from a cell that is no longer
// the intended destination (e.g. after CancelPendingDemotesTo dropped the
// entry during a merge). Idempotent by construction — a second matching ack
// finds no inflight entry.
//
// Because Tick drains pendingDemotes with <=, an ack that arrives after
// commitTick has already passed demotes on the very next Tick.
func (hd *HandoffDriver) OnHandoffAccepted(netID, epoch uint32, commitTick uint64, from MeshCellID) {
	hd.mu.Lock()
	h, ok := hd.inflight[netID]
	if !ok {
		hd.mu.Unlock()
		hd.base.eng.Log.Log(CatMeshTransfer,
			"[%s] handoff-ack ignored (no inflight handoff): netID=%d epoch=%d from=%s",
			hd.base.cellID, netID, epoch, from)
		return
	}
	if h.epoch != epoch || h.commitTick != commitTick || h.destCellID != from {
		hd.mu.Unlock()
		hd.base.eng.Log.Log(CatMeshTransfer,
			"[%s] handoff-ack ignored (superseded): netID=%d got epoch=%d commitTick=%d from=%s; inflight epoch=%d commitTick=%d dest=%s",
			hd.base.cellID, netID, epoch, commitTick, from, h.epoch, h.commitTick, h.destCellID)
		return
	}
	delete(hd.inflight, netID)
	hd.pendingDemotes[h.commitTick] = append(hd.pendingDemotes[h.commitTick], pendingDemote{
		netID:      netID,
		destCellID: h.destCellID,
		connID:     h.connID,
	})
	if hd.lastHandoff[netID] == nil {
		hd.lastHandoff[netID] = make(map[MeshCellID]uint64)
	}
	hd.lastHandoff[netID][h.destCellID] = h.commitTick
	destCellID, armedCommitTick, attempts := h.destCellID, h.commitTick, h.attempts
	hd.mu.Unlock()

	hd.base.eng.Log.Log(CatMeshTransfer,
		"[%s] handoff accepted: netID=%d dest=%s commitTick=%d epoch=%d attempts=%d — demote armed",
		hd.base.cellID, netID, destCellID, armedCommitTick, epoch, attempts)
}

// retryInflight re-sends handoffs the destination has not acknowledged, and
// garbage-collects entries whose entity is no longer Live on this cell.
//
// The epoch is NEVER rolled back on an accept timeout. handleCrossing's
// rollback is only safe because it happens in the same tick, before
// BorderDispatcher.Tick has pushed the bumped value. Once border frames
// carrying the bumped epoch have shipped, rewinding would leave every
// neighbour's highestSeenEpoch ahead of the entity and silently freeze its
// replicas everywhere.
//
// The one condition under which a handoff IS abandoned is SendHandoff
// returning false — the destination cell is gone or its host is unroutable,
// which is the only case where the destination provably cannot have
// promoted. Then the inflight entry and the cooldown are dropped, the entity
// stays Live, and BoundarySystem re-detects the crossing next tick with a
// fresh epoch bump.
func (hd *HandoffDriver) retryInflight(currentClusterTick uint64) {
	hd.mu.Lock()
	if len(hd.inflight) == 0 {
		hd.mu.Unlock()
		return
	}
	type retryTarget struct {
		netID   uint32
		entry   *inflightHandoff
		payload *HandoffPayload
		dest    MeshCellID
	}
	var due []retryTarget
	var dead []uint32
	for netID, h := range hd.inflight {
		if currentClusterTick < h.nextRetryTick {
			continue
		}
		due = append(due, retryTarget{netID: netID, entry: h, payload: h.payload, dest: h.destCellID})
	}
	hd.mu.Unlock()

	for _, t := range due {
		// GC: the entity died, was merged away, or already left this cell.
		// Without this the map grows without bound and the driver keeps
		// re-sending a handoff for something that no longer exists here.
		if _, pres, ok := hd.base.LookupNetID(t.netID); !ok || pres != PresenceLive {
			dead = append(dead, t.netID)
			continue
		}
		if !hd.bridge.SendHandoff(t.dest, t.payload) {
			hd.mu.Lock()
			delete(hd.inflight, t.netID)
			if dst, ok := hd.lastHandoff[t.netID]; ok {
				delete(dst, t.dest)
				if len(dst) == 0 {
					delete(hd.lastHandoff, t.netID)
				}
			}
			hd.mu.Unlock()
			hd.base.eng.Log.Log(CatMeshTransfer,
				"[%s] handoff abandoned (dest %s unreachable): netID=%d — source retains authority, will re-detect crossing",
				hd.base.cellID, t.dest, t.netID)
			continue
		}
		hd.mu.Lock()
		// Re-check: the ack may have landed between the snapshot and here.
		if cur, ok := hd.inflight[t.netID]; ok && cur == t.entry {
			cur.attempts++
			cur.nextRetryTick = currentClusterTick + HandoffAcceptRetryTicks
			if cur.attempts >= HandoffAcceptWarnAttempts && !cur.warned {
				cur.warned = true
				attempts, dest, epoch := cur.attempts, cur.destCellID, cur.epoch
				hd.mu.Unlock()
				hd.base.eng.Log.Log(CatMeshTransfer,
					"[%s] handoff UNACKNOWLEDGED after %d attempts: netID=%d dest=%s epoch=%d — source still authoritative, retrying",
					hd.base.cellID, attempts, t.netID, dest, epoch)
				continue
			}
		}
		hd.mu.Unlock()
	}

	if len(dead) > 0 {
		hd.mu.Lock()
		for _, netID := range dead {
			delete(hd.inflight, netID)
		}
		hd.mu.Unlock()
		for _, netID := range dead {
			hd.base.eng.Log.Log(CatMeshTransfer,
				"[%s] handoff inflight dropped (netID=%d no longer live on this cell)",
				hd.base.cellID, netID)
		}
	}
}
