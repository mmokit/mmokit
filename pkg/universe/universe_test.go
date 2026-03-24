package universe

import (
	"testing"

	"github.com/mlange-42/ark/ecs"
	"github.com/zenion/mmoserver/pkg/coords"
	"github.com/zenion/mmoserver/pkg/engine"
	"github.com/zenion/mmoserver/pkg/logger"
	"github.com/zenion/mmoserver/pkg/net"
)

// ---------------------------------------------------------------------------
// Mock GameWorld
// ---------------------------------------------------------------------------

type mockWorld struct {
	spawned         [][]byte
	replicas        []replicaCall
	ghostsRemoved   []uint32
	replicasRemoved []uint32
	chats           []ChatRelay
	logins          []SpawnTransfer
	ghostTicked     int
	cooldownTicked  int
	bridge          NodeBridge
	shutdownCalled  bool

	// SerializeEntity returns these
	serializeResult []byte
	serializeErr    error

	// SpawnFromTransfer returns these
	spawnNetID  uint32
	spawnConnID uint32
	spawnErr    error

	// Cross-node action tracking
	actionsReceived []CrossNodeAction
	actionResults   []ActionResult
	// HandleCrossNodeAction returns this
	actionResultToReturn *ActionResult
}

type replicaCall struct {
	snapshots    [][]byte
	sourceNodeID string
}

func (m *mockWorld) SerializeEntity(ecs.Entity) ([]byte, error) {
	return m.serializeResult, m.serializeErr
}

func (m *mockWorld) SpawnFromTransfer(data []byte) (uint32, uint32, error) {
	m.spawned = append(m.spawned, data)
	return m.spawnNetID, m.spawnConnID, m.spawnErr
}

func (m *mockWorld) ScanBorderEntities(map[string]NeighborInfo) map[string][][]byte {
	return nil
}

func (m *mockWorld) ApplyReplicas(snapshots [][]byte, sourceNodeID string) {
	m.replicas = append(m.replicas, replicaCall{snapshots: snapshots, sourceNodeID: sourceNodeID})
}

func (m *mockWorld) ExpireReplicas() {}

func (m *mockWorld) RemoveReplicaByNetID(netID uint32) {
	m.replicasRemoved = append(m.replicasRemoved, netID)
}

func (m *mockWorld) MarkForRemoval(ecs.Entity) {}

func (m *mockWorld) ECSWorld() *ecs.World { return nil }

func (m *mockWorld) GetAoIRadius() float32 { return 3000 }

func (m *mockWorld) TickGhosts() { m.ghostTicked++ }

func (m *mockWorld) TickTransferCooldowns() { m.cooldownTicked++ }

func (m *mockWorld) RemoveGhostByNetID(netID uint32) {
	m.ghostsRemoved = append(m.ghostsRemoved, netID)
}

func (m *mockWorld) DispatchChat(username, text string) {
	m.chats = append(m.chats, ChatRelay{Username: username, Text: text})
}

func (m *mockWorld) RegisterPendingLogin(connID uint32, username string) {
	m.logins = append(m.logins, SpawnTransfer{ConnID: connID, Username: username})
}

func (m *mockWorld) SetBridge(bridge NodeBridge) {
	m.bridge = bridge
}

func (m *mockWorld) HandleCrossNodeAction(action *CrossNodeAction) *ActionResult {
	m.actionsReceived = append(m.actionsReceived, *action)
	return m.actionResultToReturn
}

func (m *mockWorld) HandleActionResult(result *ActionResult) {
	m.actionResults = append(m.actionResults, *result)
}

func (m *mockWorld) Shutdown() {
	m.shutdownCalled = true
}

