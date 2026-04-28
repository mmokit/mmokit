package component_test

import (
	"testing"

	"github.com/zenion/mmoserver/pkg/component"
	"github.com/zenion/mmoserver/pkg/coords"
)

// TestReplicaComponent_HasProducedAtMsField is a sentinel: it fails at
// compile time if ProducedAtMs is renamed or removed from the Replica
// struct. Real end-to-end exercises of the field land in Phase E2 (
// producer stamps outbound frames from ClusterClock.TickTime) and Phase
// F1 (consumer copies the stamp into Replica on inbound border frames).
func TestReplicaComponent_HasProducedAtMsField(t *testing.T) {
	r := &component.Replica{ProducedAtMs: 12345}
	if r.ProducedAtMs != 12345 {
		t.Fatalf("ProducedAtMs not set: got %d", r.ProducedAtMs)
	}
}

func TestMoveTarget_SetTarget(t *testing.T) {
	const cellSize float32 = 1000

	mt := &component.MoveTarget{Sequence: 42}
	mt.SetTargetWithCellSize(3500, -500, cellSize)

	if mt.CellX != 3 || mt.CellY != -1 {
		t.Errorf("CellX/CellY = %d,%d, want 3,-1", mt.CellX, mt.CellY)
	}
	if mt.LocalX != 500 {
		t.Errorf("LocalX = %v, want 500", mt.LocalX)
	}
	if mt.LocalY != 500 {
		t.Errorf("LocalY = %v, want 500", mt.LocalY)
	}
	if !mt.Active {
		t.Error("Active should be true after SetTarget")
	}
	if mt.Sequence != 42 {
		t.Errorf("Sequence should be preserved (got %d, want 42)", mt.Sequence)
	}
}

func TestMoveTarget_Cancel(t *testing.T) {
	mt := &component.MoveTarget{Active: true, LocalX: 100, LocalY: 200}
	mt.Cancel()
	if mt.Active {
		t.Error("Active should be false after Cancel")
	}
}

// TestMoveTarget_SetTarget_LiveCellSize is a regression guard for the
// bug where SetTarget snapshotted coords.CellSize at init time. Verifies
// that runtime coords.SetCellSize is reflected by SetTarget — the bug
// manifest was 4node-basic's CellSize=2000 (default 8192) producing a
// 4× cell-index error.
func TestMoveTarget_SetTarget_LiveCellSize(t *testing.T) {
	// Save and restore coords.CellSize across the test.
	saved := coords.CellSize
	t.Cleanup(func() { coords.SetCellSize(saved) })

	coords.SetCellSize(2000)
	mt := &component.MoveTarget{}
	mt.SetTarget(3500, -500)

	if mt.CellX != 1 {
		t.Errorf("CellX = %d, want 1 (CellSize=2000, X=3500 → cell 1)", mt.CellX)
	}
	if mt.CellY != -1 {
		t.Errorf("CellY = %d, want -1 (CellSize=2000, Y=-500 → cell -1)", mt.CellY)
	}
}
