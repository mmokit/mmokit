// Package metrics provides zero-allocation metric primitives and per-node
// observability for the mmokit game engine. It has no dependencies on other
// pkg/ packages — all engine integration is done via callbacks wired by the
// universe layer.
package metrics

import (
	"math"
	"sync/atomic"
)

// Counter is a monotonically increasing uint64 counter.
// All methods are goroutine-safe (lock-free atomics).
type Counter struct {
	val atomic.Uint64
}

// Add increments the counter by n.
func (c *Counter) Add(n uint64) { c.val.Add(n) }

// Load returns the current value.
func (c *Counter) Load() uint64 { return c.val.Load() }

// Reset zeroes the counter and returns the previous value.
func (c *Counter) Reset() uint64 { return c.val.Swap(0) }

// Gauge is an int64 value that can go up or down.
// All methods are goroutine-safe (lock-free atomics).
type Gauge struct {
	val atomic.Int64
}

// Set overwrites the gauge value.
func (g *Gauge) Set(v int64) { g.val.Store(v) }

// Add increments (or decrements) the gauge.
func (g *Gauge) Add(delta int64) { g.val.Add(delta) }

// Load returns the current value.
func (g *Gauge) Load() int64 { return g.val.Load() }

// EWMA computes an exponentially weighted moving average.
// NOT goroutine-safe — intended for single-goroutine use on the tick loop.
type EWMA struct {
	value float64
	alpha float64
	init  bool
}

// NewEWMA creates an EWMA with the given smoothing factor (0 < alpha <= 1).
// Lower alpha = smoother / slower response. Typical: 0.1 for load scores.
func NewEWMA(alpha float64) *EWMA {
	return &EWMA{alpha: math.Max(0.001, math.Min(1.0, alpha))}
}

// Update adds a new sample to the moving average.
func (e *EWMA) Update(sample float64) {
	if !e.init {
		e.value = sample
		e.init = true
		return
	}
	e.value = e.alpha*sample + (1-e.alpha)*e.value
}

// Value returns the current smoothed value.
func (e *EWMA) Value() float64 { return e.value }
