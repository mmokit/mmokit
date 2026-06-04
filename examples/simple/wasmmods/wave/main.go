//go:build wasip1

// Command wave is the simple example's hot-swappable GAME LOGIC: it computes the
// vertical motion of the whole field of dots. The native side only spawns the
// dots and broadcasts their positions — the *movement* lives here, in a module
// you can rebuild and hot-swap into a running server.
//
// The phase accumulator is preserved across a swap (snapshot/restore), so the
// motion morphs smoothly instead of jumping when you swap.
//
// EDIT the constants or the waveY formula below, then:
//
//	just wasm-build           # (from examples/simple/)
//	# in the server console:
//	wasm swap wave            # the entire field's motion changes live
package main

import (
	"math"

	comp "github.com/zenion/mmoserver/pkg/component"
	"github.com/zenion/mmoserver/pkg/wasmsys"
)

// ─── tunables — edit, `just wasm-build`, then `wasm swap wave` ──────────────────
const (
	amplitude float32 = 220   // peak height (world units) — try 60 (calm) or 420 (wild)
	freqHz    float32 = 0.6   // temporal frequency — try 0.2 (slow) or 2.5 (frantic)
	spread    float32 = 0.012 // radians per pixel of X (wavelength) — try 0.04 for tight ripples
)

// waveY is the SHAPE of the field. Default is a traveling sine. Swap the body
// for one of these and `wasm swap wave` to see the motion transform live:
//
//	bounce:   return amplitude * float32(math.Abs(math.Sin(theta)))           // hopping
//	triangle: return amplitude * float32(2/math.Pi*math.Asin(math.Sin(theta))) // zig-zag
//	beat:     return amplitude * 0.5 * float32(math.Sin(theta)+math.Sin(theta*1.7)) // two-tone interference
//	pulse:    if math.Sin(theta) > 0 { return amplitude }; return -amplitude  // square wave
func waveY(theta float64) float32 {
	return amplitude * float32(math.Sin(theta))
}

type wave struct{ phase float32 }

func (w *wave) Query() wasmsys.Query { return wasmsys.ReadWrite[comp.Position]() }

func (w *wave) Update(ctx *wasmsys.Ctx, dt float32) {
	w.phase += dt
	t := 2 * math.Pi * float64(freqHz) * float64(w.phase)
	pos := wasmsys.Column[comp.Position](ctx)
	for i := range pos {
		pos[i].Y = waveY(t + float64(pos[i].X)*float64(spread))
	}
}

// phase survives a hot-swap so the wave doesn't jump.
func (w *wave) Snapshot() []byte { return wasmsys.MarshalState(w.phase) }
func (w *wave) Restore(b []byte) { wasmsys.UnmarshalState(b, &w.phase) }

func init() { wasmsys.Register(&wave{}) }
func main() {}
