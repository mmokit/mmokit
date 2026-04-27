package universe

import (
	"sync"
	"time"
)

// ClusterClock maintains an offset between this host's local wall clock
// and the coordinator's wall clock, seeded by the RegisterHost handshake
// and refreshed periodically via CoordTimeSync broadcasts over the
// MeshControl stream.
//
// Thread-safe. Now() is called from every cell's game-loop goroutine
// once per replication frame (up to 20 Hz * N cells) so the hot path
// takes the RLock; Observe() runs at 10 s cadence under the write lock.
//
// Pre-observation, Now() falls back to the local wall clock. Callers
// that require cluster coherence MUST gate on Observed() before acting
// on a Now() value. The coordinator's initial-sync on RegisterHost is
// the canonical trigger for Observed() flipping to true; no cell on a
// remote host starts its game loop until that flip happens.
type ClusterClock struct {
	mu          sync.RWMutex
	offsetMs    float64 // coordWall - localWall, EMA-smoothed
	initialized bool
	highestSeq  uint64

	// nowFn returns the current local wall clock in milliseconds. Defaults
	// to time.Now().UnixMilli; tests can override via SetNowFn to drive
	// the clock deterministically.
	nowFn func() uint64
}

// emaAlpha controls how fast the offset EMA tracks a new observation.
// At 10 s broadcast cadence, alpha=0.3 settles within ~3 samples
// (30 s) after a step-change in network latency. First observation
// snaps directly regardless of alpha.
const emaAlpha = 0.3

// NewClusterClock returns a fresh, un-initialized cluster clock.
func NewClusterClock() *ClusterClock {
	return &ClusterClock{
		nowFn: func() uint64 { return uint64(time.Now().UnixMilli()) },
	}
}

// SetNowFn replaces the local-wall-clock source used by Now() and
// Observe(). Test-only — production callers leave the default in place.
// Must be called before any goroutine reads from the clock.
func (c *ClusterClock) SetNowFn(fn func() uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nowFn = fn
}

// Observed reports whether the clock has received at least one
// CoordTimeSync and is safe to use for cluster-coherent stamping.
func (c *ClusterClock) Observed() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.initialized
}

// Now returns the current cluster-wall-clock in milliseconds. Before
// the first Observe, falls back to this host's local wall clock.
func (c *ClusterClock) Now() uint64 {
	return c.nowAt(c.localNowMs())
}

// localNowMs returns the local-wall-clock reading via the configured
// nowFn (defaults to time.Now().UnixMilli; tests can override).
func (c *ClusterClock) localNowMs() uint64 {
	c.mu.RLock()
	fn := c.nowFn
	c.mu.RUnlock()
	if fn == nil {
		return uint64(time.Now().UnixMilli())
	}
	return fn()
}

// ClusterTick returns the current cluster-coherent tick index: the
// cluster wall clock quantized by tickIntervalMs. Both ends of a
// handoff derive the same CommitTick from their shared ClusterClock
// so the hard-cut protocol can commute across asynchronously-ticking
// cells.
//
// A zero tickIntervalMs returns 0 — the caller should prevent this by
// reading from engine.Config.TickRate.
func (c *ClusterClock) ClusterTick(tickIntervalMs uint64) uint64 {
	if tickIntervalMs == 0 {
		return 0
	}
	return c.Now() / tickIntervalMs
}

// TickTime returns the cluster-coherent wall clock quantized down to
// the nearest tick boundary: ClusterTick(tickIntervalMs) * tickIntervalMs.
// This is the canonical stamp for client-visible replication samples —
// it represents the logical simulation time a sample encodes, not when
// a given cell's tick physically fired inside its tick window.
//
// The payoff at the authority seam: a sample produced on cell A at its
// tick N and a sample produced on cell B at its tick N+1 land exactly
// tickIntervalMs apart on the client's timeline regardless of cell
// tick stagger. The client's snapshot interpolator sees speed-continuous
// motion across handoff because the stamp gap matches the one physics
// tick of entity advance — no wall-clock stagger bleeding in.
//
// Wall-clock Now() is still the right choice for operator-facing
// diagnostics (heartbeat ages, cooldown timers). TickTime is
// specifically for the producedAtMs stamp on outbound replication.
func (c *ClusterClock) TickTime(tickIntervalMs uint64) uint64 {
	return c.ClusterTick(tickIntervalMs) * tickIntervalMs
}

// Observe incorporates a CoordTimeSync broadcast. Stale (older seq)
// broadcasts are silently dropped.
func (c *ClusterClock) Observe(coordTimeMs uint64, seq uint64) {
	c.observeAt(coordTimeMs, seq, c.localNowMs())
}

// nowAt is the pure function used for deterministic testing.
func (c *ClusterClock) nowAt(localMs uint64) uint64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if !c.initialized {
		return localMs
	}
	return uint64(float64(localMs) + c.offsetMs)
}

// observeAt is the pure function used for deterministic testing.
// Locks under write lock; drops stale seq.
func (c *ClusterClock) observeAt(coordTimeMs, seq, localMs uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.initialized && seq <= c.highestSeq {
		return
	}
	sample := float64(coordTimeMs) - float64(localMs)
	if !c.initialized {
		c.offsetMs = sample
		c.initialized = true
	} else {
		c.offsetMs = (1.0-emaAlpha)*c.offsetMs + emaAlpha*sample
	}
	c.highestSeq = seq
}
