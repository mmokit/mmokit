//go:build wasip1

// Command wave is the simple example's hot-swappable GAME LOGIC: it computes the
// vertical motion of the whole field of dots. The native side only spawns the
// dots and broadcasts their positions — the *movement* lives here, in a module
// you can rebuild and hot-swap into a running server.
//
// Amplitude/freqHz/spread are now runtime tunables: edit the defaults here OR
// change them live with `tune set wave amplitude 420` / the admin /tunables
// sliders, no rebuild needed. `wasm swap wave` still hot-reloads code changes
// (e.g. a new waveY formula). The wave Phase accumulator is auto-snapshotted by
// the framework across a swap (it's an exported, untagged field — no
// Snapshot/Restore needed); the tunables (Amplitude/FreqHz/Spread) are
// separately re-applied from the registry. So the motion morphs smoothly
// instead of jumping.
package main

import (
	"math"

	comp "github.com/zenion/mmoserver/pkg/component"
	"github.com/zenion/mmoserver/pkg/wasmsys"
)

type wave struct {
	Amplitude float32 `tune:"default=220,min=60,max=420,step=10"`
	FreqHz    float32 `tune:"default=0.6,min=0.1,max=3,step=0.1"`
	Spread    float32 `tune:"default=0.012,min=0,max=0.05,step=0.001"`
	Phase     float32
}

func (w *wave) Query() wasmsys.Query { return wasmsys.ReadWrite[comp.Position]() }

func (w *wave) Update(ctx *wasmsys.Ctx, dt float32) {
	w.Phase += dt
	t := 2 * math.Pi * float64(w.FreqHz) * float64(w.Phase)
	pos := wasmsys.Column[comp.Position](ctx)
	for i := range pos {
		pos[i].Y = w.Amplitude * float32(math.Sin(t+float64(pos[i].X)*float64(w.Spread)))
	}
}

func init() { wasmsys.Register(&wave{}) }
func main() {}
