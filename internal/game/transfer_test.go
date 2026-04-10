package game

import (
	"testing"

	"github.com/mlange-42/ark/ecs"

	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/pkg/engine"
	"github.com/zenion/mmoserver/pkg/mmokit"
	pkguniverse "github.com/zenion/mmoserver/pkg/universe"
)

// mockTransport satisfies net.Transport for testing.
type mockTransport struct{}

func (m *mockTransport) SendReliable(data []byte)   {}
func (m *mockTransport) SendUnreliable(data []byte) {}
func (m *mockTransport) DrainInput() [][]byte       { return nil }
func (m *mockTransport) DrainOpInput() [][]byte     { return nil }
func (m *mockTransport) Close()                     {}

func newTestGameWorld() *GameWorld {
	log := mmokit.NewLogger()
	connMgr := mmokit.NewConnManager()
	eng := engine.New(engine.Config{TickRate: 20}, connMgr, log)
	cfg := DefaultGameConfig()
	cfg.AsteroidCount = 0 // skip spawning asteroids in tests
	cfg.ShipShield = 200  // nonzero so the post-transfer ApplyEquipmentStats assertion is meaningful
	playerDB := NewPlayerRepo(nil)
	base := pkguniverse.NewWorldBase(eng, pkguniverse.CellID{}, cfg.AoIRadius, nil)
	base.SetSpatialGrid(mmokit.NewHashGrid(1000))
	gw := NewGameWorld(base, &cfg, playerDB, mmokit.CellCoord{}, false)
	return gw
}

// addMockConn registers a mock transport and drains the connect event.
func addMockConn(gw *GameWorld) uint32 {
	connID := gw.eng.ConnMgr.AddTransport(&mockTransport{})
	<-gw.eng.ConnMgr.Events() // drain connect event
	return connID
}

// ---------------------------------------------------------------------------
// TestFinishTransferSpawn_Asteroid
// ---------------------------------------------------------------------------

func TestFinishTransferSpawn_Asteroid(t *testing.T) {
	gw := newTestGameWorld()

	mapper := ecs.NewMap6[mmokit.Position, mmokit.Velocity, mmokit.Rotation, mmokit.Collider, mmokit.NetworkID, mmokit.EntityKind](gw.eng.ECS)
	entity := mapper.NewEntity(
		&mmokit.Position{X: 500, Y: -300},
		&mmokit.Velocity{X: 0, Y: 0},
		&mmokit.Rotation{Angle: 1.0},
		&mmokit.Collider{Radius: 2.0},
		&mmokit.NetworkID{ID: 100},
		&mmokit.EntityKind{Type: gamecomp.TypeAsteroid},
	)
	gw.C.CellCoord.Add(entity, &mmokit.CellCoord{CellX: 1, CellY: 2})
	gw.C.Minable.Add(entity, &gamecomp.Minable{ItemID: 2, Remaining: 75})

	frame := &mmokit.TransferFrame{
		NetworkID:  100,
		EntityType: gamecomp.TypeAsteroid,
		CellX:      1,
		CellY:      2,
	}

	gw.FinishTransferSpawn(entity, frame)

	if !gw.eng.ECS.Alive(entity) {
		t.Fatal("entity should be alive")
	}
	if !gw.C.Minable.HasAll(entity) {
		t.Fatal("Minable component should be present")
	}
	minable := gw.C.Minable.Get(entity)
	if minable.Remaining != 75 {
		t.Errorf("Minable.Remaining: got %f, want 75", minable.Remaining)
	}
}

// ---------------------------------------------------------------------------
// TestFinishTransferSpawn_Ship
// ---------------------------------------------------------------------------

