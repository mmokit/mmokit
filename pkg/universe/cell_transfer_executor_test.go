package universe

import (
	"context"
	"testing"
	"time"

	"github.com/mlange-42/ark/ecs"

	meshpb "github.com/zenion/mmoserver/gen/go/meshpb"
	"github.com/zenion/mmoserver/pkg/component"
	"github.com/zenion/mmoserver/pkg/coords"
)

// ═══════════════════════════════════════════════════════════════════════════
// S7 T4 — cellTransferExecutor unit tests
//
// These tests exercise the real executor without standing up a multi-host
// mesh. Fixtures create a minimal Coordinator + single in-process Host with
// real Cells via createNode, then drive the executor directly. The
// dispatcher path is tested separately; the goal here is to verify the
// executor correctly serializes per-quadrant, packs sessions, shipped bytes
// round-trip through SpawnFromTransfer, and Abort tears down a partial cell.
// ═══════════════════════════════════════════════════════════════════════════

// newExecutorTestCoord builds a minimal Coordinator with one local Host
// containing a single source cell. Returns the coord, the host, and the
// source cell. Drives Build() to populate netIDAlloc and install the host;
// the source cell's game loop is started so serialize/populate closures
// drain through PendingAdminCmds.
//
// srcCellID is ignored by the underlying Build() (which uses depth-0 cells
// at {0,0}); callers that care about depth should manually createNode a
// second cell after this helper returns and use that one as the source.
func newExecutorTestCoord(t *testing.T) (*Coordinator, *Host, *Cell) {
	t.Helper()
	coords.SetCellSize(1024)

	coord := NewCoordinator(Config{
		CellsX:        1,
		CellsY:        1,
		CellSize:      1024,
		Headless:      true,
		LoginHandler:  func(connID uint32, msgs [][]byte) (string, any, error) { return "", nil, ErrLoginPending },
	})
	coord.SetWorld(func(base *WorldBase) GameWorld { return base })
	coord.Build()

	host := coord.localHost()
	if host == nil {
		t.Fatalf("Build() produced no local host")
	}

	// Grab the single cell Build() created.
	var srcCell *Cell
	for _, c := range coord.Cells {
		srcCell = c
		break
	}
	if srcCell == nil {
		t.Fatalf("Build() produced no cells")
	}

	// Run the source cell's game loop in the background so
	// PendingAdminCmds closures fire.
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		// coord.Shutdown walks every cell (including the ones the
		// executor Receive path created via context.Background()) and
		// drains their game loops via Cell.Shutdown. Without this, the
		// Receive-spawned goroutines leak past the test and race with
		// the next test's coords.SetCellSize. See the S7-T10 race-fix
		// notes on Cell.Shutdown in cell.go.
		coord.Shutdown()
	})
	go srcCell.Run(ctx)
	time.Sleep(10 * time.Millisecond)
	return coord, host, srcCell
}

// spawnTestEntity plants an entity at (x, y) on the source cell's ECS
// world with all the components SerializeEntity needs, then returns the
// entity. Must be called on the game loop or before Run starts; tests
// funnel through PendingAdminCmds.
func spawnTestEntity(cell *Cell, netID uint32, x, y float32) ecs.Entity {
	w := cell.Engine.ECS
	e := w.NewEntity()
	ecs.NewMap1[component.Position](w).Add(e, &component.Position{X: x, Y: y})
	ecs.NewMap1[component.Velocity](w).Add(e, &component.Velocity{X: 0, Y: 0})
	ecs.NewMap1[component.NetworkID](w).Add(e, &component.NetworkID{ID: netID})
	ecs.NewMap1[component.EntityKind](w).Add(e, &component.EntityKind{Type: 1})
	ecs.NewMap1[component.Collider](w).Add(e, &component.Collider{Radius: 4})
	ecs.NewMap1[component.Rotation](w).Add(e, &component.Rotation{Angle: 0})
	ecs.NewMap1[component.CellCoord](w).Add(e, &component.CellCoord{CellX: cell.Cell.X, CellY: cell.Cell.Y})
	return e
}

// execOnLoop runs fn on the cell's game loop and blocks until it finishes.
func execOnLoop(t *testing.T, cell *Cell, fn func()) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := cell.Engine.RunOnLoop(ctx, func() error {
		fn()
		return nil
	})
	if err != nil {
		t.Fatalf("execOnLoop: %v", err)
	}
}

// ───────────────────────────────────────────────────────────────────────────
// TestPackUnpackRecordsRoundTrip — framing correctness
// ───────────────────────────────────────────────────────────────────────────

