//go:build wasip1

// Command tint is a VISIBLE hot-swappable demo system: it animates every
// entity's replicated color (comp.Tint, the `r`/`g`/`b` wire fields), so the
// circles on the client visibly change hue. The web renderer fills each circle
// from its tint, so mutating it here shows up on screen with no client changes.
//
// Rate/Spread are RUNTIME TUNABLES — change them live without a rebuild,
// either from the server console or the admin /tunables sliders:
//
//	tune set tint rate 1.0             # faster color cycling, instantly
//	tune set tint spread 0.2           # wider rainbow in variant B
//	tune reset tint                    # back to defaults
//
// To change the *logic* (a different color effect), edit Update, then:
//
//	just wasm-build
//	wasm swap tint                     # hot-reload the code on all cells
//
// Update ships three ready-made effects (rainbow sweep, marching rainbow,
// police strobe) — toggle the comments to swap which one is live, rebuild,
// and `wasm swap tint` to demo hot reloading.
//
// The animation phase is derived from the host's cluster-coherent clock
// (ctx.TimeSec), NOT a cell-local tick counter. Two consequences fall out for
// free: (1) every cell computes the SAME phase at the same instant, so a
// color-cycling circle stays smooth when it crosses a cell boundary — no hue
// jump on handoff; and (2) there is no internal state to snapshot, so a
// hot-swap also continues without a visual reset.
package main

import (
	"math"

	comp "github.com/zenion/mmoserver/pkg/component"
	"github.com/zenion/mmoserver/pkg/wasmsys"
)

// tint drives comp.Tint from cluster time. Both fields are operator-tunable
// at runtime; the system holds no internal state.
type tint struct {
	Rate   float32 `tune:"default=0.2,min=0.02,max=2,step=0.02"` // phase radians per tick (20Hz) — bigger = faster
	Spread float32 `tune:"default=0.04,min=0,max=0.5,step=0.01"` // per-entity hue offset (variant B only)
}

func (s *tint) Query() wasmsys.Query {
	return wasmsys.ReadWrite[comp.Tint]()
}

func (s *tint) Update(ctx *wasmsys.Ctx, dt float32) {
	tints := wasmsys.Column[comp.Tint](ctx)

	// phase advances with cluster-coherent time. Rate is radians-per-tick at
	// the 20Hz sim rate, so the angular velocity is Rate*20 rad/s — multiply by
	// the cluster clock (seconds) to get a phase that is identical on every
	// cell and continuous across a boundary handoff. cycle is the same thing
	// wrapped to [0,1) — one full trip around the color wheel per 2π of phase.
	// All three variants below share it, so swapping variants never causes a
	// phase jump either.
	phase := ctx.TimeSec() * float64(s.Rate) * 20
	cycle := math.Mod(phase/(2*math.Pi), 1)

	// HOT-SWAP DEMO: exactly one variant below must be active. Toggle the
	// comments, then `just wasm-build` + `wasm swap tint` to watch the colors
	// change live on every cell, mid-game, with no server restart.

	// ── Variant A: rainbow sweep — the whole world cycles hue in unison
	for i := range tints {
		tints[i] = hsv(float32(cycle), 1, 1)
	}

	// ── Variant B: marching rainbow — each entity offset along the wheel
	// for i := range tints {
	// 	hue := float32(math.Mod(cycle+float64(i)*float64(s.Spread), 1))
	// 	tints[i] = hsv(hue, 1, 1)
	// }

	// ── Variant C: police strobe — everything snaps red/blue each half-cycle
	// c := comp.Tint{R: 255, G: 30, B: 30}
	// if cycle >= 0.5 {
	// 	c = comp.Tint{R: 40, G: 90, B: 255}
	// }
	// for i := range tints {
	// 	tints[i] = c
	// }
}

// hsv converts hue/saturation/value in [0,1] to an 8-bit RGB Tint.
func hsv(h, s, v float32) comp.Tint {
	i := int(h*6) % 6
	f := h*6 - float32(int(h*6))
	p := v * (1 - s)
	q := v * (1 - f*s)
	t := v * (1 - (1-f)*s)
	var r, g, b float32
	switch i {
	case 0:
		r, g, b = v, t, p
	case 1:
		r, g, b = q, v, p
	case 2:
		r, g, b = p, v, t
	case 3:
		r, g, b = p, q, v
	case 4:
		r, g, b = t, p, v
	default:
		r, g, b = v, p, q
	}
	return comp.Tint{R: uint8(r * 255), G: uint8(g * 255), B: uint8(b * 255)}
}

func init() { wasmsys.Register(&tint{}) }
func main() {}
