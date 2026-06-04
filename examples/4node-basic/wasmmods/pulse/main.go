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
	minRadius float32 = 6   // smallest circle radius
	maxRadius float32 = 48  // largest circle radius
	rate      float32 = 0.5 // phase radians added per tick (20 ticks/s) — bigger = faster pulse
)

// pulse drives Collider.Radius from an internal phase counter. ticks is the
// preserved-across-swap internal state.
type pulse struct{ ticks uint64 }

func (s *pulse) Query() wasmsys.Query {
	return wasmsys.ReadWrite[comp.Collider]()
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

func (s *pulse) Snapshot() []byte { return wasmsys.MarshalState(s.ticks) }
func (s *pulse) Restore(b []byte) { wasmsys.UnmarshalState(b, &s.ticks) }

func init() { wasmsys.Register(&pulse{}) }
func main() {}
