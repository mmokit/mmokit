package universe

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// ═══════════════════════════════════════════════════════════════════════════
// S7 T3 — cellTransferOrchestrator unit tests
//
// These tests drive the orchestrator through its state machine with a mock
// dispatcher that just records commands. Readies are synthesized by
// calling orch.OnReady directly. No mesh control server, no gRPC, no
// goroutines apart from the orchestrator's own timeout loop where needed.
// ═══════════════════════════════════════════════════════════════════════════

// fakeDispatcher records every command sent through it and every abort
// issued by the orchestrator during rollback. Tests assert on the recorded
// sequences.
type fakeDispatcher struct {
	mu     sync.Mutex
	sent   []cellTransferCommand
	aborts []fakeAbort
	// sendErr, if non-nil, causes Dispatch to synchronously fail with this
	// error on every call.
	sendErr error
}

type fakeAbort struct {
	requestID uint64
	hostID    string
}

func (f *fakeDispatcher) Dispatch(cmd cellTransferCommand) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sendErr != nil {
		return f.sendErr
	}
	f.sent = append(f.sent, cmd)
	return nil
}

func (f *fakeDispatcher) DispatchAbort(requestID uint64, hostID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.aborts = append(f.aborts, fakeAbort{requestID, hostID})
	return nil
}

func (f *fakeDispatcher) sentSnapshot() []cellTransferCommand {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]cellTransferCommand, len(f.sent))
	copy(out, f.sent)
	return out
}

func (f *fakeDispatcher) abortSnapshot() []fakeAbort {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]fakeAbort, len(f.aborts))
	copy(out, f.aborts)
	return out
}

// newTestOrchestrator builds a minimally-wired Process + orchestrator
// with cellToHostMap pre-seeded with the given ownership. Suitable for unit
// tests: no mesh control server, no gRPC, no assignment engine.
func newTestOrchestrator(t *testing.T, ownership map[string]string) (*cellTransferOrchestrator, *fakeDispatcher, *Process) {
	t.Helper()
	coord := New(Config{CellsX: 2, CellsY: 2, Headless: true})
	for k, v := range ownership {
		coord.cellToHostMap[k] = v
	}
	disp := &fakeDispatcher{}
	coord.orchestrator.setDispatcher(disp)
	return coord.orchestrator, disp, coord
}

// ───────────────────────────────────────────────────────────────────────────
// TestOrchestratorBeginSplitDispatches4Children
// ───────────────────────────────────────────────────────────────────────────

func TestOrchestratorBeginSplitDispatches4Children(t *testing.T) {
	parent := CellID{X: 0, Y: 0, Depth: 0}
	parentKey := MeshCellID(parent)

	// Pre-seed with parent owned by host-a, siblings in the topology too
	// so locality kicks in without creating unowned-cell errors.
	orch, disp, _ := newTestOrchestrator(t, map[string]string{
		parentKey:                                   "host-a",
		MeshCellID(CellID{X: 1, Y: 0, Depth: 0}):    "host-b",
		MeshCellID(CellID{X: 0, Y: 1, Depth: 0}):    "host-b",
		MeshCellID(CellID{X: 1, Y: 1, Depth: 0}):    "host-a",
	})

	req, err := orch.BeginSplit(parent)
	if err != nil {
		t.Fatalf("BeginSplit: %v", err)
	}
	if req.Kind != CellTransferSplit {
		t.Errorf("kind=%v want SPLIT", req.Kind)
	}
	if req.ExpectedReady != 4 {
		t.Errorf("ExpectedReady=%d want 4", req.ExpectedReady)
	}
	sent := disp.sentSnapshot()
	if len(sent) != 4 {
		t.Fatalf("dispatched %d cmds, want 4", len(sent))
	}

	// Every command should reference the parent as src and carry a
	// unique child dest with a quadrant in [0,3].
	seenQuadrants := map[uint32]bool{}
	seenDests := map[string]bool{}
	for _, cmd := range sent {
		if cmd.SrcCellID != parentKey {
			t.Errorf("cmd src=%s want %s", cmd.SrcCellID, parentKey)
		}
		if cmd.SrcHostID != "host-a" {
			t.Errorf("cmd src host=%s want host-a", cmd.SrcHostID)
		}
		if cmd.RequestID != req.ID {
			t.Errorf("cmd req=%d want %d", cmd.RequestID, req.ID)
		}
		if cmd.DestHostID == "" {
			t.Errorf("cmd dest host empty")
		}
		if seenQuadrants[cmd.Quadrant] {
			t.Errorf("duplicate quadrant %d", cmd.Quadrant)
		}
		seenQuadrants[cmd.Quadrant] = true
		seenDests[cmd.DestCellID] = true
	}
	if len(seenDests) != 4 {
		t.Errorf("unique dests=%d want 4", len(seenDests))
	}
}

