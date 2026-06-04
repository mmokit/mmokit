//go:build wasip1

// Command pulse is a VISIBLE hot-swappable demo system: it pulses every
// entity's collider radius up and down over time, so the circles on the client
// visibly breathe. The replicated `radius` wire field is sourced from
// Collider.Radius (see pkg/system/auto_replicator.go QSize), so mutating it here
// shows up on screen with no client changes.
//
// EDIT THE CONSTANTS BELOW, then:
//
//	just wasm-build
//	# in the server console:
//	wasm swap 0_0 pulse
//
// and watch the pulse change live — the phase counter is preserved across the
// swap (snapshot/restore), so the new rate/size takes effect without a visual
// reset.
package main

import (
	"math"

	comp "github.com/zenion/mmoserver/pkg/component"
	"github.com/zenion/mmoserver/pkg/wasmsys"
)

// ─── tunables: edit, `just wasm-build`, then `wasm swap <cell> pulse` ───────────
const (
	minRadius float32 = 6    // smallest circle radius
	maxRadius float32 = 48   // largest circle radius
	rate      float32 = 0.06 // phase radians added per tick (20 ticks/s) — bigger = faster pulse
)

// colliderTypeID is informational in Phase 0 (the Go type drives gather size);
// it just has to be a stable nonzero id distinct from other demo modules.
const colliderTypeID uint32 = 3

// pulse drives Collider.Radius from an internal phase counter. ticks is the
// preserved-across-swap internal state.
type pulse struct{ ticks uint64 }

func (s *pulse) Query() wasmsys.Query {
	return wasmsys.ReadWrite[comp.Collider](colliderTypeID)
}

func (s *pulse) Update(ctx *wasmsys.Ctx, dt float32) {
	cols := wasmsys.Column[comp.Collider](ctx)
	// osc in [0,1] from the phase counter; ticks (not dt) drives it so the
	// oscillation phase survives a hot-swap unchanged.
	osc := 0.5 * (1 + float32(math.Sin(float64(float32(s.ticks)*rate))))
	r := minRadius + (maxRadius-minRadius)*osc
	for i := range cols {
		cols[i].Radius = r // Width/Height/Layer/Shape are gathered and written back unchanged
	}
	s.ticks++
}

func (s *pulse) Snapshot() []byte {
	b := make([]byte, 8)
	for i := 0; i < 8; i++ {
		b[i] = byte(s.ticks >> (8 * i))
	}
	return b
}

func (s *pulse) Restore(b []byte) {
	if len(b) == 8 {
		var v uint64
		for i := 0; i < 8; i++ {
			v |= uint64(b[i]) << (8 * i)
		}
		s.ticks = v
	}
}

func init() { wasmsys.Register(&pulse{}) }
func main() {}
