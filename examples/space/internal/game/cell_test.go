package game

import (
	"testing"

	"github.com/mlange-42/ark/ecs"

	"github.com/mmokit/mmokit"
	gamecomp "github.com/mmokit/mmokit/examples/space/internal/component"
	comp "github.com/mmokit/mmokit/pkg/component"
	pkguniverse "github.com/mmokit/mmokit/pkg/universe"
)

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
	w := gw.stage.ECSWorld()
	mapper := ecs.NewMap6[comp.Position, comp.Velocity, comp.Rotation, comp.Collider, comp.NetworkID, comp.EntityKind](w)
	entity := mapper.NewEntity(
		&comp.Position{X: 100, Y: 100},
		&comp.Velocity{},
		&comp.Rotation{},
		&comp.Collider{Radius: 1},
		&comp.NetworkID{ID: 123},
		&comp.EntityKind{Type: gamecomp.KindShip},
	)
	gw.stage.RegisterLiveNetID(123, entity)
	mmokit.Set(mmokit.EntityFromECS(gw.stage, entity), comp.Ghost{})

	// DrainInbox calls TickGhosts after processing messages
	node.DrainInbox()

	// Entity should be marked for removal. Flush to confirm.
	gw.eng.FlushRemovals()

	if w.Alive(entity) {
		t.Fatal("expected ghost entity to be removed after TTL expiry")
	}
}

func TestTickTransferCooldowns_Expiry(t *testing.T) {
	node := newTestCell(pkguniverse.CellID{X: 0, Y: 0})
	gw := testGW(node)

	// Create an entity with TransferCooldown
	w := gw.stage.ECSWorld()
	mapper := ecs.NewMap6[comp.Position, comp.Velocity, comp.Rotation, comp.Collider, comp.NetworkID, comp.EntityKind](w)
	entity := mapper.NewEntity(
		&comp.Position{X: 100, Y: 100},
		&comp.Velocity{},
		&comp.Rotation{},
		&comp.Collider{Radius: 1},
		&comp.NetworkID{ID: 456},
		&comp.EntityKind{Type: gamecomp.KindShip},
	)
	gw.stage.RegisterLiveNetID(456, entity)
	e := mmokit.EntityFromECS(gw.stage, entity)
	mmokit.Set(e, comp.TransferCooldown{Remaining: 1})

	node.DrainInbox()

	if mmokit.Has[comp.TransferCooldown](e) {
		t.Fatal("expected TransferCooldown component to be removed after expiry")
	}

	if !e.Alive() {
		t.Fatal("expected entity to still be alive after cooldown removal")
	}
}