// ───────────────────────────────────────────────────────────────────────────
// TestOrchestratorCommitsOnAllReady
// ───────────────────────────────────────────────────────────────────────────

func TestOrchestratorCommitsOnAllReady(t *testing.T) {
	parent := CellID{X: 2, Y: 2, Depth: 0}
	parentKey := MeshCellID(parent)
	orch, disp, coord := newTestOrchestrator(t, map[string]string{
		parentKey: "host-a",
	})

	req, err := orch.BeginSplit(parent)
	if err != nil {
		t.Fatalf("BeginSplit: %v", err)
	}
	if got := orch.commitCount.Load(); got != 0 {
		t.Fatalf("pre-commit count=%d want 0", got)
	}

	sent := disp.sentSnapshot()
	if len(sent) != 4 {
		t.Fatalf("sent=%d want 4", len(sent))
	}
	for _, cmd := range sent {
		orch.OnReady(req.ID, cmd.DestCellID, cmd.DestHostID, true, "")
	}

	select {
	case <-req.Done:
	case <-time.After(time.Second):
		t.Fatalf("req.Done did not close")
	}
	if req.Result != nil {
		t.Errorf("Result=%v want nil", req.Result)
	}
	if got := orch.commitCount.Load(); got != 1 {
		t.Errorf("commit count=%d want 1", got)
	}
	// Parent should have been removed from cellToHostMap and 4 children
	// added.
	coord.mu.RLock()
	_, parentStillThere := coord.cellToHostMap[parentKey]
	childCount := 0
	children := parent.Children()
	for _, ch := range children {
		if _, ok := coord.cellToHostMap[MeshCellID(ch)]; ok {
			childCount++
		}
	}
	coord.mu.RUnlock()
	if parentStillThere {
		t.Errorf("parent %s still in cellToHostMap after commit", parentKey)
	}
	if childCount != 4 {
		t.Errorf("children after commit=%d want 4", childCount)
	}
}

// ───────────────────────────────────────────────────────────────────────────
// TestOrchestratorRollsBackOnFailure
// ───────────────────────────────────────────────────────────────────────────

func TestOrchestratorRollsBackOnFailure(t *testing.T) {
	parent := CellID{X: 3, Y: 3, Depth: 0}
	parentKey := MeshCellID(parent)
	orch, disp, coord := newTestOrchestrator(t, map[string]string{
		parentKey: "host-a",
	})

	req, err := orch.BeginSplit(parent)
	if err != nil {
		t.Fatalf("BeginSplit: %v", err)
	}
	sent := disp.sentSnapshot()
	if len(sent) != 4 {
		t.Fatalf("sent=%d want 4", len(sent))
	}

	// Three hosts ack OK, then the fourth fails. Abort should fire on
	// the first three destinations (and only the first three).
	for i := 0; i < 3; i++ {
		orch.OnReady(req.ID, sent[i].DestCellID, sent[i].DestHostID, true, "")
	}
	orch.OnReady(req.ID, sent[3].DestCellID, sent[3].DestHostID, false, "disk full")

	select {
	case <-req.Done:
	case <-time.After(time.Second):
		t.Fatalf("req.Done did not close")
	}
	if req.Result == nil || !strings.Contains(req.Result.Error(), "disk full") {
		t.Errorf("Result=%v want error containing 'disk full'", req.Result)
	}
	// cellToHostMap unchanged — parent still there, no children added.
	coord.mu.RLock()
	_, parentStillThere := coord.cellToHostMap[parentKey]
	coord.mu.RUnlock()
	if !parentStillThere {
		t.Errorf("parent removed from cellToHostMap on failed split")
	}
	if got := orch.commitCount.Load(); got != 0 {
		t.Errorf("commit count=%d want 0", got)
	}
	// Three aborts should have fired (one per host that already
	// acked OK). Multiple hosts in this test share the same src host
	// though, so we dedupe by unique hostID.
	aborts := disp.abortSnapshot()
	uniqueAbortHosts := map[string]bool{}
	for _, a := range aborts {
		if a.requestID != req.ID {
			t.Errorf("abort reqID=%d want %d", a.requestID, req.ID)
		}
		uniqueAbortHosts[a.hostID] = true
	}
	// Split always dispatches to the src host for all 4 children — the
	// parent owner ships state to itself for quadrants that stay local.
	// So in the single-host fallback case only 1 unique host sees aborts.
	// Check that we got at least 1 and no more than 3 (one per acked OK).
	if len(uniqueAbortHosts) == 0 {
		t.Errorf("expected at least 1 abort, got 0")
	}
	if len(aborts) > 3 {
		t.Errorf("abort count=%d want <=3", len(aborts))
	}
	// Inflight map should be clean.
	orch.mu.Lock()
	if _, ok := orch.inflight[req.ID]; ok {
		t.Errorf("inflight entry not cleaned up")
	}
	orch.mu.Unlock()
}

