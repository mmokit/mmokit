package main

import (
	"context"
	"testing"
	"time"

	"github.com/mmokit/mmokit"
	"github.com/mmokit/mmokit/pkg/component"

	"github.com/mlange-42/ark/ecs"
)

// runProcess starts the real cube3d process and returns a stop func.
func runProcess(t *testing.T) (*mmokit.Process, func()) {
	t.Helper()
	process := NewProcess()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		process.Start(ctx)
	}()

	// Let every cell tick at least once so bootstrap and physics have run.
	time.Sleep(200 * time.Millisecond)

	return process, func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("process did not stop")
		}
	}
}

// cubeCensus counts live cubes across every owned cell and reports how many
// carry a non-zero Z.
//
// Cells are reached through Control.AllOwnedCells and Process.CellByID rather
// than by ranging Process.Cells directly. That field is exported but the
// framework guards it with an unexported mutex, and Build writes to it from
// the goroutine running Start — ranging it from here is a data race the
// detector reports, which is how this was found.
//
// Each cell's scan runs on that cell's own loop goroutine, which is separately
// required: iterating a cell's world from here while its loop writes trips
// Ark's world-lock guard.
func cubeCensus(t *testing.T, process *mmokit.Process) (total, aloft int) {
	t.Helper()

	var ids []mmokit.MeshCellID
	process.Control.AllOwnedCells(func(cellKey, _ string) bool {
		ids = append(ids, mmokit.MeshCellID(cellKey))
		return true
	})

	for _, id := range ids {
		cell := process.CellByID(id)
		if cell == nil || cell.Stage == nil || cell.Engine == nil {
			continue
		}
		stage := cell.Stage
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := cell.Engine.RunOnLoop(ctx, func() error {
			w := stage.ECSWorld()
			posMap := ecs.NewMap1[component.Position](w)
			filter := ecs.NewFilter1[component.Position](w).
				Without(ecs.C[component.Ghost](), ecs.C[component.Replica]())
			q := filter.Query()
			defer q.Close()
			for q.Next() {
				total++
				if posMap.Get(q.Entity()).Z != 0 {
					aloft++
				}
			}
			return nil
		})
		cancel()
		if err != nil {
			t.Fatalf("census on %s: %v", id, err)
		}
	}
	return total, aloft
}

// TestCube3D_SurvivesCellSplitWithZ is phase 2's acceptance criterion, made
// executable: "a headless 3D example survives a cell split with a non-zero
// destination entity count asserted."
//
// The Z half is the part that makes it a 3D test rather than a split test.
// Phase 1 widened the transfer frame but neither wrote nor read PosZ, so every
// cube would have arrived at Z=0 with no error anywhere — a split that looks
// perfectly healthy by entity count alone. Counting entities is the stated
// criterion; counting entities that kept their height is the one that fails
// when the 3D pipeline is broken.
func TestCube3D_SurvivesCellSplitWithZ(t *testing.T) {
	process, stop := runProcess(t)
	defer stop()

	wantTotal := CellsX * CellsY * CubesPerCell
	total, aloft := cubeCensus(t, process)
	if total != wantTotal {
		t.Fatalf("before split: %d cubes, want %d", total, wantTotal)
	}
	if aloft == 0 {
		t.Fatal("before split: every cube is at Z=0 — the fixture cannot detect a dropped Z")
	}
	beforeAloft := aloft

	parent := mmokit.CellID{X: 0, Y: 0}
	if err := process.SplitCell(parent, true); err != nil {
		t.Fatalf("SplitCell: %v", err)
	}

	// The destination cells are the four children. Assert against the whole
	// cluster so a cube lost in transit fails here rather than being masked
	// by a sibling cell's population.
	total, aloft = cubeCensus(t, process)
	if total != wantTotal {
		t.Errorf("after split: %d cubes, want %d — entities were lost or duplicated", total, wantTotal)
	}
	if total == 0 {
		t.Fatal("after split: destination entity count is zero")
	}
	if aloft != beforeAloft {
		t.Errorf("after split: %d cubes aloft, want %d — Z was dropped crossing a cell boundary",
			aloft, beforeAloft)
	}

	// The parent must be gone and its children owned.
	if owner := process.HostForCellID(parent.MeshID()); owner != "" {
		t.Errorf("parent cell %s still owned by %q after split", parent, owner)
	}
	for _, child := range parent.Children() {
		if process.HostForCellID(child.MeshID()) == "" {
			t.Errorf("child cell %s has no owner after split", child)
		}
	}
}

// TestCube3D_IsA3DProcess guards the fixture's premise. If a refactor made
// this example 2D, every assertion above would still pass — on a process that
// proves nothing about the 3D profile.
func TestCube3D_IsA3DProcess(t *testing.T) {
	process := NewProcess()
	if got := process.Dimension(); got != mmokit.Dimension3D {
		t.Fatalf("Dimension() = %v, want 3d", got)
	}
	if process.SchemaFingerprint() == 0 {
		// Only after Build; assert the facade installed a protocol at all.
		process.Build()
		if process.SchemaFingerprint() == 0 {
			t.Fatal("no schema fingerprint — this process would bypass mesh dimension admission")
		}
	}
}
