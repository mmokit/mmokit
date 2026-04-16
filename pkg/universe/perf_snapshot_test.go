package universe

import (
	"testing"
	"time"

	"github.com/zenion/mmoserver/pkg/engine"
)

func TestBuildPerfCellSnapshotPopulatesAllFields(t *testing.T) {
	// Construct a cell with a TickProfile that has one recorded sample.
	eng := &engine.Engine{
		Config: engine.Config{TickRate: 20},
		Perf:   engine.NewTickProfile([]string{"SystemA", "SystemB"}),
	}
	eng.Perf.Record(
		[]time.Duration{5 * time.Millisecond, 3 * time.Millisecond},
		8*time.Millisecond,
	)
	cell := &Cell{
		ID:     "0_0",
		Engine: eng,
	}

	snap := buildPerfCellSnapshot(cell, "host-a")

	if snap.HostID != "host-a" {
		t.Errorf("HostID = %q, want host-a", snap.HostID)
	}
	if snap.CellID != "0_0" {
		t.Errorf("CellID = %q, want 0_0", snap.CellID)
	}
	if snap.TickHz != 20 {
		t.Errorf("TickHz = %d, want 20", snap.TickHz)
	}
	if snap.BudgetMS != 50 {
		t.Errorf("BudgetMS = %d, want 50", snap.BudgetMS)
	}
	if snap.Tick.SampleCount != 1 {
		t.Errorf("SampleCount = %d, want 1", snap.Tick.SampleCount)
	}
	if snap.Tick.Avg != 8*time.Millisecond {
		t.Errorf("Tick.Avg = %v, want 8ms", snap.Tick.Avg)
	}
	if len(snap.Systems) != 2 {
		t.Fatalf("Systems len = %d, want 2", len(snap.Systems))
	}
	if snap.Systems[0].Name != "SystemA" || snap.Systems[0].Avg != 5*time.Millisecond {
		t.Errorf("Systems[0] = %+v", snap.Systems[0])
	}
}

func TestBuildPerfCellSnapshotNilMetricsTolerated(t *testing.T) {
	// Cell without Metrics should not panic; Entities/Network/Load zeroed.
	eng := &engine.Engine{
		Config: engine.Config{TickRate: 20},
		Perf:   engine.NewTickProfile(nil),
	}
	cell := &Cell{ID: "0_0", Engine: eng, Metrics: nil}

	snap := buildPerfCellSnapshot(cell, "host-a")

	if snap.Entities.Real != 0 || snap.Network.Connections != 0 {
		t.Errorf("expected zero values, got %+v / %+v", snap.Entities, snap.Network)
	}
}