func TestPackUnpackRecordsRoundTrip(t *testing.T) {
	inputs := [][]byte{
		{0x01, 0x02, 0x03},
		{},
		{0xff, 0xfe, 0xfd, 0xfc, 0xfb},
		[]byte("hello world"),
	}
	packed := packRecords(inputs)
	out, err := unpackRecords(packed)
	if err != nil {
		t.Fatalf("unpack: %v", err)
	}
	if len(out) != len(inputs) {
		t.Fatalf("len=%d want %d", len(out), len(inputs))
	}
	for i := range inputs {
		if string(out[i]) != string(inputs[i]) {
			t.Errorf("record %d mismatch: %v vs %v", i, out[i], inputs[i])
		}
	}
	// Nil and empty both decode to empty slice.
	if out, err := unpackRecords(nil); err != nil || len(out) != 0 {
		t.Errorf("nil input: out=%v err=%v", out, err)
	}
}

// ───────────────────────────────────────────────────────────────────────────
// TestExecutorSerializeSplitPerQuadrant — entities placed in each quadrant
// are selected correctly when Execute runs with Kind=SPLIT.
// ───────────────────────────────────────────────────────────────────────────

func TestExecutorSerializeSplitPerQuadrant(t *testing.T) {
	coord, host, srcCell := newExecutorTestCoord(t)
	src := srcCell.Cell
	cellSize := src.Size(coords.CellSize)
	half := cellSize / 2

	// Plant one entity in each quadrant, plus an extra in the BR quadrant
	// to double-check filtering. Positions are local cell coords.
	execOnLoop(t, srcCell, func() {
		spawnTestEntity(srcCell, 1, half*0.25, half*0.25) // BL  quadrant 0
		spawnTestEntity(srcCell, 2, half*1.75, half*0.25) // BR  quadrant 1
		spawnTestEntity(srcCell, 3, half*0.25, half*1.75) // TL  quadrant 2
		spawnTestEntity(srcCell, 4, half*1.75, half*1.75) // TR  quadrant 3
		spawnTestEntity(srcCell, 5, half*1.5, half*0.1)   // BR  quadrant 1
	})

	// Children of {0,0,0} are {0,0,1}, {1,0,1}, {0,1,1}, {1,1,1}.
	children := src.Children()
	wantCounts := [4]int{1, 2, 1, 1}

	exec := coord.hostExecutors[host.ID]

	// Stand up a second host in the same process to act as the receive
	// target. Because it's a local host, Execute's shipToDestination takes
	// the fast path and calls Receive directly — which will createNode +
	// populate.
	destHost := NewHost("dest-host")
	destHost.Log = coord.Log
	coord.Hosts[destHost.ID] = destHost
	coord.hostExecutors[destHost.ID] = newCellTransferExecutor(coord, destHost)

	// Install a fake dispatcher so orchestrator.OnReady doesn't try to
	// reach anything we haven't set up. The executor itself reports Ready
	// directly to the orchestrator — for this test we just want OnReady
	// to run without panicking. A no-op dispatcher covers that.
	coord.orchestrator.setDispatcher(&fakeDispatcher{})

	for quadrant := 0; quadrant < 4; quadrant++ {
		// Seed a fresh request in the orchestrator so OnReady has
		// somewhere to land.
		parent := src
		req := &CellTransferRequest{
			ID:            uint64(quadrant + 100),
			Kind:          CellTransferSplit,
			SrcCell:       parent,
			ExpectedReady: 1,
			receivedOK:    make(map[string]struct{}),
			Deadline:      time.Now().Add(5 * time.Second),
			Done:          make(chan struct{}),
			mutation:      topologyMutation{add: map[string]string{}},
		}
		coord.orchestrator.mu.Lock()
		coord.orchestrator.inflight[req.ID] = req
		coord.orchestrator.mu.Unlock()

		cmd := cellTransferCommand{
			RequestID:  req.ID,
			Kind:       CellTransferSplit,
			SrcCellID:  srcCell.ID,
			DestCellID: MeshCellID(children[quadrant]),
			SrcHostID:  host.ID,
			DestHostID: destHost.ID,
			Quadrant:   uint32(quadrant),
		}
		if err := exec.Execute(cmd); err != nil {
			t.Fatalf("quadrant %d Execute: %v", quadrant, err)
		}

		// Check how many entities landed on the dest cell.
		destCell := destHost.CellByID(cmd.DestCellID)
		if destCell == nil {
			t.Fatalf("quadrant %d: dest cell not created", quadrant)
		}
		var count int
		execOnLoop(t, destCell, func() {
			filter := ecs.NewFilter1[component.Position](destCell.Engine.ECS).
				Without(ecs.C[component.Ghost](), ecs.C[component.Replica]())
			q := filter.Query()
			for q.Next() {
				count++
			}
		})
		if count != wantCounts[quadrant] {
			t.Errorf("quadrant %d: got %d entities, want %d", quadrant, count, wantCounts[quadrant])
		}
	}
}

