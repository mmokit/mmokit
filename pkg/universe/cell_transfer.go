package universe

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/zenion/mmoserver/pkg/logger"
)

// ═══════════════════════════════════════════════════════════════════════════
// S7 T3 — CellTransferOrchestrator
//
// Coordinator-side state machine that drives cell-topology changes (split,
// merge, live migrate) through the CellTransfer / CellTransferReady /
// CellTransferAbort protocol defined in proto/meshpb/mesh.proto.
//
// This file is the pure Go orchestrator + a pluggable dispatcher interface.
// It deliberately does not touch mesh_control_server.go or mesh_control_client.go:
// wire-level integration happens in T4+ when SplitCell / MergeCell are
// rewired to call the orchestrator. Here we focus on the decision logic,
// in-flight bookkeeping, commit/rollback flow, and timeouts — all of which
// are unit-testable in isolation via a mock dispatcher.
// ═══════════════════════════════════════════════════════════════════════════

// CellTransferKind distinguishes the three flavors of cell-state transfer.
// Mirrors meshpb.CellTransferKind but stays in the Go layer so callers and
// tests don't have to import the proto package for what is essentially an
// enum over domain semantics.
type CellTransferKind int

const (
	CellTransferUnspecified CellTransferKind = iota
	CellTransferSplit
	CellTransferMerge
	CellTransferMigrate
)

func (k CellTransferKind) String() string {
	switch k {
	case CellTransferSplit:
		return "SPLIT"
	case CellTransferMerge:
		return "MERGE"
	case CellTransferMigrate:
		return "MIGRATE"
	default:
		return "UNSPECIFIED"
	}
}

// cellTransferCommand describes a single CellTransfer dispatch, independent
// of the wire encoding. The orchestrator hands one of these to its
// dispatcher for every host-level command it wants sent. When the real
// dispatcher lands in T4 it translates these into meshpb.CellTransfer
// MeshFrames sent via the meshControlServer. The in-test dispatcher just
// records them.
type cellTransferCommand struct {
	RequestID uint64
	Kind      CellTransferKind

	// SrcCellID / DestCellID use the canonical "cell_..." string form
	// produced by MeshCellID. Mapping to/from CellID is the caller's
	// responsibility.
	SrcCellID  string
	DestCellID string

	// SrcHostID is the host that currently owns the source cell and is
	// therefore expected to ship state. DestHostID is where the state
	// should land; for migrates it's the destination host explicitly,
	// for splits it's the rendezvous-chosen owner of the child, and for
	// merges it's the survivor's host.
	SrcHostID  string
	DestHostID string

	// Quadrant is meaningful only for SPLIT (0=BL, 1=BR, 2=TL, 3=TR).
	Quadrant uint32
}

// cellTransferDispatcher is the narrow seam between the orchestrator and
// whatever wire transport delivers CellTransfer commands to hosts. T3 uses
// it from tests with a fake implementation; T4 will add a real one backed
// by meshControlServer.
//
// Dispatch is expected to be non-blocking from the orchestrator's point of
// view: the real implementation enqueues a proto message on an outbound
// stream and returns immediately. A non-nil error reports a synchronous
// failure (e.g. host is no longer registered); the orchestrator treats it
// as an immediate Ready{ok:false} on that command.
//
// DispatchAbort tells the target host to discard a previously-dispatched
// CellTransfer that has not yet committed. Used during rollback.
type cellTransferDispatcher interface {
	Dispatch(cmd cellTransferCommand) error
	DispatchAbort(requestID uint64, hostID string) error
}

// topologyMutation is the concrete change the orchestrator will apply to
// cellToHostMap on successful commit. Captured per-request at BeginXxx time
// so the commit path doesn't have to re-run rendezvous or recompute host
// assignments after dispatch. add/remove keys are the canonical
// MeshCellID() string form.
type topologyMutation struct {
	add    map[string]string // cellID -> hostID
	remove []string          // cellID list to delete
}

