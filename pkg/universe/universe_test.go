package universe

import (
	"testing"

	"github.com/mlange-42/ark/ecs"
	"github.com/zenion/mmoserver/pkg/component"
	"github.com/zenion/mmoserver/pkg/coords"
	"github.com/zenion/mmoserver/pkg/engine"
	"github.com/zenion/mmoserver/pkg/logger"
	"github.com/zenion/mmoserver/pkg/net"
)

// ---------------------------------------------------------------------------
// Mock GameWorld
// ---------------------------------------------------------------------------

type mockWorld struct {
	spawned        [][]byte
	chats          []ChatRelay
	bridge         Bridge
	shutdownCalled bool

	// SerializeEntity returns these
	serializeResult []byte
	serializeErr    error

	// SpawnFromTransfer returns these
	spawnNetID  uint32
	spawnConnID uint32
	spawnErr    error

	// Cross-node action tracking
	actionsReceived []CrossCellAction
	actionResults   []ActionResult
	// HandleCrossCellAction returns this
	actionResultToReturn *ActionResult
}

func (m *mockWorld) Init() {}

func (m *mockWorld) SerializeEntity(ecs.Entity) ([]byte, error) {
	return m.serializeResult, m.serializeErr
}

func (m *mockWorld) SpawnFromTransfer(data []byte) (uint32, uint32, error) {
	m.spawned = append(m.spawned, data)
	return m.spawnNetID, m.spawnConnID, m.spawnErr
}

func (m *mockWorld) MarkForRemoval(ecs.Entity) {}

func (m *mockWorld) DispatchChat(username, text string) {
	m.chats = append(m.chats, ChatRelay{Username: username, Text: text})
}

func (m *mockWorld) SetBridge(bridge Bridge) {
	m.bridge = bridge
}

func (m *mockWorld) HandleCrossCellAction(action *CrossCellAction) *ActionResult {
	m.actionsReceived = append(m.actionsReceived, *action)
	return m.actionResultToReturn
}

func (m *mockWorld) HandleActionResult(result *ActionResult) {
	m.actionResults = append(m.actionResults, *result)
}

func (m *mockWorld) Shutdown() {
	m.shutdownCalled = true
}

func (m *mockWorld) UpdateCellBounds(CellID, float32) {}