// ───────────────────────────────────────────────────────────────────────────
// TestExecutorMergeSerializesAllEntities — a donor cell ships every entity
// regardless of position.
// ───────────────────────────────────────────────────────────────────────────

func TestExecutorMergeSerializesAllEntities(t *testing.T) {
	coord, host, donorCell := newExecutorTestCoord(t)

	execOnLoop(t, donorCell, func() {
		spawnTestEntity(donorCell, 10, 50, 50)
		spawnTestEntity(donorCell, 11, 100, 200)
		spawnTestEntity(donorCell, 12, 300, 100)
	})

	// Set up a survivor dest host.
	destHost := NewHost("survivor-host")
	destHost.Log = coord.Log
	coord.Hosts[destHost.ID] = destHost
	coord.hostExecutors[destHost.ID] = newCellTransferExecutor(coord, destHost)
	coord.orchestrator.setDispatcher(&fakeDispatcher{})

	// Merge populates the surviving sibling — so the survivor cell MUST
	// already exist on destHost before dispatch. Create it directly via
	// createNode and start its game loop so populateCell's closure can
	// actually run. Plant one pre-existing entity on the survivor so the
	// test can prove donor entities are added on top of what was there.
	survivorCellID := CellID{X: 1, Y: 0, Depth: 0}
	spatialCellSize := coord.resolveSpatialCellSize()
	coord.mu.Lock()
	destCell, destSystems := coord.createNode(survivorCellID, spatialCellSize, destHost, true)
	destHost.AddCell(survivorCellID, destCell)
	coord.mu.Unlock()
	destCell.World.Init()
	initSystems(destSystems)
	go destCell.Run(context.Background())
	time.Sleep(10 * time.Millisecond)
	execOnLoop(t, destCell, func() {
		spawnTestEntity(destCell, 99, 500, 500) // survivor's own entity
	})

	reqID := uint64(777)
	cmd := cellTransferCommand{
		RequestID:  reqID,
		Kind:       CellTransferMerge,
		SrcCellID:  donorCell.ID,
		DestCellID: MeshCellID(survivorCellID),
		SrcHostID:  host.ID,
		DestHostID: destHost.ID,
	}
	coord.orchestrator.mu.Lock()
	coord.orchestrator.inflight[reqID] = &CellTransferRequest{
		ID: reqID, Kind: CellTransferMerge, SrcCell: donorCell.Cell,
		ExpectedReady: 1, receivedOK: make(map[string]struct{}),
		ackedCmd: make([]bool, 1),
		commands: []cellTransferCommand{cmd},
		Deadline: time.Now().Add(5 * time.Second),
		Done:     make(chan struct{}),
		mutation: topologyMutation{add: map[string]string{}},
	}
	coord.orchestrator.mu.Unlock()
	if err := coord.hostExecutors[host.ID].Execute(cmd); err != nil {
		t.Fatalf("Execute merge: %v", err)
	}

	// Survivor should now hold its original entity (99) plus all 3 donor
	// entities (10/11/12).
	var count int
	seenIDs := map[uint32]bool{}
	execOnLoop(t, destCell, func() {
		netMap := ecs.NewMap1[component.NetworkID](destCell.Engine.ECS)
		filter := ecs.NewFilter1[component.Position](destCell.Engine.ECS).
			Without(ecs.C[component.Ghost](), ecs.C[component.Replica]())
		q := filter.Query()
		for q.Next() {
			count++
			e := q.Entity()
			if netMap.HasAll(e) {
				seenIDs[netMap.Get(e).ID] = true
			}
		}
	})
	if count != 4 {
		t.Errorf("merge: got %d entities on survivor, want 4 (1 own + 3 donor)", count)
	}
	for _, want := range []uint32{10, 11, 12, 99} {
		if !seenIDs[want] {
			t.Errorf("merge: survivor missing netID %d — seen %v", want, seenIDs)
		}
	}
}

// ───────────────────────────────────────────────────────────────────────────
// TestExecutorMigrateRoundTrip — entities on the source cell round-trip
// through serialize + ship + deserialize and land on the dest cell with
// the same NetworkIDs and positions.
// ───────────────────────────────────────────────────────────────────────────