// CellTransferRequest is the in-memory record of one in-flight orchestrator
// operation (split / merge / migrate). Lives in orchestrator.inflight from
// Begin* until commit or rollback completes.
//
// Exported so integration code (and tests) can inspect request state, but
// all fields are intentionally read-only for non-orchestrator callers —
// mutation only happens on the orchestrator goroutine under its mu.
type CellTransferRequest struct {
	ID   uint64
	Kind CellTransferKind

	// Cells describes the operation in Go-native terms. For split, SrcCell
	// is the parent and DestCells are the 4 children. For merge, SrcCell
	// is the survivor (parent-id) and DestCells are the 3 donor children
	// shipping state into it — wait, other direction: in push-merge, each
	// donor ships to the survivor, so DestCells is the survivor repeated
	// per donor. We keep it simple: DestCells has one entry per Ready the
	// orchestrator expects, paired with the dispatched command targeting
	// that destination.
	SrcCell   CellID
	DestCells []CellID

	// commands is the list of commands the orchestrator asked its
	// dispatcher to send for this request. len(commands) == ExpectedReady.
	commands []cellTransferCommand

	// ExpectedReady is how many successful CellTransferReady responses we
	// need before committing. For split = 4 (one per child), for merge = 3
	// (one per donor; push-merge convention), for migrate = 1.
	ExpectedReady int

	// receivedOK tracks hosts that have acked with ok=true. If any ack
	// comes back ok=false, the request aborts immediately.
	receivedOK map[string]struct{}

	// Deadline is the absolute wall-clock time after which the request is
	// rolled back if still incomplete. Set from orchestrator.timeout at
	// Begin* time.
	Deadline time.Time

	// mutation captures the cellToHostMap change that commit() will apply.
	mutation topologyMutation

	// Done is closed once the request reaches a terminal state (committed
	// or rolled back). Tests and future integration code can block on it.
	Done chan struct{}

	// Result communicates the terminal outcome. nil == committed.
	Result error
}

// ErrOrchestratorNoHosts is returned when Begin* cannot find any live hosts
// to dispatch to. ErrOrchestratorUnknownCell is returned when the source
// cell is not currently present in cellToHostMap.
var (
	ErrOrchestratorNoHosts      = errors.New("cell transfer orchestrator: no live hosts")
	ErrOrchestratorUnknownCell  = errors.New("cell transfer orchestrator: source cell not in topology")
	ErrOrchestratorUnknownHost  = errors.New("cell transfer orchestrator: unknown host")
	ErrOrchestratorShuttingDown = errors.New("cell transfer orchestrator: shutting down")
)

// defaultTransferTimeout is how long the orchestrator waits for all
// expected Ready responses before rolling a request back. Deliberately
// short for dev — production tuning is a T4+ concern.
const defaultTransferTimeout = 10 * time.Second

// cellTransferOrchestrator owns the inflight-request map and the state
// machine that walks each request from Begin* → all-Ready → commit (or
// Begin* → failure/timeout → rollback). One instance per coordinator
// process, constructed in NewCoordinator and wired into SplitCell/MergeCell
// by T4+.
type cellTransferOrchestrator struct {
	coord      *Coordinator
	dispatcher cellTransferDispatcher
	log        *logger.Logger
	timeout    time.Duration

	mu       sync.Mutex
	nextID   uint64
	inflight map[uint64]*CellTransferRequest

	// commitCount is a simple counter bumped on every successful commit.
	// Used by tests as a cheap spy without having to stub the coordinator.
	commitCount atomic.Uint64
}

// newCellTransferOrchestrator builds an orchestrator bound to the given
// coordinator. The dispatcher is injected so tests can pass a fake;
// production code calls setDispatcher in T4 once the real mesh-backed
// implementation exists. Passing a nil dispatcher is valid: the
// orchestrator just errors on Begin* until one is installed.
func newCellTransferOrchestrator(coord *Coordinator) *cellTransferOrchestrator {
	return &cellTransferOrchestrator{
		coord:    coord,
		log:      coord.Log,
		timeout:  defaultTransferTimeout,
		inflight: make(map[uint64]*CellTransferRequest),
	}
}

// setDispatcher installs or replaces the dispatcher implementation. Safe
// to call before any requests have been issued; tests use it during setup.
func (o *cellTransferOrchestrator) setDispatcher(d cellTransferDispatcher) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.dispatcher = d
}

