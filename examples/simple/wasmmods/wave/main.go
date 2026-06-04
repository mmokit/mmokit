//go:build wasip1

// Command wave is the simple example's hot-swappable GAME LOGIC: it computes the
// vertical motion of the whole field of dots. The native side only spawns the
// dots and broadcasts their positions — the *movement* lives here, in a module
// you can rebuild and hot-swap into a running server.
//
// Amplitude/freqHz/spread are now runtime tunables: edit the defaults here OR
// change them live with `tune set wave amplitude 420` / the admin /tunables
// sliders, no rebuild needed. `wasm swap wave` still hot-reloads code changes
// (e.g. a new waveY formula), preserving the live tunable values + the wave
// phase accumulator (snapshot/restore) so the motion morphs smoothly instead of
// jumping.
package main

import (
	"math"

	comp "github.com/zenion/mmoserver/pkg/component"
	"github.com/zenion/mmoserver/pkg/wasmsys"
)

// wave is the hot-swappable game logic computing the field's vertical motion.
// Amplitude/FreqHz/Spread are runtime tunables (slide them live); phase is
// internal state preserved across a hot-swap via Snapshot/Restore.
type wave struct {
	Amplitude float32 `tune:"default=220,min=60,max=420,step=10"`
	FreqHz    float32 `tune:"default=0.6,min=0.1,max=3,step=0.1"`
	Spread    float32 `tune:"default=0.012,min=0,max=0.05,step=0.001"`
	phase     float32
}

func (w *wave) Query() wasmsys.Query { return wasmsys.ReadWrite[comp.Position]() }

// Swap the math.Sin formula below + `wasm swap wave` to morph the motion live
// (e.g. math.Abs(math.Sin) → bounce, square wave → pulse).
func (w *wave) Update(ctx *wasmsys.Ctx, dt float32) {
	w.phase += dt
	t := 2 * math.Pi * float64(w.FreqHz) * float64(w.phase)
	pos := wasmsys.Column[comp.Position](ctx)
	for i := range pos {
		pos[i].Y = w.Amplitude * float32(math.Sin(t+float64(pos[i].X)*float64(w.Spread)))
	}
}

// phase survives a hot-swap so the wave doesn't jump.
func (w *wave) Snapshot() []byte { return wasmsys.MarshalState(w.phase) }
func (w *wave) Restore(b []byte) { wasmsys.UnmarshalState(b, &w.phase) }

func init() { wasmsys.Register(&wave{}) }
func main()  {}
