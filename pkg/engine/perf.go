package engine

import (
	"sort"
	"sync/atomic"
	"time"
)

const perfBufferSize = 1000

// statsCacheTTL is how long a TickProfile.Stats() result stays cached
// before another call recomputes. Set by the dashboard's polling cadence:
// at 1Hz polling we want the first call per second to recompute and the
// rest to hit the cache. Recompute is ~5-10ms on a busy cell with many
// systems, so without the cache every poll lands on the loop and trips
// the slow-admin-job warning.
const statsCacheTTL = time.Second

// TickSample stores timing data for a single tick.
type TickSample struct {
	Systems []time.Duration
	Total   time.Duration
}

// TimingStats holds computed statistics for a single metric.
type TimingStats struct {
	Latest time.Duration
	Avg    time.Duration
	P50    time.Duration
	P95    time.Duration
	P99    time.Duration
	Max    time.Duration
}

// PerfStats holds computed statistics for the full tick and each system.
type PerfStats struct {
	SystemNames []string
	Systems     []TimingStats
	Total       TimingStats
	SampleCount int
}

// TickProfile collects per-tick timing samples in a ring buffer.
type TickProfile struct {
	systemNames []string
	samples     [perfBufferSize]TickSample
	head        int // next write index
	count       int // number of valid samples (up to perfBufferSize)

	// cachedStats holds the last Stats() result so off-loop callers (the
	// admin dashboard) can poll without paying the sort cost on every read.
	// Loaded lock-free; stored only by Stats() itself, which is called on
	// the loop goroutine.
	cachedStats   atomic.Pointer[PerfStats]
	cachedStatsAt atomic.Int64 // unix nano, paired with cachedStats
}

// NewTickProfile creates a new profiler for the given system names.
func NewTickProfile(systemNames []string) *TickProfile {
	tp := &TickProfile{
		systemNames: systemNames,
	}
	// Pre-allocate system slices in each sample slot
	for i := range tp.samples {
		tp.samples[i].Systems = make([]time.Duration, len(systemNames))
	}
	return tp
}

// Record stores a tick's timing data.
func (tp *TickProfile) Record(systemTimings []time.Duration, total time.Duration) {
	s := &tp.samples[tp.head]
	copy(s.Systems, systemTimings)
	s.Total = total
	tp.head = (tp.head + 1) % perfBufferSize
	if tp.count < perfBufferSize {
		tp.count++
	}
}

// Reset clears all collected samples.
func (tp *TickProfile) Reset() {
	tp.head = 0
	tp.count = 0
	tp.cachedStats.Store(nil)
	tp.cachedStatsAt.Store(0)
}

// CachedStats returns the most recent Stats() result if it is still within
// statsCacheTTL; otherwise nil. Safe to call from any goroutine — the cache
// is loaded lock-free. The on-loop Stats() path is the only writer.
func (tp *TickProfile) CachedStats() *PerfStats {
	p := tp.cachedStats.Load()
	if p == nil {
		return nil
	}
	if time.Since(time.Unix(0, tp.cachedStatsAt.Load())) > statsCacheTTL {
		return nil
	}
	return p
}

// Stats computes percentile statistics from collected samples.
func (tp *TickProfile) Stats() PerfStats {
	n := tp.count
	if n == 0 {
		return PerfStats{SystemNames: tp.systemNames}
	}

	numSys := len(tp.systemNames)
	stats := PerfStats{
		SystemNames: tp.systemNames,
		Systems:     make([]TimingStats, numSys),
		SampleCount: n,
	}

	// Collect all total durations and per-system durations
	totals := make([]time.Duration, n)
	sysBuffers := make([][]time.Duration, numSys)
	for i := range sysBuffers {
		sysBuffers[i] = make([]time.Duration, n)
	}

	// Latest sample is the one before head
	latestIdx := (tp.head - 1 + perfBufferSize) % perfBufferSize

	for i := 0; i < n; i++ {
		idx := (tp.head - n + i + perfBufferSize) % perfBufferSize
		totals[i] = tp.samples[idx].Total
		for s := 0; s < numSys; s++ {
			sysBuffers[s][i] = tp.samples[idx].Systems[s]
		}
	}

	stats.Total = computeStats(totals, tp.samples[latestIdx].Total)
	for s := 0; s < numSys; s++ {
		stats.Systems[s] = computeStats(sysBuffers[s], tp.samples[latestIdx].Systems[s])
	}

	// Cache so subsequent CachedStats() calls within statsCacheTTL skip the
	// sort. Caller is expected to be the loop goroutine, so the store sees
	// no concurrent writes from Record.
	cached := stats
	tp.cachedStats.Store(&cached)
	tp.cachedStatsAt.Store(time.Now().UnixNano())
	return stats
}

func computeStats(durations []time.Duration, latest time.Duration) TimingStats {
	n := len(durations)
	if n == 0 {
		return TimingStats{}
	}

	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })

	var sum time.Duration
	for _, d := range durations {
		sum += d
	}

	return TimingStats{
		Latest: latest,
		Avg:    sum / time.Duration(n),
		P50:    durations[percentileIndex(n, 50)],
		P95:    durations[percentileIndex(n, 95)],
		P99:    durations[percentileIndex(n, 99)],
		Max:    durations[n-1],
	}
}

func percentileIndex(n, p int) int {
	idx := (n*p - 1) / 100
	if idx < 0 {
		return 0
	}
	return idx
}