// setTimeout overrides the default per-request timeout. Used by tests that
// want to exercise the rollback-on-timeout path quickly.
func (o *cellTransferOrchestrator) setTimeout(d time.Duration) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.timeout = d
}

// Start launches the timeout-watcher goroutine. Exits when ctx is
// cancelled. Safe to call multiple times — the ctx-linked goroutine is the
// only background worker, and callers can spawn a second one for tests
// without racing the first. The coordinator lifecycle calls this once from
// Build()/Start() in T4+; today the test suite drives it directly.
func (o *cellTransferOrchestrator) Start(ctx context.Context) {
	go o.timeoutLoop(ctx)
}

// allocateRequestID returns a fresh monotonic request ID. Caller must hold
// o.mu.
func (o *cellTransferOrchestrator) allocateRequestIDLocked() uint64 {
	o.nextID++
	return o.nextID
}

// ───────────────────────────────────────────────────────────────────────────
// BeginSplit
// ───────────────────────────────────────────────────────────────────────────

// BeginSplit kicks off a split of the given parent cell. It computes the 4
// children, runs locality-biased rendezvous to pick a host per child, then
// dispatches one CellTransfer command per child to the current owner of
// the parent cell. The command's DestHostID tells the source host where
// each child should land (which may be itself). Expected Ready count is 4.
//
// Returns immediately after dispatch; the caller can block on
// req.Done to wait for commit/rollback.
func (o *cellTransferOrchestrator) BeginSplit(parent CellID) (*CellTransferRequest, error) {
	o.mu.Lock()
	if o.dispatcher == nil {
		o.mu.Unlock()
		return nil, ErrOrchestratorShuttingDown
	}

	// Snapshot the coordinator's topology under its own lock. We hold
	// both locks briefly while we capture the state we need; rendezvous
	// itself runs outside the coord lock so we don't serialize every
	// rebalance behind an in-flight split.
	o.coord.mu.RLock()
	srcCellKey := MeshCellID(parent)
	srcHost := o.coord.cellToHostMap[srcCellKey]
	liveIDs := o.liveHostIDsLocked()
	ownership := make(map[string]string, len(o.coord.cellToHostMap))
	for k, v := range o.coord.cellToHostMap {
		ownership[k] = v
	}
	o.coord.mu.RUnlock()

	if len(liveIDs) == 0 {
		o.mu.Unlock()
		return nil, ErrOrchestratorNoHosts
	}
	if srcHost == "" {
		o.mu.Unlock()
		return nil, fmt.Errorf("%w: %s", ErrOrchestratorUnknownCell, srcCellKey)
	}

	children := parent.Children()
	childIDs := make([]string, 0, 4)
	for _, ch := range children {
		childIDs = append(childIDs, MeshCellID(ch))
	}

	// Locality callbacks: neighbors of a child are computed in the
	// child's own depth, and cellOwner reads from the snapshot we took
	// above plus the parent itself (since in mid-split the parent still
	// owns the region until commit).
	neighborsOf := func(cellIDStr string) []string {
		cid, err := ParseCellID(cellIDStr)
		if err != nil {
			return nil
		}
		ns := cid.Neighbors()
		out := make([]string, 0, len(ns))
		for _, n := range ns {
			out = append(out, MeshCellID(n))
		}
		return out
	}
	cellOwner := func(cellIDStr string) string {
		if h, ok := ownership[cellIDStr]; ok {
			return h
		}
		return ""
	}

	assignments := AssignCellsAcrossHostsWithLocality(childIDs, liveIDs, neighborsOf, cellOwner)

	reqID := o.allocateRequestIDLocked()
	req := &CellTransferRequest{
		ID:            reqID,
		Kind:          CellTransferSplit,
		SrcCell:       parent,
		DestCells:     children[:],
		ExpectedReady: 4,
		receivedOK:    make(map[string]struct{}, 4),
		Deadline:      time.Now().Add(o.timeout),
		Done:          make(chan struct{}),
		mutation: topologyMutation{
			add:    make(map[string]string, 4),
			remove: []string{srcCellKey},
		},
	}

	for i := range children {
		destKey := childIDs[i]
		destHost := assignments[destKey]
		cmd := cellTransferCommand{
			RequestID:  reqID,
			Kind:       CellTransferSplit,
			SrcCellID:  srcCellKey,
			DestCellID: destKey,
			SrcHostID:  srcHost,
			DestHostID: destHost,
			Quadrant:   uint32(i),
		}
		req.commands = append(req.commands, cmd)
		req.mutation.add[destKey] = destHost
	}

	o.inflight[reqID] = req
	dispatcher := o.dispatcher
	o.mu.Unlock()

	o.log.Log(CatMeshCell, "orchestrator: BeginSplit req=%d parent=%s src-host=%s children=%v",
		reqID, srcCellKey, srcHost, assignments)

	o.dispatchAll(req, dispatcher)
	return req, nil
}

