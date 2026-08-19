package game

import (
	"testing"

	"github.com/mlange-42/ark/ecs"

	"github.com/mmokit/mmokit"
	gamecomp "github.com/mmokit/mmokit/examples/space/internal/component"
	gamepersisttest "github.com/mmokit/mmokit/examples/space/internal/persist/persisttest"
	"github.com/mmokit/mmokit/pkg/engine"
	"github.com/mmokit/mmokit/pkg/net"
	"github.com/mmokit/mmokit/pkg/persist/persisttest"
	pkguniverse "github.com/mmokit/mmokit/pkg/universe"
)

// mockTransport satisfies net.Transport for testing.
type mockTransport struct{}

func (m *mockTransport) SendReliable(data []byte) net.SendResult {
	return net.SendResult{Disposition: net.SendQueued, Delivery: net.DeliveryReliableOrdered}
}
func (m *mockTransport) SendUnreliable(data []byte) net.SendResult {
	return net.SendResult{Disposition: net.SendQueued, Delivery: net.DeliveryReliableOrdered}
}
func (m *mockTransport) DrainInput() [][]byte   { return nil }
func (m *mockTransport) DrainOpInput() [][]byte { return nil }
func (m *mockTransport) InjectInput(_ []byte)   {}
func (m *mockTransport) Close()                 {}

func newTestGameWorld() (*GameWorld, *net.ConnManager) {
	log := mmokit.NewLogger()
	connMgr := mmokit.NewConnManager()
	eng := engine.New(engine.Config{TickRate: 20}, connMgr, log)
	cfg := DefaultGameConfig()
	cfg.AsteroidCount = 0 // skip spawning asteroids in tests
	cfg.ShipShield = 200  // nonzero so the post-transfer ApplyEquipmentStats assertion is meaningful
	playerDB := NewPlayerRepo(nil, persisttest.NewPlayerRepoMock(), gamepersisttest.NewPlayerStateRepoMock(), nil)
	// Realize entity kinds against the stage before NewGameWorld spawns
	// initial cell content (asteroids/station) — those calls require the
	// kind defs to be populated so Stage.Spawn invariant checks pass. The
	// coordinator comes first so the stage carries ITS registry.
	tmpCoord := mmokit.New(mmokit.Config{CellsX: 1, CellsY: 1, TickRate: 20, Headless: true})
	base := pkguniverse.NewStage(eng, pkguniverse.CellID{}, cfg.AoIRadius, nil, tmpCoord.Wire())
	base.SetSpatialGrid(mmokit.NewHashGrid(1000))
	GameSetup(tmpCoord)
	tmpCoord.RealizeKindSpecs(base)
	gw := NewGameWorld(base, &cfg, playerDB, mmokit.CellCoord{}, false, nil, nil)
	// Register the GameWorld as cell-local state so production paths that
	// resolve via mmokit.State[GameWorld](stage) (e.g. system Init, verb
	// dispatch) work inside unit tests.
	base.SetStateByName("game.GameWorld", gw)
	return gw, connMgr
}

// addMockConn registers a mock transport and drains the connect event.
// cm must be the concrete *net.ConnManager backing gw.eng.ConnMgr.
func addMockConn(gw *GameWorld, cm *net.ConnManager) uint32 {
	connID := cm.AddTransport(&mockTransport{}, "")
	<-cm.Events() // drain connect event
	return connID
}

