package universe

import (
	"time"

	"github.com/zenion/mmoserver/pkg/engine"
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

// toText converts a PerfCellSnapshot (wire format) into the minimal text-render
// input that engine.FormatPerfSnapshotText consumes.
func (s PerfCellSnapshot) toText() engine.PerfSnapshotText {
	text := engine.PerfSnapshotText{
		TickHz:   s.TickHz,
		BudgetMS: s.BudgetMS,
		Tick: engine.TimingStats{
			Latest: s.Tick.Latest,
			Avg:    s.Tick.Avg,
			P50:    s.Tick.P50,
			P95:    s.Tick.P95,
			P99:    s.Tick.P99,
			Max:    s.Tick.Max,
		},
		EntitiesReal:      s.Entities.Real,
		EntitiesReplica:   s.Entities.Replica,
		EntitiesGhost:     s.Entities.Ghost,
		EntitiesConnected: s.Entities.Connected,
		Connections:       s.Network.Connections,
		BytesSent:         s.Network.BytesSent,
		BytesRecv:         s.Network.BytesRecv,
		CompositeLoad:     s.Load,
		OverbudgetPct:     s.OverbudgetPct,
		EffectiveHz:       s.EffectiveHz,
	}
	text.SystemNames = make([]string, len(s.Systems))
	text.SystemTimings = make([]engine.TimingStats, len(s.Systems))
	for i, st := range s.Systems {
		text.SystemNames[i] = st.Name
		text.SystemTimings[i] = engine.TimingStats{Avg: st.Avg, P95: st.P95}
	}
	return text
}

// buildPerfCellSnapshot reads live state from a cell and returns a PerfCellSnapshot.
// Must run on the cell's game-loop goroutine (caller's responsibility — use
// engine.RunOnLoop). Tolerates nil Metrics; all read access to Engine.Perf is
// required, so Engine and Engine.Perf must be non-nil.
func buildPerfCellSnapshot(cell *Cell, hostID string) PerfCellSnapshot {
	stats := cell.Engine.Perf.Stats()
	return buildPerfCellSnapshotFromStats(cell, hostID, stats)
}

// buildPerfCellSnapshotFromStats composes the snapshot from a precomputed
// PerfStats. Useful when the caller already has a cached value (e.g. via
// TickProfile.CachedStats) and wants to avoid the on-loop Stats() recompute.
// cell.Metrics.Snapshot() is concurrency-safe (atomic reads), so this can
// run off the game-loop goroutine.
func buildPerfCellSnapshotFromStats(cell *Cell, hostID string, stats engine.PerfStats) PerfCellSnapshot {
	eng := cell.Engine
	budgetMS := 0
	if eng.Config.TickRate > 0 {
		budgetMS = 1000 / eng.Config.TickRate
	}
	out := PerfCellSnapshot{
		HostID:   hostID,
		CellID:   string(cell.MeshID()),
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