func TestExecutorMigrateRoundTrip(t *testing.T) {
	coord, host, srcCell := newExecutorTestCoord(t)

	execOnLoop(t, srcCell, func() {
		spawnTestEntity(srcCell, 42, 10, 20)
		spawnTestEntity(srcCell, 43, 30, 40)
	})

	destHost := NewHost("migrate-dest")
	destHost.Log = coord.Log
	coord.Hosts[destHost.ID] = destHost
	coord.hostExecutors[destHost.ID] = newCellTransferExecutor(coord, destHost)
	coord.orchestrator.setDispatcher(&fakeDispatcher{})

	// Evict the source cell from coord.Cells so Receive's createNode for
	// the dest can write under a fresh key. Real MIGRATE tears down the
	// source after commit; we simulate that up front for this unit test
	// because both cells live in the same in-process coordinator.
	destCellID := CellID{X: 9, Y: 9, Depth: 0}
	reqID := uint64(900)
	coord.orchestrator.mu.Lock()
	coord.orchestrator.inflight[reqID] = &CellTransferRequest{
		ID: reqID, Kind: CellTransferMigrate, SrcCell: srcCell.Cell,
		ExpectedReady: 1, receivedOK: make(map[string]struct{}),
		ackedCmd: make([]bool, 1),
		commands: []cellTransferCommand{{
			RequestID:  reqID,
			Kind:       CellTransferMigrate,
			SrcCellID:  srcCell.ID,
			DestCellID: MeshCellID(destCellID),
			SrcHostID:  host.ID,
			DestHostID: destHost.ID,
		}},
		Deadline: time.Now().Add(5 * time.Second),
		Done:     make(chan struct{}),
		mutation: topologyMutation{add: map[string]string{MeshCellID(destCellID): destHost.ID}},
	}
	coord.orchestrator.mu.Unlock()

	cmd := cellTransferCommand{
		RequestID:  reqID,
		Kind:       CellTransferMigrate,
		SrcCellID:  srcCell.ID,
		DestCellID: MeshCellID(destCellID),
		SrcHostID:  host.ID,
		DestHostID: destHost.ID,
	}
	if err := coord.hostExecutors[host.ID].Execute(cmd); err != nil {
		t.Fatalf("Execute migrate: %v", err)
	}

	destCell := destHost.CellByID(cmd.DestCellID)
	if destCell == nil {
		t.Fatalf("migrate: dest cell %s not created", cmd.DestCellID)
	}

	netFound := map[uint32]component.Position{}
	execOnLoop(t, destCell, func() {
		posMap := ecs.NewMap1[component.Position](destCell.Engine.ECS)
		netMap := ecs.NewMap1[component.NetworkID](destCell.Engine.ECS)
		filter := ecs.NewFilter2[component.Position, component.NetworkID](destCell.Engine.ECS).
			Without(ecs.C[component.Ghost](), ecs.C[component.Replica]())
		q := filter.Query()
		for q.Next() {
			e := q.Entity()
			if !posMap.HasAll(e) || !netMap.HasAll(e) {
				continue
			}
			netFound[netMap.Get(e).ID] = *posMap.Get(e)
		}
	})
	if len(netFound) != 2 {
		t.Fatalf("migrate: got %d entities on dest, want 2", len(netFound))
	}
	if got, ok := netFound[42]; !ok || got.X != 10 || got.Y != 20 {
		t.Errorf("netID 42: got (%.0f,%.0f) ok=%v", got.X, got.Y, ok)
	}
	if got, ok := netFound[43]; !ok || got.X != 30 || got.Y != 40 {
		t.Errorf("netID 43: got (%.0f,%.0f) ok=%v", got.X, got.Y, ok)
	}

	// Orchestrator should have committed — receivedOK contains one entry.
	coord.orchestrator.mu.Lock()
	_, stillInflight := coord.orchestrator.inflight[reqID]
	coord.orchestrator.mu.Unlock()
	if stillInflight {
		t.Errorf("orchestrator inflight entry not cleaned after commit")
	}
}

// ───────────────────────────────────────────────────────────────────────────
// TestExecutorAbortTearsDownPartialCell — Receive creates a cell, then an
// Abort with the same request_id removes it from the topology and shuts it
// down.
// ───────────────────────────────────────────────────────────────────────────

