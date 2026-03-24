package universe

import (
	"testing"

	"github.com/mlange-42/ark/ecs"

	gamepb "github.com/zenion/mmoserver/gen/go"
	comp "github.com/zenion/mmoserver/pkg/component"
	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/internal/game"
	"github.com/zenion/mmoserver/pkg/coords"
	"github.com/zenion/mmoserver/pkg/engine"
)

func TestProcessMessage_Chat(t *testing.T) {
	node := newTestNode(coords.SectorCoord{SX: 0, SY: 0})

	node.Inbox <- NodeMessage{
		Type:       MsgChat,
		FromNodeID: "node_1_0",
		Chat:       &game.ChatRelay{Username: "alice", Text: "hello world"},
	}

	node.DrainInbox()

	msgs := engine.Peek[*gamepb.ChatMsg](node.World.Queue)
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

	node.Inbox <- NodeMessage{
		Type:       MsgRespawnTransfer,
		FromNodeID: "node_1_0",
		Respawn:    &game.RespawnTransfer{ConnID: 7, Username: "bob"},
	}

	node.DrainInbox()

	if username, ok := node.World.Players.Usernames[7]; !ok || username != "bob" {
		t.Fatalf("expected Usernames[7]='bob', got '%s' (ok=%v)", username, ok)
	}
	if login, ok := node.World.Players.PendingLogins[7]; !ok || login != "bob" {
		t.Fatalf("expected PendingLogins[7]='bob', got '%s' (ok=%v)", login, ok)
	}
}

func TestTickGhosts_Expiry(t *testing.T) {
	node := newTestNode(coords.SectorCoord{SX: 0, SY: 0})

	// Create an entity with Ghost component, TTL=1
	entity := node.World.C.ReplicaMapper.NewEntity(
		&comp.Position{X: 100, Y: 100},
		&comp.Velocity{},
		&comp.Rotation{},
		&comp.Collider{Radius: 1},
		&comp.NetworkID{ID: 123},
		&comp.EntityKind{Type: gamecomp.TypeShip},
	)
	node.World.C.Ghost.Add(entity, &comp.Ghost{TTL: 1, DestNodeID: "node_1_0"})

	// DrainInbox calls tickGhosts after processing messages
	node.DrainInbox()

	// Entity should be marked for removal. Flush to confirm.
	node.World.FlushRemovals(func(e ecs.Entity) (uint32, bool) {
		return 0, false
	})

	// After flush, the entity should no longer be alive
	if node.World.ECS.Alive(entity) {
		t.Fatal("expected ghost entity to be removed after TTL expiry")
	}
}

func TestTickTransferCooldowns_Expiry(t *testing.T) {
	node := newTestNode(coords.SectorCoord{SX: 0, SY: 0})

	// Create an entity with TransferCooldown
	entity := node.World.C.ReplicaMapper.NewEntity(
		&comp.Position{X: 100, Y: 100},
		&comp.Velocity{},
		&comp.Rotation{},
		&comp.Collider{Radius: 1},
		&comp.NetworkID{ID: 456},
		&comp.EntityKind{Type: gamecomp.TypeShip},
	)
	node.World.C.TransferCooldown.Add(entity, &comp.TransferCooldown{Remaining: 1})

	node.DrainInbox()

	// TransferCooldown component should be removed
	if node.World.C.TransferCooldown.HasAll(entity) {
		t.Fatal("expected TransferCooldown component to be removed after expiry")
	}

	// Entity itself should still be alive
	if !node.World.ECS.Alive(entity) {
		t.Fatal("expected entity to still be alive after cooldown removal")
	}
}

func TestProcessMessage_ArrivalConfirm(t *testing.T) {
	node := newTestNode(coords.SectorCoord{SX: 0, SY: 0})

	// Create a ghost entity with known NetworkID
	entity := node.World.C.ReplicaMapper.NewEntity(
		&comp.Position{X: 100, Y: 100},
		&comp.Velocity{},
		&comp.Rotation{},
		&comp.Collider{Radius: 1},
		&comp.NetworkID{ID: 789},
		&comp.EntityKind{Type: gamecomp.TypeShip},
	)
	node.World.C.Ghost.Add(entity, &comp.Ghost{TTL: 10, DestNodeID: "node_1_0"})

	// Send arrival confirm for that NetworkID
	node.Inbox <- NodeMessage{
		Type:       MsgArrivalConfirm,
		FromNodeID: "node_1_0",
		ArrivalConfirm: &game.ArrivalConfirmMsg{
			NetworkID: 789,
			ConnID:    5,
		},
	}

	node.DrainInbox()

	// Flush removals so entity is actually removed from ECS
	node.World.FlushRemovals(func(e ecs.Entity) (uint32, bool) {
		return 0, false
	})

	if node.World.ECS.Alive(entity) {
		t.Fatal("expected ghost entity to be removed after arrival confirmation")
	}
}