func (m *mockWorld) Hooks() engine.Hooks {
	return engine.Hooks{}
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

func newTestCell(id string, cell CellID) (*Cell, *mockWorld) {
	mw := &mockWorld{
		spawnNetID:  100,
		spawnConnID: 42,
	}
	log := logger.New()
	connMgr := net.NewConnManager()
	eng := engine.New(engine.DefaultConfig(), connMgr, log)
	base := NewStage(eng, cell, 0, nil)
	return &Cell{
		MeshID:    MeshCellID(id),
		Cell:      cell,
		Engine:    eng,
		World:     mw,
		Stage:     base,
		Inbox:     make(chan CellMessage, 64),
		Neighbors: make(map[string]*Cell),
		Log:       log,
	}, mw
}

func newTestCoordinator(cfg Config) (*Process, map[CellID]*mockWorld) {
	worlds := make(map[CellID]*mockWorld)
	if cfg.ConnManager == nil {
		cfg.ConnManager = net.NewConnManager()
	}
	if cfg.Logger == nil {
		cfg.Logger = logger.New()
	}
	cfg.World = func(base *Stage) GameWorld {
		mw := &mockWorld{spawnNetID: 100, spawnConnID: 42}
		worlds[base.Cell()] = mw
		return mw
	}
	c := New(cfg)
	c.Build()
	// Ensure a default spawn so login routing works in the test fixture.
	// The test doesn't care about saved positions; any point inside a
	// live cell is fine — CellAtPosition resolves to whichever cell owns it.
	if c.cfg.DefaultSpawn.IsZero() {
		c.cfg.DefaultSpawn = coords.Location{
			X: c.cfg.CellSize / 2,
			Y: c.cfg.CellSize / 2,
		}
	}
	return c, worlds
}

// ---------------------------------------------------------------------------
// Cell tests
// ---------------------------------------------------------------------------

// TestCell_DrainInbox_Handoff verifies H2 hard-cut destination-side
// semantics: MsgHandoff queues a promote for CommitTick rather than
// spawning/promoting immediately. The entity only becomes Live on
// this cell when drainPendingPromotes is called with a cluster-tick
// >= CommitTick. This matches the source-side H1 contract: both ends
// flip authority at the same cluster-coherent tick.
func TestCell_DrainInbox_Handoff(t *testing.T) {
	cell, _ := newTestCell("dest", CellID{X: 1, Y: 0})
	cell.Bridge = &recordingBridge{}

	// Build a valid TransferBlob via SerializeEntity on a temp entity so
	// SpawnLiveFromTransfer -> SpawnFromTransferCore can decode it.
	world := cell.Engine.ECS
	temp := world.NewEntity()
	ecs.NewMap1[component.Position](world).Add(temp, &component.Position{X: 50, Y: 60})
	ecs.NewMap1[component.Velocity](world).Add(temp, &component.Velocity{X: 1, Y: 2})
	ecs.NewMap1[component.NetworkID](world).Add(temp, &component.NetworkID{ID: 77})
	ecs.NewMap1[component.EntityKind](world).Add(temp, &component.EntityKind{Type: 1})
	ecs.NewMap1[component.Collider](world).Add(temp, &component.Collider{Radius: 4})
	ecs.NewMap1[component.Rotation](world).Add(temp, &component.Rotation{Angle: 0})
	ecs.NewMap1[component.CellCoord](world).Add(temp, &component.CellCoord{CellX: 1, CellY: 0})

	blob, err := cell.Stage.SerializeEntity(temp)
	if err != nil {
		t.Fatalf("SerializeEntity: %v", err)
	}
	world.RemoveEntity(temp)

	const commitTick uint64 = 42
	cell.Inbox <- CellMessage{
		Type:       MsgHandoff,
		FromCellID: "source",
		Handoff: &HandoffPayload{
			NetID:        77,
			Epoch:        2,
			CommitTick:   commitTick,
			TransferBlob: blob,
		},
	}

	cell.DrainInbox()

	// Post-DrainInbox: NO Live entity yet — promote is queued.
	netFilter := ecs.NewFilter1[component.NetworkID](world)
	nq := netFilter.Query()
	for nq.Next() {
		nid := nq.Get()
		if nid.ID == 77 {
			nq.Close()
			t.Fatalf("unexpected Live entity for netID 77 before CommitTick (hard-cut must defer)")
		}
	}
	nq.Close()
	if list, ok := cell.pendingPromotes[commitTick]; !ok || len(list) != 1 {
		t.Fatalf("pendingPromotes[%d] = %v, want 1 entry", commitTick, cell.pendingPromotes)
	}

	// Ticking BEFORE CommitTick must not commit.
	cell.drainPendingPromotes(commitTick - 1)
	if _, ok := cell.pendingPromotes[commitTick]; !ok {
		t.Fatal("pendingPromotes entry cleared before CommitTick")
	}

	// Ticking AT CommitTick fires the spawn.
	cell.drainPendingPromotes(commitTick)

	// Now we expect a Live entity with NetworkID=77 and epoch=2.
	nq = netFilter.Query()
	found := false
	for nq.Next() {
		nid := nq.Get()
		if nid.ID == 77 {
			found = true
			if nid.Epoch != 2 {
				t.Errorf("NetworkID.Epoch = %d, want 2", nid.Epoch)
			}
			break
		}
	}
	nq.Close()
	if !found {
		t.Fatal("expected a Live entity with NetworkID.ID=77 after commit-tick drain")
	}
	// Pending queue drained.
	if _, ok := cell.pendingPromotes[commitTick]; ok {
		t.Fatal("pendingPromotes still has entry for committed tick")
	}
}

// TestCell_MsgHandoff_ReplacesExistingReplicaAtCommitTick verifies the
// common-case path: a border-replica already exists for netID; the
// commit-tick drain removes it and spawns a fresh Live entity from the
// TransferBlob. The entity handle is NOT preserved — the TransferBlob
// carries authoritative full state (including PlayerConn, game-specific
// components) that the border-replica doesn't have, so the commit path
// goes through SpawnLiveFromTransfer rather than a bare presence flip.
// Without this, player entities would lose their PlayerConn link and
// the engine's input router would no longer find them for the connID —
// client loses control of their player.
func TestCell_MsgHandoff_ReplacesExistingReplicaAtCommitTick(t *testing.T) {
	cell, _ := newTestCell("dest", CellID{X: 1, Y: 0})
	cell.Bridge = &recordingBridge{}
	world := cell.Engine.ECS

	// Stand up a border-replica for netID=77 via the normal upsert path.
	cell.Stage.upsertBorderReplica(77, 1, 1, 50, 60, 4, 1, 2, 0, "source", 0, nil)

	// Precondition: replica exists.
	replicaEnt, presence, ok := cell.Stage.LookupNetID(77)
	if !ok || presence != PresenceReplica {
		t.Fatalf("pre-handoff: netID=77 presence=%v ok=%v, want Replica", presence, ok)
	}

	// Build a TransferBlob carrying the authoritative state.
	temp := world.NewEntity()
	ecs.NewMap1[component.Position](world).Add(temp, &component.Position{X: 200, Y: 300})
	ecs.NewMap1[component.Velocity](world).Add(temp, &component.Velocity{X: 1, Y: 2})
	ecs.NewMap1[component.NetworkID](world).Add(temp, &component.NetworkID{ID: 77})
	ecs.NewMap1[component.EntityKind](world).Add(temp, &component.EntityKind{Type: 1})
	ecs.NewMap1[component.Collider](world).Add(temp, &component.Collider{Radius: 4})
	ecs.NewMap1[component.Rotation](world).Add(temp, &component.Rotation{Angle: 0})
	ecs.NewMap1[component.CellCoord](world).Add(temp, &component.CellCoord{CellX: 1, CellY: 0})
	blob, err := cell.Stage.SerializeEntity(temp)
	if err != nil {
		t.Fatalf("SerializeEntity: %v", err)
	}
	world.RemoveEntity(temp)

	const commitTick uint64 = 10
	cell.Inbox <- CellMessage{
		Type:       MsgHandoff,
		FromCellID: "source",
		Handoff: &HandoffPayload{
			NetID:        77,
			Epoch:        5,
			CommitTick:   commitTick,
			TransferBlob: blob,
		},
	}
	cell.DrainInbox()
	cell.drainPendingPromotes(commitTick)

	// Post-commit: the netID is Live on this cell.
	got, presence, ok := cell.Stage.LookupNetID(77)
	if !ok || presence != PresenceLive {
		t.Fatalf("post-commit: netID=77 presence=%v ok=%v, want Live", presence, ok)
	}

	// The old replica entity was torn down by RemoveReplicaByNetID; the
	// Live entity is a fresh ECS row. Handle should NOT equal the old one.
	if got == replicaEnt {
		t.Errorf("post-commit: expected fresh entity handle; got same as replica (%v)", got)
	}

	// Epoch carried through from the Handoff payload.
	netMap := ecs.NewMap1[component.NetworkID](world)
	if nid := netMap.Get(got); nid.Epoch != 5 {
		t.Errorf("post-commit: Epoch = %d, want 5", nid.Epoch)
	}

	// Position came from the BORDER-REPLICA's captured tip-of-motion
	// (50, 60). Blob's original (200, 300) is overwritten. No forward
	// extrapolation: source's final authoritative push at commitTick
	// PostSystems step 2 anchors the receiver's replica naturally.
	posMap := ecs.NewMap1[component.Position](world)
	if p := posMap.Get(got); p.X != 50 || p.Y != 60 {
		t.Errorf("post-commit: Position = (%.3f, %.3f), want (50, 60) from border replica", p.X, p.Y)
	}
	// Velocity also carried through from the replica (1, 2), not the
	// blob (which also had (1,2) — they match in this test so the
	// assertion is less discriminating, but document the invariant).
	velMap := ecs.NewMap1[component.Velocity](world)
	if v := velMap.Get(got); v.X != 1 || v.Y != 2 {
		t.Errorf("post-commit: Velocity = (%.1f, %.1f), want (1, 2)", v.X, v.Y)
	}

	// Old replica is gone from the registry.
	if _, ok := cell.Stage.ReplicaNetIDs()[77]; ok {
		t.Error("post-commit: replicaNetIDs[77] still present — replica should have been removed")
	}
}

// TestCell_MsgHandoff_PreservesDebugFlagsOnSession is the regression test for
// the topology-overlay-stops-after-cross-cell-walk bug.
//
// Scenario: a player has DebugTopology granted on cell A, walks east, crosses
// the cell boundary, and lands on cell B via a regular boundary handoff (NOT
// a split/merge/migrate). After the commit tick, the session that lives in
// cell B's PlayerManager must carry the SAME DebugFlags bitmask the source
// session had. Otherwise the destination cell's debugBroadcaster sees
// `sess.DebugFlags == 0` on its first scan, short-circuits, and the player
// never receives another SE_DEBUG_INFO event — meaning a subsequent cell
// split/merge produces no client-visible topology refresh and the overlay
// stays frozen on the pre-handoff layout.
//
// The boundary handoff path differs from the cell-transfer (split/merge/
// migrate) path: cell.MsgHandoff handler calls
// `Engine.Players.RegisterTransferSession(localID, username)` BEFORE the
// commit-tick spawn, then drainPendingPromotes invokes
// SpawnLiveFromTransfer → SpawnFromTransferCore → onPlayerTransferReceived
// hook to rejoin the session to the spawned entity. The hook must copy
// frame.DebugFlags onto the pre-registered session — the wire format already
// carries the bitmask (TestTransferFrame_DebugFlagsRoundtrip pins that), but
// the destination side has to actually read it back into the session. The
// cell-transfer path handles this in cell_transfer_executor.populateCell;
// the boundary-handoff path handles it in the hook.
func TestCell_MsgHandoff_PreservesDebugFlagsOnSession(t *testing.T) {
	const cellSize = float32(1024)
	coords.SetCellSize(cellSize)

	c, _ := newTestCoordinator(Config{CellsX: 2, CellsY: 1, CellSize: cellSize})
	dst := c.Cells[CellID{X: 1, Y: 0}.MeshID()]
	if dst == nil {
		t.Fatal("dest cell 1_0 missing from coordinator")
	}

	const connID uint32 = 99
	const netID uint32 = 4242
	const wantFlags = engine.DebugTopology

	// Build a TransferFrame directly — exercise the wire-format path.
	frame := &TransferFrame{
		NetworkID:  netID,
		Epoch:      1,
		EntityType: 1,
		ConnID:     connID,
		Username:   "alice",
		PosX:       50,
		PosY:       60,
		VelX:       0,
		VelY:       0,
		Rotation:   0,
		Collider:   component.Collider{Radius: 5},
		CellX:      1,
		CellY:      0,
		DebugFlags: uint32(wantFlags),
	}
	blob, err := MarshalTransferFrame(frame)
	if err != nil {
		t.Fatalf("MarshalTransferFrame: %v", err)
	}

	const commitTick uint64 = 7
	dst.Inbox <- CellMessage{
		Type:       MsgHandoff,
		FromCellID: "cell_0_0",
		Handoff: &HandoffPayload{
			NetID:        netID,
			Epoch:        1,
			CommitTick:   commitTick,
			TransferBlob: blob,
			ConnID:       connID,
		},
	}
	dst.DrainInbox()

	// Pre-condition: MsgHandoff handler pre-registered the session with
	// DebugFlags=0. The fix must restore the bits at commit-tick spawn.
	if s := dst.Engine.Players.ByConnID(connID); s == nil {
		t.Fatal("after DrainInbox: session not pre-registered by RegisterTransferSession")
	} else if s.DebugFlags != 0 {
		t.Fatalf("pre-spawn DebugFlags = 0b%b, want 0 (set by RegisterTransferSession, no flags carried)",
			s.DebugFlags)
	}

	// Commit-tick spawn fires SpawnLiveFromTransfer → SpawnFromTransferCore →
	// onPlayerTransferReceived. The hook is what propagates frame.DebugFlags
	// onto the pre-registered session.
	dst.drainPendingPromotes(commitTick)

	s := dst.Engine.Players.ByConnID(connID)
	if s == nil {
		t.Fatal("after drainPendingPromotes: session vanished")
	}
	if s.DebugFlags != wantFlags {
		t.Errorf("post-spawn DebugFlags = 0b%b, want 0b%b — boundary handoff dropped DebugFlags",
			s.DebugFlags, wantFlags)
	}
}

func TestCell_DrainInbox_Chat(t *testing.T) {
	node, mw := newTestCell("dest", CellID{X: 0, Y: 0})
	node.Bridge = &recordingBridge{}

	node.Inbox <- CellMessage{
		Type:       MsgChat,
		FromCellID: "other",
		Chat:       &ChatRelay{Username: "alice", Text: "hello"},
	}

	node.DrainInbox()

	if len(mw.chats) != 1 {
		t.Fatalf("expected 1 chat dispatch, got %d", len(mw.chats))
	}
	if mw.chats[0].Username != "alice" || mw.chats[0].Text != "hello" {
		t.Fatalf("unexpected chat: %+v", mw.chats[0])
	}
}

func TestCell_DrainInbox_SpawnTransfer(t *testing.T) {
	node, _ := newTestCell("default", CellID{X: 0, Y: 0})
	node.Bridge = &recordingBridge{}

	node.Inbox <- CellMessage{
		Type:       MsgSpawnTransfer,
		FromCellID: "other",
		Spawn:      &SpawnTransfer{ConnID: 99, Username: "bob"},
	}

	node.DrainInbox()

	s := node.Engine.Players.ByUsername("bob")
	if s == nil {
		t.Fatal("expected session for 'bob' after RegisterTransferSession")
	}
	if s.ConnID != 99 {
		t.Fatalf("expected ConnID 99, got %d", s.ConnID)
	}
}

func TestCell_DrainInbox_TicksAfterDrain(t *testing.T) {
	node, _ := newTestCell("n", CellID{X: 0, Y: 0})
	node.Bridge = &recordingBridge{}

	// Empty inbox — DrainInbox calls TickGhosts and TickTransferCooldowns on Base
	node.DrainInbox()
	// No panic = success; ticks go to real Stage (no-op with empty world)
}

func TestCell_DrainInbox_MultipleMessages(t *testing.T) {
	node, mw := newTestCell("n", CellID{X: 0, Y: 0})
	node.Bridge = &recordingBridge{}

	node.Inbox <- CellMessage{
		Type:       MsgChat,
		FromCellID: "a",
		Chat:       &ChatRelay{Username: "u1", Text: "t1"},
	}
	node.Inbox <- CellMessage{
		Type:       MsgChat,
		FromCellID: "b",
		Chat:       &ChatRelay{Username: "u2", Text: "t2"},
	}

	node.DrainInbox()

	if len(mw.chats) != 2 {
		t.Fatalf("expected 2 chats, got %d", len(mw.chats))
	}
}

func TestCell_DrainInbox_CrossCellAction(t *testing.T) {
	node, mw := newTestCell("target", CellID{X: 0, Y: 0})
	rb := &recordingBridge{}
	node.Bridge = rb

	mw.actionResultToReturn = &ActionResult{
		Type:        1,
		TargetNetID: 42,
		SourceNetID: 10,
		Success:     true,
		Payload:     []byte("damage-result"),
	}

	node.Inbox <- CellMessage{
		Type:       MsgCrossCellAction,
		FromCellID: "source",
		Action: &CrossCellAction{
			Type:         1,
			TargetNetID:  42,
			SourceNetID:  10,
			SourceCellID: "source",
			Payload:      []byte("damage-payload"),
		},
	}

	node.DrainInbox()

	if len(mw.actionsReceived) != 1 {
		t.Fatalf("expected 1 action, got %d", len(mw.actionsReceived))
	}
	action := mw.actionsReceived[0]
	if action.TargetNetID != 42 || action.SourceNetID != 10 {
		t.Fatalf("unexpected action: %+v", action)
	}
	if string(action.Payload) != "damage-payload" {
		t.Fatalf("unexpected payload: %s", action.Payload)
	}

	// Result should be sent back via bridge
	if len(rb.actionResults) != 1 {
		t.Fatalf("expected 1 action result sent, got %d", len(rb.actionResults))
	}
	rec := rb.actionResults[0]
	if rec.destCellID != "source" {
		t.Fatalf("expected result sent to 'source', got '%s'", rec.destCellID)
	}
	if rec.result.TargetNetID != 42 || !rec.result.Success {
		t.Fatalf("unexpected result: %+v", rec.result)
	}
}

func TestCell_DrainInbox_CrossCellAction_NilResult(t *testing.T) {
	node, mw := newTestCell("target", CellID{X: 0, Y: 0})
	rb := &recordingBridge{}
	node.Bridge = rb

	mw.actionResultToReturn = nil // handler returns no result

	node.Inbox <- CellMessage{
		Type:       MsgCrossCellAction,
		FromCellID: "source",
		Action: &CrossCellAction{
			Type:         1,
			TargetNetID:  999,
			SourceNetID:  10,
			SourceCellID: "source",
			Payload:      []byte("miss"),
		},
	}

	node.DrainInbox()

	if len(mw.actionsReceived) != 1 {
		t.Fatalf("expected 1 action, got %d", len(mw.actionsReceived))
	}
	// No result should be sent back
	if len(rb.actionResults) != 0 {
		t.Fatalf("expected no action results sent, got %d", len(rb.actionResults))
	}
}

func TestCell_DrainInbox_ActionResult(t *testing.T) {
	node, mw := newTestCell("source", CellID{X: 0, Y: 0})
	node.Bridge = &recordingBridge{}

	node.Inbox <- CellMessage{
		Type:       MsgActionResult,
		FromCellID: "target",
		ActionResult: &ActionResult{
			Type:        1,
			TargetNetID: 42,
			SourceNetID: 10,
			Success:     true,
			Payload:     []byte("result-data"),
		},
	}

	node.DrainInbox()

	if len(mw.actionResults) != 1 {
		t.Fatalf("expected 1 action result, got %d", len(mw.actionResults))
	}
	result := mw.actionResults[0]
	if result.TargetNetID != 42 || !result.Success {
		t.Fatalf("unexpected result: %+v", result)
	}
	if string(result.Payload) != "result-data" {
		t.Fatalf("unexpected result payload: %s", result.Payload)
	}
}

// ---------------------------------------------------------------------------
// Process tests
// ---------------------------------------------------------------------------

func TestCoordinator_GridCreation(t *testing.T) {
	tests := []struct {
		name     string
		grid     Config
		expected int
	}{
		{"1x1", Config{CellsX: 1, CellsY: 1}, 1},
		{"2x2", Config{CellsX: 2, CellsY: 2}, 4},
		{"3x3", Config{CellsX: 3, CellsY: 3}, 9},
		{"2x3", Config{CellsX: 2, CellsY: 3}, 6},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := newTestCoordinator(tc.grid)
			if len(c.Cells) != tc.expected {
				t.Fatalf("expected %d nodes, got %d", tc.expected, len(c.Cells))
			}
		})
	}
}