// ───────────────────────────────────────────────────────────────────────────
// BeginMerge
// ───────────────────────────────────────────────────────────────────────────

// BeginMerge collapses the 4 siblings of parentCellID back into their
// shared parent. The survivor host is chosen by rendezvous hashing over
// the parent cell ID across live hosts. The survivor sibling (the one
// whose contents stay in place and get renamed to the parent) is then
// picked by preferring a sibling already owned by the survivor host; if
// none, the first sibling in deterministic order wins.
//
// The other 3 siblings are donors — the orchestrator dispatches one
// CellTransfer{MERGE} command per donor so each donor host pushes its
// state to the survivor. Expected Ready count is 3.
//
// Returns an error if fewer than 4 siblings are in the topology or if no
// hosts are alive.
func (o *cellTransferOrchestrator) BeginMerge(parent CellID) (*CellTransferRequest, error) {
	o.mu.Lock()
	if o.dispatcher == nil {
		o.mu.Unlock()
		return nil, ErrOrchestratorShuttingDown
	}

	o.coord.mu.RLock()
	liveIDs := o.liveHostIDsLocked()
	siblings := parent.Children()
	siblingKeys := [4]string{}
	siblingHosts := [4]string{}
	allPresent := true
	for i, sib := range siblings {
		key := MeshCellID(sib)
		siblingKeys[i] = key
		host, ok := o.coord.cellToHostMap[key]
		if !ok {
			allPresent = false
			break
		}
		siblingHosts[i] = host
	}
	o.coord.mu.RUnlock()

	if len(liveIDs) == 0 {
		o.mu.Unlock()
		return nil, ErrOrchestratorNoHosts
	}
	if !allPresent {
		o.mu.Unlock()
		return nil, fmt.Errorf("%w: merge requires all 4 children of %s", ErrOrchestratorUnknownCell, MeshCellID(parent))
	}

	// Survivor host: rendezvous over the parent key.
	parentKey := MeshCellID(parent)
	survivorHost := AssignCellToHost(parentKey, liveIDs)

	// Pick the survivor sibling: prefer one already living on survivor
	// host. If none (cluster-wide reassignment since last rebalance),
	// pick index 0 as a stable tie-breaker.
	survivorIdx := -1
	for i := 0; i < 4; i++ {
		if siblingHosts[i] == survivorHost {
			survivorIdx = i
			break
		}
	}
	if survivorIdx == -1 {
		survivorIdx = 0
		survivorHost = siblingHosts[0]
	}

	reqID := o.allocateRequestIDLocked()
	req := &CellTransferRequest{
		ID:            reqID,
		Kind:          CellTransferMerge,
		SrcCell:       parent,
		ExpectedReady: 3,
		receivedOK:    make(map[string]struct{}, 3),
		Deadline:      time.Now().Add(o.timeout),
		Done:          make(chan struct{}),
		mutation: topologyMutation{
			add:    map[string]string{parentKey: survivorHost},
			remove: make([]string, 0, 4),
		},
	}
	// All 4 siblings disappear from the map on commit; the parent key
	// replaces them.
	for _, k := range siblingKeys {
		req.mutation.remove = append(req.mutation.remove, k)
	}

	// One command per donor sibling.
	for i := 0; i < 4; i++ {
		if i == survivorIdx {
			continue
		}
		donorKey := siblingKeys[i]
		donorHost := siblingHosts[i]
		cmd := cellTransferCommand{
			RequestID:  reqID,
			Kind:       CellTransferMerge,
			SrcCellID:  donorKey,
			DestCellID: siblingKeys[survivorIdx],
			SrcHostID:  donorHost,
			DestHostID: survivorHost,
		}
		req.commands = append(req.commands, cmd)
		req.DestCells = append(req.DestCells, siblings[i])
	}

	o.inflight[reqID] = req
	dispatcher := o.dispatcher
	o.mu.Unlock()

	o.log.Log(CatMeshCell, "orchestrator: BeginMerge req=%d parent=%s survivor=%s (idx=%d) donors=%d",
		reqID, parentKey, survivorHost, survivorIdx, len(req.commands))

	o.dispatchAll(req, dispatcher)
	return req, nil
}

