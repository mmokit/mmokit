package metrics

import "time"

// DefaultEntityCap is the default entity capacity used to normalize entity
// load in the composite score. Override with NodeMetricsOption.
const DefaultEntityCap = 1000

// NodeMetrics collects per-node metrics on the tick hot path.
//
// Write methods (RecordTick) are called from the game loop goroutine.
// Byte counter methods (AddBytesSent/AddBytesRecv) are called from transport
// goroutines via lock-free atomics.
// Read methods (Snapshot) allocate and are intended for low-frequency scraping.
type NodeMetrics struct {
	nodeID     string
	tickBudget time.Duration
	entityCap  float64

	// Entity counts — set each tick from game loop.
	realEntities    Gauge
	replicaEntities Gauge
	ghostEntities   Gauge
	players         Gauge

	// Network I/O — atomics, safe from any goroutine.
	bytesSent   Counter
	bytesRecv   Counter
	connections Gauge

	// Composite load score — EWMA-smoothed, game loop only.
	loadEWMA     *EWMA
	tickRateEWMA *EWMA

	// Overbudget tick counter (cumulative).
	overbudget Counter
	// Total tick counter for overbudget percentage.
	totalTicks Counter

	// Callbacks injected at construction time (avoids circular imports).
	tickStatsFn    func() TickStats
	networkStatsFn func() (bytesSent, bytesRecv uint64, connCount int)
}

// NodeMetricsOption configures optional NodeMetrics settings.
type NodeMetricsOption func(*NodeMetrics)

// WithEntityCap overrides the default entity capacity used to normalize
// entity load in the composite score.
func WithEntityCap(cap int) NodeMetricsOption {
	return func(nm *NodeMetrics) {
		if cap > 0 {
			nm.entityCap = float64(cap)
		}
	}
}

// NewNodeMetrics creates a per-node metrics collector.
//
// tickStatsFn returns tick profiling stats (from TickProfile.Stats()).
// networkStatsFn returns cumulative bytes sent/recv and connection count.
// Both callbacks are called only on Snapshot() — not on every tick.
func NewNodeMetrics(
	nodeID string,
	tickRate int,
	tickStatsFn func() TickStats,
	networkStatsFn func() (bytesSent, bytesRecv uint64, connCount int),
	opts ...NodeMetricsOption,
) *NodeMetrics {
	budget := time.Duration(1000/tickRate) * time.Millisecond
	nm := &NodeMetrics{
		nodeID:         nodeID,
		tickBudget:     budget,
		entityCap:      DefaultEntityCap,
		loadEWMA:       NewEWMA(0.1),
		tickRateEWMA:   NewEWMA(0.1),
		tickStatsFn:    tickStatsFn,
		networkStatsFn: networkStatsFn,
	}
	for _, opt := range opts {
		opt(nm)
	}
	return nm
}

// RecordTick is called once per tick from the game loop goroutine.
// Zero-alloc on the hot path.
func (nm *NodeMetrics) RecordTick(tickDuration time.Duration, realCount, replicaCount, ghostCount, playerCount int) {
	// Update entity gauges.
	nm.realEntities.Set(int64(realCount))
	nm.replicaEntities.Set(int64(replicaCount))
	nm.ghostEntities.Set(int64(ghostCount))
	nm.players.Set(int64(playerCount))

	// Track overbudget ticks.
	nm.totalTicks.Add(1)
	if tickDuration > nm.tickBudget {
		nm.overbudget.Add(1)
	}

	// Compute composite load score.
	// tickLoad: 1.0 = exactly on budget, >1.0 = overloaded.
	tickLoad := float64(tickDuration) / float64(nm.tickBudget)
	// entityLoad: fraction of entity capacity used.
	entityLoad := float64(realCount) / nm.entityCap
	composite := 0.7*tickLoad + 0.3*entityLoad
	nm.loadEWMA.Update(composite)

	// Track effective tick rate (Hz).
	nm.tickRateEWMA.Update(float64(tickDuration))
}

// AddBytesSent records bytes sent (called from transport goroutines).
func (nm *NodeMetrics) AddBytesSent(n int) { nm.bytesSent.Add(uint64(n)) }

// AddBytesRecv records bytes received (called from transport goroutines).
func (nm *NodeMetrics) AddBytesRecv(n int) { nm.bytesRecv.Add(uint64(n)) }

// Snapshot returns a read-consistent LoadSnapshot. Allocates on read —
// acceptable for scrape intervals (0.1-15 Hz).
func (nm *NodeMetrics) Snapshot() LoadSnapshot {
	var tick TickHealthSnapshot
	if nm.tickStatsFn != nil {
		ts := nm.tickStatsFn()
		tick.AvgDuration = ts.Total.Avg
		tick.P95Duration = ts.Total.P95
		tick.P99Duration = ts.Total.P99
		tick.MaxDuration = ts.Total.Max
	}

	// Effective tick rate derived from EWMA of tick duration.
	avgTickNs := nm.tickRateEWMA.Value()
	if avgTickNs > 0 {
		tick.EffectiveHz = float64(time.Second) / avgTickNs
	}

	total := nm.totalTicks.Load()
	if total > 0 {
		tick.OverbudgetPct = float64(nm.overbudget.Load()) / float64(total)
	}

	// Network stats from callback (reads ConnManager state).
	var net NetworkSnapshot
	if nm.networkStatsFn != nil {
		sent, recv, conns := nm.networkStatsFn()
		net.BytesSent = sent
		net.BytesRecv = recv
		net.Connections = conns
	}

	return LoadSnapshot{
		NodeID: nm.nodeID,
		Tick:   tick,
		Entities: EntitySnapshot{
			Real:    int(nm.realEntities.Load()),
			Replica: int(nm.replicaEntities.Load()),
			Ghost:   int(nm.ghostEntities.Load()),
			Players: int(nm.players.Load()),
		},
		Network:       net,
		CompositeLoad: nm.loadEWMA.Value(),
		Timestamp:     time.Now(),
	}
}

// TickStatsSnapshot returns detailed per-system tick timing.
// Used by the console perf command for the full breakdown.
func (nm *NodeMetrics) TickStatsSnapshot() TickStats {
	if nm.tickStatsFn != nil {
		return nm.tickStatsFn()
	}
	return TickStats{}
}

// NodeID returns this metric collector's node identifier.
func (nm *NodeMetrics) NodeID() string { return nm.nodeID }