func TestCoordinator_CellOwnership(t *testing.T) {
	grid := Config{CellsX: 3, CellsY: 3}
	c, _ := newTestCoordinator(grid)

	seen := make(map[string]bool)
	for sy := int32(0); sy <= 2; sy++ {
		for sx := int32(0); sx <= 2; sx++ {
			cell := CellID{X: sx, Y: sy}
			nodeID, ok := c.CellOwner[cell]
			if !ok {
				t.Fatalf("cell (%d,%d) has no owner", sx, sy)
			}
			if seen[string(nodeID)] {
				t.Fatalf("nodeID %s owns multiple cells", nodeID)
			}
			seen[string(nodeID)] = true
		}
	}

	if len(seen) != 9 {
		t.Fatalf("expected 9 unique node owners, got %d", len(seen))
	}
}

func TestCoordinator_NeighborWiring(t *testing.T) {
	grid := Config{CellsX: 3, CellsY: 3}
	c, _ := newTestCoordinator(grid)

	// Center node (1,1) should have 8 neighbors
	centerID := CellID{X: 1, Y: 1}.MeshID()
	center := c.Cells[centerID]
	if len(center.Neighbors) != 8 {
		t.Fatalf("center node expected 8 neighbors, got %d", len(center.Neighbors))
	}

	// Corner node (0,0) should have 3 neighbors
	cornerID := CellID{X: 0, Y: 0}.MeshID()
	corner := c.Cells[cornerID]
	if len(corner.Neighbors) != 3 {
		t.Fatalf("corner node expected 3 neighbors, got %d", len(corner.Neighbors))
	}

	// Edge node (1,0) should have 5 neighbors
	edgeID := CellID{X: 1, Y: 0}.MeshID()
	edge := c.Cells[edgeID]
	if len(edge.Neighbors) != 5 {
		t.Fatalf("edge node expected 5 neighbors, got %d", len(edge.Neighbors))
	}
}