// ───────────────────────────────────────────────────────────────────────────
// BeginMigrate
// ───────────────────────────────────────────────────────────────────────────

// BeginMigrate moves a cell from its current host to destHost without
// changing the cell ID. Dispatches exactly one CellTransfer{MIGRATE}
// command to the current owner; the source host is responsible for
// shipping state to destHost. Expected Ready count is 1.
//
// Returns an error if the cell isn't in the topology, if destHost isn't
// live, or if destHost already owns the cell (no-op migration).
func (o *cellTransferOrchestrator) BeginMigrate(cellID CellID, destHost string) (*CellTransferRequest, error) {
	o.mu.Lock()
	if o.dispatcher == nil {
		o.mu.Unlock()
		return nil, ErrOrchestratorShuttingDown
	}

	cellKey := MeshCellID(cellID)
	o.coord.mu.RLock()
	srcHost, ok := o.coord.cellToHostMap[cellKey]
	liveIDs := o.liveHostIDsLocked()
	o.coord.mu.RUnlock()

	if !ok {
		o.mu.Unlock()
		return nil, fmt.Errorf("%w: %s", ErrOrchestratorUnknownCell, cellKey)
	}
	hostAlive := false
	for _, h := range liveIDs {
		if h == destHost {
			hostAlive = true
			break
		}
	}
	if !hostAlive {
		o.mu.Unlock()
		return nil, fmt.Errorf("%w: %s", ErrOrchestratorUnknownHost, destHost)
	}
	if srcHost == destHost {
		o.mu.Unlock()
		return nil, fmt.Errorf("cell transfer orchestrator: %s already owned by %s", cellKey, destHost)
	}

	reqID := o.allocateRequestIDLocked()
	req := &CellTransferRequest{
		ID:            reqID,
		Kind:          CellTransferMigrate,
		SrcCell:       cellID,
		DestCells:     []CellID{cellID},
		ExpectedReady: 1,
		receivedOK:    make(map[string]struct{}, 1),
		Deadline:      time.Now().Add(o.timeout),
		Done:          make(chan struct{}),
		mutation: topologyMutation{
			add:    map[string]string{cellKey: destHost},
			remove: nil, // migrate overwrites in place
		},
	}
	req.commands = append(req.commands, cellTransferCommand{
		RequestID:  reqID,
		Kind:       CellTransferMigrate,
		SrcCellID:  cellKey,
		DestCellID: cellKey,
		SrcHostID:  srcHost,
		DestHostID: destHost,
	})

	o.inflight[reqID] = req
	dispatcher := o.dispatcher
	o.mu.Unlock()

	o.log.Log(CatMeshCell, "orchestrator: BeginMigrate req=%d cell=%s %s -> %s",
		reqID, cellKey, srcHost, destHost)

	o.dispatchAll(req, dispatcher)
	return req, nil
}

// ───────────────────────────────────────────────────────────────────────────
// dispatchAll / OnReady / commit / rollback
// ───────────────────────────────────────────────────────────────────────────

// dispatchAll sends every command in req through the dispatcher. Must be
// called with o.mu NOT held — the dispatcher is allowed to be slow and we
// don't want to serialize concurrent Begin*s behind a single send.
//
// If a synchronous Dispatch error occurs, we treat it as an immediate
// Ready{ok=false} for that command. This avoids a class of silently-stuck
// requests when e.g. the target host died between Begin* and Dispatch.
func (o *cellTransferOrchestrator) dispatchAll(req *CellTransferRequest, d cellTransferDispatcher) {
	for _, cmd := range req.commands {
		if err := d.Dispatch(cmd); err != nil {
			o.log.Log(CatMeshCell, "orchestrator: req=%d dispatch to %s failed: %v",
				req.ID, cmd.DestHostID, err)
			o.OnReady(req.ID, cmd.DestCellID, cmd.DestHostID, false, err.Error())
			return // subsequent Dispatches are moot once we're rolling back
		}
	}
}

