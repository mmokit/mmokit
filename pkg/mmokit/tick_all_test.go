package mmokit_test

import (
	"testing"

	"github.com/zenion/mmoserver/pkg/mmokit"
	pkguniverse "github.com/zenion/mmoserver/pkg/universe"
)

func TestOnWorldTickAll_FiresOnAllStages(t *testing.T) {
	p := mmokit.New(mmokit.Config{
		Mode:     "all",
		CellsX:   2,
		CellsY:   1,
		Headless: true,
	})

	fired := map[pkguniverse.MeshCellID]int{}
	mmokit.OnWorldTickAll(p, func(stage *pkguniverse.Stage, dt float32) {
		fired[stage.CellID()]++
	})

	p.Build()

	// Drive 3 ticks per stage manually (matches the existing OnWorldTick test).
	if len(p.Cells) != 2 {
		t.Fatalf("expected 2 cells, got %d", len(p.Cells))
	}
	for _, cell := range p.Cells {
		runTicks(t, cell.Stage, 3)
	}

	if len(fired) != 2 {
		t.Fatalf("WorldTick fired on %d cells, want 2", len(fired))
	}
	for cid, n := range fired {
		if n != 3 {
			t.Errorf("cell %s: %d ticks, want 3", cid, n)
		}
	}
}

func TestOnTickAll_FiresPerEntityOnAllStages(t *testing.T) {
	p := mmokit.New(mmokit.Config{
		Mode:     "all",
		CellsX:   2,
		CellsY:   1,
		Headless: true,
	})
	registerTestKindOnProcess(t, p)

	mmokit.OnTickAll[tickComp](p, func(e mmokit.Entity, dt float32) {
		c := mmokit.Get[tickComp](e)
		c.N++
	})

	p.Build()

	// Spawn one entity per cell with tickComp. Each must increment 4 times.
	entities := []mmokit.Entity{}
	for _, cell := range p.Cells {
		e := cell.Stage.Spawn(mmokit.Position{}, mmokit.EntityKind{Type: testKindID})
		mmokit.Set(e, tickComp{N: 0})
		entities = append(entities, e)
		runTicks(t, cell.Stage, 4)
	}

	for _, e := range entities {
		if got := mmokit.Get[tickComp](e).N; got != 4 {
			t.Errorf("entity %s: N=%d, want 4", e, got)
		}
	}
}

func TestOnTickEachAll_BundleAccessOnAllStages(t *testing.T) {
	p := mmokit.New(mmokit.Config{
		Mode:     "all",
		CellsX:   2,
		CellsY:   1,
		Headless: true,
	})
	registerTestKindOnProcess(t, p)

	type bundle struct {
		A *tickAccum
		R *tickRate
	}
	mmokit.OnTickEachAll[bundle](p, func(e mmokit.Entity, b *bundle, dt float32) {
		b.A.Acc += b.R.Rate * dt
	})

	p.Build()

	entities := []mmokit.Entity{}
	for _, cell := range p.Cells {
		e := cell.Stage.Spawn(mmokit.Position{}, mmokit.EntityKind{Type: testKindID})
		mmokit.Set(e, tickAccum{Acc: 0})
		mmokit.Set(e, tickRate{Rate: 2})
		entities = append(entities, e)
		runTicks(t, cell.Stage, 4) // 4 ticks of dt=1/20=0.05
	}

	expected := float32(2 * 4 * 0.05) // 0.4
	for _, e := range entities {
		if got := mmokit.Get[tickAccum](e).Acc; got != expected {
			t.Errorf("entity %s: Acc=%v, want %v", e, got, expected)
		}
	}
}
