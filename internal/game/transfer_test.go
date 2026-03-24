package game

import (
	"testing"

	"github.com/mlange-42/ark/ecs"

	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/pkg/engine"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// mockTransport satisfies net.Transport for testing.
type mockTransport struct{}

func (m *mockTransport) SendReliable(data []byte)  {}
func (m *mockTransport) SendUnreliable(data []byte) {}
func (m *mockTransport) DrainInput() [][]byte       { return nil }
func (m *mockTransport) DrainOpInput() [][]byte     { return nil }
func (m *mockTransport) Close()                     {}

func newTestGameWorld() *GameWorld {
	log := mmokit.NewLogger()
	connMgr := mmokit.NewConnManager()
	eng := engine.New(mmokit.EngineConfig{TickRate: 20}, connMgr, log)
	grid := mmokit.NewGrid(1000)
	cfg := DefaultGameConfig()
	cfg.AsteroidCount = 0 // skip spawning asteroids in tests
	playerDB := NewPlayerRepo(nil)
	gw := NewGameWorld(eng, cfg, playerDB, grid, mmokit.SectorCoord{})
	return gw
}

// addMockConn registers a mock transport and drains the connect event.
func addMockConn(gw *GameWorld) uint32 {
	connID := gw.ConnMgr.AddTransport(&mockTransport{})
	<-gw.ConnMgr.Events() // drain connect event
	return connID
}

// ---------------------------------------------------------------------------
// TestValOr
// ---------------------------------------------------------------------------

func TestValOr_NilReturnsDefault(t *testing.T) {
	result := valOr[int](nil, 42)
	if result == nil {
		t.Fatal("expected non-nil pointer")
	}
	if *result != 42 {
		t.Fatalf("expected 42, got %d", *result)
	}
}

func TestValOr_NonNilReturnsOriginal(t *testing.T) {
	v := 99
	result := valOr(&v, 42)
	if result != &v {
		t.Fatal("expected original pointer to be returned")
	}
	if *result != 99 {
		t.Fatalf("expected 99, got %d", *result)
	}
}

// ---------------------------------------------------------------------------
// TestSerializeEntity_Ship
// ---------------------------------------------------------------------------

func TestSerializeEntity_Ship(t *testing.T) {
	gw := newTestGameWorld()

	// Create a minimal ship entity via the component mappers.
	mapper := ecs.NewMap6[mmokit.Position, mmokit.Velocity, mmokit.Rotation, mmokit.Collider, mmokit.NetworkID, mmokit.EntityKind](gw.ECS)
	entity := mapper.NewEntity(
		&mmokit.Position{X: 100, Y: 200},
		&mmokit.Velocity{X: 1, Y: 2},
		&mmokit.Rotation{Angle: 0.5},
		&mmokit.Collider{Radius: 10},
		&mmokit.NetworkID{ID: 42},
		&mmokit.EntityKind{Type: gamecomp.TypeShip},
	)
	gw.C.Health.Add(entity, &mmokit.Health{Current: 50, Max: 100})
	gw.C.Inventory.Add(entity, &gamecomp.Inventory{
		Items:   map[uint32]int32{1: 5, 2: 10},
		MaxMass: 500,
	})
	gw.C.SectorCoord.Add(entity, &mmokit.SectorCoord{SX: 3, SY: -1})

	p := gw.SerializeEntity(entity)

	if p.NetworkID != 42 {
		t.Errorf("NetworkID: got %d, want 42", p.NetworkID)
	}
	if p.EntityType != gamecomp.TypeShip {
		t.Errorf("EntityType: got %d, want %d", p.EntityType, gamecomp.TypeShip)
	}
	if p.Position.X != 100 || p.Position.Y != 200 {
		t.Errorf("Position: got (%.0f,%.0f), want (100,200)", p.Position.X, p.Position.Y)
	}
	if p.Velocity.X != 1 || p.Velocity.Y != 2 {
		t.Errorf("Velocity: got (%.0f,%.0f), want (1,2)", p.Velocity.X, p.Velocity.Y)
	}
	if p.Rotation.Angle != 0.5 {
		t.Errorf("Rotation: got %f, want 0.5", p.Rotation.Angle)
	}
	if p.Health == nil {
		t.Fatal("Health should not be nil")
	}
	if p.Health.Current != 50 || p.Health.Max != 100 {
		t.Errorf("Health: got %f/%f, want 50/100", p.Health.Current, p.Health.Max)
	}
	if p.Sector.SX != 3 || p.Sector.SY != -1 {
		t.Errorf("Sector: got (%d,%d), want (3,-1)", p.Sector.SX, p.Sector.SY)
	}
	if len(p.CargoItems) != 2 {
		t.Fatalf("CargoItems len: got %d, want 2", len(p.CargoItems))
	}
	if p.CargoItems[1] != 5 || p.CargoItems[2] != 10 {
		t.Errorf("CargoItems: got %v, want map[1:5 2:10]", p.CargoItems)
	}
	if p.MaxCargo != 500 {
		t.Errorf("MaxCargo: got %f, want 500", p.MaxCargo)
	}
}

// ---------------------------------------------------------------------------
// TestSerializeEntity_DeepCopiesCargo
// ---------------------------------------------------------------------------

func TestSerializeEntity_DeepCopiesCargo(t *testing.T) {
	gw := newTestGameWorld()

	mapper := ecs.NewMap6[mmokit.Position, mmokit.Velocity, mmokit.Rotation, mmokit.Collider, mmokit.NetworkID, mmokit.EntityKind](gw.ECS)
	entity := mapper.NewEntity(
		&mmokit.Position{}, &mmokit.Velocity{}, &mmokit.Rotation{},
		&mmokit.Collider{}, &mmokit.NetworkID{ID: 1},
		&mmokit.EntityKind{Type: gamecomp.TypeShip},
	)
	gw.C.Inventory.Add(entity, &gamecomp.Inventory{
		Items:   map[uint32]int32{10: 100},
		MaxMass: 999,
	})

	p := gw.SerializeEntity(entity)

	// Mutate the original inventory after serialization.
	inv := gw.C.Inventory.Get(entity)
	inv.Items[10] = 999
	inv.Items[20] = 50

	// The payload's cargo should be unaffected.
	if p.CargoItems[10] != 100 {
		t.Errorf("cargo item 10: got %d, want 100", p.CargoItems[10])
	}
	if _, exists := p.CargoItems[20]; exists {
		t.Error("cargo item 20 should not exist in payload after mutating original")
	}
}

// ---------------------------------------------------------------------------
// TestSerializeEntity_ClearsStatusEffectSources
// ---------------------------------------------------------------------------

func TestSerializeEntity_ClearsStatusEffectSources(t *testing.T) {
	gw := newTestGameWorld()

	mapper := ecs.NewMap6[mmokit.Position, mmokit.Velocity, mmokit.Rotation, mmokit.Collider, mmokit.NetworkID, mmokit.EntityKind](gw.ECS)
	entity := mapper.NewEntity(
		&mmokit.Position{}, &mmokit.Velocity{}, &mmokit.Rotation{},
		&mmokit.Collider{}, &mmokit.NetworkID{ID: 1},
		&mmokit.EntityKind{Type: gamecomp.TypeShip},
	)

	// Create a dummy source entity to use as the Source reference.
	dummyMapper := ecs.NewMap1[mmokit.Position](gw.ECS)
	dummyEntity := dummyMapper.NewEntity(&mmokit.Position{})

	se := &gamecomp.StatusEffects{}
	se.Add(gamecomp.StatusEffect{
		Type:     gamecomp.StatusIonBurn,
		Duration: 5.0,
		Value:    10.0,
		Source:   dummyEntity,
	})
	gw.C.StatusEffects.Add(entity, se)

	p := gw.SerializeEntity(entity)

	if p.StatusEffects == nil {
		t.Fatal("StatusEffects should not be nil")
	}
	if p.StatusEffects.Count != 1 {
		t.Fatalf("StatusEffects.Count: got %d, want 1", p.StatusEffects.Count)
	}
	if p.StatusEffects.Effects[0].Source != (ecs.Entity{}) {
		t.Error("StatusEffect source should be zeroed after serialization")
	}
	// Original entity's StatusEffects source should be unchanged.
	origSE := gw.C.StatusEffects.Get(entity)
	if origSE.Effects[0].Source != dummyEntity {
		t.Error("original StatusEffect source should not be modified")
	}
}

// ---------------------------------------------------------------------------
// TestSpawnFromTransfer_Asteroid
// ---------------------------------------------------------------------------

func TestSpawnFromTransfer_Asteroid(t *testing.T) {
	gw := newTestGameWorld()

	p := &TransferPayload{
		NetworkID:  100,
		EntityType: gamecomp.TypeAsteroid,
		Position:   mmokit.Position{X: 500, Y: -300},
		Velocity:   mmokit.Velocity{X: 0, Y: 0},
		Rotation:   mmokit.Rotation{Angle: 1.0},
		Collider:   mmokit.Collider{Radius: 2.0},
		Sector:     mmokit.SectorCoord{SX: 1, SY: 2},
		Minable:    &gamecomp.Minable{ResourceType: gamecomp.ResourceOre, Remaining: 75},
	}

	entity := gw.SpawnFromTransfer(p)
	if !gw.ECS.Alive(entity) {
		t.Fatal("spawned asteroid entity should be alive")
	}

	// Verify core components
	pos := gw.C.Position.Get(entity)
	if pos.X != 500 || pos.Y != -300 {
		t.Errorf("Position: got (%.0f,%.0f), want (500,-300)", pos.X, pos.Y)
	}
	netID := gw.C.NetworkID.Get(entity)
	if netID.ID != 100 {
		t.Errorf("NetworkID: got %d, want 100", netID.ID)
	}
	kind := gw.C.EntityKind.Get(entity)
	if kind.Type != gamecomp.TypeAsteroid {
		t.Errorf("EntityKind: got %d, want %d", kind.Type, gamecomp.TypeAsteroid)
	}
	if !gw.C.Minable.HasAll(entity) {
		t.Fatal("Minable component should be present")
	}
	minable := gw.C.Minable.Get(entity)
	if minable.Remaining != 75 {
		t.Errorf("Minable.Remaining: got %f, want 75", minable.Remaining)
	}
	if !gw.C.TransferCooldown.HasAll(entity) {
		t.Error("TransferCooldown should be present")
	}
	if !gw.C.SectorCoord.HasAll(entity) {
		t.Error("SectorCoord should be present")
	}
	sec := gw.C.SectorCoord.Get(entity)
	if sec.SX != 1 || sec.SY != 2 {
		t.Errorf("SectorCoord: got (%d,%d), want (1,2)", sec.SX, sec.SY)
	}
}

// ---------------------------------------------------------------------------
// TestSpawnFromTransfer_Ship
// ---------------------------------------------------------------------------

func TestSpawnFromTransfer_Ship(t *testing.T) {
	gw := newTestGameWorld()

	connID := addMockConn(gw)

	p := &TransferPayload{
		NetworkID:  200,
		EntityType: gamecomp.TypeShip,
		ConnID:     connID,
		Username:   "testplayer",
		Position:   mmokit.Position{X: 10, Y: 20},
		Velocity:   mmokit.Velocity{X: 3, Y: 4},
		Rotation:   mmokit.Rotation{Angle: 1.5},
		Collider:   mmokit.Collider{Radius: 5},
		Sector:     mmokit.SectorCoord{SX: 0, SY: 0},
		Health:     &mmokit.Health{Current: 80, Max: 100},
		Shield:     &mmokit.Shield{Current: 30, Max: 50, RegenRate: 2, RegenDelay: 1},
		CargoItems: map[uint32]int32{5: 20},
		MaxCargo:   300,
	}

	entity := gw.SpawnFromTransfer(p)
	if !gw.ECS.Alive(entity) {
		t.Fatal("spawned ship entity should be alive")
	}

	// Verify player connection wiring
	if got, ok := gw.Players.Entities[connID]; !ok || got != entity {
		t.Error("Players.Entities should map connID to the spawned entity")
	}
	if got := gw.Players.Usernames[connID]; got != "testplayer" {
		t.Errorf("Players.Usernames: got %q, want %q", got, "testplayer")
	}

	// Verify components
	health := gw.C.Health.Get(entity)
	if health.Current != 80 || health.Max != 100 {
		t.Errorf("Health: got %f/%f, want 80/100", health.Current, health.Max)
	}
	// ApplyEquipmentStats recalculates shield from equipment + base config.
	// With no shield equipment and ShipShield=0 in defaults, shield is clamped to 0.
	shield := gw.C.Shield.Get(entity)
	if shield.Max != gw.Config.ShipShield {
		t.Errorf("Shield.Max: got %f, want %f (base config)", shield.Max, gw.Config.ShipShield)
	}
	if !gw.C.PlayerConn.HasAll(entity) {
		t.Fatal("PlayerConn should be present")
	}
	pc := gw.C.PlayerConn.Get(entity)
	if pc.ConnID != connID {
		t.Errorf("PlayerConn.ConnID: got %d, want %d", pc.ConnID, connID)
	}
	if !gw.C.TransferCooldown.HasAll(entity) {
		t.Error("TransferCooldown should be present")
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
}

// ---------------------------------------------------------------------------
// TestSpawnFromTransfer_LootCrate
// ---------------------------------------------------------------------------

func TestSpawnFromTransfer_LootCrate(t *testing.T) {
	gw := newTestGameWorld()

	p := &TransferPayload{
		NetworkID:  300,
		EntityType: gamecomp.TypeLootCrate,
		Position:   mmokit.Position{X: -50, Y: 75},
		Velocity:   mmokit.Velocity{},
		Rotation:   mmokit.Rotation{},
		Collider:   mmokit.Collider{Radius: 0.4},
		Sector:     mmokit.SectorCoord{SX: 0, SY: 1},
		CargoItems: map[uint32]int32{3: 15},
		MaxCargo:   100,
		Lifetime:   &mmokit.Lifetime{Remaining: 45},
	}

	entity := gw.SpawnFromTransfer(p)
	if !gw.ECS.Alive(entity) {
		t.Fatal("spawned loot crate entity should be alive")
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
	if !gw.C.TransferCooldown.HasAll(entity) {
		t.Error("TransferCooldown should be present")
	}
}

// ---------------------------------------------------------------------------
// TestTransferRoundTrip
// ---------------------------------------------------------------------------

func TestTransferRoundTrip(t *testing.T) {
	// Source world: create an entity, serialize it.
	src := newTestGameWorld()

	mapper := ecs.NewMap6[mmokit.Position, mmokit.Velocity, mmokit.Rotation, mmokit.Collider, mmokit.NetworkID, mmokit.EntityKind](src.ECS)
	entity := mapper.NewEntity(
		&mmokit.Position{X: 77, Y: 88},
		&mmokit.Velocity{X: 5, Y: -3},
		&mmokit.Rotation{Angle: 2.1},
		&mmokit.Collider{Radius: 1.5},
		&mmokit.NetworkID{ID: 555},
		&mmokit.EntityKind{Type: gamecomp.TypeAsteroid},
	)
	src.C.SectorCoord.Add(entity, &mmokit.SectorCoord{SX: 2, SY: -2})
	src.C.Minable.Add(entity, &gamecomp.Minable{ResourceType: gamecomp.ResourceCrystal, Remaining: 42.5})

	payload := src.SerializeEntity(entity)

	// Destination world: spawn from the payload.
	dst := newTestGameWorld()
	newEntity := dst.SpawnFromTransfer(payload)

	if !dst.ECS.Alive(newEntity) {
		t.Fatal("round-trip entity should be alive")
	}

	pos := dst.C.Position.Get(newEntity)
	if pos.X != 77 || pos.Y != 88 {
		t.Errorf("Position: got (%.0f,%.0f), want (77,88)", pos.X, pos.Y)
	}
	vel := dst.C.Velocity.Get(newEntity)
	if vel.X != 5 || vel.Y != -3 {
		t.Errorf("Velocity: got (%.0f,%.0f), want (5,-3)", vel.X, vel.Y)
	}
	rot := dst.C.Rotation.Get(newEntity)
	if rot.Angle != 2.1 {
		t.Errorf("Rotation: got %f, want 2.1", rot.Angle)
	}
	netID := dst.C.NetworkID.Get(newEntity)
	if netID.ID != 555 {
		t.Errorf("NetworkID: got %d, want 555", netID.ID)
	}
	kind := dst.C.EntityKind.Get(newEntity)
	if kind.Type != gamecomp.TypeAsteroid {
		t.Errorf("EntityKind: got %d, want %d", kind.Type, gamecomp.TypeAsteroid)
	}
	sec := dst.C.SectorCoord.Get(newEntity)
	if sec.SX != 2 || sec.SY != -2 {
		t.Errorf("SectorCoord: got (%d,%d), want (2,-2)", sec.SX, sec.SY)
	}
	minable := dst.C.Minable.Get(newEntity)
	if minable.ResourceType != gamecomp.ResourceCrystal {
		t.Errorf("Minable.ResourceType: got %d, want %d", minable.ResourceType, gamecomp.ResourceCrystal)
	}
	if minable.Remaining != 42.5 {
		t.Errorf("Minable.Remaining: got %f, want 42.5", minable.Remaining)
	}
}
