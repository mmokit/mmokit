package metrics

import "time"

// LoadSnapshot is a point-in-time health report for a single node.
// Used by the Coordinator for cluster-wide views and by Feature #7
// (dynamic partitioning) to make rebalancing decisions.
type LoadSnapshot struct {
	NodeID        string
	Tick          TickHealthSnapshot
	Entities      EntitySnapshot
	Network       NetworkSnapshot
	CompositeLoad float64 // 0.0 = idle, 1.0 = at budget, >1.0 = overloaded
	Timestamp     time.Time
}

// TickHealthSnapshot captures tick timing metrics.
type TickHealthSnapshot struct {
	AvgDuration   time.Duration
	P95Duration   time.Duration
	P99Duration   time.Duration
	MaxDuration   time.Duration
	EffectiveHz   float64 // actual tick rate
	OverbudgetPct float64 // fraction of recent ticks exceeding budget (0.0-1.0)
}

// EntitySnapshot breaks down entity counts per node.
type EntitySnapshot struct {
	Real    int
	Replica int
	Ghost   int
	Players int
}

// NetworkSnapshot captures bandwidth and connection metrics.
type NetworkSnapshot struct {
	Connections int
	BytesSent   uint64 // cumulative
	BytesRecv   uint64 // cumulative
}

// TimingStats holds computed percentile statistics for a single metric.
// Mirrors engine.TimingStats so pkg/metrics stays dependency-free.
type TimingStats struct {
	Latest time.Duration
	Avg    time.Duration
	P50    time.Duration
	P95    time.Duration
	P99    time.Duration
	Max    time.Duration
}

// TickStats holds computed tick profiling statistics.
// Populated by a callback injected at wiring time (avoids importing engine).
type TickStats struct {
	SystemNames []string
	Systems     []TimingStats
	Total       TimingStats
	SampleCount int
}
