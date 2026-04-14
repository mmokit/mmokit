package game

import (
	"testing"

	gamecomp "github.com/zenion/mmoserver/internal/component"
	comp "github.com/zenion/mmoserver/pkg/component"
	"github.com/zenion/mmoserver/pkg/engine"
	pkguniverse "github.com/zenion/mmoserver/pkg/universe"

	enginepb "github.com/zenion/mmoserver/gen/go/enginepb"
)

func TestProcessMessage_Chat(t *testing.T) {
	node := newTestCell(pkguniverse.CellID{X: 0, Y: 0})
	gw := testGW(node)

	node.Inbox <- pkguniverse.CellMessage{
		Type:       pkguniverse.MsgChat,
		FromCellID: "cell_1_0",
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
	node := newTestCell(pkguniverse.CellID{X: 0, Y: 0})
	gw := testGW(node)

	node.Inbox <- pkguniverse.CellMessage{
		Type:       pkguniverse.MsgSpawnTransfer,
		FromCellID: "cell_1_0",
		Spawn:      &pkguniverse.SpawnTransfer{ConnID: 7, Username: "bob"},
	}

	node.DrainInbox()

	sess := gw.eng.Players.ByUsername("bob")
	if sess == nil {
		t.Fatal("expected session for 'bob' after RegisterTransferSession")
	}
	if sess.ConnID != 7 {
		t.Fatalf("expected ConnID 7, got %d", sess.ConnID)
	}
}

func TestTickGhosts_Expiry(t *testing.T) {
	node := newTestCell(pkguniverse.CellID{X: 0, Y: 0})
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
	gw.C.Ghost.Add(entity, &comp.Ghost{})

	// DrainInbox calls TickGhosts after processing messages
	node.DrainInbox()

	// Entity should be marked for removal. Flush to confirm.
	gw.eng.FlushRemovals()

	if gw.eng.ECS.Alive(entity) {
		t.Fatal("expected ghost entity to be removed after TTL expiry")
	}
}

func TestTickTransferCooldowns_Expiry(t *testing.T) {
	node := newTestCell(pkguniverse.CellID{X: 0, Y: 0})
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

	if !gw.eng.ECS.Alive(entity) {
		t.Fatal("expected entity to still be alive after cooldown removal")
	}
}

