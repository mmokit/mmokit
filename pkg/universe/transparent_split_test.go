package universe

import (
	"testing"
	"time"

	"github.com/zenion/mmokit/pkg/coords"
)

// ═══════════════════════════════════════════════════════════════════════════
// Transparent cell transfers — capstone regression guard (Task 8)
//
// Tasks 3-7 made a SPLIT ship "border context": the source (parent) cell
// serializes its cross-cell visible set (sibling-quadrant locals + outer-
// neighbor replicas) into meshpb.CellTransfer.Context, and the destination
// child cell materializes those as border replicas (PresenceReplica) inside
// populateCell — synchronously, during the split commit, before the child's
// first replication tick. The payoff: the child's first FreshSnapshot to a
// client already contains the full visible set, so the client needs no
// reconcile churn.
//
// Failure mode BEFORE the fix (the rubber-band bug): the child cell held ONLY
// its own quadrant's authoritative entities at commit time; sibling-quadrant
// entities were absent and only reconstructed asynchronously via normal
// border replication over the next 1-2s. A client snapped to that child saw a
// sparse world for several frames and entities "rubber-banded" in.
//
// ── Why this test drives the executor directly instead of a live cluster ────
// In a live colocated fixture, every child cell ticks at 20Hz and the bridge's
// BorderDispatcher fills sibling replicas within ~1-2 ticks of the split. That
// async fill happens BEFORE any post-SplitCell inspection can run, so an
// index check against a live fixture cannot distinguish "context materialized
// synchronously" from "async border replication caught up" — it passes even
// with IncludeBorderContext=false (verified). To assert the SYNCHRONOUS,
// commit-time precondition we drive the real cellTransferExecutor (same path
// the orchestrator uses) against a dest host that holds ONLY the one child
// quadrant. That child has no sibling cells on the dest host, so its
// BorderDispatcher never fires (len(neighbors)==0) — the context-
// materialization loop is the SOLE source of sibling replicas. Determinism +
// non-vacuousness in one shot. The executor unit tests (cell_transfer_executor_test.go)
// established this harness; the frame-derivation path (grid → ReplicationSystem
// → frame) is covered by pkg/system tests.
// ═══════════════════════════════════════════════════════════════════════════

