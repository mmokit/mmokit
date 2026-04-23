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
	base := NewWorldBase(eng, cell, 0, nil)
	return &Cell{
		ID:        id,
		Cell:      cell,
		Engine:    eng,
		World:     mw,
		Base:      base,
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
	if cfg.LoginHandler == nil {
		cfg.LoginHandler = func(connID uint32, msgs [][]byte) (string, any, error) {
			return "", nil, ErrLoginPending
		}
	}
	c := New(cfg)
	c.SetWorld(func(base *WorldBase) GameWorld {
		mw := &mockWorld{spawnNetID: 100, spawnConnID: 42}
		worlds[base.Cell()] = mw
		return mw
	})
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

	blob, err := cell.Base.SerializeEntity(temp)
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

// TestCell_MsgHandoff_PromotesExistingReplicaAtCommitTick verifies the
// common-case path: a border-replica already exists for netID; the
// commit-tick drain flips it in place to Live via PromoteReplicaToLive
// rather than spawning a new entity from the TransferBlob.
func TestCell_MsgHandoff_PromotesExistingReplicaAtCommitTick(t *testing.T) {
	cell, _ := newTestCell("dest", CellID{X: 1, Y: 0})
	cell.Bridge = &recordingBridge{}
	world := cell.Engine.ECS

	// Stand up a border-replica for netID=77 via the normal upsert path.
	// ApplyBorderFrame would wire it; here we call upsertBorderReplica
	// directly because we just need the end state.
	cell.Base.upsertBorderReplica(77, 1, 1, 50, 60, 4, 1, 2, "source", 0, nil)

	// Precondition: replica exists.
	ent, presence, ok := cell.Base.LookupNetID(77)
	if !ok || presence != PresenceReplica {
		t.Fatalf("pre-handoff: netID=77 presence=%v ok=%v, want Replica", presence, ok)
	}

	// Build a TransferBlob (same shape as above, but we expect this NOT
	// to be used because the replica will be promoted in place).
	temp := world.NewEntity()
	ecs.NewMap1[component.Position](world).Add(temp, &component.Position{X: 200, Y: 300})
	ecs.NewMap1[component.Velocity](world).Add(temp, &component.Velocity{X: 1, Y: 2})
	ecs.NewMap1[component.NetworkID](world).Add(temp, &component.NetworkID{ID: 77})
	ecs.NewMap1[component.EntityKind](world).Add(temp, &component.EntityKind{Type: 1})
	ecs.NewMap1[component.Collider](world).Add(temp, &component.Collider{Radius: 4})
	ecs.NewMap1[component.Rotation](world).Add(temp, &component.Rotation{Angle: 0})
	ecs.NewMap1[component.CellCoord](world).Add(temp, &component.CellCoord{CellX: 1, CellY: 0})
	blob, err := cell.Base.SerializeEntity(temp)
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

	// Post-commit: the SAME ECS entity survives, now Live.
	got, presence, ok := cell.Base.LookupNetID(77)
	if !ok || presence != PresenceLive {
		t.Fatalf("post-commit: netID=77 presence=%v ok=%v, want Live", presence, ok)
	}
	if got != ent {
		t.Fatalf("post-commit: entity handle changed (%v != %v) — replica should have been promoted in place", got, ent)
	}

	// Epoch was bumped to the Handoff payload value.
	netMap := ecs.NewMap1[component.NetworkID](world)
	if nid := netMap.Get(ent); nid.Epoch != 5 {
		t.Errorf("post-commit: Epoch = %d, want 5", nid.Epoch)
	}

	// Replica component was removed.
	repMap := ecs.NewMap1[component.Replica](world)
	if repMap.HasAll(ent) {
		t.Error("post-commit: Replica component still present — should have been removed")
	}
	if _, ok := cell.Base.ReplicaNetIDs()[77]; ok {
		t.Error("post-commit: replicaNetIDs[77] still present — should have been deleted")
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
	// No panic = success; ticks go to real WorldBase (no-op with empty world)
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
			if seen[nodeID] {
				t.Fatalf("nodeID %s owns multiple cells", nodeID)
			}
			seen[nodeID] = true
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
	centerID := MeshCellID(CellID{X: 1, Y: 1})
	center := c.Cells[centerID]
	if len(center.Neighbors) != 8 {
		t.Fatalf("center node expected 8 neighbors, got %d", len(center.Neighbors))
	}

	// Corner node (0,0) should have 3 neighbors
	cornerID := MeshCellID(CellID{X: 0, Y: 0})
	corner := c.Cells[cornerID]
	if len(corner.Neighbors) != 3 {
		t.Fatalf("corner node expected 3 neighbors, got %d", len(corner.Neighbors))
	}

	// Edge node (1,0) should have 5 neighbors
	edgeID := MeshCellID(CellID{X: 1, Y: 0})
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
			t.Fatalf("node %s has nil Bridge", node.ID)
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
		engines[node.Engine] = id
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

	srcID := MeshCellID(CellID{X: 0, Y: 0})
	dstID := MeshCellID(CellID{X: 1, Y: 0})
	src := c.Cells[srcID]
	dst := c.Cells[dstID]

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

	senderID := MeshCellID(CellID{X: 1, Y: 0})
	sender := c.Cells[senderID]

	sender.Bridge.RelayChatToOtherCells("alice", "hello world")

	// All nodes except sender should get the chat
	for id, node := range c.Cells {
		if id == senderID {
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

	targetID := MeshCellID(CellID{X: 0, Y: 0})
	// Point the default spawn into the target cell — RequestRespawn uses the
	// same resolution path as login (SpawnResolver → CellAtPosition).
	targetCell, err := ParseCellID(targetID)
	if err != nil {
		t.Fatalf("ParseCellID: %v", err)
	}
	minX, minY, maxX, maxY := targetCell.WorldBounds(coords.CellSize)
	c.cfg.DefaultSpawn = coords.Location{X: (minX + maxX) / 2, Y: (minY + maxY) / 2}

	otherID := MeshCellID(CellID{X: 1, Y: 0})
	other := c.Cells[otherID]
	target := c.Cells[targetID]

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

	srcID := MeshCellID(CellID{X: 0, Y: 0})
	dstID := MeshCellID(CellID{X: 1, Y: 0})
	src := c.Cells[srcID]
	dst := c.Cells[dstID]

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

	srcID := MeshCellID(CellID{X: 0, Y: 0})
	dstID := MeshCellID(CellID{X: 1, Y: 0})
	src := c.Cells[srcID]
	dst := c.Cells[dstID]

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

	nodeID := MeshCellID(CellID{X: 0, Y: 0})
	node := c.Cells[nodeID]

	// Known cell
	owner := node.Bridge.CellOwner(CellID{X: 1, Y: 0})
	expected := MeshCellID(CellID{X: 1, Y: 0})
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

func TestMeshCellID(t *testing.T) {
	tests := []struct {
		cell     CellID
		expected string
	}{
		{CellID{X: 0, Y: 0}, "cell_0_0"},
		{CellID{X: 1, Y: 2}, "cell_1_2"},
		{CellID{X: -1, Y: -1}, "cell_-1_-1"},
	}

	for _, tc := range tests {
		got := MeshCellID(tc.cell)
		if got != tc.expected {
			t.Fatalf("MeshCellID(%v) = %q, want %q", tc.cell, got, tc.expected)
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
