package game

import (
	"testing"

	"github.com/mlange-42/ark/ecs"

	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/pkg/engine"
	"github.com/zenion/mmoserver/pkg/mmokit"
	"github.com/zenion/mmoserver/pkg/net"
	"github.com/zenion/mmoserver/pkg/persist/persisttest"
	pkguniverse "github.com/zenion/mmoserver/pkg/universe"
)

// mockTransport satisfies net.Transport for testing.
type mockTransport struct{}

func (m *mockTransport) SendReliable(data []byte)   {}
func (m *mockTransport) SendUnreliable(data []byte) {}
func (m *mockTransport) DrainInput() [][]byte       { return nil }
func (m *mockTransport) DrainOpInput() [][]byte     { return nil }
func (m *mockTransport) InjectInput(_ []byte)       {}
func (m *mockTransport) Close()                     {}

func newTestGameWorld() (*GameWorld, *net.ConnManager) {
	log := mmokit.NewLogger()
	connMgr := mmokit.NewConnManager()
	eng := engine.New(engine.Config{TickRate: 20}, connMgr, log)
	cfg := DefaultGameConfig()
	cfg.AsteroidCount = 0 // skip spawning asteroids in tests
	cfg.ShipShield = 200  // nonzero so the post-transfer ApplyEquipmentStats assertion is meaningful
	playerDB := NewPlayerRepo(persisttest.NewPlayerRepoMock(), nil)
	base := pkguniverse.NewStage(eng, pkguniverse.CellID{}, cfg.AoIRadius, nil)
	base.SetSpatialGrid(mmokit.NewHashGrid(1000))
	// Realize entity kinds against the stage before NewGameWorld spawns
	// initial cell content (asteroids/station) — those calls require the
	// kind defs to be populated for WithComponents() auto-fill.
	tmpCoord := pkguniverse.New(pkguniverse.Config{CellsX: 1, CellsY: 1, TickRate: 20})
	GameSetup(tmpCoord)
	tmpCoord.RealizeKindSpecs(base)
	gw := NewGameWorld(base, &cfg, playerDB, mmokit.CellCoord{}, false)
	return gw, connMgr
}

// addMockConn registers a mock transport and drains the connect event.
// cm must be the concrete *net.ConnManager backing gw.eng.ConnMgr.
func addMockConn(gw *GameWorld, cm *net.ConnManager) uint32 {
	connID := cm.AddTransport(&mockTransport{})
	<-cm.Events() // drain connect event
	return connID
}

// ---------------------------------------------------------------------------
// TestFinishTransferSpawn_Asteroid
// ---------------------------------------------------------------------------

func TestFinishTransferSpawn_Asteroid(t *testing.T) {
	gw, _ := newTestGameWorld()

	mapper := ecs.NewMap6[mmokit.Position, mmokit.Velocity, mmokit.Rotation, mmokit.Collider, mmokit.NetworkID, mmokit.EntityKind](gw.Stage.ECSWorld())
	entity := mapper.NewEntity(
		&mmokit.Position{X: 500, Y: -300},
		&mmokit.Velocity{X: 0, Y: 0},
		&mmokit.Rotation{Angle: 1.0},
		&mmokit.Collider{Radius: 2.0},
		&mmokit.NetworkID{ID: 100},
		&mmokit.EntityKind{Type: gamecomp.KindAsteroid},
	)
	gw.Stage.RegisterLiveNetID(100, entity)
	e := mmokit.EntityFromECS(gw.Stage, entity)
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

	mapper := ecs.NewMap6[mmokit.Position, mmokit.Velocity, mmokit.Rotation, mmokit.Collider, mmokit.NetworkID, mmokit.EntityKind](gw.Stage.ECSWorld())
	entity := mapper.NewEntity(
		&mmokit.Position{X: 10, Y: 20},
		&mmokit.Velocity{X: 3, Y: 4},
		&mmokit.Rotation{Angle: 1.5},
		&mmokit.Collider{Radius: 5},
		&mmokit.NetworkID{ID: 200},
		&mmokit.EntityKind{Type: gamecomp.KindShip},
	)
	gw.Stage.RegisterLiveNetID(200, entity)
	e := mmokit.EntityFromECS(gw.Stage, entity)
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
	gw.EnsureEntityKindComponents(entity)
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
	if !mmokit.Has[gamecomp.TargetLock](e) {
		t.Error("TargetLock should be present (default)")
	}
}

// ---------------------------------------------------------------------------
// TestFinishTransferSpawn_LootCrate
// ---------------------------------------------------------------------------

func TestFinishTransferSpawn_LootCrate(t *testing.T) {
	gw, _ := newTestGameWorld()

	mapper := ecs.NewMap6[mmokit.Position, mmokit.Velocity, mmokit.Rotation, mmokit.Collider, mmokit.NetworkID, mmokit.EntityKind](gw.Stage.ECSWorld())
	entity := mapper.NewEntity(
		&mmokit.Position{X: -50, Y: 75},
		&mmokit.Velocity{},
		&mmokit.Rotation{},
		&mmokit.Collider{Radius: 0.4},
		&mmokit.NetworkID{ID: 300},
		&mmokit.EntityKind{Type: gamecomp.KindLootCrate},
	)
	gw.Stage.RegisterLiveNetID(300, entity)
	e := mmokit.EntityFromECS(gw.Stage, entity)
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
	gw.EnsureEntityKindComponents(entity)
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
