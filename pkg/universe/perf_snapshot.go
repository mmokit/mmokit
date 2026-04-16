package universe

import (
	"time"

	"github.com/zenion/mmoserver/pkg/metrics"
)

// PerfCellSnapshot is the wire format returned by perf.snapshot for a single
// cell. Every field is JSON-serializable so the dispatcher can ship it across
// MeshControl when the worker runs on a remote host.
type PerfCellSnapshot struct {
	HostID   string
	CellID   string
	TickHz   int
	BudgetMS int
	Tick     TickTimingStats
	Systems  []SystemTiming
	Entities metrics.EntitySnapshot
	Network  metrics.NetworkSnapshot
	Load     float64
	// OverbudgetPct is fraction of ticks that exceeded the budget (0..1).
	OverbudgetPct float64
	// EffectiveHz is the measured sustainable tick rate.
	EffectiveHz float64
}

// TickTimingStats is a JSON-friendly copy of engine.TimingStats for the
// whole-tick bucket, with SampleCount so the caller can detect empty
// profiles (newly reset).
type TickTimingStats struct {
	SampleCount int
	Latest      time.Duration
	Avg         time.Duration
	P50         time.Duration
	P95         time.Duration
	P99         time.Duration
	Max         time.Duration
}

// SystemTiming is a JSON-friendly per-system timing pair.
type SystemTiming struct {
	Name string
	Avg  time.Duration
	P95  time.Duration
}

// buildPerfCellSnapshot reads live state from a cell and returns a PerfCellSnapshot.
// Must run on the cell's game-loop goroutine (caller's responsibility — use
// engine.RunOnLoop). Tolerates nil Metrics; all read access to Engine.Perf is
// required, so Engine and Engine.Perf must be non-nil.
func buildPerfCellSnapshot(cell *Cell, hostID string) PerfCellSnapshot {
	eng := cell.Engine
	stats := eng.Perf.Stats()

	budgetMS := 0
	if eng.Config.TickRate > 0 {
		budgetMS = 1000 / eng.Config.TickRate
	}
	out := PerfCellSnapshot{
		HostID:   hostID,
		CellID:   cell.ID,
		TickHz:   eng.Config.TickRate,
		BudgetMS: budgetMS,
		Tick: TickTimingStats{
			SampleCount: stats.SampleCount,
			Latest:      stats.Total.Latest,
			Avg:         stats.Total.Avg,
			P50:         stats.Total.P50,
			P95:         stats.Total.P95,
			P99:         stats.Total.P99,
			Max:         stats.Total.Max,
		},
	}
	out.Systems = make([]SystemTiming, len(stats.Systems))
	for i, s := range stats.Systems {
		out.Systems[i] = SystemTiming{
			Name: stats.SystemNames[i],
			Avg:  s.Avg,
			P95:  s.P95,
		}
	}
	if cell.Metrics != nil {
		snap := cell.Metrics.Snapshot()
		out.Entities = snap.Entities
		out.Network = snap.Network
		out.Load = snap.CompositeLoad
		out.OverbudgetPct = snap.Tick.OverbudgetPct
		out.EffectiveHz = snap.Tick.EffectiveHz
	}
	return out
}