func TestExecutorAbortTearsDownPartialCell(t *testing.T) {
	// Use Build() to get a properly-initialized coord with a netIDAlloc.
	coord, host, _ := newExecutorTestCoord(t)
	exec := coord.hostExecutors[host.ID]

	// Synthesize a fresh "pending receive" by createNode-ing a new cell
	// under the same host + coord. Real Receive would do this; here we
	// short-circuit to focus on Abort semantics.
	destCellID := CellID{X: 3, Y: 3, Depth: 0}
	destKey := MeshCellID(destCellID)
	spatialCellSize := coord.resolveSpatialCellSize()
	coord.mu.Lock()
	node, systems := coord.createNode(destCellID, spatialCellSize, host, true)
	host.AddCell(destCellID, node)
	coord.mu.Unlock()
	node.World.Init()
	initSystems(systems)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go node.Run(ctx)

	reqID := uint64(555)
	exec.mu.Lock()
	exec.pending[reqID] = &pendingReceive{
		requestID: reqID,
		cellID:    destCellID,
		cellKey:   destKey,
		cell:      node,
	}
	exec.mu.Unlock()

	// Sanity: cell is in the topology.
	coord.mu.RLock()
	if _, ok := coord.Cells[destKey]; !ok {
		coord.mu.RUnlock()
		t.Fatalf("pre-abort: cell %s missing from coord.Cells", destKey)
	}
	coord.mu.RUnlock()

	exec.Abort(&meshpb.CellTransferAbort{RequestId: reqID})

	coord.mu.RLock()
	_, stillThere := coord.Cells[destKey]
	coord.mu.RUnlock()
	if stillThere {
		t.Errorf("post-abort: cell %s still in coord.Cells", destKey)
	}
	if host.CellByID(destKey) != nil {
		t.Errorf("post-abort: host still holds cell %s", destKey)
	}
	exec.mu.Lock()
	if _, ok := exec.pending[reqID]; ok {
		t.Errorf("post-abort: pending entry not cleared")
	}
	exec.mu.Unlock()
}

// ───────────────────────────────────────────────────────────────────────────
// TestExecutorCellTransferReadyReachesOrchestrator — a Ready MeshFrame routed
// through routeInboundFrame propagates to orchestrator.OnReady.
// ───────────────────────────────────────────────────────────────────────────

func TestExecutorCellTransferReadyReachesOrchestrator(t *testing.T) {
	coord, host, _ := newExecutorTestCoord(t)
	coord.orchestrator.setDispatcher(&fakeDispatcher{})

	// Open a real HostNetwork (:0) so routeInboundFrame has a code path to
	// attribute the frame to. We don't actually send over the wire — just
	// invoke routeInboundFrame with a crafted MeshFrame.
	hn, err := NewHostNetwork(host, ":0", coord.Log)
	if err != nil {
		t.Fatalf("NewHostNetwork: %v", err)
	}
	defer hn.Shutdown()
	host.Network = hn
	hn.SetCoord(coord)

	// Seed an inflight request keyed on (hostID, destCellID).
	destCellID := CellID{X: 11, Y: 11, Depth: 0}
	destKey := MeshCellID(destCellID)
	reqID := uint64(424242)
	req := &CellTransferRequest{
		ID: reqID, Kind: CellTransferMigrate, SrcCell: destCellID,
		ExpectedReady: 1, receivedOK: make(map[string]struct{}),
		ackedCmd: make([]bool, 1),
		commands: []cellTransferCommand{{
			RequestID:  reqID,
			Kind:       CellTransferMigrate,
			SrcCellID:  destKey,
			DestCellID: destKey,
			SrcHostID:  host.ID,
			DestHostID: host.ID,
		}},
		Deadline: time.Now().Add(5 * time.Second),
		Done:     make(chan struct{}),
		mutation: topologyMutation{add: map[string]string{destKey: "some-host"}},
	}
	coord.orchestrator.mu.Lock()
	coord.orchestrator.inflight[reqID] = req
	coord.orchestrator.mu.Unlock()

	frame := &meshpb.MeshFrame{
		DestCellId: destKey,
		Msg: &meshpb.MeshFrame_CellTransferReady{
			CellTransferReady: &meshpb.CellTransferReady{
				RequestId:  reqID,
				DestCellId: destKey,
				HostId:     host.ID,
				Ok:         true,
			},
		},
	}
	if err := hn.routeInboundFrame(frame); err != nil {
		t.Fatalf("routeInboundFrame: %v", err)
	}

	select {
	case <-req.Done:
	case <-time.After(time.Second):
		t.Fatalf("req.Done did not close after Ready")
	}
	if req.Result != nil {
		t.Errorf("Result=%v want nil (committed)", req.Result)
	}
	if got := coord.orchestrator.commitCount.Load(); got != 1 {
		t.Errorf("commitCount=%d want 1", got)
	}
}
