package universe

import (
	"context"
	"maps"
	"sync"
	"time"

	meshpb "github.com/zenion/mmoserver/gen/go/meshpb"
	"github.com/zenion/mmoserver/pkg/logger"
)

// settleWindow is how long the coordinator waits after the first host
// registration before running the first cell-assignment pass. Extends
// on each subsequent registration so rapid startup bursts are handled
// in a single pass. Matches the S4 spec's 5s setting.
const settleWindow = 5 * time.Second

// assignmentEngine owns the settle-window timer, rendezvous-based
// assignment logic, and CellAssign / NetIDRangeGrant dispatch. One
// instance per coordinator process. Started by meshControlServer in
// Task 2's Build() wiring when Mode == "coordinator".
type assignmentEngine struct {
	coord    *Process
	registry *HostRegistry
	ctrl     *meshControlServer
	log      *logger.Logger

	mu              sync.Mutex
	firstRegistered bool
	settleDeadline  time.Time
	settled         bool

	// nextNetIDStart advances by netIDRangeSize on every NetIDRangeGrant
	// sent to a host. Starts from the coord's netIDAlloc base so we
	// don't reuse ID space already allocated in any prior in-process
	// Build() path. Reset semantics are deliberately simple: the range
	// grows forever; a restart reseeds from time.Now().UnixNano() via
	// the coordEpoch which guarantees uniqueness across coordinator
	// restarts (two different coordinator processes won't pick the
	// same starting base).
	nextNetIDStart uint32
}

// newAssignmentEngine constructs an engine bound to the given
// registry and control server. The caller is responsible for
// starting the settle-window goroutine via Start().
func newAssignmentEngine(coord *Process, registry *HostRegistry, ctrl *meshControlServer) *assignmentEngine {
	return &assignmentEngine{
		coord:    coord,
		registry: registry,
		ctrl:     ctrl,
		log:      coord.Log,
	}
}

const deadThreshold = 3 * time.Second

// gatewayDeadThreshold is intentionally longer than deadThreshold (nodes).
// Gateway death is more user-visible — live player connections drop — so we
// grant more grace time for restarts before cleaning up sessions.
const gatewayDeadThreshold = 5 * time.Second

// Start launches background goroutines that drive the settle-window
// timer, host liveness watcher, and gateway liveness watcher.
// All exit when ctx is cancelled.
func (e *assignmentEngine) Start(ctx context.Context) {
	go e.runSettleLoop(ctx)
	go e.runLivenessWatcher(ctx)
	go e.runGatewayLivenessWatcher(ctx)
}

// runLivenessWatcher polls the registry every 500ms. Any host in
// Live state whose LastHeartbeat is older than deadThreshold is
// marked Dead and its cells are reassigned via rendezvous hashing
// across the surviving live hosts.
func (e *assignmentEngine) runLivenessWatcher(ctx context.Context) {
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			e.checkLiveness()
		}
	}
}

func (e *assignmentEngine) checkLiveness() {
	now := time.Now()
	for _, host := range e.registry.LiveHosts() {
		if host.State != RemoteHostLive {
			continue
		}
		if host.Local {
			// Local in-process hosts never heartbeat; skip liveness checks.
			continue
		}
		if now.Sub(host.LastHeartbeat) <= deadThreshold {
			continue
		}
		e.log.Log(CatMeshCell, "coordinator: host %s DEAD (no heartbeat for %s)", host.ID, now.Sub(host.LastHeartbeat).Round(time.Millisecond))
		e.registry.MarkDead(host.ID)
		e.reassignOrphanedCells(host)
	}
}

// runGatewayLivenessWatcher polls the gateway registry every 500ms. Any
// gateway in Live state whose LastHeartbeat is older than gatewayDeadThreshold
// is marked Dead and its sessions are removed from sessionRoutes. Clients
// will reconnect; sessions are not reassigned.
func (e *assignmentEngine) runGatewayLivenessWatcher(ctx context.Context) {
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			e.checkGatewayLiveness()
		}
	}
}

