package universe

import (
	"context"
	"testing"

	"github.com/mmokit/mmokit/pkg/engine"
	"github.com/mmokit/mmokit/pkg/logger"
)

// stopCellLoop runs and immediately exits a game loop on the cell's engine,
// which is what leaves the loop-job queue permanently closed. Run with an
// already-cancelled context returns synchronously after closing the gate, so
// no goroutine or waiting is involved.
func stopCellLoop(cell *Cell) {
	gl := engine.NewGameLoop(cell.Engine, nil, nil, engine.Hooks{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	gl.Run(ctx)
}

// TestInvariant_NoDuplicatePresence_SkipsStoppedCell covers the interaction
// that forces the ErrLoopStopped callers to land in the same commit as the
// gate. The invariant scans each cell on that cell's own loop. Once a loop
// stops — a merged-away donor, a migrated cell, a process shutting down —
// RunOnLoop answers ErrLoopStopped instantly and deterministically instead of
// blocking for a second. Treating that as "scan failed" makes it an integrity
// violation, and under InvariantPanic an ordinary shutdown race takes the
// process down.
func TestInvariant_NoDuplicatePresence_SkipsStoppedCell(t *testing.T) {
	cellID := CellID{X: 0, Y: 0}
	cell := newTestCell("cell_0_0", cellID)
	stopCellLoop(cell)

	c := &Process{
		Cells:         map[MeshCellID]*Cell{"cell_0_0": cell},
		CellOwner:     map[CellID]MeshCellID{cellID: "cell_0_0"},
		Log:           logger.New(),
		invariantMode: InvariantPanic,
	}

	if err := invNoDuplicatePresencePerCell.Check(c); err != nil {
		t.Fatalf("stopped cell reported an integrity violation: %v", err)
	}

	// The panicking path is the one that actually matters in 4node-basic,
	// which sets InvariantPanic.
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("CheckInvariants panicked on a stopped cell: %v", r)
		}
	}()
	c.CheckInvariants([]Invariant{invNoDuplicatePresencePerCell}, "post-merge")
}

// TestLiveEntityCensus_SkipsStoppedCell is the same shutdown race on the
// conservation path, which compares a census taken before a topology commit
// against one taken after. A cell retired by that very commit must not turn
// the after-census into an error.
func TestLiveEntityCensus_SkipsStoppedCell(t *testing.T) {
	cellID := CellID{X: 0, Y: 0}
	cell := newTestCell("cell_0_0", cellID)
	stopCellLoop(cell)

	c := &Process{
		Cells:     map[MeshCellID]*Cell{"cell_0_0": cell},
		CellOwner: map[CellID]MeshCellID{cellID: "cell_0_0"},
		Log:       logger.New(),
	}

	census, err := c.liveEntityCensus()
	if err != nil {
		t.Fatalf("census over a stopped cell failed: %v", err)
	}
	if len(census) != 0 {
		t.Fatalf("census = %v, want empty", census)
	}
}