func TestFinishTransferSpawn_Ship(t *testing.T) {
	gw := newTestGameWorld()

	connID := addMockConn(gw)
	gw.Players.RegisterTransferSession(connID, "testplayer")

	mapper := ecs.NewMap6[mmokit.Position, mmokit.Velocity, mmokit.Rotation, mmokit.Collider, mmokit.NetworkID, mmokit.EntityKind](gw.eng.ECS)
	entity := mapper.NewEntity(
		&mmokit.Position{X: 10, Y: 20},
		&mmokit.Velocity{X: 3, Y: 4},
		&mmokit.Rotation{Angle: 1.5},
		&mmokit.Collider{Radius: 5},
		&mmokit.NetworkID{ID: 200},
		&mmokit.EntityKind{Type: gamecomp.TypeShip},
	)
	gw.C.CellCoord.Add(entity, &mmokit.CellCoord{CellX: 0, CellY: 0})
	gw.C.PlayerConn.Add(entity, &mmokit.PlayerConn{ConnID: connID})

	// Simulate components added by the registry during transfer
	gw.C.Health.Add(entity, &gamecomp.Health{Current: 80, Max: 100})
	gw.C.Shield.Add(entity, &gamecomp.Shield{Current: 30, Max: 50, RegenRate: 2, RegenDelay: 1})
	gw.C.Inventory.Add(entity, &gamecomp.Inventory{
		Items:   map[uint32]int32{5: 20},
		MaxMass: 300,
	})

	frame := &mmokit.TransferFrame{
		NetworkID:  200,
		EntityType: gamecomp.TypeShip,
		ConnID:     connID,
		Username:   "testplayer",
	}

	// Real transfer path auto-adds kind components before FinishTransferSpawn.
	gw.EnsureEntityKindComponents(entity)
	gw.FinishTransferSpawn(entity, frame)

	if !gw.eng.ECS.Alive(entity) {
		t.Fatal("entity should be alive")
	}

	// Verify components
	health := gw.C.Health.Get(entity)
	if health.Current != 80 || health.Max != 100 {
		t.Errorf("Health: got %f/%f, want 80/100", health.Current, health.Max)
	}
	// ApplyEquipmentStats recalculates shield from equipment + base config.
	shield := gw.C.Shield.Get(entity)
	if shield.Max != gw.Config.ShipShield {
		t.Errorf("Shield.Max: got %f, want %f (base config)", shield.Max, gw.Config.ShipShield)
	}
	if !gw.C.Inventory.HasAll(entity) {
		t.Fatal("Inventory should be present")
	}
	inv := gw.C.Inventory.Get(entity)
	if inv.Items[5] != 20 {
		t.Errorf("Inventory item 5: got %d, want 20", inv.Items[5])
	}
	if inv.MaxMass != 300 {
		t.Errorf("Inventory.MaxMass: got %f, want 300", inv.MaxMass)
	}
	// Verify ship-specific defaults were applied
	if !gw.C.PlayerInput.HasAll(entity) {
		t.Error("PlayerInput should be present")
	}
	if !gw.C.MiningLaser.HasAll(entity) {
		t.Error("MiningLaser should be present")
	}
	if !gw.C.ShipControl.HasAll(entity) {
		t.Error("ShipControl should be present (default)")
	}
	if !gw.C.TargetLock.HasAll(entity) {
		t.Error("TargetLock should be present (default)")
	}
}

// ---------------------------------------------------------------------------
// TestFinishTransferSpawn_LootCrate
// ---------------------------------------------------------------------------

func TestFinishTransferSpawn_LootCrate(t *testing.T) {
	gw := newTestGameWorld()

	mapper := ecs.NewMap6[mmokit.Position, mmokit.Velocity, mmokit.Rotation, mmokit.Collider, mmokit.NetworkID, mmokit.EntityKind](gw.eng.ECS)
	entity := mapper.NewEntity(
		&mmokit.Position{X: -50, Y: 75},
		&mmokit.Velocity{},
		&mmokit.Rotation{},
		&mmokit.Collider{Radius: 0.4},
		&mmokit.NetworkID{ID: 300},
		&mmokit.EntityKind{Type: gamecomp.TypeLootCrate},
	)
	gw.C.CellCoord.Add(entity, &mmokit.CellCoord{CellX: 0, CellY: 1})

	// Simulate components added by registry
	gw.C.Inventory.Add(entity, &gamecomp.Inventory{
		Items:   map[uint32]int32{3: 15},
		MaxMass: 100,
	})
	gw.C.Lifetime.Add(entity, &mmokit.Lifetime{Remaining: 45})

	frame := &mmokit.TransferFrame{
		NetworkID:  300,
		EntityType: gamecomp.TypeLootCrate,
	}

	// Real transfer path auto-adds kind components before FinishTransferSpawn.
	gw.EnsureEntityKindComponents(entity)
	gw.FinishTransferSpawn(entity, frame)

	if !gw.eng.ECS.Alive(entity) {
		t.Fatal("entity should be alive")
	}
	kind := gw.C.EntityKind.Get(entity)
	if kind.Type != gamecomp.TypeLootCrate {
		t.Errorf("EntityKind: got %d, want %d", kind.Type, gamecomp.TypeLootCrate)
	}
	if !gw.C.Lifetime.HasAll(entity) {
		t.Fatal("Lifetime component should be present")
	}
	lt := gw.C.Lifetime.Get(entity)
	if lt.Remaining != 45 {
		t.Errorf("Lifetime.Remaining: got %f, want 45", lt.Remaining)
	}
	if !gw.C.LootCrate.HasAll(entity) {
		t.Error("LootCrate marker component should be present")
	}
	if !gw.C.Inventory.HasAll(entity) {
		t.Fatal("Inventory should be present")
	}
	inv := gw.C.Inventory.Get(entity)
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