func TestPlayerTransfer_PreservesSourceContinuityReplicaWithoutTombstone(t *testing.T) {
	gw, cm := newTestGameWorld()
	connID := addMockConn(gw, cm)
	gw.Players.RegisterPlayer(connID, "player")
	sess := gw.Players.ByConnID(connID)
	sess.UserID = testUserID(sess.Username)
	if err := gw.Players.Transition(sess, mmokit.StateActive); err != nil {
		t.Fatalf("activate player session: %v", err)
	}

	entity := gw.stage.Spawn(
		mmokit.Position{X: 100, Y: 200},
		mmokit.Collider{Radius: 5},
	).Handle()
	netID := gw.stage.NetworkIDMap().Get(entity).ID
	sess.Entity = entity
	if err := gw.stage.DemoteLiveToReplica(netID, "cell_1_0"); err != nil {
		t.Fatalf("demote source entity: %v", err)
	}
	gw.Engine().BeginRemovalTick()

	if err := gw.Players.Transition(sess, mmokit.StateTransferring); err != nil {
		t.Fatalf("transition player session: %v", err)
	}
	gw.Engine().FlushRemovals()

	if sess.Entity != (mmokit.EntityHandle{}) {
		t.Fatalf("source session entity = %v, want detached zero handle", sess.Entity)
	}
	if !gw.stage.ECSWorld().Alive(entity) {
		t.Fatal("game transfer cleanup removed source continuity replica")
	}
	if !gw.Spatial.IsRegistered(entity) {
		t.Fatal("game transfer cleanup deregistered source continuity replica from spatial AoI")
	}
	if got, presence, ok := gw.stage.LookupNetID(netID); !ok || got != entity || presence != pkguniverse.PresenceReplica {
		t.Fatalf("source netID slot = (%v, %v, %v), want original entity PresenceReplica", got, presence, ok)
	}
	if got := gw.Engine().SampleRemovedNetIDs(); len(got) != 0 {
		t.Fatalf("game transfer cleanup published false tombstones: %v", got)
	}
	saved := gw.PlayerDB.GetByUserID(sess.UserID)
	if saved == nil {
		t.Fatal("active transfer did not run the persistence checkpoint")
	}
	if !saved.HasSave || saved.X != 100 || saved.Y != 200 {
		t.Fatalf("persisted transfer state = {HasSave:%v X:%v Y:%v}, want {true 100 200}", saved.HasSave, saved.X, saved.Y)
	}
	gw.PlayerDB.mu.RLock()
	dirty := gw.PlayerDB.dirty[sess.UserID]
	gw.PlayerDB.mu.RUnlock()
	if !dirty {
		t.Fatal("active transfer persistence checkpoint did not mark player state dirty")
	}
}

func TestDeadPlayerTransfer_PreservesSourceContinuityReplicaWithoutTombstone(t *testing.T) {
	gw, cm := newTestGameWorld()
	connID := addMockConn(gw, cm)
	gw.Players.RegisterPlayer(connID, "dead-player")
	sess := gw.Players.ByConnID(connID)
	gw.Players.AddTransition(mmokit.StateTransition{From: mmokit.StatePending, To: StateDead})
	if err := gw.Players.Transition(sess, StateDead); err != nil {
		t.Fatalf("enter dead state: %v", err)
	}

	entity := gw.stage.Spawn(
		mmokit.Position{X: 300, Y: 400},
		mmokit.Collider{Radius: 5},
	).Handle()
	netID := gw.stage.NetworkIDMap().Get(entity).ID
	sess.Entity = entity
	if err := gw.stage.DemoteLiveToReplica(netID, "cell_1_0"); err != nil {
		t.Fatalf("demote source entity: %v", err)
	}
	gw.Engine().BeginRemovalTick()

	if err := gw.Players.Transition(sess, mmokit.StateTransferring); err != nil {
		t.Fatalf("transition dead player session: %v", err)
	}
	gw.Engine().FlushRemovals()

	if sess.Entity != (mmokit.EntityHandle{}) {
		t.Fatalf("source session entity = %v, want detached zero handle", sess.Entity)
	}
	if !gw.stage.ECSWorld().Alive(entity) {
		t.Fatal("dead-player transfer cleanup removed source continuity replica")
	}
	if !gw.Spatial.IsRegistered(entity) {
		t.Fatal("dead-player transfer cleanup deregistered source continuity replica from spatial AoI")
	}
	if got, presence, ok := gw.stage.LookupNetID(netID); !ok || got != entity || presence != pkguniverse.PresenceReplica {
		t.Fatalf("source netID slot = (%v, %v, %v), want original entity PresenceReplica", got, presence, ok)
	}
	if got := gw.Engine().SampleRemovedNetIDs(); len(got) != 0 {
		t.Fatalf("dead-player transfer cleanup published false tombstones: %v", got)
	}
}

