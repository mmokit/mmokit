//go:build wasip1

// Command colorwave is a hot-swappable wasm system for the simple example: it
// drives every sine-wave entity's Hue, scrolling the rainbow across the field.
//
// The durable color state lives in the ECS Hue components (host side), so this
// module is STATELESS — a hot-swap just picks up the current hues and keeps
// going, no snapshot/restore needed.
//
// EDIT the constants below, then:
//
//	just wasm-build           # (from examples/simple/)
//	# in the server console:
//	wasm swap colorwave       # new scroll speed/direction takes effect live
package main

import (
	"math"

	"github.com/zenion/mmoserver/examples/simple/wavecomp"
	"github.com/zenion/mmoserver/pkg/wasmsys"
)

// ─── tunables: edit, `just wasm-build`, then `wasm swap colorwave` ──────────────
const (
	speed float32 = 0.004 // hue fraction advanced per tick (20 ticks/s) — bigger = faster scroll
)

type colorWave struct{}

func (colorWave) Query() wasmsys.Query { return wasmsys.ReadWrite[wavecomp.Hue]() }

func (colorWave) Update(ctx *wasmsys.Ctx, dt float32) {
	hues := wasmsys.Column[wavecomp.Hue](ctx)
	for i := range hues {
		// Advance each hue and wrap into [0,1). Because entities start with a
		// rainbow spread (hue = index/count), advancing them all by the same
		// step scrolls the whole rainbow across the field.
		hues[i].Value = float32(math.Mod(float64(hues[i].Value+speed), 1.0))
	}
}

func init() { wasmsys.Register(colorWave{}) }
func main() {}