// ───────────────────────────────────────────────────────────────────────────
// TestOrchestratorTimeoutTriggersRollback
// ───────────────────────────────────────────────────────────────────────────

func TestOrchestratorTimeoutTriggersRollback(t *testing.T) {
	parent := CellID{X: 4, Y: 4, Depth: 0}
	parentKey := MeshCellID(parent)
	orch, disp, _ := newTestOrchestrator(t, map[string]string{
		parentKey: "host-a",
	})
	orch.setTimeout(50 * time.Millisecond)

	req, err := orch.BeginSplit(parent)
	if err != nil {
		t.Fatalf("BeginSplit: %v", err)
	}
	// Don't reply at all. Drive the timeout scanner directly after a
	// short wait so we don't race the real ticker.
	time.Sleep(100 * time.Millisecond)
	orch.scanTimeouts()

	select {
	case <-req.Done:
	case <-time.After(time.Second):
		t.Fatalf("req.Done did not close after timeout")
	}
	if req.Result == nil || !strings.Contains(req.Result.Error(), "timeout") {
		t.Errorf("Result=%v want timeout error", req.Result)
	}
	// No acks were received, so no aborts should have been dispatched.
	if aborts := disp.abortSnapshot(); len(aborts) != 0 {
		t.Errorf("unexpected aborts on timeout: %v", aborts)
	}
	if got := orch.commitCount.Load(); got != 0 {
		t.Errorf("commit count=%d want 0", got)
	}
}

// ───────────────────────────────────────────────────────────────────────────
// TestOrchestratorBeginMergeDispatches4Donors — actually 3 donors (merge
// convention: survivor stays put, 3 donors ship to survivor).
// ───────────────────────────────────────────────────────────────────────────

func TestOrchestratorBeginMergeDispatches4Donors(t *testing.T) {
	// Start with 4 sibling children of a depth-0 parent.
	parent := CellID{X: 5, Y: 5, Depth: 0}
	children := parent.Children()
	ownership := make(map[string]string, 4)
	for i, ch := range children {
		// Sprinkle across a few hosts so at least one sibling is on each
		// candidate survivor.
		host := "host-a"
		if i%2 == 1 {
			host = "host-b"
		}
		ownership[MeshCellID(ch)] = host
	}
	orch, disp, _ := newTestOrchestrator(t, ownership)

	req, err := orch.BeginMerge(parent)
	if err != nil {
		t.Fatalf("BeginMerge: %v", err)
	}
	if req.Kind != CellTransferMerge {
		t.Errorf("kind=%v want MERGE", req.Kind)
	}
	if req.ExpectedReady != 3 {
		t.Errorf("ExpectedReady=%d want 3", req.ExpectedReady)
	}
	sent := disp.sentSnapshot()
	if len(sent) != 3 {
		t.Fatalf("sent=%d want 3", len(sent))
	}

	// Every command has the same DestCellID (the survivor sibling) and
	// the same DestHostID, but distinct SrcCellIDs (the 3 donors).
	destKey := sent[0].DestCellID
	destHost := sent[0].DestHostID
	donorKeys := map[string]bool{}
	for _, cmd := range sent {
		if cmd.DestCellID != destKey {
			t.Errorf("dest mismatch: %s vs %s", cmd.DestCellID, destKey)
		}
		if cmd.DestHostID != destHost {
			t.Errorf("dest host mismatch: %s vs %s", cmd.DestHostID, destHost)
		}
		if cmd.RequestID != req.ID {
			t.Errorf("reqID=%d want %d", cmd.RequestID, req.ID)
		}
		donorKeys[cmd.SrcCellID] = true
	}
	if len(donorKeys) != 3 {
		t.Errorf("unique donors=%d want 3", len(donorKeys))
	}
	// None of the donors should equal the survivor.
	if donorKeys[destKey] {
		t.Errorf("survivor %s listed as donor", destKey)
	}
}