// ---------------------------------------------------------------------------
// TestFinishTransferSpawn_Asteroid
// ---------------------------------------------------------------------------

func TestFinishTransferSpawn_Asteroid(t *testing.T) {
	gw, _ := newTestGameWorld()

	mapper := ecs.NewMap6[mmokit.Position, mmokit.Velocity, mmokit.Rotation, mmokit.Collider, mmokit.NetworkID, mmokit.EntityKind](gw.stage.ECSWorld())
	rotA := mmokit.RotationFromYaw(1.0)
	entity := mapper.NewEntity(
		&mmokit.Position{X: 500, Y: -300},
		&mmokit.Velocity{X: 0, Y: 0},
		&rotA,
		&mmokit.Collider{Radius: 2.0},
		&mmokit.NetworkID{ID: 100},
		&mmokit.EntityKind{Type: gamecomp.KindAsteroid},
	)
	gw.stage.RegisterLiveNetID(100, entity)
	e := mmokit.EntityFromECS(gw.stage, entity)
	mmokit.Set(e, mmokit.CellCoord{CellX: 1, CellY: 2})
	mmokit.Set(e, gamecomp.Minable{ItemID: 2, Remaining: 75})

	frame := &mmokit.TransferFrame{
		NetworkID:  100,
		EntityType: gamecomp.KindAsteroid,
		CellX:      1,
		CellY:      2,
	}

	gw.FinishTransferSpawn(entity, frame)

	if !e.Alive() {
		t.Fatal("entity should be alive")
	}
	minable := mmokit.Get[gamecomp.Minable](e)
	if minable == nil {
		t.Fatal("Minable component should be present")
	}
	if minable.Remaining != 75 {
		t.Errorf("Minable.Remaining: got %f, want 75", minable.Remaining)
	}
}

// ---------------------------------------------------------------------------
// TestFinishTransferSpawn_Ship
// ---------------------------------------------------------------------------

