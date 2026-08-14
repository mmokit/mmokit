package universe

import (
	"fmt"
	"testing"

	"github.com/zenion/mmokit/pkg/component"
	"github.com/zenion/mmokit/pkg/coords"
)

// moveTestBridge implements just enough of the Bridge surface to drive
// MoveEntityTo's CellOwnerAtPos lookup in unit tests. Maps any world
// coord to the depth-0 cell containing it (production bridges do the
// same plus split-aware sub-cell resolution, which is exercised by
// integration tests that wire the real cellBridge).
type moveTestBridge struct{ NoopBridge }

func (moveTestBridge) CellOwnerAtPos(worldX, worldY float32) string {
	cellSize := coords.CellSize
	cx := int32(worldX / cellSize)
	cy := int32(worldY / cellSize)
	if worldX < 0 && worldX != float32(cx)*cellSize {
		cx--
	}
	if worldY < 0 && worldY != float32(cy)*cellSize {
		cy--
	}
	return fmt.Sprintf("cell_%d_%d", cx, cy)
}

func newMoveTestStage(t *testing.T, cell CellID) *Stage {
	stage := newTestWorldBase(t, cell)
	stage.SetBridge(moveTestBridge{})
	return stage
}

func TestMoveEntityTo_SameCell_UpdatesPositionInline(t *testing.T) {
	stage := newMoveTestStage(t, CellID{X: 0, Y: 0})
	ent := stage.Spawn(
		component.Position{X: 10, Y: 10},
		component.Velocity{X: 5, Y: 5},
		component.EntityKind{Type: 1},
		component.Collider{Radius: 5},
	).Handle()

	if err := stage.MoveEntityTo(ent, 50, 60); err != nil {
		t.Fatalf("MoveEntityTo: %v", err)
	}
	pos := stage.PositionMap().Get(ent)
	if pos.X != 50 || pos.Y != 60 {
		t.Fatalf("Position = (%g,%g), want (50,60)", pos.X, pos.Y)
	}
	vel := stage.VelocityMap().Get(ent)
	if vel.X != 0 || vel.Y != 0 {
		t.Fatalf("Velocity should reset to zero, got (%g,%g)", vel.X, vel.Y)
	}
	if got := stage.DrainCrossingQueue(); len(got) != 0 {
		t.Fatalf("same-cell move should not enqueue a crossing, got %d", len(got))
	}
}

func TestMoveEntityTo_CrossCell_EnqueuesCrossingWithBypass(t *testing.T) {
	stage := newMoveTestStage(t, CellID{X: 0, Y: 0})
	ent := stage.Spawn(
		component.Position{X: 10, Y: 10},
		component.EntityKind{Type: 1},
		component.Collider{Radius: 5},
	).Handle()
	netID := stage.NetworkIDMap().Get(ent).ID

	farX := coords.CellSize*5 + 10
	farY := coords.CellSize*5 + 10
	if err := stage.MoveEntityTo(ent, farX, farY, MoveBypassCooldown()); err != nil {
		t.Fatalf("MoveEntityTo: %v", err)
	}
	q := stage.DrainCrossingQueue()
	if len(q) != 1 {
		t.Fatalf("cross-cell move should enqueue one crossing, got %d", len(q))
	}
	if !q[0].BypassCooldown {
		t.Fatalf("crossing should carry BypassCooldown=true")
	}
	if q[0].DestCellID == "" {
		t.Fatalf("crossing missing DestCellID")
	}
	if q[0].NetID != netID {
		t.Fatalf("crossing NetID = %d, want %d", q[0].NetID, netID)
	}
	// Position must be pre-stamped to destination-frame coords (so the
	// destination receives the post-TP world position when it deserializes).
	pos := stage.PositionMap().Get(ent)
	cellSize := coords.CellSize
	wantX := farX - 5*cellSize // dest cellX = 5
	wantY := farY - 5*cellSize // dest cellY = 5
	if pos.X != wantX || pos.Y != wantY {
		t.Fatalf("Position not normalized to dest cell: got (%g,%g), want (%g,%g)", pos.X, pos.Y, wantX, wantY)
	}
}

func TestMoveEntityTo_DeadEntityReturnsError(t *testing.T) {
	stage := newMoveTestStage(t, CellID{X: 0, Y: 0})
	ent := stage.Spawn(component.Position{X: 0, Y: 0}, component.EntityKind{Type: 1}).Handle()
	stage.ECSWorld().RemoveEntity(ent)
	if err := stage.MoveEntityTo(ent, 100, 100); err == nil {
		t.Fatal("MoveEntityTo on dead entity should return error")
	}
}

func TestMoveEntityTo_MoveAsPlayer_PopulatesCrossing(t *testing.T) {
	stage := newMoveTestStage(t, CellID{X: 0, Y: 0})
	ent := stage.Spawn(
		component.Position{X: 10, Y: 10},
		component.EntityKind{Type: 1},
		component.Collider{Radius: 5},
	).Handle()
	farX := coords.CellSize*2 + 10
	if err := stage.MoveEntityTo(ent, farX, 10, MoveAsPlayer(42, "alice")); err != nil {
		t.Fatalf("MoveEntityTo: %v", err)
	}
	q := stage.DrainCrossingQueue()
	if len(q) != 1 {
		t.Fatalf("cross-cell move should enqueue one crossing, got %d", len(q))
	}
	if q[0].ConnID != 42 || q[0].Username != "alice" {
		t.Fatalf("crossing player info = (%d, %q), want (42, %q)", q[0].ConnID, q[0].Username, "alice")
	}
}
