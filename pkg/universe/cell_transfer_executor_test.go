package universe

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/mlange-42/ark/ecs"

	meshpb "github.com/zenion/mmoserver/gen/go/meshpb"
	"github.com/zenion/mmoserver/pkg/component"
	"github.com/zenion/mmoserver/pkg/coords"
	"github.com/zenion/mmoserver/pkg/engine"
)

// ═══════════════════════════════════════════════════════════════════════════
// S7 T4 — cellTransferExecutor unit tests
//
// These tests exercise the real executor without standing up a multi-host
// mesh. Fixtures create a minimal Process + single in-process Host with
// real Cells via createNode, then drive the executor directly. The
// dispatcher path is tested separately; the goal here is to verify the
// executor correctly serializes per-quadrant, packs sessions, shipped bytes
// round-trip through SpawnFromTransfer, and Abort tears down a partial cell.
// ═══════════════════════════════════════════════════════════════════════════

// newExecutorTestCoord builds a minimal Process with one local Host
// containing a single source cell. Returns the coord, the host, and the
// source cell. Drives Build() to populate netIDAlloc and install the host;
// the source cell's game loop is started so serialize/populate closures
// drain through the loop-job queue.
//
// srcCellID is ignored by the underlying Build() (which uses depth-0 cells
// at {0,0}); callers that care about depth should manually createNode a
// second cell after this helper returns and use that one as the source.
func newExecutorTestCoord(t *testing.T) (*Process, *Host, *Cell) {
	t.Helper()
	coords.SetCellSize(1024)

	coord := New(Config{
		CellsX:   1,
		CellsY:   1,
		CellSize: 1024,
		Headless: true,
	})
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
	// queued loop-job closures fire.
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
// funnel through the loop-job queue.
func spawnTestEntity(cell *Cell, netID uint32, x, y float32) ecs.Entity {
	w := cell.Engine.ECS
	e := w.NewEntity()
	ecs.NewMap1[component.Position](w).Add(e, &component.Position{X: x, Y: y})
	ecs.NewMap1[component.Velocity](w).Add(e, &component.Velocity{X: 0, Y: 0})
	ecs.NewMap1[component.NetworkID](w).Add(e, &component.NetworkID{ID: netID})
	ecs.NewMap1[component.EntityKind](w).Add(e, &component.EntityKind{Type: 1})
	ecs.NewMap1[component.Collider](w).Add(e, &component.Collider{Radius: 4})
	ecs.NewMap1[component.Rotation](w).Add(e, &component.Rotation{Angle: 0})
	ecs.NewMap1[component.CellCoord](w).Add(e, &component.CellCoord{CellX: cell.CellID().X, CellY: cell.CellID().Y})
	cell.Stage.netIDIdx.Enter(netID, e, PresenceLive)
	return e
}

func spawnTestPlayerEntity(cell *Cell, netID, connID uint32, username string, generation uint32, x, y float32) ecs.Entity {
	e := spawnTestEntity(cell, netID, x, y)
	ecs.NewMap1[component.PlayerConn](cell.Engine.ECS).Add(e, &component.PlayerConn{ConnID: connID})
	cell.Engine.Players.RegisterSessionTransfer(connID, username, "active", nil)
	sess := cell.Engine.Players.ByConnID(connID)
	sess.Entity = e
	sess.StreamGeneration = generation
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

func TestUnpackRecordsRejectsImpossibleCountBeforeAllocation(t *testing.T) {
	buf := make([]byte, 4)
	binary.BigEndian.PutUint32(buf, ^uint32(0))
	if _, err := unpackRecords(buf); err == nil {
		t.Fatal("unpackRecords accepted a count with no record headers")
	}
}

func TestEntitylessSessionTransferAdvancesOnlyTransferredCopy(t *testing.T) {
	src := newTestCell("source", CellID{X: 0, Y: 0})
	src.Engine.Players.RegisterSessionTransfer(81, "entityless", "active", nil)
	sourceSession := src.Engine.Players.ByConnID(81)
	sourceSession.StreamGeneration = ^uint32(0)

	records, err := serializeEntitylessSessions(src)
	if err != nil {
		t.Fatalf("serializeEntitylessSessions: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("serialized session records = %d, want 1", len(records))
	}
	var transferred SessionTransfer
	if err := json.Unmarshal(records[0], &transferred); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if transferred.StreamGeneration != 0 {
		t.Fatalf("transferred generation = %d, want wrapped N+1 = 0", transferred.StreamGeneration)
	}
	if sourceSession.StreamGeneration != ^uint32(0) {
		t.Fatalf("source generation changed during serialization: got %d", sourceSession.StreamGeneration)
	}

	dst := newTestCell("destination", CellID{X: 1, Y: 0})
	dst.processMessage(CellMessage{Type: MsgSessionTransfer, Sessions: []SessionTransfer{transferred}})
	destinationSession := dst.Engine.Players.ByConnID(81)
	if destinationSession == nil || destinationSession.StreamGeneration != 0 {
		t.Fatalf("destination session = %+v, want wrapped generation 0", destinationSession)
	}
}

func TestEntitylessSessionTransferRemapsCrossHostIdentityAndCommitRoute(t *testing.T) {
	coord := New(Config{Headless: true})
	source := newTestCell("cell_0_0", CellID{X: 0, Y: 0})
	source.Stage.coord = coord
	sourceDead := source.Engine.Players.RegisterState("dead")

	key := SessionKey{GatewayID: "gateway-a", ConnID: 9001}
	const sourceEpoch = uint64(41)
	sourceVCM := NewVirtualConnManager(nil, source.Log)
	sourceLocalID := sourceVCM.RegisterSession(key, "docked-pilot", sourceEpoch, source.MeshID())
	coord.vcm = sourceVCM

	userID := uuid.MustParse("dc59b284-9701-40cf-a840-d8e98fcb8cdc")
	source.Engine.Players.RegisterSessionTransfer(sourceLocalID, "docked-pilot", "dead", nil)
	sourceSession := source.Engine.Players.ByConnID(sourceLocalID)
	sourceSession.State = sourceDead
	sourceSession.StreamGeneration = 17
	sourceSession.UserID = userID

	records, err := serializeEntitylessSessions(source)
	if err != nil {
		t.Fatalf("serializeEntitylessSessions: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("serialized records = %d, want 1", len(records))
	}
	var transferred SessionTransfer
	if err := json.Unmarshal(records[0], &transferred); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if transferred.ConnID != sourceLocalID || transferred.GatewayID != key.GatewayID ||
		transferred.GatewayConnID != key.ConnID || transferred.SessionEpoch != sourceEpoch {
		t.Fatalf("serialized routing identity = local:%d key:%s:%d epoch:%d, want local:%d key:%s:%d epoch:%d",
			transferred.ConnID, transferred.GatewayID, transferred.GatewayConnID, transferred.SessionEpoch,
			sourceLocalID, key.GatewayID, key.ConnID, sourceEpoch)
	}

	// Allocate an unrelated destination session first so the destination's
	// local namespace demonstrably differs from the source's.
	destinationVCM := NewVirtualConnManager(nil, source.Log)
	destinationVCM.RegisterSession(SessionKey{GatewayID: "gateway-other", ConnID: 1}, "other", 1, "cell_other")
	coord.vcm = destinationVCM

	destination := newTestCell("cell_d1_0_0", CellID{X: 0, Y: 0, Depth: 1})
	destination.Stage.coord = coord
	destinationDead := destination.Engine.Players.RegisterState("dead")
	destinationHost := NewHost("host-b")
	destinationHost.Log = coord.Log
	executor := newCellTransferExecutor(coord, destinationHost)

	adoptedUsers, err := executor.populateCell(destination, &meshpb.CellTransfer{
		RequestId:  77,
		Kind:       meshpb.CellTransferKind_CELL_TRANSFER_SPLIT,
		SrcCellId:  string(source.MeshID()),
		DestCellId: string(destination.MeshID()),
		DestHostId: destinationHost.ID,
		Sessions:   packRecords(records),
	})
	if err != nil {
		t.Fatalf("populateCell: %v", err)
	}
	destinationLocalID, ok := destinationVCM.LookupByKey(key)
	if !ok {
		t.Fatal("destination VCM did not register stable SessionKey")
	}
	if destinationLocalID == sourceLocalID {
		t.Fatalf("test setup did not create distinct local IDs: source=%d destination=%d", sourceLocalID, destinationLocalID)
	}
	if stale := destination.Engine.Players.ByConnID(sourceLocalID); stale != nil {
		t.Fatalf("destination registered source-local connID %d: %+v", sourceLocalID, stale)
	}
	destinationSession := destination.Engine.Players.ByConnID(destinationLocalID)
	if destinationSession == nil {
		t.Fatalf("destination missing session at local connID %d", destinationLocalID)
	}
	if destinationSession.UserID != userID || destinationSession.StreamGeneration != 18 || destinationSession.State != destinationDead {
		t.Fatalf("destination session = %+v, want user=%s generation=18 state=%d",
			destinationSession, userID, destinationDead)
	}
	if len(adoptedUsers) != 1 || adoptedUsers[0] != "docked-pilot" {
		t.Fatalf("adopted users = %v, want [docked-pilot]", adoptedUsers)
	}

	// Prove the Ready/adopted-user result drives the authoritative route to
	// this exact cell+host at commit (not merely to a locally registered
	// session or an unrelated split fallback child).
	fallbackCell := MeshCellID("cell_d1_1_0")
	coord.sessionRoutes.Set(&SessionRoute{
		Key: key, Username: "docked-pilot", HostID: "host-a", CellID: source.MeshID(), Epoch: sourceEpoch,
	})
	req := &CellTransferRequest{
		adoptedUsers: map[string]MeshCellID{"docked-pilot": destination.MeshID()},
		mutation: topologyMutation{add: map[MeshCellID]string{
			destination.MeshID(): destinationHost.ID,
			fallbackCell:         "host-c",
		}},
	}
	ctx := &CommitContext{Req: req, ParentKey: source.MeshID(), FallbackChildKey: fallbackCell}
	if err := stepSplitRemapSessions(coord, ctx); err != nil {
		t.Fatalf("stepSplitRemapSessions: %v", err)
	}
	route, ok := coord.sessionRoutes.Get(key)
	if !ok {
		t.Fatal("committed session route vanished")
	}
	if route.HostID != destinationHost.ID || route.CellID != destination.MeshID() || route.Epoch != sourceEpoch+1 {
		t.Fatalf("committed route = host:%s cell:%s epoch:%d, want host:%s cell:%s epoch:%d",
			route.HostID, route.CellID, route.Epoch,
			destinationHost.ID, destination.MeshID(), sourceEpoch+1)
	}
	routedKey, routedEpoch, ok := destinationVCM.LookupRouteByLocal(destinationLocalID)
	if !ok || routedKey != key || routedEpoch != sourceEpoch+1 {
		t.Fatalf("destination VCM route = key:%+v epoch:%d ok:%v, want key:%+v epoch:%d",
			routedKey, routedEpoch, ok, key, sourceEpoch+1)
	}
	destinationVCM.mu.RLock()
	virtual := destinationVCM.byLocal[destinationLocalID]
	destinationVCM.mu.RUnlock()
	if virtual == nil || virtual.cellID != destination.MeshID() || virtual.username != "docked-pilot" {
		t.Fatalf("destination VCM session = %+v, want cell=%s username=docked-pilot", virtual, destination.MeshID())
	}
}

// ───────────────────────────────────────────────────────────────────────────
// TestExecutorSerializeSplitPerQuadrant — entities placed in each quadrant
// are selected correctly when Execute runs with Kind=SPLIT.
// ───────────────────────────────────────────────────────────────────────────

func TestExecutorSerializeSplitPerQuadrant(t *testing.T) {
	coord, host, srcCell := newExecutorTestCoord(t)
	src := srcCell.CellID()
	cellSize := src.Size(coords.CellSize)
	half := cellSize / 2

	// Plant one entity in each quadrant, plus an extra in the BR quadrant
	// to double-check filtering. Positions are local cell coords.
	execOnLoop(t, srcCell, func() {
		spawnTestEntity(srcCell, 1, half*0.25, half*0.25) // BL  quadrant 0
		spawnTestPlayerEntity(srcCell, 6, 600, "split-player", 7, half*0.3, half*0.3)
		dead := srcCell.Engine.Players.RegisterState("dead")
		srcCell.Engine.Players.RegisterSessionTransfer(601, "entityless-split-player", "dead", nil)
		srcCell.Engine.Players.ByConnID(601).State = dead
		spawnTestEntity(srcCell, 2, half*1.75, half*0.25) // BR  quadrant 1
		spawnTestEntity(srcCell, 3, half*0.25, half*1.75) // TL  quadrant 2
		spawnTestEntity(srcCell, 4, half*1.75, half*1.75) // TR  quadrant 3
		spawnTestEntity(srcCell, 5, half*1.5, half*0.1)   // BR  quadrant 1
	})

	// Children of {0,0,0} are {0,0,1}, {1,0,1}, {0,1,1}, {1,1,1}.
	children := src.Children()
	wantCounts := [4]int{2, 2, 1, 1}

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
			mutation:      topologyMutation{add: map[MeshCellID]string{}},
		}
		coord.orchestrator.mu.Lock()
		coord.orchestrator.inflight[req.ID] = req
		coord.orchestrator.mu.Unlock()

		cmd := cellTransferCommand{
			RequestID:  req.ID,
			Kind:       CellTransferSplit,
			SrcCellID:  srcCell.MeshID(),
			DestCellID: children[quadrant].MeshID(),
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
			netMap := ecs.NewMap1[component.NetworkID](destCell.Engine.ECS)
			filter := ecs.NewFilter1[component.Position](destCell.Engine.ECS).
				Without(ecs.C[component.Ghost](), ecs.C[component.Replica]())
			q := filter.Query()
			for q.Next() {
				count++
				if got := netMap.Get(q.Entity()).Epoch; got != 1 {
					t.Errorf("quadrant %d: transferred entity epoch = %d, want 1", quadrant, got)
				}
			}
		})
		if count != wantCounts[quadrant] {
			t.Errorf("quadrant %d: got %d entities, want %d", quadrant, count, wantCounts[quadrant])
		}
		if quadrant == 0 {
			sess := destCell.Engine.Players.ByConnID(600)
			if sess == nil {
				t.Fatal("quadrant 0: transferred player session missing")
			}
			if sess.StreamGeneration != 8 {
				t.Errorf("quadrant 0: StreamGeneration = %d, want source N+1 = 8", sess.StreamGeneration)
			}
		}
		entityless := destCell.Engine.Players.ByConnID(601)
		if quadrant == 0 && entityless == nil {
			t.Fatal("quadrant 0: entity-less session missing from deterministic fallback child")
		}
		if quadrant != 0 && entityless != nil {
			t.Fatalf("quadrant %d: entity-less session duplicated outside fallback child", quadrant)
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
		spawnTestPlayerEntity(donorCell, 13, 1300, "merge-donor-player", 11, 350, 150)
		// Simulate a player that already crossed to the survivor before the
		// merge snapshot. The donor copy is stale and must be deduped without
		// overwriting the continuing survivor stream generation.
		spawnTestPlayerEntity(donorCell, 99, 9900, "merge-survivor-player", 5, 400, 200)
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
	destCell.Stage.Init()
	initSystems(destSystems)
	go destCell.Run(context.Background())
	time.Sleep(10 * time.Millisecond)
	execOnLoop(t, destCell, func() {
		spawnTestPlayerEntity(destCell, 99, 9900, "merge-survivor-player", 31, 500, 500)
	})

	reqID := uint64(777)
	cmd := cellTransferCommand{
		RequestID:  reqID,
		Kind:       CellTransferMerge,
		SrcCellID:  donorCell.MeshID(),
		DestCellID: survivorCellID.MeshID(),
		SrcHostID:  host.ID,
		DestHostID: destHost.ID,
	}
	coord.orchestrator.mu.Lock()
	coord.orchestrator.inflight[reqID] = &CellTransferRequest{
		ID: reqID, Kind: CellTransferMerge, SrcCell: donorCell.CellID(),
		ExpectedReady: 1, receivedOK: make(map[string]struct{}),
		ackedCmd: make([]bool, 1),
		commands: []cellTransferCommand{cmd},
		Deadline: time.Now().Add(5 * time.Second),
		Done:     make(chan struct{}),
		mutation: topologyMutation{add: map[MeshCellID]string{}},
	}
	coord.orchestrator.mu.Unlock()
	if err := coord.hostExecutors[host.ID].Execute(cmd); err != nil {
		t.Fatalf("Execute merge: %v", err)
	}

	// Survivor should now hold its original entity (99) plus all 3 donor
	// entities (10/11/12/13).
	var count int
	seenEpochs := map[uint32]uint32{}
	execOnLoop(t, destCell, func() {
		netMap := ecs.NewMap1[component.NetworkID](destCell.Engine.ECS)
		filter := ecs.NewFilter1[component.Position](destCell.Engine.ECS).
			Without(ecs.C[component.Ghost](), ecs.C[component.Replica]())
		q := filter.Query()
		for q.Next() {
			count++
			e := q.Entity()
			if netMap.HasAll(e) {
				nid := netMap.Get(e)
				seenEpochs[nid.ID] = nid.Epoch
			}
		}
	})
	if count != 5 {
		t.Errorf("merge: got %d entities on survivor, want 5 (1 own + 4 donor)", count)
	}
	for _, want := range []uint32{10, 11, 12, 13, 99} {
		if _, ok := seenEpochs[want]; !ok {
			t.Errorf("merge: survivor missing netID %d — seen %v", want, seenEpochs)
		}
	}
	for _, transferred := range []uint32{10, 11, 12, 13} {
		if got := seenEpochs[transferred]; got != 1 {
			t.Errorf("merge: donor netID %d epoch = %d, want 1", transferred, got)
		}
	}
	if got := seenEpochs[99]; got != 0 {
		t.Errorf("merge: continuing survivor netID 99 epoch = %d, want 0", got)
	}
	if sess := destCell.Engine.Players.ByConnID(1300); sess == nil {
		t.Fatal("merge: donor player session missing on survivor")
	} else if sess.StreamGeneration != 12 {
		t.Errorf("merge: donor StreamGeneration = %d, want source N+1 = 12", sess.StreamGeneration)
	}
	if sess := destCell.Engine.Players.ByConnID(9900); sess == nil {
		t.Fatal("merge: continuing survivor player session missing")
	} else if sess.StreamGeneration != 31 {
		t.Errorf("merge: continuing survivor StreamGeneration = %d, want unchanged 31", sess.StreamGeneration)
	}
}

func TestExecutorMergeDedupKeepsNativeAndReplacesStaleDonor(t *testing.T) {
	coord, host, survivor := newExecutorTestCoord(t)
	exec := coord.hostExecutors[host.ID]
	const requestID = uint64(0xD3D0)

	execOnLoop(t, survivor, func() {
		native := spawnTestPlayerEntity(survivor, 700, 9700, "native", 9, 700, 700)
		survivor.Stage.NetworkIDMap().Get(native).Epoch = 3
	})

	sendDonor := func(source string, frames ...*TransferFrame) {
		t.Helper()
		records := make([][]byte, 0, len(frames))
		for _, frame := range frames {
			blob, err := MarshalTransferFrame(frame)
			if err != nil {
				t.Fatalf("MarshalTransferFrame: %v", err)
			}
			records = append(records, blob)
		}
		if err := exec.Receive(&meshpb.CellTransfer{
			RequestId:  requestID,
			Kind:       meshpb.CellTransferKind_CELL_TRANSFER_MERGE,
			SrcCellId:  source,
			DestCellId: string(survivor.MeshID()),
			DestHostId: host.ID,
			Entities:   packRecords(records),
		}); err != nil {
			t.Fatalf("Receive donor %s: %v", source, err)
		}
	}
	player := func(netID, connID, epoch, generation uint32, username string, x float32) *TransferFrame {
		return &TransferFrame{
			NetworkID:        netID,
			Epoch:            epoch,
			StreamGeneration: generation,
			EntityType:       1,
			ConnID:           connID,
			Username:         username,
			PosX:             x,
			PosY:             x,
			Collider:         component.Collider{Radius: 4},
		}
	}

	// Native survivor authority wins even when a donor advertises larger
	// serials. For donor-only collisions, epoch wins first, then stream
	// generation; wrapped zero is newer than MaxUint32.
	sendDonor("cell_10_0",
		player(700, 9700, 100, 100, "native", 100),
		player(701, 9701, 10, 20, "epoch-winner", 10),
		player(702, 9702, 5, 10, "stream-winner", 10),
		player(703, 9703, ^uint32(0)-1, ^uint32(0)-1, "wrap-winner", 10),
	)
	sendDonor("cell_11_0",
		player(701, 9701, 11, 1, "epoch-winner", 20),
		player(702, 9702, 5, 11, "stream-winner", 20),
		player(703, 9703, ^uint32(0), ^uint32(0), "wrap-winner", 20),
	)
	sendDonor("cell_12_0",
		player(701, 9701, 10, 99, "epoch-winner", 30),
		player(702, 9702, 5, 10, "stream-winner", 30),
		player(703, 9703, ^uint32(0)-1, ^uint32(0)-1, "wrap-winner", 30),
	)

	type authority struct {
		epoch      uint32
		generation uint32
		x          float32
	}
	got := make(map[uint32]authority)
	var inspectErrors []string
	execOnLoop(t, survivor, func() {
		for _, netID := range []uint32{700, 701, 702, 703} {
			entity, presence, ok := survivor.Stage.LookupNetID(netID)
			if !ok || presence != PresenceLive {
				inspectErrors = append(inspectErrors, fmt.Sprintf("netID %d missing live authority", netID))
				continue
			}
			nid := survivor.Stage.NetworkIDMap().Get(entity)
			pos := survivor.Stage.PositionMap().Get(entity)
			connID := survivor.Stage.playerMap.Get(entity).ConnID
			session := survivor.Engine.Players.ByConnID(connID)
			if session == nil {
				inspectErrors = append(inspectErrors, fmt.Sprintf("netID %d missing player session", netID))
				continue
			}
			got[netID] = authority{epoch: nid.Epoch, generation: session.StreamGeneration, x: pos.X}
		}
	})
	if len(inspectErrors) != 0 {
		t.Fatal(inspectErrors)
	}

	want := map[uint32]authority{
		700: {epoch: 3, generation: 9, x: 700},
		701: {epoch: 12, generation: 2, x: 20},
		702: {epoch: 6, generation: 12, x: 20},
		703: {epoch: 0, generation: 0, x: 20},
	}
	for netID, expected := range want {
		if got[netID] != expected {
			t.Errorf("netID %d authority = %+v, want %+v", netID, got[netID], expected)
		}
	}
}

func TestExecutorAbortRemovesPartialMergeImports(t *testing.T) {
	coord, host, survivor := newExecutorTestCoord(t)
	exec := coord.hostExecutors[host.ID]
	const requestID = uint64(0xAB07)
	coord.vcm = NewVirtualConnManager(nil, coord.Log)

	execOnLoop(t, survivor, func() {
		native := spawnTestPlayerEntity(survivor, 800, 9800, "native-abort", 15, 800, 800)
		survivor.Stage.NetworkIDMap().Get(native).Epoch = 4
	})

	var entityRecords [][]byte
	for _, frame := range []*TransferFrame{
		{NetworkID: 801, Epoch: 7, StreamGeneration: 20, EntityType: 1, ConnID: 9801, Username: "donor-one", PosX: 10, PosY: 10},
		{NetworkID: 802, Epoch: 8, StreamGeneration: 30, EntityType: 1, ConnID: 9802, Username: "donor-two", PosX: 20, PosY: 20},
	} {
		blob, err := MarshalTransferFrame(frame)
		if err != nil {
			t.Fatalf("MarshalTransferFrame: %v", err)
		}
		entityRecords = append(entityRecords, blob)
	}
	entityless, err := json.Marshal(SessionTransfer{
		ConnID:           9803,
		GatewayID:        "gateway-new",
		GatewayConnID:    19803,
		SessionEpoch:     6,
		Username:         "entityless-donor",
		StateTag:         "active",
		StreamGeneration: 40,
	})
	if err != nil {
		t.Fatalf("json.Marshal entityless session: %v", err)
	}
	if err := exec.Receive(&meshpb.CellTransfer{
		RequestId:  requestID,
		Kind:       meshpb.CellTransferKind_CELL_TRANSFER_MERGE,
		SrcCellId:  "cell_20_0",
		DestCellId: string(survivor.MeshID()),
		DestHostId: host.ID,
		Entities:   packRecords(entityRecords),
		Sessions:   packRecords([][]byte{entityless}),
	}); err != nil {
		t.Fatalf("Receive partial donor: %v", err)
	}
	entitylessLocalID, ok := coord.vcm.LookupByKey(SessionKey{GatewayID: "gateway-new", ConnID: 19803})
	if !ok {
		t.Fatal("destination did not register entity-less SessionKey")
	}

	execOnLoop(t, survivor, func() {
		for _, netID := range []uint32{801, 802} {
			if _, presence, ok := survivor.Stage.LookupNetID(netID); !ok || presence != PresenceLive {
				t.Errorf("pre-abort imported netID %d missing", netID)
			}
		}
		for _, connID := range []uint32{9801, 9802, entitylessLocalID} {
			if survivor.Engine.Players.ByConnID(connID) == nil {
				t.Errorf("pre-abort imported session %d missing", connID)
			}
		}
	})
	if !survivor.Stage.IsDrainingForMerge() {
		t.Fatal("merge survivor was not frozen before abort")
	}

	exec.Abort(&meshpb.CellTransferAbort{RequestId: requestID})

	execOnLoop(t, survivor, func() {
		if _, presence, ok := survivor.Stage.LookupNetID(800); !ok || presence != PresenceLive {
			t.Error("abort removed native survivor authority")
		}
		if session := survivor.Engine.Players.ByConnID(9800); session == nil || session.StreamGeneration != 15 {
			t.Errorf("native session after abort = %+v, want generation 15", session)
		}
		for _, netID := range []uint32{801, 802} {
			if _, _, ok := survivor.Stage.LookupNetID(netID); ok {
				t.Errorf("abort retained imported donor netID %d", netID)
			}
		}
		for _, connID := range []uint32{9801, 9802, entitylessLocalID} {
			if session := survivor.Engine.Players.ByConnID(connID); session != nil {
				t.Errorf("abort retained imported session %d: %+v", connID, session)
			}
		}
	})
	if survivor.Stage.IsDrainingForMerge() {
		t.Fatal("abort left merge survivor frozen")
	}
	if _, ok := coord.vcm.LookupByKey(SessionKey{GatewayID: "gateway-new", ConnID: 19803}); ok {
		t.Fatal("abort retained destination-created VCM mapping")
	}
	exec.mu.Lock()
	_, nativeTracked := exec.mergeNativeLive[requestID]
	_, importsTracked := exec.mergeImportedLive[requestID]
	_, sessionsTracked := exec.mergeImportedSessions[requestID]
	exec.mu.Unlock()
	if nativeTracked || importsTracked || sessionsTracked {
		t.Fatalf("abort retained merge rollback bookkeeping: native=%v imports=%v sessions=%v",
			nativeTracked, importsTracked, sessionsTracked)
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
		spawnTestPlayerEntity(srcCell, 44, 4400, "migrate-player", 19, 50, 60)
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
		ID: reqID, Kind: CellTransferMigrate, SrcCell: srcCell.CellID(),
		ExpectedReady: 1, receivedOK: make(map[string]struct{}),
		ackedCmd: make([]bool, 1),
		commands: []cellTransferCommand{{
			RequestID:  reqID,
			Kind:       CellTransferMigrate,
			SrcCellID:  srcCell.MeshID(),
			DestCellID: destCellID.MeshID(),
			SrcHostID:  host.ID,
			DestHostID: destHost.ID,
		}},
		Deadline: time.Now().Add(5 * time.Second),
		Done:     make(chan struct{}),
		mutation: topologyMutation{add: map[MeshCellID]string{destCellID.MeshID(): destHost.ID}},
	}
	coord.orchestrator.mu.Unlock()

	cmd := cellTransferCommand{
		RequestID:  reqID,
		Kind:       CellTransferMigrate,
		SrcCellID:  srcCell.MeshID(),
		DestCellID: destCellID.MeshID(),
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

	type migratedEntity struct {
		position component.Position
		epoch    uint32
	}
	netFound := map[uint32]migratedEntity{}
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
			nid := netMap.Get(e)
			netFound[nid.ID] = migratedEntity{position: *posMap.Get(e), epoch: nid.Epoch}
		}
	})
	if len(netFound) != 3 {
		t.Fatalf("migrate: got %d entities on dest, want 3", len(netFound))
	}
	if got, ok := netFound[42]; !ok || got.position.X != 10 || got.position.Y != 20 || got.epoch != 1 {
		t.Errorf("netID 42: got (%.0f,%.0f) epoch=%d ok=%v", got.position.X, got.position.Y, got.epoch, ok)
	}
	if got, ok := netFound[43]; !ok || got.position.X != 30 || got.position.Y != 40 || got.epoch != 1 {
		t.Errorf("netID 43: got (%.0f,%.0f) epoch=%d ok=%v", got.position.X, got.position.Y, got.epoch, ok)
	}
	if got, ok := netFound[44]; !ok || got.position.X != 50 || got.position.Y != 60 || got.epoch != 1 {
		t.Errorf("netID 44: got (%.0f,%.0f) epoch=%d ok=%v", got.position.X, got.position.Y, got.epoch, ok)
	}
	if sess := destCell.Engine.Players.ByConnID(4400); sess == nil {
		t.Fatal("migrate: player session missing on destination")
	} else if sess.StreamGeneration != 20 {
		t.Errorf("migrate: StreamGeneration = %d, want source N+1 = 20", sess.StreamGeneration)
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
	destKey := destCellID.MeshID()
	spatialCellSize := coord.resolveSpatialCellSize()
	coord.mu.Lock()
	node, systems := coord.createNode(destCellID, spatialCellSize, host, true)
	host.AddCell(destCellID, node)
	coord.mu.Unlock()
	node.Stage.Init()
	initSystems(systems)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go node.Run(ctx)

	reqID := uint64(555)
	exec.mu.Lock()
	exec.pending[reqID] = map[MeshCellID]*pendingReceive{
		destKey: {
			requestID: reqID,
			cellID:    destCellID,
			cellKey:   destKey,
			cell:      node,
		},
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

func TestExecutorReceiveRetainsRollbackStateUntilCommit(t *testing.T) {
	coord, host, srcCell := newExecutorTestCoord(t)
	exec := coord.hostExecutors[host.ID]
	const requestID = uint64(556)
	destCellID := CellID{X: 4, Y: 4, Depth: 0}
	destKey := destCellID.MeshID()

	err := exec.Receive(&meshpb.CellTransfer{
		RequestId:  requestID,
		Kind:       meshpb.CellTransferKind_CELL_TRANSFER_SPLIT,
		SrcCellId:  string(srcCell.MeshID()),
		DestCellId: string(destKey),
		DestHostId: host.ID,
	})
	if err != nil {
		t.Fatalf("Receive: %v", err)
	}

	exec.mu.Lock()
	pending := exec.pending[requestID]
	_, tracksDest := pending[destKey]
	exec.mu.Unlock()
	if !tracksDest {
		t.Fatalf("successful Receive released rollback state before commit: pending=%v", pending)
	}

	exec.Commit(requestID)
	exec.mu.Lock()
	_, stillPending := exec.pending[requestID]
	exec.mu.Unlock()
	if stillPending {
		t.Fatal("Commit did not release rollback state")
	}
	coord.mu.RLock()
	_, stillLive := coord.Cells[destKey]
	coord.mu.RUnlock()
	if !stillLive || host.CellByID(destKey) == nil {
		t.Fatal("Commit tore down the successfully received destination cell")
	}
}

func TestExecutorAbortReactivatesEveryViewerOnForwardGeneration(t *testing.T) {
	coord, host, srcCell := newExecutorTestCoord(t)
	const requestID = uint64(0xA80)

	var activeSession, customSession *engine.PlayerSession
	var activeEntity ecs.Entity
	execOnLoop(t, srcCell, func() {
		// Keep the source entity alive so the test can prove abort recovery
		// does not mutate NetworkID.Epoch. Games may override the framework's
		// default leave cleanup in the same last-writer-wins manner.
		srcCell.Engine.Players.AddTransition(engine.StateTransition{
			From: engine.StateActive,
			To:   engine.StateTransferring,
		})
		activeEntity = spawnTestPlayerEntity(srcCell, 701, 7001, "active-abort", 40, 100, 100)
		srcCell.Stage.NetworkIDMap().Get(activeEntity).Epoch = 9
		activeSession = srcCell.Engine.Players.ByConnID(7001)

		spawnTestPlayerEntity(srcCell, 702, 7002, "custom-abort", 90, 200, 200)
		customSession = srcCell.Engine.Players.ByConnID(7002)
		customSession.State = srcCell.Engine.Players.RegisterState("dead-test")

		srcCell.Stage.DeactivateTransferViewers()
	})

	if !activeSession.ReplicationSuspended || !customSession.ReplicationSuspended {
		t.Fatalf("transfer did not suspend all connected viewers: active=%v custom=%v",
			activeSession.ReplicationSuspended, customSession.ReplicationSuspended)
	}
	if activeSession.State != engine.StateTransferring {
		t.Fatalf("active session state = %s, want transferring", srcCell.Engine.Players.StateName(activeSession.State))
	}
	customState := customSession.State
	if customState == engine.StateTransferring {
		t.Fatal("custom lifecycle session was rewritten to transferring")
	}

	// The destination copy is serialized at N+1 and may emit before the
	// coordinator's abort reaches both hosts.
	activeDestinationGeneration := activeSession.StreamGeneration + 1
	customDestinationGeneration := customSession.StreamGeneration + 1

	exec := coord.hostExecutors[host.ID]
	exec.mu.Lock()
	exec.transferSrcCells[requestID] = map[MeshCellID]*Cell{srcCell.MeshID(): srcCell}
	exec.mu.Unlock()
	exec.Abort(&meshpb.CellTransferAbort{RequestId: requestID})

	execOnLoop(t, srcCell, func() {
		if activeSession.ReplicationSuspended || customSession.ReplicationSuspended {
			t.Fatalf("abort left viewers suspended: active=%v custom=%v",
				activeSession.ReplicationSuspended, customSession.ReplicationSuspended)
		}
		if activeSession.State != engine.StateActive {
			t.Errorf("active session state after abort = %s, want active", srcCell.Engine.Players.StateName(activeSession.State))
		}
		if customSession.State != customState {
			t.Errorf("custom session state after abort = %s, want unchanged %s",
				srcCell.Engine.Players.StateName(customSession.State), srcCell.Engine.Players.StateName(customState))
		}
		if activeSession.StreamGeneration != activeDestinationGeneration+1 {
			t.Errorf("active source generation after abort = %d, want destination+1 = %d",
				activeSession.StreamGeneration, activeDestinationGeneration+1)
		}
		if customSession.StreamGeneration != customDestinationGeneration+1 {
			t.Errorf("custom source generation after abort = %d, want destination+1 = %d",
				customSession.StreamGeneration, customDestinationGeneration+1)
		}
		if got := srcCell.Stage.NetworkIDMap().Get(activeEntity).Epoch; got != 9 {
			t.Errorf("NetworkID.Epoch changed on abort: got %d, want 9", got)
		}
	})
}

func TestExecutorAbortRestoresSameHostVCMAndReconnectRoute(t *testing.T) {
	coord, host, srcCell := newExecutorTestCoord(t)
	exec := coord.hostExecutors[host.ID]
	coord.vcm = NewVirtualConnManager(nil, coord.Log)
	key := SessionKey{GatewayID: "gateway-shared", ConnID: 4200}
	localID := coord.vcm.RegisterSession(key, "same-host-abort", 17, srcCell.MeshID())
	userID := uuid.MustParse("de52bd12-7605-441c-900f-8d55de93f4cb")
	const requestID = uint64(0x5A4E)

	execOnLoop(t, srcCell, func() {
		entity := spawnTestPlayerEntity(srcCell, 740, localID, "same-host-abort", 50, 100, 100)
		session := srcCell.Engine.Players.ByConnID(localID)
		session.UserID = userID
		session.Entity = entity
		coord.touchActiveUser(userID, session.Username, key.GatewayID, key.ConnID, host.ID, srcCell.MeshID())
		srcCell.Stage.DeactivateTransferViewers()
	})

	speculativeDest := MeshCellID("cell_9_9")
	if got := coord.vcm.RegisterSession(key, "same-host-abort", 17, speculativeDest); got != localID {
		t.Fatalf("speculative registration localID = %d, want stable %d", got, localID)
	}
	coord.touchActiveUser(userID, "same-host-abort", key.GatewayID, key.ConnID, host.ID, speculativeDest)
	exec.mu.Lock()
	exec.transferSrcCells[requestID] = map[MeshCellID]*Cell{srcCell.MeshID(): srcCell}
	exec.mu.Unlock()

	exec.Abort(&meshpb.CellTransferAbort{RequestId: requestID})

	var generation uint32
	var suspended bool
	execOnLoop(t, srcCell, func() {
		session := srcCell.Engine.Players.ByConnID(localID)
		if session == nil {
			t.Error("source session missing after abort")
			return
		}
		generation = session.StreamGeneration
		suspended = session.ReplicationSuspended
	})
	if generation != 52 || suspended {
		t.Fatalf("source session after abort: generation=%d suspended=%v, want 52/false", generation, suspended)
	}
	coord.vcm.mu.RLock()
	vcmCell := coord.vcm.byLocal[localID].cellID
	coord.vcm.mu.RUnlock()
	if vcmCell != srcCell.MeshID() {
		t.Fatalf("VCM cell after abort = %s, want source %s", vcmCell, srcCell.MeshID())
	}
	coord.mu.RLock()
	active := coord.activeUsers[userID]
	coord.mu.RUnlock()
	if active == nil || active.HostID != host.ID || active.CellID != srcCell.MeshID() {
		t.Fatalf("active user route after abort = %+v, want host=%s cell=%s", active, host.ID, srcCell.MeshID())
	}
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
	hn, err := NewHostNetwork(host, ":0", coord.Log, 50*time.Millisecond)
	if err != nil {
		t.Fatalf("NewHostNetwork: %v", err)
	}
	defer hn.Shutdown()
	host.Network = hn
	hn.SetCoord(coord)

	// Seed an inflight request keyed on (hostID, destCellID).
	destCellID := CellID{X: 11, Y: 11, Depth: 0}
	destKey := destCellID.MeshID()
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
		mutation: topologyMutation{add: map[MeshCellID]string{destKey: "some-host"}},
	}
	coord.orchestrator.mu.Lock()
	coord.orchestrator.inflight[reqID] = req
	coord.orchestrator.mu.Unlock()

	frame := &meshpb.MeshFrame{
		DestCellId: string(destKey),
		Msg: &meshpb.MeshFrame_CellTransferReady{
			CellTransferReady: &meshpb.CellTransferReady{
				RequestId:  reqID,
				DestCellId: string(destKey),
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
