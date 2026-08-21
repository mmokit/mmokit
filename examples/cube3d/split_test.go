package main

import (
	"context"
	"testing"
	"time"

	"github.com/mmokit/mmokit"
	"github.com/mmokit/mmokit/pkg/component"
	"github.com/mmokit/mmokit/pkg/engine"
	"github.com/mmokit/mmokit/pkg/quantize"
	pkguniverse "github.com/mmokit/mmokit/pkg/universe"

	"github.com/mlange-42/ark/ecs"
)

// runProcess starts the real cube3d process and returns a stop func.
func runProcess(t *testing.T) (*mmokit.Process, func()) {
	t.Helper()
	process := NewProcess(true)

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
	process := NewProcess(true)
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

// TestCube3D_ReplicatesThe3DBindingSet is the gap phase 2 left and did not
// know about: cube3d simulated 3D correctly and replicated NOTHING, because it
// registered neither the spatial system nor the network system. The 3D engine
// binding set was schema-pinned but had never produced a byte on the wire, and
// the split test could not notice — a cell transfer travels through
// TransferFrame, not through replication.
//
// This asserts the wire layout the process actually declares, which is what a
// generated client decodes against.
func TestCube3D_ReplicatesThe3DBindingSet(t *testing.T) {
	process := NewProcess(true)
	process.Build()
	t.Cleanup(process.Shutdown)

	if process.SchemaFingerprint() == 0 {
		t.Fatal("no protocol installed")
	}

	// Assert the systems that actually SEND are registered. The schema
	// cannot: removing NewNetworkSystem leaves cube3d's fingerprint at
	// b6b9ce73, byte for byte, because --dump-schema pins the LAYOUT and not
	// whether anything emits it. That is precisely how this example shipped
	// in phase 2 replicating nothing, with schema-check green.
	// Assert the systems that actually SEND are registered. The schema
	// cannot: removing NewNetworkSystem leaves cube3d's fingerprint at
	// b6b9ce73, byte for byte, because --dump-schema pins the LAYOUT and not
	// whether anything emits it. That is precisely how this example shipped
	// in phase 2 replicating nothing, with schema-check green.
	var order []string
	for _, cell := range cellsOf(t, process) {
		cell.Loop.EachSystem(func(name string, _ engine.System) {
			order = append(order, name)
		})
		break
	}

	indexOf := func(name string) int {
		for i, n := range order {
			if n == name {
				return i
			}
		}
		return -1
	}
	spatial, network := indexOf("Spatial"), indexOf("Network")
	if spatial < 0 || network < 0 {
		t.Fatalf("Spatial and Network must both be registered — cube3d would replicate nothing (order: %v)", order)
	}
	// Network runs after everything that mutates state this tick, so a frame
	// carries the state that tick produced. CellBoundary is appended by the
	// framework after it, so "Network last" is a rule about GAME systems.
	if network < spatial {
		t.Errorf("Network runs before Spatial (order: %v)", order)
	}
	if i := indexOf("Tumble"); i < 0 || network < i {
		t.Errorf("Network must run after the game systems (order: %v)", order)
	}

	world := ecs.NewWorld()
	reg := mmokit.BuildReplicators(world, process, cubeKindDefs(t, process)...)

	for _, c := range []struct {
		kind   uint8
		name   string
		fields int
	}{
		// 11 engine fields (33 bytes) + Spin's three qvel fields.
		{kind: KindCube, name: "Cube", fields: 14},
		// The viewer's only game field is initial-only, so its fixed layout
		// is the engine set exactly.
		{kind: KindViewer, name: "Viewer", fields: 11},
	} {
		rep := reg.Get(c.kind)
		if rep == nil {
			t.Fatalf("%s: kind %d has no replicator — the network system would send nothing", c.name, c.kind)
		}
		layout := rep.SnapshotLayout()
		if len(layout) != c.fields {
			t.Errorf("%s: layout has %d fields (%v), want %d", c.name, len(layout), layout, c.fields)
		}
		// The quaternion is one field of QuatWireSize, never four scalars.
		found := false
		for _, w := range layout {
			if w == quantize.QuatWireSize {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: layout %v carries no %d-byte orientation field", c.name, layout, quantize.QuatWireSize)
		}
	}
}

// cellsOf returns the process's cells through the locked accessors. Ranging
// Process.Cells directly is a data race: the field is exported but guarded by
// an unexported mutex that Build writes under.
func cellsOf(t *testing.T, process *mmokit.Process) []*pkguniverse.Cell {
	t.Helper()
	var out []*pkguniverse.Cell
	process.Control.AllOwnedCells(func(cellKey, _ string) bool {
		if c := process.CellByID(pkguniverse.MeshCellID(cellKey)); c != nil {
			out = append(out, c)
		}
		return true
	})
	if len(out) == 0 {
		t.Fatal("process has no cells")
	}
	return out
}

// cubeKindDefs pulls the registered kind definitions off a built process.
func cubeKindDefs(t *testing.T, process *mmokit.Process) []pkguniverse.EntityKindDef {
	t.Helper()
	stage := process.NewSchemaStage()
	var out []pkguniverse.EntityKindDef
	for _, def := range stage.EntityKindDefs() {
		out = append(out, *def)
	}
	if len(out) != 2 {
		t.Fatalf("expected 2 entity kinds, got %d", len(out))
	}
	return out
}
