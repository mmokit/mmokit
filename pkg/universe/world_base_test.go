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
	if base.NodeID() != MeshCellID(parent) {
		t.Errorf("nodeID = %s, want %s", base.NodeID(), MeshCellID(parent))
	}

	// Position should NOT have changed — entities use base-cell coords
	pos := posMap.Get(entity)
	if pos.X != 5000 || pos.Y != 3000 {
		t.Errorf("position = (%.0f, %.0f), want (5000, 3000) — positions should not shift during same-root-cell merge", pos.X, pos.Y)
	}
}