func (m *mockWorld) Hooks() engine.Hooks {
	return engine.Hooks{}
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

func newTestNode(id string, sector coords.SectorCoord) (*Node, *mockWorld) {
	mw := &mockWorld{
		spawnNetID:  100,
		spawnConnID: 42,
	}
	return &Node{
		ID:        id,
		Sector:    sector,
		World:     mw,
		Inbox:     make(chan NodeMessage, 64),
		Neighbors: make(map[string]*Node),
		Log:       logger.New(),
	}, mw
}

func mockNodeFactory() (NodeFactory, map[coords.SectorCoord]*mockWorld) {
	worlds := make(map[coords.SectorCoord]*mockWorld)
	factory := func(base *WorldBase) (GameWorld, []engine.System) {
		mw := &mockWorld{spawnNetID: 100, spawnConnID: 42}
		worlds[base.Sector()] = mw
		return mw, nil
	}
	return factory, worlds
}

func newTestCoordinator(grid GridConfig) (*Coordinator, map[coords.SectorCoord]*mockWorld) {
	connMgr := net.NewConnManager()
	gameLog := logger.New()
	factory, worlds := mockNodeFactory()
	c := NewCoordinator(grid, engine.DefaultConfig(), factory,
		WithConnManager(connMgr), WithLogger(gameLog))
	return c, worlds
}

// ---------------------------------------------------------------------------
// Node tests
// ---------------------------------------------------------------------------

func TestNode_DrainInbox_Transfer(t *testing.T) {
	node, mw := newTestNode("dest", coords.SectorCoord{SX: 0, SY: 0})
	sourceNode, _ := newTestNode("source", coords.SectorCoord{SX: 1, SY: 0})
	node.Neighbors["source"] = sourceNode

	// Use a recording bridge
	rb := &recordingBridge{}
	node.Bridge = rb

	transferData := []byte("player-entity-data")
	node.Inbox <- NodeMessage{
		Type:          MsgTransfer,
		FromNodeID:    "source",
		TransferNetID: 55,
		Transfer:      transferData,
	}

	node.DrainInbox()

	// RemoveReplicaByNetID called with TransferNetID
	if len(mw.replicasRemoved) != 1 || mw.replicasRemoved[0] != 55 {
		t.Fatalf("expected RemoveReplicaByNetID(55), got %v", mw.replicasRemoved)
	}

	// SpawnFromTransfer called
	if len(mw.spawned) != 1 || string(mw.spawned[0]) != "player-entity-data" {
		t.Fatalf("expected SpawnFromTransfer with transfer data, got %v", mw.spawned)
	}

	// Bridge.SendArrivalConfirm called back to source
	if len(rb.arrivalConfirms) != 1 {
		t.Fatalf("expected 1 arrival confirm, got %d", len(rb.arrivalConfirms))
	}
	ac := rb.arrivalConfirms[0]
	if ac.destNodeID != "source" || ac.confirm.NetworkID != 100 || ac.confirm.ConnID != 42 {
		t.Fatalf("unexpected arrival confirm: %+v", ac)
	}
}

func TestNode_DrainInbox_ArrivalConfirm(t *testing.T) {
	node, mw := newTestNode("source", coords.SectorCoord{SX: 0, SY: 0})
	node.Bridge = &recordingBridge{}

	node.Inbox <- NodeMessage{
		Type:       MsgArrivalConfirm,
		FromNodeID: "dest",
		ArrivalConfirm: &ArrivalConfirmMsg{
			NetworkID: 77,
			ConnID:    10,
		},
	}

	node.DrainInbox()

	if len(mw.ghostsRemoved) != 1 || mw.ghostsRemoved[0] != 77 {
		t.Fatalf("expected RemoveGhostByNetID(77), got %v", mw.ghostsRemoved)
	}
}

func TestNode_DrainInbox_Replica(t *testing.T) {
	node, mw := newTestNode("dest", coords.SectorCoord{SX: 0, SY: 0})
	node.Bridge = &recordingBridge{}

	snaps := [][]byte{[]byte("snap1"), []byte("snap2")}
	node.Inbox <- NodeMessage{
		Type:       MsgReplica,
		FromNodeID: "source",
		Replicas:   snaps,
	}

	node.DrainInbox()

	if len(mw.replicas) != 1 {
		t.Fatalf("expected 1 ApplyReplicas call, got %d", len(mw.replicas))
	}
	if mw.replicas[0].sourceNodeID != "source" {
		t.Fatalf("expected sourceNodeID 'source', got '%s'", mw.replicas[0].sourceNodeID)
	}
	if len(mw.replicas[0].snapshots) != 2 {
		t.Fatalf("expected 2 snapshots, got %d", len(mw.replicas[0].snapshots))
	}
}

func TestNode_DrainInbox_Chat(t *testing.T) {
	node, mw := newTestNode("dest", coords.SectorCoord{SX: 0, SY: 0})
	node.Bridge = &recordingBridge{}

	node.Inbox <- NodeMessage{
		Type:       MsgChat,
		FromNodeID: "other",
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

func TestNode_DrainInbox_SpawnTransfer(t *testing.T) {
	node, mw := newTestNode("default", coords.SectorCoord{SX: 0, SY: 0})
	node.Bridge = &recordingBridge{}

	node.Inbox <- NodeMessage{
		Type:       MsgSpawnTransfer,
		FromNodeID: "other",
		Spawn:      &SpawnTransfer{ConnID: 99, Username: "bob"},
	}

	node.DrainInbox()

	if len(mw.logins) != 1 {
		t.Fatalf("expected 1 RegisterPendingLogin call, got %d", len(mw.logins))
	}
	if mw.logins[0].ConnID != 99 || mw.logins[0].Username != "bob" {
		t.Fatalf("unexpected login: %+v", mw.logins[0])
	}
}

func TestNode_DrainInbox_TicksAfterDrain(t *testing.T) {
	node, mw := newTestNode("n", coords.SectorCoord{SX: 0, SY: 0})
	node.Bridge = &recordingBridge{}

	// Empty inbox — DrainInbox should still call TickGhosts and TickTransferCooldowns
	node.DrainInbox()

	if mw.ghostTicked != 1 {
		t.Fatalf("expected TickGhosts called once, got %d", mw.ghostTicked)
	}
	if mw.cooldownTicked != 1 {
		t.Fatalf("expected TickTransferCooldowns called once, got %d", mw.cooldownTicked)
	}
}

func TestNode_DrainInbox_MultipleMessages(t *testing.T) {
	node, mw := newTestNode("n", coords.SectorCoord{SX: 0, SY: 0})
	node.Bridge = &recordingBridge{}

	node.Inbox <- NodeMessage{
		Type:       MsgChat,
		FromNodeID: "a",
		Chat:       &ChatRelay{Username: "u1", Text: "t1"},
	}
	node.Inbox <- NodeMessage{
		Type:       MsgChat,
		FromNodeID: "b",
		Chat:       &ChatRelay{Username: "u2", Text: "t2"},
	}
	node.Inbox <- NodeMessage{
		Type:       MsgArrivalConfirm,
		FromNodeID: "c",
		ArrivalConfirm: &ArrivalConfirmMsg{NetworkID: 88},
	}

	node.DrainInbox()

	if len(mw.chats) != 2 {
		t.Fatalf("expected 2 chats, got %d", len(mw.chats))
	}
	if len(mw.ghostsRemoved) != 1 || mw.ghostsRemoved[0] != 88 {
		t.Fatalf("expected ghost 88 removed, got %v", mw.ghostsRemoved)
	}
	// Ticks still called once at end
	if mw.ghostTicked != 1 || mw.cooldownTicked != 1 {
		t.Fatal("expected ticks called exactly once after draining all messages")
	}
}

func TestNode_DrainInbox_CrossNodeAction(t *testing.T) {
	node, mw := newTestNode("target", coords.SectorCoord{SX: 0, SY: 0})
	rb := &recordingBridge{}
	node.Bridge = rb

	mw.actionResultToReturn = &ActionResult{
		Type:        1,
		TargetNetID: 42,
		SourceNetID: 10,
		Success:     true,
		Payload:     []byte("damage-result"),
	}

	node.Inbox <- NodeMessage{
		Type:       MsgCrossNodeAction,
		FromNodeID: "source",
		Action: &CrossNodeAction{
			Type:         1,
			TargetNetID:  42,
			SourceNetID:  10,
			SourceNodeID: "source",
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
	if rec.destNodeID != "source" {
		t.Fatalf("expected result sent to 'source', got '%s'", rec.destNodeID)
	}
	if rec.result.TargetNetID != 42 || !rec.result.Success {
		t.Fatalf("unexpected result: %+v", rec.result)
	}
}

func TestNode_DrainInbox_CrossNodeAction_NilResult(t *testing.T) {
	node, mw := newTestNode("target", coords.SectorCoord{SX: 0, SY: 0})
	rb := &recordingBridge{}
	node.Bridge = rb

	mw.actionResultToReturn = nil // handler returns no result

	node.Inbox <- NodeMessage{
		Type:       MsgCrossNodeAction,
		FromNodeID: "source",
		Action: &CrossNodeAction{
			Type:         1,
			TargetNetID:  999,
			SourceNetID:  10,
			SourceNodeID: "source",
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

func TestNode_DrainInbox_ActionResult(t *testing.T) {
	node, mw := newTestNode("source", coords.SectorCoord{SX: 0, SY: 0})
	node.Bridge = &recordingBridge{}

	node.Inbox <- NodeMessage{
		Type:       MsgActionResult,
		FromNodeID: "target",
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
// Coordinator tests
// ---------------------------------------------------------------------------

func TestCoordinator_GridCreation(t *testing.T) {
	tests := []struct {
		name     string
		grid     GridConfig
		expected int
	}{
		{"1x1", GridConfig{0, 0, 0, 0}, 1},
		{"2x2", GridConfig{0, 1, 0, 1}, 4},
		{"3x3", GridConfig{-1, 1, -1, 1}, 9},
		{"2x3", GridConfig{0, 1, 0, 2}, 6},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := newTestCoordinator(tc.grid)
			if len(c.Nodes) != tc.expected {
				t.Fatalf("expected %d nodes, got %d", tc.expected, len(c.Nodes))
			}
		})
	}
}

func TestCoordinator_SectorOwnership(t *testing.T) {
	grid := GridConfig{MinSX: 0, MaxSX: 2, MinSY: 0, MaxSY: 2}
	c, _ := newTestCoordinator(grid)

	seen := make(map[string]bool)
	for sy := int32(0); sy <= 2; sy++ {
		for sx := int32(0); sx <= 2; sx++ {
			sector := coords.SectorCoord{SX: sx, SY: sy}
			nodeID, ok := c.SectorOwner[sector]
			if !ok {
				t.Fatalf("sector (%d,%d) has no owner", sx, sy)
			}
			if seen[nodeID] {
				t.Fatalf("nodeID %s owns multiple sectors", nodeID)
			}
			seen[nodeID] = true
		}
	}

	if len(seen) != 9 {
		t.Fatalf("expected 9 unique node owners, got %d", len(seen))
	}
}

func TestCoordinator_NeighborWiring(t *testing.T) {
	grid := GridConfig{MinSX: -1, MaxSX: 1, MinSY: -1, MaxSY: 1}
	c, _ := newTestCoordinator(grid)

	// Center node (0,0) should have 8 neighbors
	centerID := SectorID(coords.SectorCoord{SX: 0, SY: 0})
	center := c.Nodes[centerID]
	if len(center.Neighbors) != 8 {
		t.Fatalf("center node expected 8 neighbors, got %d", len(center.Neighbors))
	}

	// Corner node (-1,-1) should have 3 neighbors
	cornerID := SectorID(coords.SectorCoord{SX: -1, SY: -1})
	corner := c.Nodes[cornerID]
	if len(corner.Neighbors) != 3 {
		t.Fatalf("corner node expected 3 neighbors, got %d", len(corner.Neighbors))
	}

	// Edge node (0,-1) should have 5 neighbors
	edgeID := SectorID(coords.SectorCoord{SX: 0, SY: -1})
	edge := c.Nodes[edgeID]
	if len(edge.Neighbors) != 5 {
		t.Fatalf("edge node expected 5 neighbors, got %d", len(edge.Neighbors))
	}
}

func TestCoordinator_BridgeWired(t *testing.T) {
	grid := GridConfig{MinSX: 0, MaxSX: 1, MinSY: 0, MaxSY: 1}
	c, worlds := newTestCoordinator(grid)

	for _, node := range c.Nodes {
		if node.Bridge == nil {
			t.Fatalf("node %s has nil Bridge", node.ID)
		}
	}

	for sector, mw := range worlds {
		if mw.bridge == nil {
			t.Fatalf("world for sector (%d,%d) has nil bridge (SetBridge not called)", sector.SX, sector.SY)
		}
	}
}

func TestCoordinator_DefaultNode(t *testing.T) {
	grid := GridConfig{MinSX: -1, MaxSX: 1, MinSY: -1, MaxSY: 1}
	c, _ := newTestCoordinator(grid)

	def := c.DefaultNode()
	if def == nil {
		t.Fatal("DefaultNode returned nil")
	}
	expectedID := SectorID(coords.SectorCoord{SX: 0, SY: 0})
	if def.ID != expectedID {
		t.Fatalf("expected default node ID %s, got %s", expectedID, def.ID)
	}
}

func TestCoordinator_NetIDRanges(t *testing.T) {
	grid := GridConfig{MinSX: 0, MaxSX: 2, MinSY: 0, MaxSY: 0}
	c, _ := newTestCoordinator(grid)

	// Each node should have a unique engine (pointer).
	engines := make(map[*engine.Engine]string)
	for id, node := range c.Nodes {
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

func TestBridge_SendTransfer(t *testing.T) {
	grid := GridConfig{MinSX: 0, MaxSX: 1, MinSY: 0, MaxSY: 0}
	c, _ := newTestCoordinator(grid)

	srcID := SectorID(coords.SectorCoord{SX: 0, SY: 0})
	dstID := SectorID(coords.SectorCoord{SX: 1, SY: 0})
	src := c.Nodes[srcID]
	dst := c.Nodes[dstID]

	data := []byte("transfer-payload")
	src.Bridge.SendTransfer(dstID, data, 42)

	select {
	case msg := <-dst.Inbox:
		if msg.Type != MsgTransfer {
			t.Fatalf("expected MsgTransfer, got %d", msg.Type)
		}
		if msg.FromNodeID != srcID {
			t.Fatalf("expected FromNodeID %s, got %s", srcID, msg.FromNodeID)
		}
		if msg.TransferNetID != 42 {
			t.Fatalf("expected TransferNetID 42, got %d", msg.TransferNetID)
		}
		if string(msg.Transfer) != "transfer-payload" {
			t.Fatalf("unexpected transfer data: %s", msg.Transfer)
		}
	default:
		t.Fatal("no message in destination inbox")
	}
}

func TestBridge_SendArrivalConfirm(t *testing.T) {
	grid := GridConfig{MinSX: 0, MaxSX: 1, MinSY: 0, MaxSY: 0}
	c, _ := newTestCoordinator(grid)

	srcID := SectorID(coords.SectorCoord{SX: 0, SY: 0})
	dstID := SectorID(coords.SectorCoord{SX: 1, SY: 0})
	src := c.Nodes[srcID]
	dst := c.Nodes[dstID]

	confirm := &ArrivalConfirmMsg{NetworkID: 99, ConnID: 5}
	src.Bridge.SendArrivalConfirm(dstID, confirm)

	select {
	case msg := <-dst.Inbox:
		if msg.Type != MsgArrivalConfirm {
			t.Fatalf("expected MsgArrivalConfirm, got %d", msg.Type)
		}
		if msg.ArrivalConfirm.NetworkID != 99 || msg.ArrivalConfirm.ConnID != 5 {
			t.Fatalf("unexpected confirm: %+v", msg.ArrivalConfirm)
		}
	default:
		t.Fatal("no message in destination inbox")
	}
}

func TestBridge_RelayChatToOtherNodes(t *testing.T) {
	grid := GridConfig{MinSX: 0, MaxSX: 2, MinSY: 0, MaxSY: 0}
	c, _ := newTestCoordinator(grid)

	senderID := SectorID(coords.SectorCoord{SX: 1, SY: 0})
	sender := c.Nodes[senderID]

	sender.Bridge.RelayChatToOtherNodes("alice", "hello world")

	// All nodes except sender should get the chat
	for id, node := range c.Nodes {
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

func TestBridge_RequestSpawnOnNode(t *testing.T) {
	grid := GridConfig{MinSX: 0, MaxSX: 1, MinSY: 0, MaxSY: 0}
	c, _ := newTestCoordinator(grid)

	// Request spawn from non-default node
	otherID := SectorID(coords.SectorCoord{SX: 1, SY: 0})
	other := c.Nodes[otherID]
	defaultID := SectorID(coords.SectorCoord{SX: 0, SY: 0})
	defaultNode := c.Nodes[defaultID]

	other.Bridge.RequestSpawnOnNode(77, "charlie")

	select {
	case msg := <-defaultNode.Inbox:
		if msg.Type != MsgSpawnTransfer {
			t.Fatalf("expected MsgSpawnTransfer, got %d", msg.Type)
		}
		if msg.Spawn.ConnID != 77 || msg.Spawn.Username != "charlie" {
			t.Fatalf("unexpected spawn: %+v", msg.Spawn)
		}
	default:
		t.Fatal("no message in default node inbox")
	}
}

func TestBridge_SendAction(t *testing.T) {
	grid := GridConfig{MinSX: 0, MaxSX: 1, MinSY: 0, MaxSY: 0}
	c, _ := newTestCoordinator(grid)

	srcID := SectorID(coords.SectorCoord{SX: 0, SY: 0})
	dstID := SectorID(coords.SectorCoord{SX: 1, SY: 0})
	src := c.Nodes[srcID]
	dst := c.Nodes[dstID]

	action := &CrossNodeAction{
		Type:         1,
		TargetNetID:  42,
		SourceNetID:  10,
		SourceNodeID: srcID,
		Payload:      []byte("dmg"),
	}
	src.Bridge.SendAction(dstID, action)

	select {
	case msg := <-dst.Inbox:
		if msg.Type != MsgCrossNodeAction {
			t.Fatalf("expected MsgCrossNodeAction, got %d", msg.Type)
		}
		if msg.Action.TargetNetID != 42 || msg.Action.SourceNetID != 10 {
			t.Fatalf("unexpected action: %+v", msg.Action)
		}
		if msg.FromNodeID != srcID {
			t.Fatalf("expected FromNodeID %s, got %s", srcID, msg.FromNodeID)
		}
	default:
		t.Fatal("no message in destination inbox")
	}
}

func TestBridge_SendActionResult(t *testing.T) {
	grid := GridConfig{MinSX: 0, MaxSX: 1, MinSY: 0, MaxSY: 0}
	c, _ := newTestCoordinator(grid)

	srcID := SectorID(coords.SectorCoord{SX: 0, SY: 0})
	dstID := SectorID(coords.SectorCoord{SX: 1, SY: 0})
	src := c.Nodes[srcID]
	dst := c.Nodes[dstID]

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

func TestBridge_SectorOwner(t *testing.T) {
	grid := GridConfig{MinSX: 0, MaxSX: 1, MinSY: 0, MaxSY: 0}
	c, _ := newTestCoordinator(grid)

	nodeID := SectorID(coords.SectorCoord{SX: 0, SY: 0})
	node := c.Nodes[nodeID]

	// Known sector
	owner := node.Bridge.SectorOwner(coords.SectorCoord{SX: 1, SY: 0})
	expected := SectorID(coords.SectorCoord{SX: 1, SY: 0})
	if owner != expected {
		t.Fatalf("expected owner %s, got %s", expected, owner)
	}

	// Unknown sector
	owner = node.Bridge.SectorOwner(coords.SectorCoord{SX: 99, SY: 99})
	if owner != "" {
		t.Fatalf("expected empty owner for unknown sector, got %s", owner)
	}
}

// ---------------------------------------------------------------------------
// Topology tests
// ---------------------------------------------------------------------------

func TestComputeTopology_3x3(t *testing.T) {
	var sectors []coords.SectorCoord
	for sy := int32(-1); sy <= 1; sy++ {
		for sx := int32(-1); sx <= 1; sx++ {
			sectors = append(sectors, coords.SectorCoord{SX: sx, SY: sy})
		}
	}

	topo := ComputeTopology(sectors)

	center := coords.SectorCoord{SX: 0, SY: 0}
	if len(topo.Neighbors[center]) != 8 {
		t.Fatalf("center expected 8 neighbors, got %d", len(topo.Neighbors[center]))
	}

	corner := coords.SectorCoord{SX: -1, SY: -1}
	if len(topo.Neighbors[corner]) != 3 {
		t.Fatalf("corner expected 3 neighbors, got %d", len(topo.Neighbors[corner]))
	}

	edge := coords.SectorCoord{SX: 0, SY: -1}
	if len(topo.Neighbors[edge]) != 5 {
		t.Fatalf("edge expected 5 neighbors, got %d", len(topo.Neighbors[edge]))
	}
}

func TestSectorID(t *testing.T) {
	tests := []struct {
		sector   coords.SectorCoord
		expected string
	}{
		{coords.SectorCoord{SX: 0, SY: 0}, "node_0_0"},
		{coords.SectorCoord{SX: 1, SY: 2}, "node_1_2"},
		{coords.SectorCoord{SX: -1, SY: -1}, "node_-1_-1"},
	}

	for _, tc := range tests {
		got := SectorID(tc.sector)
		if got != tc.expected {
			t.Fatalf("SectorID(%v) = %q, want %q", tc.sector, got, tc.expected)
		}
	}
}

// ---------------------------------------------------------------------------
// Recording bridge (for Node tests that don't use a full coordinator)
// ---------------------------------------------------------------------------

type arrivalConfirmRecord struct {
	destNodeID string
	confirm    *ArrivalConfirmMsg
}

type actionResultRecord struct {
	destNodeID string
	result     *ActionResult
}

type recordingBridge struct {
	NoopNodeBridge
	arrivalConfirms []arrivalConfirmRecord
	actionResults   []actionResultRecord
}

func (rb *recordingBridge) SendArrivalConfirm(destNodeID string, confirm *ArrivalConfirmMsg) {
	rb.arrivalConfirms = append(rb.arrivalConfirms, arrivalConfirmRecord{
		destNodeID: destNodeID,
		confirm:    confirm,
	})
}

func (rb *recordingBridge) SendActionResult(destNodeID string, result *ActionResult) {
	rb.actionResults = append(rb.actionResults, actionResultRecord{
		destNodeID: destNodeID,
		result:     result,
	})
}