func (e *assignmentEngine) checkGatewayLiveness() {
	if e.coord.gatewayRegistry == nil {
		return
	}
	now := time.Now()
	for _, gw := range e.coord.gatewayRegistry.LiveGateways() {
		if gw.State != RemoteGatewayLive {
			continue
		}
		if gw.Local {
			// Embedded in-process gateway — lives and dies with the
			// coordinator process, doesn't heartbeat.
			continue
		}
		if now.Sub(gw.LastHeartbeat) <= gatewayDeadThreshold {
			continue
		}
		e.log.Log(CatMeshCell, "coordinator: gateway %s DEAD (no heartbeat for %s)", gw.ID, now.Sub(gw.LastHeartbeat).Round(time.Millisecond))
		e.coord.gatewayRegistry.MarkDead(gw.ID)
		n := e.coord.sessionRoutes.RemoveByGateway(gw.ID)
		e.log.Log(CatMeshCell, "coordinator: gateway %s DEAD, cleaned up %d sessions", gw.ID, n)
	}
}

// reassignOrphanedCells takes a dead host's OwnedCells snapshot and
// dispatches fresh CellAssign messages for each cell to the highest-
// scoring surviving live host via rendezvous hashing. If no live
// hosts remain, the cells are logged as orphaned — they'll be
// reassigned when a new host registers. Local and remote hosts are
// treated identically: the rendezvous ring is the single source of
// truth for cell ownership after T2.
func (e *assignmentEngine) reassignOrphanedCells(dead *RemoteHost) {
	live := e.registry.LiveHosts()
	liveIDs := make([]string, 0, len(live))
	for _, h := range live {
		if h.State == RemoteHostLive {
			liveIDs = append(liveIDs, h.ID)
		}
	}
	if len(liveIDs) == 0 {
		e.log.Log(CatMeshCell, "coordinator: no live hosts — %d cells orphaned from %s", len(dead.OwnedCells), dead.ID)
		return
	}
	for cellID := range dead.OwnedCells {
		newHost := AssignCellToHost(cellID, liveIDs)
		e.log.Log(CatMeshCell, "coordinator: reassigning %s: %s (dead) -> %s", cellID, dead.ID, newHost)
		e.registry.ReleaseCell(dead.ID, cellID)
		e.dispatchCellAssign(newHost, cellID)
	}
	e.broadcastPeerList()
}

// onHostRegistered is called by meshControlServer.Control after it
// inserts a newly-registered host into the registry. Starts or
// extends the settle-window deadline; the runSettleLoop goroutine
// will fire settle() when the deadline elapses.
func (e *assignmentEngine) onHostRegistered(host *RemoteHost) {
	e.mu.Lock()
	defer e.mu.Unlock()
	now := time.Now()
	e.settleDeadline = now.Add(settleWindow)
	if !e.firstRegistered {
		e.firstRegistered = true
		e.log.Log(CatMeshCell, "coordinator: first host %s registered, settle window %s", host.ID, settleWindow)
	} else if e.settled {
		// Post-settle re-registration: run the rebalance immediately
		// without waiting for another settle window. This handles the
		// case where a previously-dead host comes back after crash
		// recovery.
		e.log.Log(CatMeshCell, "coordinator: host %s re-registered post-settle, running rebalance", host.ID)
		go e.rebalance()
	}
}

// runSettleLoop is the background goroutine that polls the settle
// deadline. Simpler than a resettable timer; the 200ms wake-up is
// well below settleWindow so the effective resolution is fine.
func (e *assignmentEngine) runSettleLoop(ctx context.Context) {
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			e.mu.Lock()
			if !e.firstRegistered || e.settled {
				e.mu.Unlock()
				continue
			}
			if time.Now().Before(e.settleDeadline) {
				e.mu.Unlock()
				continue
			}
			e.settled = true
			e.mu.Unlock()
			e.log.Log(CatMeshCell, "coordinator: settle window closed, running first assignment pass")
			e.rebalance()
		}
	}
}