func TestCoordinator_BridgeWired(t *testing.T) {
	grid := Config{CellsX: 2, CellsY: 2}
	c, worlds := newTestCoordinator(grid)

	for _, node := range c.Cells {
		if node.Bridge == nil {
			t.Fatalf("node %s has nil Bridge", node.MeshID)
		}
	}

	for cell, mw := range worlds {
		if mw.bridge == nil {
			t.Fatalf("world for cell (%d,%d) has nil bridge (SetBridge not called)", cell.X, cell.Y)
		}
	}
}

func TestCoordinator_NetIDRanges(t *testing.T) {
	grid := Config{CellsX: 3, CellsY: 1}
	c, _ := newTestCoordinator(grid)

	// Each node should have a unique engine (pointer).
	engines := make(map[*engine.Engine]string)
	for id, node := range c.Cells {
		if prev, exists := engines[node.Engine]; exists {
			t.Fatalf("nodes %s and %s share the same engine", prev, id)
		}
		engines[node.Engine] = string(id)
	}

	if len(engines) != 3 {
		t.Fatalf("expected 3 unique engines, got %d", len(engines))
	}
}

// ---------------------------------------------------------------------------
// Bridge tests (via coordinator-created nodeBridge)
// ---------------------------------------------------------------------------

func TestBridge_SendHandoff(t *testing.T) {
	grid := Config{CellsX: 2, CellsY: 1}
	c, _ := newTestCoordinator(grid)

	srcID := string(CellID{X: 0, Y: 0}.MeshID())
	dstID := string(CellID{X: 1, Y: 0}.MeshID())
	src := c.Cells[MeshCellID(srcID)]
	dst := c.Cells[MeshCellID(dstID)]

	payload := &HandoffPayload{
		NetID:        123,
		Epoch:        4,
		CommitTick:   505,
		TransferBlob: []byte("blob"),
		ConnID:       42,
	}
	src.Bridge.SendHandoff(dstID, payload)

	select {
	case msg := <-dst.Inbox:
		if msg.Type != MsgHandoff {
			t.Fatalf("expected MsgHandoff, got %d", msg.Type)
		}
		if msg.FromCellID != srcID {
			t.Fatalf("expected FromCellID %s, got %s", srcID, msg.FromCellID)
		}
		if msg.Handoff == nil {
			t.Fatal("Handoff payload is nil")
		}
		if msg.Handoff.NetID != 123 {
			t.Fatalf("NetID = %d, want 123", msg.Handoff.NetID)
		}
		if msg.Handoff.Epoch != 4 {
			t.Fatalf("Epoch = %d, want 4", msg.Handoff.Epoch)
		}
		if msg.Handoff.CommitTick != 505 {
			t.Fatalf("CommitTick = %d, want 505", msg.Handoff.CommitTick)
		}
		if string(msg.Handoff.TransferBlob) != "blob" {
			t.Fatalf("TransferBlob = %q, want \"blob\"", msg.Handoff.TransferBlob)
		}
		if msg.Handoff.ConnID != 42 {
			t.Fatalf("ConnID = %d, want 42", msg.Handoff.ConnID)
		}
	default:
		t.Fatal("no message in destination inbox")
	}
}

