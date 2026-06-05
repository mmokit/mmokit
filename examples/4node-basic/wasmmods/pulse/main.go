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
// Ticks (the phase counter) is exported + untagged, so the framework
// auto-snapshots it across a swap — the oscillation continues without a visual
// reset, and your tuned values are re-applied automatically. No Snapshot/
// Restore boilerplate needed.
package main

import (
	"math"

	comp "github.com/zenion/mmoserver/pkg/component"
	"github.com/zenion/mmoserver/pkg/wasmsys"
)

// pulse drives Collider.Radius from an internal phase counter. The three
// tune-tagged fields are operator-tunable at runtime; Ticks is exported +
// untagged, so it is the auto-snapshotted state preserved across a hot-swap.
type pulse struct {
	MinRadius float32 `tune:"default=6,min=2,max=80,step=2"`        // smallest circle radius
	MaxRadius float32 `tune:"default=48,min=4,max=120,step=2"`      // largest circle radius
	Rate      float32 `tune:"default=0.5,min=0.05,max=2,step=0.05"` // phase radians per tick (20/s) — bigger = faster
	Ticks     uint64  // internal phase counter — auto-snapshotted across swap
}

func (s *pulse) Query() wasmsys.Query {
	return wasmsys.ReadWrite[comp.Collider]()
}

func (s *pulse) Update(ctx *wasmsys.Ctx, dt float32) {
	cols := wasmsys.Column[comp.Collider](ctx)
	// osc in [0,1] from the phase counter; Ticks (not dt) drives it so the
	// oscillation phase survives a hot-swap unchanged.
	osc := 0.5 * (1 + float32(math.Sin(float64(float32(s.Ticks)*s.Rate))))
	r := s.MinRadius + (s.MaxRadius-s.MinRadius)*osc
	for i := range cols {
		cols[i].Radius = r // Width/Height/Layer/Shape are gathered and written back unchanged
	}
	s.Ticks++
}

func init() { wasmsys.Register(&pulse{}) }
func main() {}