// OnReady is the entry point the future real dispatcher calls when a
// target host replies with CellTransferReady on its mesh control stream.
// For T3 the unit tests drive it directly.
//
// Semantics:
//   - unknown requestID → dropped (post-terminal arrivals are benign)
//   - ok=false → immediate rollback
//   - ok=true and this is the final expected Ready → commit
//   - ok=true otherwise → accumulate and wait
//
// destCellID is used for logging and to disambiguate readies in tests;
// hostID identifies the replying host.
func (o *cellTransferOrchestrator) OnReady(requestID uint64, destCellID, hostID string, ok bool, errMsg string) {
	o.mu.Lock()
	req, exists := o.inflight[requestID]
	if !exists {
		o.mu.Unlock()
		return
	}
	if !ok {
		delete(o.inflight, requestID)
		o.mu.Unlock()
		o.log.Log(CatMeshCell, "orchestrator: req=%d cell=%s host=%s FAILED: %s",
			requestID, destCellID, hostID, errMsg)
		o.rollback(req, fmt.Sprintf("host %s rejected: %s", hostID, errMsg))
		return
	}
	// Key readies by (hostID, destCellID) so duplicate replies from the
	// same host for the same dest don't advance the counter twice — the
	// split path has 4 dest cells on the same src host, merge has up to
	// 3 donor hosts writing to the same survivor, migrate has 1 of each.
	key := hostID + "|" + destCellID
	if _, dup := req.receivedOK[key]; dup {
		o.mu.Unlock()
		return
	}
	req.receivedOK[key] = struct{}{}
	if len(req.receivedOK) < req.ExpectedReady {
		o.mu.Unlock()
		return
	}
	// Terminal: all expected readies received.
	delete(o.inflight, requestID)
	o.mu.Unlock()
	o.commit(req)
}

// commit atomically applies the request's topology mutation to
// Coordinator.cellToHostMap and signals req.Done.
func (o *cellTransferOrchestrator) commit(req *CellTransferRequest) {
	o.coord.applyCellTopologyChange(req.mutation)
	o.commitCount.Add(1)
	req.Result = nil
	close(req.Done)
	o.log.Log(CatMeshCell, "orchestrator: req=%d %s committed", req.ID, req.Kind)
}

// rollback fires CellTransferAbort on every host that has already replied
// Ready (they hold transfer state that must be discarded), logs the
// failure, and signals req.Done with an error. No topology change is
// applied — the pre-Begin state remains authoritative.
//
// Hosts that failed Ready or never replied do not need abort (they either
// already reported failure or were unresponsive); we only target hosts
// whose receivedOK entry is set.
func (o *cellTransferOrchestrator) rollback(req *CellTransferRequest, reason string) {
	o.mu.Lock()
	d := o.dispatcher
	// Remove inflight defensively — caller usually did this already, but
	// timeoutLoop calls rollback directly.
	delete(o.inflight, req.ID)
	// Build the hostID set that has already ack'd: keys in receivedOK are
	// "host|cellID" so split out the host prefix.
	targets := make(map[string]struct{}, len(req.receivedOK))
	for key := range req.receivedOK {
		if sep := strings.IndexByte(key, '|'); sep >= 0 {
			targets[key[:sep]] = struct{}{}
		}
	}
	o.mu.Unlock()

	if d != nil {
		for hostID := range targets {
			if err := d.DispatchAbort(req.ID, hostID); err != nil {
				o.log.Log(CatMeshCell, "orchestrator: req=%d abort to %s failed: %v",
					req.ID, hostID, err)
			}
		}
	}
	req.Result = errors.New(reason)
	close(req.Done)
	o.log.Log(CatMeshCell, "orchestrator: req=%d %s ROLLBACK: %s", req.ID, req.Kind, reason)
}