// ───────────────────────────────────────────────────────────────────────────
// TestOrchestratorBeginMigrateSingleDispatch
// ───────────────────────────────────────────────────────────────────────────

func TestOrchestratorBeginMigrateSingleDispatch(t *testing.T) {
	cell := CellID{X: 7, Y: 7, Depth: 0}
	cellKey := MeshCellID(cell)
	orch, disp, coord := newTestOrchestrator(t, map[string]string{
		cellKey:                                  "host-a",
		MeshCellID(CellID{X: 8, Y: 7, Depth: 0}): "host-b",
	})

	req, err := orch.BeginMigrate(cell, "host-b")
	if err != nil {
		t.Fatalf("BeginMigrate: %v", err)
	}
	if req.Kind != CellTransferMigrate {
		t.Errorf("kind=%v want MIGRATE", req.Kind)
	}
	if req.ExpectedReady != 1 {
		t.Errorf("ExpectedReady=%d want 1", req.ExpectedReady)
	}
	sent := disp.sentSnapshot()
	if len(sent) != 1 {
		t.Fatalf("sent=%d want 1", len(sent))
	}
	cmd := sent[0]
	if cmd.SrcCellID != cellKey || cmd.DestCellID != cellKey {
		t.Errorf("migrate src=%s dest=%s want both %s", cmd.SrcCellID, cmd.DestCellID, cellKey)
	}
	if cmd.SrcHostID != "host-a" || cmd.DestHostID != "host-b" {
		t.Errorf("migrate hosts: src=%s dest=%s want host-a/host-b", cmd.SrcHostID, cmd.DestHostID)
	}

	// Ack and verify commit: cell ownership flips to host-b.
	orch.OnReady(req.ID, cmd.DestCellID, cmd.DestHostID, true, "")
	select {
	case <-req.Done:
	case <-time.After(time.Second):
		t.Fatalf("req.Done did not close")
	}
	coord.mu.RLock()
	owner := coord.cellToHostMap[cellKey]
	coord.mu.RUnlock()
	if owner != "host-b" {
		t.Errorf("post-commit owner=%s want host-b", owner)
	}

	// Error paths: migrating to a dead host, or to the current owner, or
	// a cell not in the topology.
	if _, err := orch.BeginMigrate(cell, "host-b"); err == nil {
		t.Errorf("expected error migrating to current owner")
	}
	unknown := CellID{X: 99, Y: 99, Depth: 0}
	if _, err := orch.BeginMigrate(unknown, "host-a"); err == nil {
		t.Errorf("expected error migrating unknown cell")
	}
	if _, err := orch.BeginMigrate(cell, "host-ghost"); err == nil {
		t.Errorf("expected error migrating to unknown host")
	}
}

// ───────────────────────────────────────────────────────────────────────────
// TestOrchestratorConcurrentRequests
// ───────────────────────────────────────────────────────────────────────────

