package metrics

import "time"

// LoadSnapshot is a point-in-time health report for a single cell.
// Used by the Process for cluster-wide views and by Feature #7
// (dynamic partitioning) to make rebalancing decisions.
type LoadSnapshot struct {
	CellID        string
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
	Real      int
	Replica   int
	Ghost     int
	Connected int
}

// NetworkSnapshot captures bandwidth and connection metrics.
type NetworkSnapshot struct {
	Connections int
	BytesSent   uint64 // cumulative
	BytesRecv   uint64 // cumulative
}

// InterNodeSnapshot captures per-node inter-node channel traffic from
// the new tiered-push border replication path. Tracked independently
// from NetworkSnapshot because mesh messaging goes through Go channels,
// not WebSocket, and doesn't hit the transport-layer byte counters.
type InterNodeSnapshot struct {
	BytesSent        uint64 // cumulative encoded border frame bytes sent to neighbors
	BytesRecv        uint64 // cumulative encoded border frame bytes received from neighbors
	BorderFramesSent uint64 // count of MsgBorderFrame envelopes sent
	BorderFramesRecv uint64 // count of MsgBorderFrame envelopes received
}

// InputAckSnapshot captures the client-prediction input-acknowledgement loop.
// Counters are deliberately unlabelled: a per-player label would make
// cardinality scale with the player count.
type InputAckSnapshot struct {
	FramesWithInputAck uint64 // replication frames carrying the processed-input trailer
	SequencesRejected  uint64 // client movement commands dropped as stale or duplicate
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
