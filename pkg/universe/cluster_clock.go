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
}

// emaAlpha controls how fast the offset EMA tracks a new observation.
// At 10 s broadcast cadence, alpha=0.3 settles within ~3 samples
// (30 s) after a step-change in network latency. First observation
// snaps directly regardless of alpha.
const emaAlpha = 0.3

// NewClusterClock returns a fresh, un-initialized cluster clock.
func NewClusterClock() *ClusterClock {
	return &ClusterClock{}
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
	return c.nowAt(uint64(time.Now().UnixMilli()))
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

// Observe incorporates a CoordTimeSync broadcast. Stale (older seq)
// broadcasts are silently dropped.
func (c *ClusterClock) Observe(coordTimeMs uint64, seq uint64) {
	c.observeAt(coordTimeMs, seq, uint64(time.Now().UnixMilli()))
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
