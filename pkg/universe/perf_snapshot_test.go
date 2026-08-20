package universe

import (
	"testing"
	"time"

	"github.com/mmokit/mmokit/pkg/engine"
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
	cell := NewCell("0_0", CellID{})
	cell.Engine = eng

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
	cell := NewCell("0_0", CellID{})
	cell.Engine = eng

	snap := buildPerfCellSnapshot(cell, "host-a")

	if snap.Entities.Real != 0 || snap.Network.Connections != 0 {
		t.Errorf("expected zero values, got %+v / %+v", snap.Entities, snap.Network)
	}
}

func TestPerfCellSnapshotToText(t *testing.T) {
	snap := PerfCellSnapshot{
		TickHz:   20,
		BudgetMS: 50,
		Tick: TickTimingStats{
			SampleCount: 1,
			Avg:         10 * time.Millisecond,
			P95:         15 * time.Millisecond,
		},
		Systems: []SystemTiming{
			{Name: "Phys", Avg: 3 * time.Millisecond, P95: 4 * time.Millisecond},
		},
	}
	snap.Entities.Real = 42

	text := snap.toText()

	if text.TickHz != 20 || text.BudgetMS != 50 {
		t.Errorf("tick mapping wrong: %+v", text)
	}
	if len(text.SystemNames) != 1 || text.SystemNames[0] != "Phys" {
		t.Errorf("systems not copied: %+v", text.SystemNames)
	}
	if text.EntitiesReal != 42 {
		t.Errorf("entities not copied: %d", text.EntitiesReal)
	}
}

// TestBuildPerfCellSnapshotBudgetTracksScheduledPeriod pins the admin-visible
// budget to the period the loop actually schedules rather than to a second
// truncating derivation from TickRate. At 60Hz the loop schedules 17ms; a
// snapshot claiming a 16ms budget would report every tick as nearly
// overbudget against a period the loop never runs.
func TestBuildPerfCellSnapshotBudgetTracksScheduledPeriod(t *testing.T) {
	for _, c := range []struct {
		rate     int
		budgetMS int
	}{
		{rate: 20, budgetMS: 50},
		{rate: 60, budgetMS: 17},
		{rate: 0, budgetMS: 50},
	} {
		eng := &engine.Engine{
			Config: engine.Config{TickRate: c.rate},
			Perf:   engine.NewTickProfile(nil),
		}
		cell := NewCell("0_0", CellID{})
		cell.Engine = eng

		snap := buildPerfCellSnapshot(cell, "host-a")
		if snap.BudgetMS != c.budgetMS {
			t.Errorf("rate %d: BudgetMS = %d, want %d", c.rate, snap.BudgetMS, c.budgetMS)
		}
		if snap.BudgetMS != int(eng.TickIntervalMs()) {
			t.Errorf("rate %d: BudgetMS %d != TickIntervalMs %d", c.rate, snap.BudgetMS, eng.TickIntervalMs())
		}
	}
}