func TestOrchestratorConcurrentRequests(t *testing.T) {
	parent1 := CellID{X: 10, Y: 10, Depth: 0}
	parent2 := CellID{X: 20, Y: 20, Depth: 0}
	orch, disp, _ := newTestOrchestrator(t, map[string]string{
		MeshCellID(parent1): "host-a",
		MeshCellID(parent2): "host-b",
	})

	req1, err := orch.BeginSplit(parent1)
	if err != nil {
		t.Fatalf("BeginSplit parent1: %v", err)
	}
	req2, err := orch.BeginSplit(parent2)
	if err != nil {
		t.Fatalf("BeginSplit parent2: %v", err)
	}
	if req1.ID == req2.ID {
		t.Fatalf("reqIDs collided: %d", req1.ID)
	}

	sent := disp.sentSnapshot()
	if len(sent) != 8 {
		t.Fatalf("sent=%d want 8 (4+4)", len(sent))
	}

	// Collect req1 and req2 commands.
	var sent1, sent2 []cellTransferCommand
	for _, cmd := range sent {
		switch cmd.RequestID {
		case req1.ID:
			sent1 = append(sent1, cmd)
		case req2.ID:
			sent2 = append(sent2, cmd)
		}
	}
	if len(sent1) != 4 || len(sent2) != 4 {
		t.Fatalf("sent1=%d sent2=%d want 4/4", len(sent1), len(sent2))
	}

	// Ack all of req1. req2 should still be inflight.
	for _, cmd := range sent1 {
		orch.OnReady(req1.ID, cmd.DestCellID, cmd.DestHostID, true, "")
	}
	select {
	case <-req1.Done:
	case <-time.After(time.Second):
		t.Fatalf("req1.Done did not close")
	}
	select {
	case <-req2.Done:
		t.Fatalf("req2.Done closed prematurely")
	default:
	}
	orch.mu.Lock()
	if _, ok := orch.inflight[req2.ID]; !ok {
		t.Errorf("req2 prematurely removed from inflight")
	}
	orch.mu.Unlock()

	// Now ack req2 and verify it also commits.
	for _, cmd := range sent2 {
		orch.OnReady(req2.ID, cmd.DestCellID, cmd.DestHostID, true, "")
	}
	select {
	case <-req2.Done:
	case <-time.After(time.Second):
		t.Fatalf("req2.Done did not close")
	}
	if got := orch.commitCount.Load(); got != 2 {
		t.Errorf("commit count=%d want 2", got)
	}
}

// ───────────────────────────────────────────────────────────────────────────
// Timeout loop goroutine smoke test — verifies Start() wires up the
// ticker and rolls back requests without manual scanTimeouts() prodding.
// Kept short and independent of the main test matrix.
// ───────────────────────────────────────────────────────────────────────────

// TestOrchestratorBeginWithoutDispatcher verifies that calling Begin*
// before a dispatcher has been installed returns the pre-init sentinel, not
// the shutdown sentinel.
func TestOrchestratorBeginWithoutDispatcher(t *testing.T) {
	coord := New(Config{CellsX: 2, CellsY: 2, Headless: true})
	orch := coord.orchestrator
	// New installs the real dispatcher automatically in T4+;
	// clear it to exercise the pre-init sentinel path.
	orch.setDispatcher(nil)
	cell := CellID{X: 0, Y: 0, Depth: 0}
	coord.cellToHostMap[MeshCellID(cell)] = "host-a"
	coord.cellToHostMap[MeshCellID(CellID{X: 1, Y: 0, Depth: 0})] = "host-b"

	if _, err := orch.BeginMigrate(cell, "host-b"); !errors.Is(err, ErrOrchestratorNoDispatcher) {
		t.Errorf("BeginMigrate err=%v want ErrOrchestratorNoDispatcher", err)
	}
	if _, err := orch.BeginSplit(cell); !errors.Is(err, ErrOrchestratorNoDispatcher) {
		t.Errorf("BeginSplit err=%v want ErrOrchestratorNoDispatcher", err)
	}
	parent := CellID{X: 0, Y: 0, Depth: 0}
	if _, err := orch.BeginMerge(parent); !errors.Is(err, ErrOrchestratorNoDispatcher) {
		t.Errorf("BeginMerge err=%v want ErrOrchestratorNoDispatcher", err)
	}
}

