package metrics

import (
	"sync/atomic"
	"time"
)

// CellMetrics collects per-node metrics on the tick hot path.
//
// Write methods (RecordTick) are called from the game loop goroutine.
// Byte counter methods (AddBytesSent/AddBytesRecv) are called from transport
// goroutines via lock-free atomics.
// Read methods (Snapshot) allocate and are intended for low-frequency scraping.
type CellMetrics struct {
	cellID     atomic.Pointer[string]
	tickBudget time.Duration

	// Entity counts — set each tick from game loop.
	realEntities    Gauge
	replicaEntities Gauge
	ghostEntities   Gauge
	connected       Gauge

	// Network I/O — atomics, safe from any goroutine.
	bytesSent   Counter
	bytesRecv   Counter
	connections Gauge

	// Inter-node channel traffic — atomics, tracked independently from
	// WebSocket I/O because mesh messaging goes through Go channels and
	// doesn't hit the transport-layer byte counters.
	interNodeBytesSent Counter
	interNodeBytesRecv Counter
	borderFramesSent   Counter
	borderFramesRecv   Counter

	// Client-prediction input-ack loop. Observable in production, not only
	// in the browser dev overlay. Plain counters, no per-player labels — the
	// cardinality has to stay bounded.
	inputAckFramesSent     Counter
	inputSequencesRejected Counter

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

// NewCellMetrics creates a per-node metrics collector.
//
// tickStatsFn returns tick profiling stats (from TickProfile.Stats()).
// networkStatsFn returns cumulative bytes sent/recv and connection count.
// Both callbacks are called only on Snapshot() — not on every tick.
func NewCellMetrics(
	cellID string,
	tickRate int,
	tickStatsFn func() TickStats,
	networkStatsFn func() (bytesSent, bytesRecv uint64, connCount int),
) *CellMetrics {
	budget := time.Duration(1000/tickRate) * time.Millisecond
	nm := &CellMetrics{
		tickBudget:     budget,
		loadEWMA:       NewEWMA(0.1),
		tickRateEWMA:   NewEWMA(0.1),
		tickStatsFn:    tickStatsFn,
		networkStatsFn: networkStatsFn,
	}
	nm.SetCellID(cellID)
	return nm
}

// RecordTick is called once per tick from the game loop goroutine.
// Zero-alloc on the hot path.
func (nm *CellMetrics) RecordTick(tickDuration time.Duration, realCount, replicaCount, ghostCount, connectedCount int) {
	// Update entity gauges.
	nm.realEntities.Set(int64(realCount))
	nm.replicaEntities.Set(int64(replicaCount))
	nm.ghostEntities.Set(int64(ghostCount))
	nm.connected.Set(int64(connectedCount))

	// Track overbudget ticks.
	nm.totalTicks.Add(1)
	if tickDuration > nm.tickBudget {
		nm.overbudget.Add(1)
	}

	// CompositeLoad = tick-time saturation. 1.0 = exactly on budget,
	// >1.0 = overloaded. Tick time is the authoritative saturation
	// signal — as entity count grows, per-system cost grows with it.
	tickLoad := float64(tickDuration) / float64(nm.tickBudget)
	nm.loadEWMA.Update(tickLoad)

	// Track effective tick rate (Hz).
	nm.tickRateEWMA.Update(float64(tickDuration))
}

// AddBytesSent records bytes sent (called from transport goroutines).
func (nm *CellMetrics) AddBytesSent(n int) { nm.bytesSent.Add(uint64(n)) }

// AddBytesRecv records bytes received (called from transport goroutines).
func (nm *CellMetrics) AddBytesRecv(n int) { nm.bytesRecv.Add(uint64(n)) }

// RecordBorderFrameSent is called by NodeViewer.Send once per encoded
// MsgBorderFrame handed to a neighbor's inbox. The byte count is the
// encoded frame size.
func (nm *CellMetrics) RecordBorderFrameSent(bytes int) {
	if nm == nil {
		return
	}
	nm.borderFramesSent.Add(1)
	nm.interNodeBytesSent.Add(uint64(bytes))
}

// RecordBorderFrameRecv is called by Node.processMessage on receiving a
// MsgBorderFrame, after the payload is decoded successfully.
func (nm *CellMetrics) RecordBorderFrameRecv(bytes int) {
	if nm == nil {
		return
	}
	nm.borderFramesRecv.Add(1)
	nm.interNodeBytesRecv.Add(uint64(bytes))
}

// RecordInputAckFrame is called once per replication frame that carries the
// four-byte processed-input-sequence trailer. Together with the rejection
// counter below it makes the client-prediction ACK loop visible in
// production; before this it was observable only in the browser dev overlay.
func (nm *CellMetrics) RecordInputAckFrame() {
	if nm == nil {
		return
	}
	nm.inputAckFramesSent.Add(1)
}

// RecordInputSequenceRejected is called when a client movement command is
// dropped as stale or duplicate. A sustained non-zero rate means clients are
// replaying or reordering input — the symptom that precedes rubber-banding.
func (nm *CellMetrics) RecordInputSequenceRejected() {
	if nm == nil {
		return
	}
	nm.inputSequencesRejected.Add(1)
}

// InputAckSnapshot returns the client-prediction ACK counters as a single
// read-consistent view.
func (nm *CellMetrics) InputAckSnapshot() InputAckSnapshot {
	if nm == nil {
		return InputAckSnapshot{}
	}
	return InputAckSnapshot{
		FramesWithInputAck: nm.inputAckFramesSent.Load(),
		SequencesRejected:  nm.inputSequencesRejected.Load(),
	}
}

// InterNodeSnapshot returns the current inter-node traffic counters as a
// single read-consistent view. Intended for the perf console and
// integration tests.
func (nm *CellMetrics) InterNodeSnapshot() InterNodeSnapshot {
	return InterNodeSnapshot{
		BytesSent:        nm.interNodeBytesSent.Load(),
		BytesRecv:        nm.interNodeBytesRecv.Load(),
		BorderFramesSent: nm.borderFramesSent.Load(),
		BorderFramesRecv: nm.borderFramesRecv.Load(),
	}
}

// Snapshot returns a read-consistent LoadSnapshot. Allocates on read —
// acceptable for scrape intervals (0.1-15 Hz).
func (nm *CellMetrics) Snapshot() LoadSnapshot {
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
		CellID: nm.CellID(),
		Tick:   tick,
		Entities: EntitySnapshot{
			Real:      int(nm.realEntities.Load()),
			Replica:   int(nm.replicaEntities.Load()),
			Ghost:     int(nm.ghostEntities.Load()),
			Connected: int(nm.connected.Load()),
		},
		Network:       net,
		CompositeLoad: nm.loadEWMA.Value(),
		Timestamp:     time.Now(),
	}
}

// TickStatsSnapshot returns detailed per-system tick timing.
// Used by the console perf command for the full breakdown.
func (nm *CellMetrics) TickStatsSnapshot() TickStats {
	if nm.tickStatsFn != nil {
		return nm.tickStatsFn()
	}
	return TickStats{}
}

// CellID returns this metric collector's node identifier.
func (nm *CellMetrics) CellID() string {
	id := nm.cellID.Load()
	if id == nil {
		return ""
	}
	return *id
}

// SetCellID updates the node's identifier (used during cell split/merge).
func (nm *CellMetrics) SetCellID(id string) { nm.cellID.Store(&id) }