func TestFinishTransferSpawn_Ship(t *testing.T) {
	gw, cm := newTestGameWorld()

	connID := addMockConn(gw, cm)
	gw.Players.RegisterTransferSession(connID, "testplayer")

	mapper := ecs.NewMap6[mmokit.Position, mmokit.Velocity, mmokit.Rotation, mmokit.Collider, mmokit.NetworkID, mmokit.EntityKind](gw.stage.ECSWorld())
	rotB := mmokit.RotationFromYaw(1.5)
	entity := mapper.NewEntity(
		&mmokit.Position{X: 10, Y: 20},
		&mmokit.Velocity{X: 3, Y: 4},
		&rotB,
		&mmokit.Collider{Radius: 5},
		&mmokit.NetworkID{ID: 200},
		&mmokit.EntityKind{Type: gamecomp.KindShip},
	)
	gw.stage.RegisterLiveNetID(200, entity)
	e := mmokit.EntityFromECS(gw.stage, entity)
	mmokit.Set(e, mmokit.CellCoord{CellX: 0, CellY: 0})
	mmokit.Set(e, mmokit.PlayerConn{ConnID: connID})

	// Simulate components added by the registry during transfer
	mmokit.Set(e, gamecomp.Health{Current: 80, Max: 100})
	mmokit.Set(e, gamecomp.Shield{Current: 30, Max: 50, RegenRate: 2, RegenDelay: 1})
	mmokit.Set(e, gamecomp.Inventory{
		Items:   map[uint32]int32{5: 20},
		MaxMass: 300,
	})

	frame := &mmokit.TransferFrame{
		NetworkID:  200,
		EntityType: gamecomp.KindShip,
		ConnID:     connID,
		Username:   "testplayer",
	}

	// Real transfer path auto-adds kind components before FinishTransferSpawn.
	gw.stage.EnsureEntityKindComponents(entity)
	gw.FinishTransferSpawn(entity, frame)

	if !e.Alive() {
		t.Fatal("entity should be alive")
	}

	// Verify components
	health := mmokit.Get[gamecomp.Health](e)
	if health.Current != 80 || health.Max != 100 {
		t.Errorf("Health: got %f/%f, want 80/100", health.Current, health.Max)
	}
	// ApplyEquipmentStats recalculates shield from equipment + base config.
	shield := mmokit.Get[gamecomp.Shield](e)
	if shield.Max != gw.Config.ShipShield {
		t.Errorf("Shield.Max: got %f, want %f (base config)", shield.Max, gw.Config.ShipShield)
	}
	inv := mmokit.Get[gamecomp.Inventory](e)
	if inv == nil {
		t.Fatal("Inventory should be present")
	}
	if inv.Items[5] != 20 {
		t.Errorf("Inventory item 5: got %d, want 20", inv.Items[5])
	}
	// ApplyEquipmentStats re-syncs Inventory.MaxMass from config (the transfer
	// source value 300 is overridden) so runtime `config set MaxCargo` takes
	// effect immediately after a cell crossing rather than lingering at the
	// value serialized by the origin node.
	if inv.MaxMass != gw.Config.MaxCargo {
		t.Errorf("Inventory.MaxMass: got %f, want %f (config)", inv.MaxMass, gw.Config.MaxCargo)
	}
	// Verify ship-specific defaults were applied
	if !mmokit.Has[gamecomp.PlayerInput](e) {
		t.Error("PlayerInput should be present")
	}
	if !mmokit.Has[gamecomp.MiningLaser](e) {
		t.Error("MiningLaser should be present")
	}
	if !mmokit.Has[gamecomp.ShipControl](e) {
		t.Error("ShipControl should be present (default)")
	}
}

// TestSpawnFromTransferCore_FiresOnTransferReceived guards against
// regressions where the stage's onTransferReceived hook is left unwired —
// the post-transfer player ship must have its Shield re-synced from
// equipment via FinishTransferSpawn → ApplyEquipmentStats. Without the
// hook firing, the docked-then-roamed player ends up with Shield.Max=0.
func TestSpawnFromTransferCore_FiresOnTransferReceived(t *testing.T) {
	gw, cm := newTestGameWorld()

	connID := addMockConn(gw, cm)
	gw.Players.RegisterTransferSession(connID, "transferred")

	frame := &mmokit.TransferFrame{
		NetworkID:  4242,
		EntityType: gamecomp.KindShip,
		ConnID:     connID,
		Username:   "transferred",
		PosX:       10, PosY: 20,
		CellX: 0, CellY: 0,
		Rotation: 0,
		Collider: mmokit.Collider{Radius: 5, Shape: mmokit.ShapeRect},
	}
	blob, err := mmokit.MarshalTransferFrame(frame)
	if err != nil {
		t.Fatalf("MarshalTransferFrame: %v", err)
	}

	handle, _, err := gw.stage.SpawnFromTransferCore(blob, pkguniverse.PresenceLive)
	if err != nil {
		t.Fatalf("SpawnFromTransferCore: %v", err)
	}
	e := mmokit.EntityFromECS(gw.stage, handle)

	sh := mmokit.Get[gamecomp.Shield](e)
	if sh == nil {
		t.Fatal("Shield missing after transfer — EnsureEntityKindComponents not run")
	}
	if sh.Max != gw.Config.ShipShield {
		t.Fatalf("Shield.Max: got %f, want %f — onTransferReceived hook not wired", sh.Max, gw.Config.ShipShield)
	}
}

// ---------------------------------------------------------------------------
// TestFinishTransferSpawn_LootCrate
// ---------------------------------------------------------------------------