func TestOrchestratorTimeoutLoopGoroutine(t *testing.T) {
	parent := CellID{X: 30, Y: 30, Depth: 0}
	orch, _, _ := newTestOrchestrator(t, map[string]string{
		MeshCellID(parent): "host-a",
	})
	orch.setTimeout(80 * time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	orch.Start(ctx)

	req, err := orch.BeginSplit(parent)
	if err != nil {
		t.Fatalf("BeginSplit: %v", err)
	}
	select {
	case <-req.Done:
	case <-time.After(2 * time.Second):
		t.Fatalf("req.Done did not close via background timeout loop")
	}
	if req.Result == nil {
		t.Errorf("Result nil after timeout rollback")
	}
}

// ───────────────────────────────────────────────────────────────────────────
// TestS7RollbackOnTimeout (T10) — force a migrate request to time out with
// a dispatcher that never fires OnReady, assert the orchestrator rolls the
// request back, and verify the pre-Begin state (cellToHostMap, CellOwner)
// is untouched. The critical invariant post-rollback is that the source
// cell retains authority — the topology mutation is NEVER applied when
// the destination host fails to respond in time.
//
// This extends TestOrchestratorTimeoutTriggersRollback, which only
// exercises SPLIT. MIGRATE has a different state-machine shape
// (ExpectedReady=1, no child cells to allocate) and is the path the live
// `cell migrate` admin command uses — worth its own regression.
// ───────────────────────────────────────────────────────────────────────────

func TestS7RollbackOnTimeout(t *testing.T) {
	cellID := CellID{X: 42, Y: 17, Depth: 0}
	cellKey := MeshCellID(cellID)

	// Seed a 2-host ownership map with the source cell on host-a.
	orch, disp, coord := newTestOrchestrator(t, map[string]string{
		cellKey:                                  "host-a",
		MeshCellID(CellID{X: 43, Y: 17}):         "host-b",
	})

	// The test dispatcher records the Dispatch call but never calls
	// OnReady — simulating a target host that accepts the CellTransfer
	// but then silently stalls. With a sub-second timeout the orchestrator
	// should roll the request back via scanTimeouts.
	orch.setTimeout(50 * time.Millisecond)

	// Pre-rollback ownership snapshot so we can diff after the timeout.
	coord.mu.RLock()
	preOwnership := make(map[string]string, len(coord.cellToHostMap))
	for k, v := range coord.cellToHostMap {
		preOwnership[k] = v
	}
	coord.mu.RUnlock()

	req, err := orch.BeginMigrate(cellID, "host-b")
	if err != nil {
		t.Fatalf("BeginMigrate: %v", err)
	}
	if req.Kind != CellTransferMigrate {
		t.Errorf("kind=%v want MIGRATE", req.Kind)
	}

	// The dispatcher should have seen exactly one Dispatch call (migrate
	// has ExpectedReady=1).
	if sent := disp.sentSnapshot(); len(sent) != 1 {
		t.Fatalf("dispatch count=%d want 1", len(sent))
	}

	// Wait long enough for the deadline to lapse, then prod the
	// scanner directly so we don't race the real ticker.
	time.Sleep(100 * time.Millisecond)
	orch.scanTimeouts()

	// Invariant 1: req.Done closes with a non-nil timeout error.
	select {
	case <-req.Done:
	case <-time.After(2 * time.Second):
		t.Fatalf("req.Done did not close after timeout")
	}
	if req.Result == nil {
		t.Fatal("post-timeout: req.Result is nil, want timeout error")
	}
	if !strings.Contains(req.Result.Error(), "timeout") {
		t.Errorf("Result=%v want timeout error", req.Result)
	}

	// Invariant 2: source cell still exists in cellToHostMap with its
	// original owner. Rollback must not flip ownership under any
	// circumstances — migrate's committed mutation flips host-a→host-b,
	// and we're verifying the rollback path left that alone.
	coord.mu.RLock()
	postOwner := coord.cellToHostMap[cellKey]
	coord.mu.RUnlock()
	if postOwner != "host-a" {
		t.Errorf("post-rollback: cellToHostMap[%s] = %q, want host-a", cellKey, postOwner)
	}

	// Invariant 3: full cellToHostMap is byte-identical to the pre-state.
	// Rollback only frees the inflight record; no topology side effects.
	coord.mu.RLock()
	postOwnership := make(map[string]string, len(coord.cellToHostMap))
	for k, v := range coord.cellToHostMap {
		postOwnership[k] = v
	}
	coord.mu.RUnlock()
	if len(postOwnership) != len(preOwnership) {
		t.Errorf("post-rollback: cellToHostMap size changed %d -> %d",
			len(preOwnership), len(postOwnership))
	}
	for k, v := range preOwnership {
		if got := postOwnership[k]; got != v {
			t.Errorf("post-rollback: cellToHostMap[%s] = %q, want %q (pre-state)", k, got, v)
		}
	}

	// Invariant 4: no aborts were dispatched — the dispatcher never
	// fired OnReady, so the orchestrator has no host in receivedOK to
	// target. The rollback path must be purely coordinator-local.
	if aborts := disp.abortSnapshot(); len(aborts) != 0 {
		t.Errorf("post-rollback: %d abort(s) dispatched, want 0 (nobody ack'd)", len(aborts))
	}

	// Invariant 5: inflight map is clean.
	orch.mu.Lock()
	if _, stillInflight := orch.inflight[req.ID]; stillInflight {
		t.Errorf("post-rollback: inflight[%d] still present", req.ID)
	}
	orch.mu.Unlock()

	// Invariant 6: commit counter is 0 — no commit ran.
	if got := orch.commitCount.Load(); got != 0 {
		t.Errorf("post-rollback: commitCount=%d want 0", got)
	}
}

// ───────────────────────────────────────────────────────────────────────────
// TestSnapshotOwnershipFromCommands
//
// Regression test for the pure-coordinator migrate drift. On a process
// running only `coordinator` (or coordinator+gateway) with all hosts remote,
// cellToHostMap is never populated — remote-host cell ownership lives only
// in hostRegistry, updated through CellReady messages. BeginMigrate
// correctly reads srcHost via HostForCellID (which consults both) and bakes
// it into req.commands[].SrcHostID, but applyMigrateCommit's ownership
// snapshot used to read cellToHostMap directly, returning "" on pure-coord
// processes. That empty string then caused sendCellRelease("",…) to fail
// with "no control stream for host \"\"" and applyRegistryDelta to skip
// ReleaseCell (prev != "" guard), so every migrate leaked a duplicate
// ownership entry and left the source host's game loop running.
//
// The fix: snapshotOwnershipLocked now treats req.commands[].SrcHostID as
// authoritative, with cellToHostMap as a fallback only.
// ───────────────────────────────────────────────────────────────────────────

func TestSnapshotOwnershipFromCommands(t *testing.T) {
	coord := New(Config{CellsX: 2, CellsY: 2, Headless: true})
	// Intentionally do NOT populate cellToHostMap — simulating a pure
	// coordinator process whose ownership truth is elsewhere (hostRegistry
	// for remote hosts), with commands[].SrcHostID as the canonical
	// pre-mutation owner baked at BeginXxx time.

	cellKey := MeshCellID(CellID{X: 1, Y: 1, Depth: 0})
	req := &CellTransferRequest{
		Kind: CellTransferMigrate,
		commands: []cellTransferCommand{{
			RequestID:  42,
			Kind:       CellTransferMigrate,
			SrcCellID:  cellKey,
			DestCellID: cellKey,
			SrcHostID:  "test-node-0",
			DestHostID: "test-node-3",
		}},
		mutation: topologyMutation{
			add: map[string]string{cellKey: "test-node-3"},
		},
	}

	coord.mu.Lock()
	preOwnership := coord.snapshotOwnershipLocked(req)
	coord.mu.Unlock()

	if got := preOwnership[cellKey]; got != "test-node-0" {
		t.Errorf("preOwnership[%s] = %q, want %q — snapshotOwnershipLocked must "+
			"read commands[].SrcHostID when cellToHostMap is empty",
			cellKey, got, "test-node-0")
	}
}

// TestSnapshotOwnershipCommandsBeatCellToHostMap verifies that when both
// sources have an entry, commands[].SrcHostID wins. Guards against a future
// regression where someone flips the fallback priority and makes a stale
// cellToHostMap entry shadow the authoritative value.
func TestSnapshotOwnershipCommandsBeatCellToHostMap(t *testing.T) {
	coord := New(Config{CellsX: 2, CellsY: 2, Headless: true})
	cellKey := MeshCellID(CellID{X: 1, Y: 1, Depth: 0})
	// Simulate a stale cellToHostMap entry pointing at the wrong host.
	coord.cellToHostMap[cellKey] = "host-stale"

	req := &CellTransferRequest{
		Kind: CellTransferMigrate,
		commands: []cellTransferCommand{{
			RequestID:  1,
			Kind:       CellTransferMigrate,
			SrcCellID:  cellKey,
			DestCellID: cellKey,
			SrcHostID:  "host-authoritative",
			DestHostID: "host-dest",
		}},
		mutation: topologyMutation{add: map[string]string{cellKey: "host-dest"}},
	}

	coord.mu.Lock()
	pre := coord.snapshotOwnershipLocked(req)
	coord.mu.Unlock()

	if got := pre[cellKey]; got != "host-authoritative" {
		t.Errorf("preOwnership[%s] = %q, want %q — commands[].SrcHostID must "+
			"override cellToHostMap", cellKey, got, "host-authoritative")
	}
}