// rebalance enumerates the current cell set (quadtree-aware — reads
// cellToHostMap keys so split children and merge survivors are picked
// up), runs locality-biased rendezvous hashing over the currently-live
// hosts, and dispatches CellAssign + NetIDRangeGrant for each
// (cellID, hostID) pair. Called once when the settle window first
// closes, and again on any post-settle re-registration or
// crash-triggered reassignment.
//
// Local and remote hosts participate on equal footing. There is no
// short-circuit for local-host presence: after S7 T2 the rendezvous
// ring is the single authoritative owner assignment, regardless of
// whether a host is in-process or remote.
func (e *assignmentEngine) rebalance() {
	live := e.registry.LiveHosts()

	liveIDs := make([]string, 0, len(live))
	for _, h := range live {
		// Include Registered hosts too — they become Live on first
		// Heartbeat, but we want to start assigning cells immediately
		// so the node can receive CellAssign and start its game loop.
		if h.State == RemoteHostRegistered || h.State == RemoteHostLive {
			liveIDs = append(liveIDs, h.ID)
		}
	}
	if len(liveIDs) == 0 {
		e.log.Log(CatMeshCell, "coordinator: rebalance skipped — no live hosts")
		return
	}
	cells := e.enumerateCells()

	// Snapshot the current ownership via AllOwnedCells for the locality bias.
	// Post-snapshot mutations are fine: rendezvous is eventually consistent
	// and a stale neighbor owner just means a slightly less accurate locality
	// bonus for one rebalance pass.
	ownership := make(map[string]string)
	if e.coord.Control != nil {
		e.coord.Control.AllOwnedCells(func(k, v string) bool {
			ownership[k] = v
			return true
		})
	} else {
		// Fallback for minimal test fixtures that wire cellToHostMap without
		// a ControlPlane (Phase 2.5 will update those tests).
		e.coord.mu.RLock()
		maps.Copy(ownership, e.coord.cellToHostMap)
		e.coord.mu.RUnlock()
	}

	neighborsOf := func(cellIDStr string) []string {
		cid, err := ParseCellID(cellIDStr)
		if err != nil {
			return nil
		}
		out := make([]string, 0, 8)
		for _, n := range cid.Neighbors() {
			out = append(out, MeshCellID(n))
		}
		return out
	}
	cellOwner := func(cellIDStr string) string {
		return ownership[cellIDStr]
	}

	assignments := AssignCellsAcrossHostsWithLocality(cells, liveIDs, neighborsOf, cellOwner)
	e.log.Log(CatMeshCell, "coordinator: rebalance — %d cells across %d hosts", len(cells), len(liveIDs))
	for cellID, hostID := range assignments {
		existing := e.registry.HostForCell(cellID)
		if existing == hostID {
			continue // already assigned correctly
		}
		if existing != "" {
			// Reassignment — release from old host first.
			e.dispatchCellRelease(existing, cellID)
		}
		e.dispatchCellAssign(hostID, cellID)
	}
	e.broadcastPeerList()
}

// enumerateCells returns the list of cell string IDs that the
// rebalance should operate over. The authoritative source is the
// coordinator's cellToHostMap — it reflects the live quadtree including
// any split children and merge survivors committed since the last
// rebalance. For cold start (before any cells have been committed into
// the map) we fall back to the configured depth-0 grid so the very
// first assignment pass has something to hand out.
func (e *assignmentEngine) enumerateCells() []string {
	var cells []string
	if e.coord.Control != nil {
		e.coord.Control.AllOwnedCells(func(id, _ string) bool {
			cells = append(cells, id)
			return true
		})
	} else {
		// Fallback for minimal test fixtures that wire cellToHostMap without
		// a ControlPlane (Phase 2.5 will update those tests).
		e.coord.mu.RLock()
		for id := range e.coord.cellToHostMap {
			cells = append(cells, id)
		}
		e.coord.mu.RUnlock()
	}
	if len(cells) > 0 {
		return cells
	}

	// Cold-start fallback: no committed cells yet — enumerate the
	// configured depth-0 grid so the first pass can assign something.
	cells = make([]string, 0, int(e.coord.cfg.CellsX)*int(e.coord.cfg.CellsY))
	for sy := uint32(0); sy < e.coord.cfg.CellsY; sy++ {
		for sx := uint32(0); sx < e.coord.cfg.CellsX; sx++ {
			cells = append(cells, MeshCellID(CellID{X: int32(sx), Y: int32(sy)}))
		}
	}
	return cells
}

