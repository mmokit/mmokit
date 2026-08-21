package main

import (
	"context"
	"sort"
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

// census is one instant's view of the cluster's cube population.
//
// drifterZ is sorted so two censuses compare as multisets: which cell a cube
// is in changes constantly now that the drifting half roams, and a comparison
// that depended on cell order would fail for a cube that merely moved.
type census struct {
	total    int
	drifterZ []float32
}

// cubeCensus counts live cubes across every owned cell and records the height
// of every drifting one.
//
// The drifters are the assertion's anchor because their Z is CONSTANT: they
// are MoveFly at their spawn height and nothing ever changes it, so a
// difference across a split is a dropped or corrupted Z and nothing else. The
// bouncing half is deliberately excluded — its Z is a function of time, so it
// can prove that gravity runs (TestCube3D_GravityMovesTheBouncers) but not
// that a transfer preserved anything.
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
func cubeCensus(t *testing.T, process *mmokit.Process) census {
	t.Helper()

	var ids []mmokit.MeshCellID
	process.Control.AllOwnedCells(func(cellKey, _ string) bool {
		ids = append(ids, mmokit.MeshCellID(cellKey))
		return true
	})

	var out census
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
			motionMap := ecs.NewMap1[component.Motion](w)
			spinMap := ecs.NewMap1[Spin](w)
			filter := ecs.NewFilter1[component.Position](w).
				Without(ecs.C[component.Ghost](), ecs.C[component.Replica]())
			q := filter.Query()
			defer q.Close()
			for q.Next() {
				e := q.Entity()
				// Spin is the cube marker: a viewer has none. Headless runs
				// have no players today, but a census that silently counted
				// one would make every number here wrong by an amount that
				// depends on who is connected.
				if !spinMap.HasAll(e) {
					continue
				}
				out.total++
				if motionMap.HasAll(e) && motionMap.Get(e).Mode == component.MoveFly {
					out.drifterZ = append(out.drifterZ, posMap.Get(e).Z)
				}
			}
			return nil
		})
		cancel()
		if err != nil {
			t.Fatalf("census on %s: %v", id, err)
		}
	}
	sort.Slice(out.drifterZ, func(i, j int) bool { return out.drifterZ[i] < out.drifterZ[j] })
	return out
}

// awaitCensus polls until the cluster holds exactly want cubes.
//
// The retry is not papering over a flake, it is what makes the count
// meaningful now that cubes cross cell lines on their own. A cube in flight
// between two cells is Live in neither for the tick the handoff takes, and the
// census walks cells one at a time — so an instantaneous count is legitimately
// allowed to be short. "Converges to want" is the property; a cube actually
// lost never converges and this still fails.
func awaitCensus(t *testing.T, process *mmokit.Process, want int) census {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var last census
	for {
		last = cubeCensus(t, process)
		if last.total == want {
			return last
		}
		if time.Now().After(deadline) {
			t.Fatalf("cluster settled at %d cubes, want %d", last.total, want)
		}
		time.Sleep(25 * time.Millisecond)
	}
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
	before := awaitCensus(t, process, wantTotal)
	if len(before.drifterZ) == 0 {
		t.Fatal("before split: no drifting cubes — the fixture cannot detect a dropped Z")
	}
	for _, z := range before.drifterZ {
		if z == 0 {
			t.Fatal("before split: a drifter is at Z=0 — indistinguishable from a dropped Z")
		}
	}

	parent := mmokit.CellID{X: 0, Y: 0}
	if err := process.SplitCell(parent, true); err != nil {
		t.Fatalf("SplitCell: %v", err)
	}

	// The destination cells are the four children. Assert against the whole
	// cluster so a cube lost in transit fails here rather than being masked
	// by a sibling cell's population.
	after := awaitCensus(t, process, wantTotal)
	if len(after.drifterZ) != len(before.drifterZ) {
		t.Fatalf("after split: %d drifters, want %d", len(after.drifterZ), len(before.drifterZ))
	}
	// Every drifter's exact height, not a count of non-zero ones. A count
	// survives a Z that arrives merely WRONG rather than zeroed, which is
	// what a mis-sized transfer field produces.
	for i := range after.drifterZ {
		if after.drifterZ[i] != before.drifterZ[i] {
			t.Errorf("after split: drifter heights changed: %v, want %v — Z was corrupted crossing a cell boundary",
				after.drifterZ, before.drifterZ)
			break
		}
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

// bouncerHeights samples every bouncing cube's height, keyed by NetworkID so
// samples taken at different instants describe the same cubes.
func bouncerHeights(t *testing.T, process *mmokit.Process) map[uint32]float32 {
	t.Helper()

	var ids []mmokit.MeshCellID
	process.Control.AllOwnedCells(func(cellKey, _ string) bool {
		ids = append(ids, mmokit.MeshCellID(cellKey))
		return true
	})

	out := make(map[uint32]float32)
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
			netMap := ecs.NewMap1[component.NetworkID](w)
			bounceMap := ecs.NewMap1[Bounce](w)
			filter := ecs.NewFilter1[component.Position](w).
				Without(ecs.C[component.Ghost](), ecs.C[component.Replica]())
			q := filter.Query()
			defer q.Close()
			for q.Next() {
				e := q.Entity()
				if !bounceMap.HasAll(e) || !netMap.HasAll(e) {
					continue
				}
				out[netMap.Get(e).ID] = posMap.Get(e).Z
			}
			return nil
		})
		cancel()
		if err != nil {
			t.Fatalf("bouncer sample on %s: %v", id, err)
		}
	}
	return out
}

