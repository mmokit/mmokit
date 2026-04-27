package universe

import (
	"testing"

	"github.com/mlange-42/ark/ecs"
	"github.com/zenion/mmoserver/pkg/component"
	"github.com/zenion/mmoserver/pkg/coords"
	"github.com/zenion/mmoserver/pkg/engine"
	"github.com/zenion/mmoserver/pkg/logger"
	"github.com/zenion/mmoserver/pkg/net"
)

func TestUpdateCellBounds_SubcellToParent_NoPositionShift(t *testing.T) {
	coords.SetCellSize(8192)
	log := logger.New()
	eng := engine.New(engine.DefaultConfig(), net.NewConnManager(), log)

	// Survivor is subcell {X:1, Y:0, Depth:1} — the RIGHT half of root {0,0}.
	// Entities have base-cell positions: e.g. (5000, 3000) which is valid
	// in the right subcell's LocalBounds [4096, 8192) x [0, 4096).
	subcell := CellID{X: 1, Y: 0, Depth: 1}
	base := NewWorldBase(eng, subcell, 3000, nil)

	// Spawn a test entity with a known position
	spawnMap := ecs.NewMap2[component.Position, component.CellCoord](eng.ECS)
	entity := spawnMap.NewEntity(&component.Position{X: 5000, Y: 3000}, &component.CellCoord{CellX: 0, CellY: 0})
	posMap := ecs.NewMap1[component.Position](eng.ECS)

	// Merge subcell into parent {X:0, Y:0, Depth:0}
	parent := CellID{X: 0, Y: 0, Depth: 0}
	base.UpdateCellBounds(parent, coords.CellSize)

	// Cell identity should be updated
	if base.Cell() != parent {
		t.Errorf("cell = %v, want %v", base.Cell(), parent)
	}
	if base.CellID() != MeshCellID(parent) {
		t.Errorf("nodeID = %s, want %s", base.CellID(), MeshCellID(parent))
	}

	// Position should NOT have changed — entities use base-cell coords
	pos := posMap.Get(entity)
	if pos.X != 5000 || pos.Y != 3000 {
		t.Errorf("position = (%.0f, %.0f), want (5000, 3000) — positions should not shift during same-root-cell merge", pos.X, pos.Y)
	}
}

func TestWithFacing_SetsRotation(t *testing.T) {
	var o spawnOpts
	WithFacing(1.5708).apply(&o)
	if !o.hasRot {
		t.Fatalf("WithFacing did not set hasRot")
	}
	if o.rotation != 1.5708 {
		t.Fatalf("WithFacing rotation = %v, want 1.5708", o.rotation)
	}
}

// test-only helper so we can apply a SpawnOption without running through
// a full WorldBase.SpawnEntity. Lives in the test file.
func (f SpawnOption) apply(o *spawnOpts) { f(o) }

func TestSpawnAtLocation_ConvertsWorldToLocal(t *testing.T) {
	// Fixture: cellSize=2000, rootCell=(1, 1). World origin of this cell is (2000, 2000).
	// World point (2500, 2900) should become local (500, 900).
	wb := newTestWorldBase(t, CellID{X: 1, Y: 1})
	prev := coords.CellSize
	coords.SetCellSize(2000)
	defer coords.SetCellSize(prev)

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
	wb := newTestWorldBase(t, CellID{X: 0, Y: 0}) // bounds [0,2000)×[0,2000)
	prev := coords.CellSize
	coords.SetCellSize(2000)
	defer coords.SetCellSize(prev)

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
	prev := coords.CellSize
	coords.SetCellSize(2000)
	defer coords.SetCellSize(prev)

	wb := newTestWorldBase(t, CellID{X: 0, Y: 0})
	wb.coord = &Process{invariantMode: InvariantPanic}

	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("expected panic on out-of-bounds SpawnAtLocation under InvariantPanic")
		}
	}()

	_ = wb.SpawnAtLocation(coords.Location{X: 99999, Y: 99999})
}

type kindAutoAttachMarker struct{ V float32 }

func TestSpawnEntity_WithEntityKind_AutoAttachesComponents(t *testing.T) {
	base := newTestWorldBase(t, CellID{X: 0, Y: 0})
	def := EntityKindDef{Kind: 42, Name: "Marker"}
	KindComponent(&def, ecs.NewMap1[kindAutoAttachMarker](base.ECSWorld()))
	base.RegisterEntityKind(def)

	e := base.SpawnEntity(component.Position{X: 0, Y: 0}, WithEntityKind(42))

	mp := ecs.NewMap1[kindAutoAttachMarker](base.ECSWorld())
	if !mp.HasAll(e) {
		t.Fatal("expected kindAutoAttachMarker to be auto-attached when WithEntityKind(42) is set")
	}
}
