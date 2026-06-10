//go:build wasip1

// Command pulse is a VISIBLE hot-swappable demo system: it pulses every
// entity's collider radius up and down over time, so the circles on the client
// visibly breathe. The replicated `radius` wire field is sourced from
// Collider.Radius (see pkg/system/auto_replicator.go QSize), so mutating it here
// shows up on screen with no client changes.
//
// MinRadius/MaxRadius/Rate are RUNTIME TUNABLES — change them live without a
// rebuild, either from the server console or the admin /tunables sliders:
//
//	tune set pulse rate 1.0            # faster pulse, instantly
//	tune set pulse maxRadius 80        # bigger swell
//	tune reset pulse                   # back to defaults
//
// To change the *logic* (e.g. a different oscillation shape), edit Update,
// then:
//
//	just wasm-build
//	wasm swap pulse                    # hot-reload the code on all cells
//
// The oscillation phase is derived from the host's cluster-coherent clock
// (ctx.TimeSec), NOT a cell-local tick counter. Two consequences fall out for
// free: (1) every cell computes the SAME phase at the same instant, so a
// breathing circle stays smooth when it crosses a cell boundary — no phase
// jump on handoff; and (2) there is no internal state to snapshot, so a
// hot-swap also continues without a visual reset.
package main

import (
	"math"

	comp "github.com/zenion/mmoserver/pkg/component"
	"github.com/zenion/mmoserver/pkg/wasmsys"
)

// pulse drives Collider.Radius from cluster time. All three fields are
// operator-tunable at runtime; the system holds no internal state.
type pulse struct {
	MinRadius float32 `tune:"default=6,min=2,max=80,step=2"`        // smallest circle radius
	MaxRadius float32 `tune:"default=48,min=4,max=120,step=2"`      // largest circle radius
	Rate      float32 `tune:"default=0.5,min=0.05,max=2,step=0.05"` // phase radians per tick (20Hz) — bigger = faster
}

func (s *pulse) Query() wasmsys.Query {
	return wasmsys.ReadWrite[comp.Collider]()
}

func (s *pulse) Update(ctx *wasmsys.Ctx, dt float32) {
	cols := wasmsys.Column[comp.Collider](ctx)
	// osc in [0,1] from cluster-coherent time. Rate is radians-per-tick at the
	// 20Hz sim rate, so the angular velocity is Rate*20 rad/s — multiply by the
	// cluster clock (seconds) to get a phase that is identical on every cell and
	// continuous across a boundary handoff.
	osc := 0.5 * (1 + float32(math.Sin(ctx.TimeSec()*float64(s.Rate)*20)))
	r := s.MinRadius + (s.MaxRadius-s.MinRadius)*osc
	for i := range cols {
		cols[i].Radius = r // Width/Height/Layer/Shape are gathered and written back unchanged
	}
}

func init() { wasmsys.Register(&pulse{}) }
func main() {}
