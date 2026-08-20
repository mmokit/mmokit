package mmokit

import (
	"strings"
	"testing"

	"github.com/mlange-42/ark/ecs"
	"github.com/mmokit/mmokit/pkg/component"
	"github.com/mmokit/mmokit/pkg/system"
	"github.com/mmokit/mmokit/pkg/universe"
)

func newDimensionTestProcess(t *testing.T, d Dimension) *universe.Process {
	t.Helper()
	return New(Config{
		Mode:      "all",
		CellsX:    1,
		CellsY:    1,
		Dimension: d,
		Headless:  true,
		HTTPPort:  -1,
	})
}

// TestBuildReplicators_SelectsTheProfileBindings pins that the dimension on
// Config actually reaches the wire layout — the whole point of the profile
// seam. A regression here is invisible: a 3D process would replicate two
// coordinates and its operator would believe it replicated three, and
// TypeIDOf cannot detect that because one component set means a 2D and a 3D
// Position hash identically.
func TestBuildReplicators_SelectsTheProfileBindings(t *testing.T) {
	for _, c := range []struct {
		dim    Dimension
		fields int
		bytes  int
	}{
		{dim: Dimension2D, fields: 7, bytes: 18},
		{dim: Dimension3D, fields: 11, bytes: 33},
	} {
		world := ecs.NewWorld()
		coord := newDimensionTestProcess(t, c.dim)
		def := universe.EntityKindDef{Kind: 1}

		reg := BuildReplicators(world, coord, def)
		rep := reg.Get(1)
		if rep == nil {
			t.Fatalf("%s: kind 1 not registered", c.dim)
		}
		layout := rep.SnapshotLayout()
		if len(layout) != c.fields {
			t.Errorf("%s: layout has %d fields (%v), want %d", c.dim, len(layout), layout, c.fields)
		}
		total := 0
		for _, n := range layout {
			total += n
		}
		if total != c.bytes {
			t.Errorf("%s: snapshot is %d bytes, want %d", c.dim, total, c.bytes)
		}
	}
}

// TestBuildReplicators_RefusesDoubleOrientation covers the composition that
// would otherwise ship silently: the 3D engine set emits orientation, and a
// game ported from 2D keeps its per-kind QAngle. Two orientation fields would
// land in every snapshot and no generated client would read the second.
func TestBuildReplicators_RefusesDoubleOrientation(t *testing.T) {
	world := ecs.NewWorld()
	rotMap := ecs.NewMap1[component.Rotation](world)
	def := universe.EntityKindDef{
		Kind:            7,
		NetworkBindings: []system.ComponentBinding{system.QAngle(rotMap)},
	}

	// 2D: the engine set has no orientation, so the game supplying its own is
	// the supported arrangement and must keep working.
	coord2D := newDimensionTestProcess(t, Dimension2D)
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("2D refused a game-supplied orientation binding: %v", r)
			}
		}()
		BuildReplicators(ecs.NewWorld(), coord2D, def)
	}()

	// 3D: the engine set owns orientation, so the same kind is a conflict.
	coord3D := newDimensionTestProcess(t, Dimension3D)
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("3D accepted a doubly-emitted orientation")
		}
		msg, _ := r.(string)
		if !strings.Contains(msg, "orientation") {
			t.Fatalf("panic does not name the problem: %v", r)
		}
	}()
	BuildReplicators(world, coord3D, def)
}