func TestBridge_RelayChatToOtherCells(t *testing.T) {
	grid := Config{CellsX: 3, CellsY: 1}
	c, _ := newTestCoordinator(grid)

	senderID := string(CellID{X: 1, Y: 0}.MeshID())
	sender := c.Cells[MeshCellID(senderID)]

	sender.Bridge.RelayChatToOtherCells("alice", "hello world")

	// All nodes except sender should get the chat
	for id, node := range c.Cells {
		if string(id) == senderID {
			// Sender's inbox should be empty
			select {
			case msg := <-node.Inbox:
				t.Fatalf("sender should not receive own chat, got: %+v", msg)
			default:
			}
			continue
		}

		select {
		case msg := <-node.Inbox:
			if msg.Type != MsgChat {
				t.Fatalf("expected MsgChat on %s, got %d", id, msg.Type)
			}
			if msg.Chat.Username != "alice" || msg.Chat.Text != "hello world" {
				t.Fatalf("unexpected chat on %s: %+v", id, msg.Chat)
			}
		default:
			t.Fatalf("node %s did not receive chat", id)
		}
	}
}

func TestBridge_RequestRespawn(t *testing.T) {
	grid := Config{CellsX: 2, CellsY: 1}
	c, _ := newTestCoordinator(grid)

	targetID := string(CellID{X: 0, Y: 0}.MeshID())
	// Point the default spawn into the target cell — RequestRespawn uses the
	// same resolution path as login (SpawnResolver → CellAtPosition).
	targetCell, err := ParseCellID(targetID)
	if err != nil {
		t.Fatalf("ParseCellID: %v", err)
	}
	minX, minY, maxX, maxY := targetCell.WorldBounds(coords.CellSize)
	c.cfg.DefaultSpawn = coords.Location{X: (minX + maxX) / 2, Y: (minY + maxY) / 2}

	otherID := string(CellID{X: 1, Y: 0}.MeshID())
	other := c.Cells[MeshCellID(otherID)]
	target := c.Cells[MeshCellID(targetID)]

	other.Bridge.RequestRespawn(77, "charlie")

	select {
	case msg := <-target.Inbox:
		if msg.Type != MsgSpawnTransfer {
			t.Fatalf("expected MsgSpawnTransfer, got %d", msg.Type)
		}
		if msg.Spawn.ConnID != 77 || msg.Spawn.Username != "charlie" {
			t.Fatalf("unexpected spawn: %+v", msg.Spawn)
		}
		if msg.Spawn.SpawnLocation != c.cfg.DefaultSpawn {
			t.Fatalf("SpawnLocation = %+v, want %+v (DefaultSpawn)",
				msg.Spawn.SpawnLocation, c.cfg.DefaultSpawn)
		}
	default:
		t.Fatal("no message in target node inbox")
	}
}

