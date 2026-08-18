package universe

import (
	"testing"

	"github.com/mlange-42/ark/ecs"
	"github.com/mmokit/mmokit/pkg/component"
	"github.com/mmokit/mmokit/pkg/coords"
	"github.com/mmokit/mmokit/pkg/engine"
	"github.com/mmokit/mmokit/pkg/logger"
	"github.com/mmokit/mmokit/pkg/net"
)

func TestUpdateCellBounds_SubcellToParent_NoPositionShift(t *testing.T) {
	log := logger.New()
	eng := engine.New(engine.DefaultConfig(), net.NewConnManager(), log)

	// Survivor is subcell {X:1, Y:0, Depth:1} — the RIGHT half of root {0,0}.
	// Entities have base-cell positions: e.g. (5000, 3000) which is valid
	// in the right subcell's LocalBounds [4096, 8192) x [0, 4096).
	subcell := CellID{X: 1, Y: 0, Depth: 1}
	base := NewStage(eng, subcell, 3000, nil, NewWireRegistry())

	// Spawn a test entity with a known position
	spawnMap := ecs.NewMap2[component.Position, component.CellCoord](eng.ECS)
	entity := spawnMap.NewEntity(&component.Position{X: 5000, Y: 3000}, &component.CellCoord{CellX: 0, CellY: 0})
	posMap := ecs.NewMap1[component.Position](eng.ECS)

	// Merge subcell into parent {X:0, Y:0, Depth:0}
	parent := CellID{X: 0, Y: 0, Depth: 0}
	base.UpdateCellBounds(parent, base.CellSize())

	// Cell identity should be updated
	if base.Cell() != parent {
		t.Errorf("cell = %v, want %v", base.Cell(), parent)
	}
	if base.CellID() != parent.MeshID() {
		t.Errorf("nodeID = %s, want %s", base.CellID(), parent.MeshID())
	}

	// Position should NOT have changed — entities use base-cell coords
	pos := posMap.Get(entity)
	if pos.X != 5000 || pos.Y != 3000 {
		t.Errorf("position = (%.0f, %.0f), want (5000, 3000) — positions should not shift during same-root-cell merge", pos.X, pos.Y)
	}
}

func TestSpawnAtLocation_ConvertsWorldToLocal(t *testing.T) {
	// Fixture: cellSize=2000, rootCell=(1, 1). World origin of this cell is (2000, 2000).
	// World point (2500, 2900) should become local (500, 900).
	wb := newTestWorldBase(t, CellID{X: 1, Y: 1}, 2000)

	loc := coords.Location{X: 2500, Y: 2900}
	entity := wb.SpawnAtLocation(loc)
	if entity == (ecs.Entity{}) {
		t.Fatalf("SpawnAtLocation returned zero entity")
	}
	posMap := ecs.NewMap1[component.Position](wb.ECSWorld())
	pos := posMap.Get(entity)
	if pos.X != 500 || pos.Y != 900 {
		t.Fatalf("Position=(%v,%v) want (500,900)", pos.X, pos.Y)
	}
	ccMap := ecs.NewMap1[component.CellCoord](wb.ECSWorld())
	cc := ccMap.Get(entity)
	if cc.CellX != 1 || cc.CellY != 1 {
		t.Fatalf("CellCoord=(%v,%v) want (1,1)", cc.CellX, cc.CellY)
	}
}

func TestSpawnAtLocation_OutOfBounds_InvariantLog_Clamps(t *testing.T) {
	// Size passed to the fixture, not set afterwards: a Stage captures its
	// geometry at construction, so a later SetCellSize would leave this test
	// asserting a [0,1024) clamp while claiming to assert [0,2000).
	wb := newTestWorldBase(t, CellID{X: 0, Y: 0}, 2000) // bounds [0,2000)×[0,2000)

	wb.coord = &Process{invariantMode: InvariantLog} // non-nil so invariant path runs, but no panic

	loc := coords.Location{X: 5000, Y: -100}
	entity := wb.SpawnAtLocation(loc)
	if entity == (ecs.Entity{}) {
		t.Fatalf("expected clamped spawn to succeed under InvariantLog")
	}
	posMap := ecs.NewMap1[component.Position](wb.ECSWorld())
	pos := posMap.Get(entity)
	if pos.X >= 2000 || pos.X < 0 {
		t.Fatalf("pos.X=%v not clamped into [0,2000)", pos.X)
	}
	if pos.Y >= 2000 || pos.Y < 0 {
		t.Fatalf("pos.Y=%v not clamped into [0,2000)", pos.Y)
	}
}

func TestSpawnAtLocation_OutOfBounds_InvariantPanic(t *testing.T) {

	wb := newTestWorldBase(t, CellID{X: 0, Y: 0})
	wb.coord = &Process{invariantMode: InvariantPanic}

	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("expected panic on out-of-bounds SpawnAtLocation under InvariantPanic")
		}
	}()

	_ = wb.SpawnAtLocation(coords.Location{X: 99999, Y: 99999})
}

func TestSpawnPlayer_AttachesPlayerConn(t *testing.T) {
	base := newTestWorldBase(t, CellID{X: 0, Y: 0})
	def := EntityKindDef{Kind: 7, Name: "Player"}
	base.RegisterEntityKind(def)

	session := &engine.PlayerSession{
		ConnID:        42,
		Username:      "alice",
		SpawnLocation: coords.Location{X: 100, Y: 200},
	}
	e := base.SpawnPlayer(session, component.EntityKind{Type: 7})

	if e.NetID() == 0 {
		t.Fatal("expected non-zero entity")
	}
	if session.Entity != e.Handle() {
		t.Errorf("expected session.Entity to be set to %v, got %v", e.Handle(), session.Entity)
	}
	pcMap := ecs.NewMap1[component.PlayerConn](base.ECSWorld())
	if !pcMap.HasAll(e.Handle()) {
		t.Fatal("expected PlayerConn component attached")
	}
	if pcMap.Get(e.Handle()).ConnID != 42 {
		t.Errorf("expected ConnID 42, got %d", pcMap.Get(e.Handle()).ConnID)
	}
}
