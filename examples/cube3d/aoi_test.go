package main

import (
	"context"
	"testing"
	"time"

	"github.com/mmokit/mmokit"
	"github.com/mmokit/mmokit/pkg/component"
	pkguniverse "github.com/mmokit/mmokit/pkg/universe"
)

// TestCube3D_AreaOfInterestIsASphere is roadmap §7.5 phase 4a's acceptance
// criterion, made executable against the real process.
//
// Nothing else in CI observes this phase. `just schema-check` cannot — area of
// interest is not schema-visible and no wire byte moved. Neither can any of
// the three SDKs or the cross-language delta golden, for the same reason. Nor
// can split_test.go, because a cell transfer travels through TransferFrame
// rather than through replication.
//
// The unit tests in pkg/spatial and pkg/system pin the arithmetic. This pins
// that a REAL cube3d process wires it together: SpatialSystem writing Entry.Z,
// PlayerViewerSource writing ViewerInfo.Z, and the cutoff reading both.
func TestCube3D_AreaOfInterestIsASphere(t *testing.T) {
	process, stop := runProcess(t)
	defer stop()

	cell := firstCell(t, process)

	// Query the cell's own grid the way ReplicationSystem does, from a viewer
	// far above the field. Everything in the world is below it, so a
	// cylindrical query returns cubes and a spherical one returns none.
	const high = 10000
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var fromAbove, fromInside int
	err := cell.Engine.RunOnLoop(ctx, func() error {
		grid := cell.Stage.SpatialGrid()
		if grid == nil {
			t.Fatal("cell has no spatial grid")
		}
		if grid.TrackedCount() == 0 {
			t.Fatal("the grid is empty — this test would pass on any implementation")
		}
		// Cube lattice centre, in this cell's own frame.
		centre := component.Vec3{X: CellSize / 2, Y: CellSize / 2}
		fromAbove = len(grid.QueryRadius(component.Vec3{X: centre.X, Y: centre.Y, Z: high}, AoIRadius, nil))
		fromInside = len(grid.QueryRadius(centre, AoIRadius, nil))
		return nil
	})
	if err != nil {
		t.Fatalf("grid query: %v", err)
	}

	if fromInside == 0 {
		t.Fatal("a query from inside the cube field finds nothing — the fixture is broken, not the code")
	}
	if fromAbove != 0 {
		t.Errorf("a viewer %d units above the world sees %d of %d cubes within a %v-unit radius — "+
			"area of interest is still an infinite cylinder", high, fromAbove, fromInside, float32(AoIRadius))
	}
}

// TestCube3D_SpatialEntriesCarryHeight is the wiring half: SpatialSystem must
// be putting Position.Z into the grid at all. Without it the assertion above
// passes for the wrong reason — every entry at Z=0 is far from a viewer at
// Z=10000 whether or not the entry's own height was ever recorded.
func TestCube3D_SpatialEntriesCarryHeight(t *testing.T) {
	process, stop := runProcess(t)
	defer stop()

	cell := firstCell(t, process)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var aloft, total int
	err := cell.Engine.RunOnLoop(ctx, func() error {
		grid := cell.Stage.SpatialGrid()
		// A radius that covers the whole cell from its centre, at the height
		// the cube lattice actually occupies.
		centre := component.Vec3{X: CellSize / 2, Y: CellSize / 2, Z: CubeHeight(CubesPerCell / 2)}
		for _, entry := range grid.QueryRadius(centre, 4000, nil) {
			total++
			if entry.Z != 0 {
				aloft++
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("grid query: %v", err)
	}

	if total == 0 {
		t.Fatal("no entries in the grid")
	}
	if aloft == 0 {
		t.Fatalf("all %d spatial entries are at Z=0 — SpatialSystem is not writing Position.Z "+
			"into the grid, so every 3D spatial query is answering in two dimensions", total)
	}
}

// firstCell returns any owned cell, through the locked accessors — ranging
// Process.Cells directly is a data race (see cubeCensus).
func firstCell(t *testing.T, process *mmokit.Process) *pkguniverse.Cell {
	t.Helper()
	for _, c := range cellsOf(t, process) {
		if c.Stage != nil && c.Engine != nil {
			return c
		}
	}
	t.Fatal("process has no usable cell")
	return nil
}