// dispatchCellAssign sends NetIDRangeGrant + CellAssign to the target
// host via the control server. Updates the registry if both sends
// succeed. A partial failure (range grant sent but assign failed)
// leaves the host with an unused range — acceptable in S4.
func (e *assignmentEngine) dispatchCellAssign(hostID, cellID string) {
	// NetIDRangeGrant carries the next slice of ID space. 10M IDs per
	// grant (netIDRangeSize) is plenty for normal operation; a host
	// would need ~500k entity spawns to exhaust one grant.
	e.mu.Lock()
	if e.nextNetIDStart == 0 {
		e.nextNetIDStart = e.coord.netIDAlloc.Allocate()
	}
	start := e.nextNetIDStart
	e.nextNetIDStart += netIDRangeSize
	e.mu.Unlock()

	grant := &meshpb.CoordMessage{
		CoordEpoch: e.coord.coordEpoch,
		Msg: &meshpb.CoordMessage_NetidRange{
			NetidRange: &meshpb.NetIDRangeGrant{
				HostId: hostID,
				Start:  start,
				Count:  netIDRangeSize,
			},
		},
	}
	if err := e.ctrl.sendCoordMessageToHost(hostID, grant); err != nil {
		e.log.Log(CatMeshCell, "coordinator: NetIDRangeGrant to %s failed: %v", hostID, err)
		return
	}

	assign := &meshpb.CoordMessage{
		CoordEpoch: e.coord.coordEpoch,
		Msg: &meshpb.CoordMessage_CellAssign{
			CellAssign: &meshpb.CellAssign{CellId: cellID},
		},
	}
	if err := e.ctrl.sendCoordMessageToHost(hostID, assign); err != nil {
		e.log.Log(CatMeshCell, "coordinator: CellAssign %s -> %s failed: %v", cellID, hostID, err)
		return
	}

	if err := e.registry.AssignCell(hostID, cellID); err != nil {
		e.log.Log(CatMeshCell, "coordinator: AssignCell bookkeeping failed: %v", err)
	}
	e.log.Log(CatMeshCell, "coordinator: assigned %s -> %s (epoch=%d)", cellID, hostID, e.coord.coordEpoch)
}

// buildPeerList constructs a CoordMessage_PeerList from the current
// HostRegistry snapshot. Called by broadcastPeerList after rebalances
// and by the control server for one-shot targeted sends.
func (e *assignmentEngine) buildPeerList() *meshpb.CoordMessage {
	live := e.registry.LiveHosts()
	hostRecs := make([]*meshpb.HostRecord, 0, len(live))
	for _, h := range live {
		if h.State != RemoteHostLive && h.State != RemoteHostRegistered {
			continue
		}
		hostRecs = append(hostRecs, &meshpb.HostRecord{
			HostId:   h.ID,
			GrpcAddr: h.GrpcAddr,
		})
	}
	ownership := make([]*meshpb.CellOwnership, 0)
	for _, h := range live {
		for cellID := range h.OwnedCells {
			ownership = append(ownership, &meshpb.CellOwnership{
				CellId: cellID,
				HostId: h.ID,
			})
		}
	}
	// Include registered and live gateway records so nodes can open MeshData
	// streams back to gateways. We include RemoteGatewayRegistered (before the
	// first heartbeat) as well as RemoteGatewayLive so nodes learn about the
	// gateway immediately on registration, not only after the first heartbeat.
	var gwRecs []*meshpb.GatewayRecord
	if e.coord.gatewayRegistry != nil {
		for _, gw := range e.coord.gatewayRegistry.LiveGateways() {
			if gw.State != RemoteGatewayLive && gw.State != RemoteGatewayRegistered {
				continue
			}
			if gw.GRPCAddr == "" {
				continue
			}
			gwRecs = append(gwRecs, &meshpb.GatewayRecord{
				GatewayId: gw.ID,
				GrpcAddr:  gw.GRPCAddr,
			})
		}
	}
	return &meshpb.CoordMessage{
		CoordEpoch: e.coord.coordEpoch,
		Msg: &meshpb.CoordMessage_PeerList{
			PeerList: &meshpb.PeerList{
				Hosts:    hostRecs,
				Cells:    ownership,
				Gateways: gwRecs,
			},
		},
	}
}