// TestSplit_ChildCellSeededWithBorderContext spawns four entities clustered
// around the parent cell's center (one per child quadrant), runs the real
// split-executor for one child quadrant against an isolated dest host, and
// asserts the child cell's netID index holds its own quadrant entity as
// PresenceLive and every sibling-quadrant entity as PresenceReplica — the
// synchronous, commit-time visible set, with zero async replication in play.
func TestSplit_ChildCellSeededWithBorderContext(t *testing.T) {
	// newExecutorTestCoord builds a 1x1 coord with a single source cell on the
	// local host and starts its game loop. DynamicPartitioning is nil →
	// collectTransferContext treats nil config as IncludeBorderContext=true, so
	// context ships. AoIRadius defaults to 500, far larger than the ~85u
	// cluster radius below, so every entity is within AoI of every quadrant.
	coord, srcHost, srcCell := newExecutorTestCoord(t)
	parent := srcCell.CellID()

	// newExecutorTestCoord forces CellSize 1024 → parent center is (512,512);
	// each depth-1 child is 512 wide. Cluster four entities tightly around the
	// center, one per quadrant. Quadrant formula (matches collectTransferContext):
	// xi = x>=512?1:0, yi = y>=512?1:0, q = xi | (yi<<1). spawnTestEntity stores
	// positions raw and at depth-0 cell {0,0} local coords == world coords.
	type plant struct {
		netID    uint32
		x, y     float32
		quadrant int
	}
	plants := []plant{
		{netID: 101, x: 480, y: 480, quadrant: 0}, // BL → child {0,0,1}
		{netID: 102, x: 540, y: 480, quadrant: 1}, // BR → child {1,0,1}
		{netID: 103, x: 480, y: 540, quadrant: 2}, // TL → child {0,1,1}
		{netID: 104, x: 540, y: 540, quadrant: 3}, // TR → child {1,1,1}
	}
	execOnLoop(t, srcCell, func() {
		for _, p := range plants {
			spawnTestEntity(srcCell, p.netID, p.x, p.y)
		}
	})

	children := parent.Children() // [4]CellID ordered by quadrant index

	// Stand up an isolated dest host to receive children. Because it's a local
	// host, Execute's shipToDestination takes the fast path and calls Receive
	// directly (createNode + populate). The dest host holds only the one child
	// per Execute call, so the child cell has no sibling neighbors → its
	// BorderDispatcher never fires. The context-materialization loop in
	// populateCell is therefore the ONLY way sibling replicas can appear.
	destHost := NewHost("dest-host")
	destHost.Log = coord.Log
	coord.Hosts[destHost.ID] = destHost
	coord.hostExecutors[destHost.ID] = newCellTransferExecutor(coord, destHost)
	coord.orchestrator.setDispatcher(&fakeDispatcher{})

	exec := coord.hostExecutors[srcHost.ID]

	// assertChildVisibleSet runs the executor for quadrant q and asserts the
	// resulting dest child cell holds its own entity Live + every sibling Replica.
	assertChildVisibleSet := func(q int) {
		req := &CellTransferRequest{
			ID:            uint64(q + 100),
			Kind:          CellTransferSplit,
			SrcCell:       parent,
			ExpectedReady: 1,
			receivedOK:    make(map[string]struct{}),
			Deadline:      time.Now().Add(5 * time.Second),
			Done:          make(chan struct{}),
			mutation:      topologyMutation{add: map[MeshCellID]string{}},
		}
		coord.orchestrator.mu.Lock()
		coord.orchestrator.inflight[req.ID] = req
		coord.orchestrator.mu.Unlock()

		cmd := cellTransferCommand{
			RequestID:  req.ID,
			Kind:       CellTransferSplit,
			SrcCellID:  srcCell.MeshID(),
			DestCellID: children[q].MeshID(),
			SrcHostID:  srcHost.ID,
			DestHostID: destHost.ID,
			Quadrant:   uint32(q),
		}
		if err := exec.Execute(cmd); err != nil {
			t.Fatalf("quadrant %d Execute: %v", q, err)
		}

		childCell := destHost.CellByID(cmd.DestCellID)
		if childCell == nil {
			t.Fatalf("quadrant %d: dest child cell %s not created", q, cmd.DestCellID)
		}

		execOnLoop(t, childCell, func() {
			for _, p := range plants {
				_, presence, ok := childCell.Stage.netIDIdx.Lookup(p.netID)
				if p.quadrant == q {
					// This child's own authoritative entity — shipped as a Live
					// transfer Entity (not Context).
					if !ok {
						t.Errorf("child q%d (%s): own netID %d absent — split dropped authoritative entity",
							q, cmd.DestCellID, p.netID)
						continue
					}
					if presence != PresenceLive {
						t.Errorf("child q%d (%s): own netID %d presence=%v, want PresenceLive",
							q, cmd.DestCellID, p.netID, presence)
					}
				} else {
					// Sibling-quadrant entity — THE regression assertion. The
					// only way it reaches this child's index is the synchronous
					// border-context materialization in populateCell. Without
					// Tasks 6-7 (IncludeBorderContext / context loop) it would be
					// absent here, and in a live cluster would only appear 1-2
					// ticks later via async border replication (rubber-band).
					if !ok {
						t.Errorf("child q%d (%s): sibling netID %d (quadrant %d) absent — "+
							"border context not materialized synchronously (rubber-band regression)",
							q, cmd.DestCellID, p.netID, p.quadrant)
						continue
					}
					if presence != PresenceReplica {
						t.Errorf("child q%d (%s): sibling netID %d presence=%v, want PresenceReplica",
							q, cmd.DestCellID, p.netID, presence)
					}
				}
			}
		})
	}

	// Quadrant 0 (BL): 101 Live, 102/103/104 Replica.
	assertChildVisibleSet(0)
	// Quadrant 1 (BR) strengthens the guard: 102 Live, 101/103/104 Replica.
	assertChildVisibleSet(1)

	// Keep coords import meaningful and pin the cell-size assumption the
	// quadrant math relies on (newExecutorTestCoord sets it to 1024).
	if coords.CellSize != 1024 {
		t.Fatalf("expected CellSize 1024, got %v", coords.CellSize)
	}
}