// ───────────────────────────────────────────────────────────────────────────
// timeout watcher
// ───────────────────────────────────────────────────────────────────────────

// timeoutLoop scans inflight every timeoutPollInterval and rolls back any
// request whose deadline has passed. Exits when ctx is cancelled.
func (o *cellTransferOrchestrator) timeoutLoop(ctx context.Context) {
	tick := time.NewTicker(timeoutPollInterval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			o.scanTimeouts()
		}
	}
}

// scanTimeouts is the per-tick body of timeoutLoop, factored out so tests
// can drive it directly without waiting on a real ticker. Each timed-out
// request is removed from inflight and rolled back with a timeout reason.
func (o *cellTransferOrchestrator) scanTimeouts() {
	now := time.Now()
	o.mu.Lock()
	var expired []*CellTransferRequest
	for id, req := range o.inflight {
		if now.After(req.Deadline) {
			expired = append(expired, req)
			delete(o.inflight, id)
		}
	}
	o.mu.Unlock()
	for _, req := range expired {
		o.rollback(req, fmt.Sprintf("timeout after %s", o.timeout))
	}
}

// timeoutPollInterval is intentionally short-but-not-microscopic: 50ms is
// fast enough that tests with 100ms timeouts observe rollback within a
// couple of polls, and light enough that it's invisible in prod.
const timeoutPollInterval = 50 * time.Millisecond

// ───────────────────────────────────────────────────────────────────────────
// helpers
// ───────────────────────────────────────────────────────────────────────────

// liveHostIDsLocked returns the currently-live host IDs from the
// coordinator's host registry. Callers must hold o.coord.mu (reader
// suffices). Falls back to scanning cellToHostMap when hostRegistry is nil
// — tests that stub out the registry configure ownership directly.
func (o *cellTransferOrchestrator) liveHostIDsLocked() []string {
	if o.coord.hostRegistry != nil {
		live := o.coord.hostRegistry.LiveHosts()
		ids := make([]string, 0, len(live))
		for _, h := range live {
			if h.State == RemoteHostRegistered || h.State == RemoteHostLive {
				ids = append(ids, h.ID)
			}
		}
		if len(ids) > 0 {
			return ids
		}
	}
	// Fallback: derive from the ownership map. Used by unit tests that
	// pre-seed cellToHostMap without wiring a full HostRegistry.
	seen := make(map[string]struct{}, len(o.coord.cellToHostMap))
	var ids []string
	for _, h := range o.coord.cellToHostMap {
		if _, ok := seen[h]; ok {
			continue
		}
		seen[h] = struct{}{}
		ids = append(ids, h)
	}
	return ids
}

// ═══════════════════════════════════════════════════════════════════════════
// Coordinator.applyCellTopologyChange — T3 stub
//
// This is the atomic-commit hook the orchestrator calls after all Ready
// responses arrive. In T3 we only update cellToHostMap (and nothing else)
// under the coordinator's write lock. This is enough for the orchestrator
// unit tests and leaves a clearly-marked integration point for T9, which
// will extend this to rebuild Topology, remap sessionRoutes to the new
// cell owners, and trigger a PeerList broadcast.
//
// Keeping the stub on Coordinator (rather than on the orchestrator itself)
// lets T9 replace it in place without re-touching cell_transfer.go.
// ═══════════════════════════════════════════════════════════════════════════

// applyCellTopologyChange mutates cellToHostMap per the given mutation.
// Removals are processed first so that a migrate (which both adds and
// does not remove) overwrites cleanly, and a split's parent->children
// swap is atomic from any reader's perspective (both steps happen under
// the same lock).
//
// TODO(T9): extend to (a) rebuild c.Topology for split/merge so
// BoundarySystem picks up the new cell ids, (b) call
// sessionRoutes.Remap on every affected cell key so in-flight gateway
// inputs route to the new host, and (c) trigger
// assignmentEngine.broadcastPeerList() so standalone gateways and remote
// hosts learn the new owner.
func (c *Coordinator) applyCellTopologyChange(m topologyMutation) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, k := range m.remove {
		delete(c.cellToHostMap, k)
	}
	for k, v := range m.add {
		c.cellToHostMap[k] = v
	}
}