// broadcastPeerList sends the current PeerList to every registered
// host. Called after rebalance() and after reassignOrphanedCells()
// so every node has a fresh peer roster + cell ownership view.
func (e *assignmentEngine) broadcastPeerList() {
	msg := e.buildPeerList()
	live := e.registry.LiveHosts()
	sent := 0
	for _, h := range live {
		if h.State != RemoteHostLive && h.State != RemoteHostRegistered {
			continue
		}
		if err := e.ctrl.sendCoordMessageToHost(h.ID, msg); err != nil {
			e.log.Log(CatMeshCell, "coordinator: PeerList to %s failed: %v", h.ID, err)
			continue
		}
		sent++
	}
	e.log.Log(CatMeshCell, "coordinator: broadcast PeerList to %d host(s) (%d hosts, %d cells)",
		sent, len(msg.GetPeerList().GetHosts()), len(msg.GetPeerList().GetCells()))

	// Reconcile the in-process gateway's outbound peer set. Standalone
	// gateways learn about peers via the CoordMessage broadcast on their
	// MeshControl stream; an embedded gateway shares the coordinator
	// process so there's no control stream to receive on — we call
	// reconcileRemotePeers directly with the same PeerList we just built.
	// The call is a no-op when gateway.hostNetwork is nil (classic
	// `all` preset with local cells needs no outbound mesh peers).
	if e.coord.gateway != nil {
		e.coord.gateway.reconcileRemotePeers(msg.GetPeerList())
	}

	// Also broadcast to all registered/live gateways so their cached topology stays fresh.
	if e.coord.gatewayRegistry != nil {
		gwSent := 0
		for _, gw := range e.coord.gatewayRegistry.LiveGateways() {
			if gw.State != RemoteGatewayLive && gw.State != RemoteGatewayRegistered {
				continue
			}
			if err := e.ctrl.sendCoordMessageToGateway(gw.ID, msg); err != nil {
				e.log.Log(CatMeshCell, "coordinator: PeerList to gateway %s failed: %v", gw.ID, err)
				continue
			}
			gwSent++
		}
		if gwSent > 0 {
			e.log.Log(CatMeshCell, "coordinator: broadcast PeerList to %d gateway(s)", gwSent)
		}
	}
}

// dispatchCellRelease tells a host to stop owning the given cell.
// Used during reassignment (the same cell moves from host A to host B).
func (e *assignmentEngine) dispatchCellRelease(hostID, cellID string) {
	rel := &meshpb.CoordMessage{
		CoordEpoch: e.coord.coordEpoch,
		Msg: &meshpb.CoordMessage_CellRelease{
			CellRelease: &meshpb.CellRelease{CellId: cellID},
		},
	}
	if err := e.ctrl.sendCoordMessageToHost(hostID, rel); err != nil {
		e.log.Log(CatMeshCell, "coordinator: CellRelease %s -> %s failed: %v", cellID, hostID, err)
		return
	}
	e.registry.ReleaseCell(hostID, cellID)
	e.log.Log(CatMeshCell, "coordinator: released %s from %s", cellID, hostID)
}
