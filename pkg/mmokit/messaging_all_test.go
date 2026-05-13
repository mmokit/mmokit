package mmokit_test

import (
	"reflect"
	"testing"

	"github.com/mlange-42/ark/ecs"
	"github.com/zenion/mmoserver/pkg/mmokit"
	pkguniverse "github.com/zenion/mmoserver/pkg/universe"
)

type allPing struct{ N int }

// registerTestKindOnProcess wires the test entity kind into a Process via
// RegisterKindSpec so every cell created by Build gets it. Mirrors the
// per-stage registerTestKind helper but at the Process level.
func registerTestKindOnProcess(t *testing.T, p *pkguniverse.Process) {
	t.Helper()
	p.RegisterKindSpec(func(stage *pkguniverse.Stage) {
		def := pkguniverse.EntityKindDef{Kind: uint8(testKindID), Name: "TestKind"}
		w := stage.ECSWorld()
		pkguniverse.KindComponentByID(
			&def, w,
			ecs.ComponentID[testKindHealth](w),
			reflect.TypeFor[testKindHealth](),
			false,
		)
		stage.RegisterEntityKind(def)
	})
}

func TestHandleAll_RegistersOnAllStages(t *testing.T) {
	p := mmokit.New(mmokit.Config{
		Mode:     "all",
		CellsX:   2,
		CellsY:   1,
		Headless: true,
	})

	registerTestKindOnProcess(t, p)

	fired := map[pkguniverse.MeshCellID]int{}
	mmokit.HandleAll(p, func(target mmokit.Entity, msg *allPing) {
		fired[target.Stage().CellID()]++
	})

	p.Build()

	// Spawn an entity on each cell, send a ping, verify per-cell fire.
	if len(p.Cells) != 2 {
		t.Fatalf("expected 2 cells, got %d", len(p.Cells))
	}
	for _, cell := range p.Cells {
		e := cell.Stage.Spawn(mmokit.Position{}, mmokit.EntityKind{Type: testKindID})
		e.Send(&allPing{N: 1})
	}

	if len(fired) != 2 {
		t.Fatalf("HandleAll fired on %d cells, want 2 (cells: %v)", len(fired), fired)
	}
	for cellID, count := range fired {
		if count != 1 {
			t.Errorf("cell %s: %d invocations, want 1", cellID, count)
		}
	}
}

// TestHandleAll_LateRegistrationStillWorks verifies that calling HandleAll
// AFTER Build() still attaches the handler to every existing stage —
// the late-registration catch-up path on Process.OnStageInit.
func TestHandleAll_LateRegistrationStillWorks(t *testing.T) {
	p := mmokit.New(mmokit.Config{
		Mode:     "all",
		CellsX:   2,
		CellsY:   1,
		Headless: true,
	})

	registerTestKindOnProcess(t, p)
	p.Build()

	fired := 0
	mmokit.HandleAll(p, func(target mmokit.Entity, msg *allPing) {
		fired++
	})

	for _, cell := range p.Cells {
		e := cell.Stage.Spawn(mmokit.Position{}, mmokit.EntityKind{Type: testKindID})
		e.Send(&allPing{N: 1})
	}

	if fired != 2 {
		t.Fatalf("HandleAll fired %d times, want 2", fired)
	}
}