func TestBridge_SendAction(t *testing.T) {
	grid := Config{CellsX: 2, CellsY: 1}
	c, _ := newTestCoordinator(grid)

	srcID := string(CellID{X: 0, Y: 0}.MeshID())
	dstID := string(CellID{X: 1, Y: 0}.MeshID())
	src := c.Cells[MeshCellID(srcID)]
	dst := c.Cells[MeshCellID(dstID)]

	action := &CrossCellAction{
		Type:         1,
		TargetNetID:  42,
		SourceNetID:  10,
		SourceCellID: srcID,
		Payload:      []byte("dmg"),
	}
	src.Bridge.SendAction(dstID, action)

	select {
	case msg := <-dst.Inbox:
		if msg.Type != MsgCrossCellAction {
			t.Fatalf("expected MsgCrossCellAction, got %d", msg.Type)
		}
		if msg.Action.TargetNetID != 42 || msg.Action.SourceNetID != 10 {
			t.Fatalf("unexpected action: %+v", msg.Action)
		}
		if msg.FromCellID != srcID {
			t.Fatalf("expected FromCellID %s, got %s", srcID, msg.FromCellID)
		}
	default:
		t.Fatal("no message in destination inbox")
	}
}

func TestBridge_SendActionResult(t *testing.T) {
	grid := Config{CellsX: 2, CellsY: 1}
	c, _ := newTestCoordinator(grid)

	srcID := string(CellID{X: 0, Y: 0}.MeshID())
	dstID := string(CellID{X: 1, Y: 0}.MeshID())
	src := c.Cells[MeshCellID(srcID)]
	dst := c.Cells[MeshCellID(dstID)]

	result := &ActionResult{
		Type:        1,
		TargetNetID: 42,
		SourceNetID: 10,
		Success:     true,
		Payload:     []byte("result"),
	}
	src.Bridge.SendActionResult(dstID, result)

	select {
	case msg := <-dst.Inbox:
		if msg.Type != MsgActionResult {
			t.Fatalf("expected MsgActionResult, got %d", msg.Type)
		}
		if msg.ActionResult.TargetNetID != 42 || !msg.ActionResult.Success {
			t.Fatalf("unexpected result: %+v", msg.ActionResult)
		}
	default:
		t.Fatal("no message in destination inbox")
	}
}

