package universe

import (
	"testing"

	comp "github.com/zenion/mmoserver/pkg/component"
	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/pkg/coords"
	"github.com/zenion/mmoserver/pkg/engine"
	pkguniverse "github.com/zenion/mmoserver/pkg/universe"

	enginepb "github.com/zenion/mmoserver/gen/go/enginepb"
)

func TestProcessMessage_Chat(t *testing.T) {
	node := newTestNode(coords.SectorCoord{SX: 0, SY: 0})
	gw := testGW(node)

	node.Inbox <- pkguniverse.NodeMessage{
		Type:       pkguniverse.MsgChat,
		FromNodeID: "node_1_0",
		Chat:       &pkguniverse.ChatRelay{Username: "alice", Text: "hello world"},
	}

	node.DrainInbox()

	msgs := engine.Peek[*enginepb.ChatMsg](gw.Queue)
	if len(msgs) == 0 {
		t.Fatal("expected chat message in queue")
	}
	if msgs[0].Username != "alice" {
		t.Fatalf("expected username 'alice', got '%s'", msgs[0].Username)
	}
	if msgs[0].Text != "hello world" {
		t.Fatalf("expected text 'hello world', got '%s'", msgs[0].Text)
	}
}

func TestProcessMessage_RespawnTransfer(t *testing.T) {
	node := newTestNode(coords.SectorCoord{SX: 0, SY: 0})
	gw := testGW(node)

	node.Inbox <- pkguniverse.NodeMessage{
		Type:       pkguniverse.MsgSpawnTransfer,
		FromNodeID: "node_1_0",
		Spawn:      &pkguniverse.SpawnTransfer{ConnID: 7, Username: "bob"},
	}

	node.DrainInbox()

	if username, ok := gw.Players.Usernames[7]; !ok || username != "bob" {
		t.Fatalf("expected Usernames[7]='bob', got '%s' (ok=%v)", username, ok)
	}
	if login, ok := gw.Players.PendingLogins[7]; !ok || login != "bob" {
		t.Fatalf("expected PendingLogins[7]='bob', got '%s' (ok=%v)", login, ok)
	}
}

func TestTickGhosts_Expiry(t *testing.T) {
	node := newTestNode(coords.SectorCoord{SX: 0, SY: 0})
	gw := testGW(node)

	// Create an entity with Ghost component, TTL=1
	entity := gw.C.ReplicaMapper.NewEntity(
		&comp.Position{X: 100, Y: 100},
		&comp.Velocity{},
		&comp.Rotation{},
		&comp.Collider{Radius: 1},
		&comp.NetworkID{ID: 123},
		&comp.EntityKind{Type: gamecomp.TypeShip},
	)
	gw.C.Ghost.Add(entity, &comp.Ghost{TTL: 1, DestNodeID: "node_1_0"})

	// DrainInbox calls TickGhosts after processing messages
	node.DrainInbox()

	// Entity should be marked for removal. Flush to confirm.
	gw.FlushRemovals()

	if gw.ECS.Alive(entity) {
		t.Fatal("expected ghost entity to be removed after TTL expiry")
	}
}

func TestTickTransferCooldowns_Expiry(t *testing.T) {
	node := newTestNode(coords.SectorCoord{SX: 0, SY: 0})
	gw := testGW(node)

	// Create an entity with TransferCooldown
	entity := gw.C.ReplicaMapper.NewEntity(
		&comp.Position{X: 100, Y: 100},
		&comp.Velocity{},
		&comp.Rotation{},
		&comp.Collider{Radius: 1},
		&comp.NetworkID{ID: 456},
		&comp.EntityKind{Type: gamecomp.TypeShip},
	)
	gw.C.TransferCooldown.Add(entity, &comp.TransferCooldown{Remaining: 1})

	node.DrainInbox()

	if gw.C.TransferCooldown.HasAll(entity) {
		t.Fatal("expected TransferCooldown component to be removed after expiry")
	}

	if !gw.ECS.Alive(entity) {
		t.Fatal("expected entity to still be alive after cooldown removal")
	}
}

func TestProcessMessage_ArrivalConfirm(t *testing.T) {
	node := newTestNode(coords.SectorCoord{SX: 0, SY: 0})
	gw := testGW(node)

	// Create a ghost entity with known NetworkID
	entity := gw.C.ReplicaMapper.NewEntity(
		&comp.Position{X: 100, Y: 100},
		&comp.Velocity{},
		&comp.Rotation{},
		&comp.Collider{Radius: 1},
		&comp.NetworkID{ID: 789},
		&comp.EntityKind{Type: gamecomp.TypeShip},
	)
	gw.C.Ghost.Add(entity, &comp.Ghost{TTL: 10, DestNodeID: "node_1_0"})

	node.Inbox <- pkguniverse.NodeMessage{
		Type:       pkguniverse.MsgArrivalConfirm,
		FromNodeID: "node_1_0",
		ArrivalConfirm: &pkguniverse.ArrivalConfirmMsg{
			NetworkID: 789,
			ConnID:    5,
		},
	}

	node.DrainInbox()

	// Flush removals so entity is actually removed from ECS
	gw.FlushRemovals()

	if gw.ECS.Alive(entity) {
		t.Fatal("expected ghost entity to be removed after arrival confirmation")
	}
}