func TestFinishTransferSpawn_LootCrate(t *testing.T) {
	gw, _ := newTestGameWorld()

	mapper := ecs.NewMap6[mmokit.Position, mmokit.Velocity, mmokit.Rotation, mmokit.Collider, mmokit.NetworkID, mmokit.EntityKind](gw.stage.ECSWorld())
	entity := mapper.NewEntity(
		&mmokit.Position{X: -50, Y: 75},
		&mmokit.Velocity{},
		&mmokit.Rotation{},
		&mmokit.Collider{Radius: 0.4},
		&mmokit.NetworkID{ID: 300},
		&mmokit.EntityKind{Type: gamecomp.KindLootCrate},
	)
	gw.stage.RegisterLiveNetID(300, entity)
	e := mmokit.EntityFromECS(gw.stage, entity)
	mmokit.Set(e, mmokit.CellCoord{CellX: 0, CellY: 1})

	// Simulate components added by registry
	mmokit.Set(e, gamecomp.Inventory{
		Items:   map[uint32]int32{3: 15},
		MaxMass: 100,
	})
	mmokit.Set(e, mmokit.Lifetime{Remaining: 45})

	frame := &mmokit.TransferFrame{
		NetworkID:  300,
		EntityType: gamecomp.KindLootCrate,
	}

	// Real transfer path auto-adds kind components before FinishTransferSpawn.
	gw.stage.EnsureEntityKindComponents(entity)
	gw.FinishTransferSpawn(entity, frame)

	if !e.Alive() {
		t.Fatal("entity should be alive")
	}
	kind := mmokit.Get[mmokit.EntityKind](e)
	if kind.Type != gamecomp.KindLootCrate {
		t.Errorf("EntityKind: got %d, want %d", kind.Type, gamecomp.KindLootCrate)
	}
	lt := mmokit.Get[mmokit.Lifetime](e)
	if lt == nil {
		t.Fatal("Lifetime component should be present")
	}
	if lt.Remaining != 45 {
		t.Errorf("Lifetime.Remaining: got %f, want 45", lt.Remaining)
	}
	if !mmokit.Has[gamecomp.LootCrate](e) {
		t.Error("LootCrate marker component should be present")
	}
	inv := mmokit.Get[gamecomp.Inventory](e)
	if inv == nil {
		t.Fatal("Inventory should be present")
	}
	if inv.Items[3] != 15 {
		t.Errorf("Inventory item 3: got %d, want 15", inv.Items[3])
	}
}

// ---------------------------------------------------------------------------
// TestMarshalUnmarshal_Inventory — Inventory uses custom marshal (map field).
// Other components use reflection, tested in pkg/universe/reflect_marshal_test.go.
// ---------------------------------------------------------------------------

func TestMarshalUnmarshal_Inventory(t *testing.T) {
	inv := &gamecomp.Inventory{Items: map[uint32]int32{1: 5, 2: 10, 100: -3}, MaxMass: 999}
	data := marshalInventory(inv)
	got := unmarshalInventory(data)
	if got.MaxMass != 999 {
		t.Errorf("MaxMass: got %f, want 999", got.MaxMass)
	}
	if len(got.Items) != 3 {
		t.Fatalf("Items len: got %d, want 3", len(got.Items))
	}
	for k, v := range inv.Items {
		if got.Items[k] != v {
			t.Errorf("Items[%d]: got %d, want %d", k, got.Items[k], v)
		}
	}
}

// ---------------------------------------------------------------------------
// TestMarshalInventory_Exported — verify the exported wrappers work.
// ---------------------------------------------------------------------------

func TestMarshalInventory_Exported(t *testing.T) {
	inv := &gamecomp.Inventory{Items: map[uint32]int32{7: 3}, MaxMass: 42}
	data := MarshalInventory(inv)
	var got gamecomp.Inventory
	UnmarshalInventoryInto(data, &got)
	if got.MaxMass != 42 {
		t.Errorf("MaxMass: got %f, want 42", got.MaxMass)
	}
	if got.Items[7] != 3 {
		t.Errorf("Items[7]: got %d, want 3", got.Items[7])
	}
}