func TestBridge_CellOwner(t *testing.T) {
	grid := Config{CellsX: 2, CellsY: 1}
	c, _ := newTestCoordinator(grid)

	nodeID := string(CellID{X: 0, Y: 0}.MeshID())
	node := c.Cells[MeshCellID(nodeID)]

	// Known cell
	owner := node.Bridge.CellOwner(CellID{X: 1, Y: 0})
	expected := string(CellID{X: 1, Y: 0}.MeshID())
	if owner != expected {
		t.Fatalf("expected owner %s, got %s", expected, owner)
	}

	// Unknown cell
	owner = node.Bridge.CellOwner(CellID{X: 99, Y: 99})
	if owner != "" {
		t.Fatalf("expected empty owner for unknown cell, got %s", owner)
	}
}

// ---------------------------------------------------------------------------
// Topology tests
// ---------------------------------------------------------------------------

func TestComputeTopology_3x3(t *testing.T) {
	var cells []CellID
	for sy := int32(0); sy <= 2; sy++ {
		for sx := int32(0); sx <= 2; sx++ {
			cells = append(cells, CellID{X: sx, Y: sy})
		}
	}

	topo := ComputeTopology(cells, coords.CellSize)

	center := CellID{X: 1, Y: 1}
	if len(topo.Neighbors[center]) != 8 {
		t.Fatalf("center expected 8 neighbors, got %d", len(topo.Neighbors[center]))
	}

	corner := CellID{X: 0, Y: 0}
	if len(topo.Neighbors[corner]) != 3 {
		t.Fatalf("corner expected 3 neighbors, got %d", len(topo.Neighbors[corner]))
	}

	edge := CellID{X: 1, Y: 0}
	if len(topo.Neighbors[edge]) != 5 {
		t.Fatalf("edge expected 5 neighbors, got %d", len(topo.Neighbors[edge]))
	}
}

func TestCellID_MeshID(t *testing.T) {
	tests := []struct {
		cell     CellID
		expected string
	}{
		{CellID{X: 0, Y: 0}, "cell_0_0"},
		{CellID{X: 1, Y: 2}, "cell_1_2"},
		{CellID{X: -1, Y: -1}, "cell_-1_-1"},
	}

	for _, tc := range tests {
		got := string(tc.cell.MeshID())
		if got != tc.expected {
			t.Fatalf("CellID(%v).MeshID() = %q, want %q", tc.cell, got, tc.expected)
		}
	}
}

// ---------------------------------------------------------------------------
// Recording bridge (for Cell tests that don't use a full coordinator)
// ---------------------------------------------------------------------------

type actionResultRecord struct {
	destCellID string
	result     *ActionResult
}

type recordingBridge struct {
	NoopBridge
	actionResults []actionResultRecord
}

func (rb *recordingBridge) SendActionResult(destCellID string, result *ActionResult) {
	rb.actionResults = append(rb.actionResults, actionResultRecord{
		destCellID: destCellID,
		result:     result,
	})
}