// TestCube3D_GravityMovesTheBouncers asserts the thing a reader of this
// example is meant to see: cubes fall, hit the ground, and come back up.
//
// Nothing else here can. The split test's drifters deliberately hold their
// height, and BounceSystem's arithmetic is unit-tested in isolation — this is
// the only assertion that the system is REGISTERED, runs after physics, and
// that its cubes were spawned MoveBallistic. Registering it before physics, or
// spawning bouncers MoveWalk, leaves every unit test green.
func TestCube3D_GravityMovesTheBouncers(t *testing.T) {
	process, stop := runProcess(t)
	defer stop()

	first := bouncerHeights(t, process)
	if len(first) == 0 {
		t.Fatal("no bouncing cubes exist")
	}

	fell := make(map[uint32]bool)
	rose := false
	prev := first
	// The lowest bouncer's apex is CubeHeight(1) = 70, which under this
	// gravity is a 0.6 s fall — so two seconds covers a fall and a bounce
	// with room to spare even on a loaded machine.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && !rose {
		time.Sleep(25 * time.Millisecond)
		now := bouncerHeights(t, process)
		for id, z := range now {
			p, ok := prev[id]
			if !ok {
				continue
			}
			switch {
			case z < p:
				fell[id] = true
			case z > p && fell[id]:
				// Fell, then climbed: that is a bounce, and it can only
				// happen if BounceSystem ran on a position physics had
				// already pushed below the plane.
				rose = true
			}
		}
		prev = now
	}

	if len(fell) == 0 {
		t.Fatal("no bouncing cube ever lost height — gravity is not reaching them")
	}
	if !rose {
		t.Fatal("cubes fell but none came back up — BounceSystem is not running, or runs before physics")
	}
}

// TestCube3D_EveryCubeCarriesBounce pins a framework behaviour that is easy to
// design against by accident.
//
// A kind's component set is UNIFORM after a transfer: the destination calls
// Stage.EnsureEntityKindComponents, which adds a zero value for every
// component the kind declares. So `mmokit:"optional"` means "the caller may
// omit it at spawn", not "this entity may lack it" — an entity spawned without
// one acquires it, silently, the first time it crosses a cell line.
//
// cube3d hit exactly that: Bounce was optional and omitted for drifting cubes,
// and within two seconds the eight drifters that had crossed a boundary were
// carrying a Bounce nobody spawned. The fix is for the zero value to mean
// something, which is only safe if every cube really does start with one.
func TestCube3D_EveryCubeCarriesBounce(t *testing.T) {
	process, stop := runProcess(t)
	defer stop()

	var ids []mmokit.MeshCellID
	process.Control.AllOwnedCells(func(cellKey, _ string) bool {
		ids = append(ids, mmokit.MeshCellID(cellKey))
		return true
	})

	cubes, withBounce, bouncers := 0, 0, 0
	for _, id := range ids {
		cell := process.CellByID(id)
		if cell == nil || cell.Stage == nil || cell.Engine == nil {
			continue
		}
		stage := cell.Stage
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := cell.Engine.RunOnLoop(ctx, func() error {
			w := stage.ECSWorld()
			spinMap := ecs.NewMap1[Spin](w)
			bounceMap := ecs.NewMap1[Bounce](w)
			filter := ecs.NewFilter1[component.Position](w).
				Without(ecs.C[component.Ghost](), ecs.C[component.Replica]())
			q := filter.Query()
			defer q.Close()
			for q.Next() {
				e := q.Entity()
				if !spinMap.HasAll(e) {
					continue
				}
				cubes++
				if !bounceMap.HasAll(e) {
					continue
				}
				withBounce++
				if bounceMap.Get(e).Launch > 0 {
					bouncers++
				}
			}
			return nil
		})
		cancel()
		if err != nil {
			t.Fatalf("scan on %s: %v", id, err)
		}
	}

	if cubes == 0 {
		t.Fatal("no cubes exist")
	}
	if withBounce != cubes {
		t.Errorf("%d of %d cubes carry a Bounce — a drifter would acquire one on its first "+
			"cell crossing instead of starting with it", withBounce, cubes)
	}
	// Both roles must be represented, or the uniformity above is vacuous.
	if bouncers == 0 || bouncers == cubes {
		t.Errorf("%d of %d cubes have a non-zero launch — want a mix of both roles", bouncers, cubes)
	}
}
