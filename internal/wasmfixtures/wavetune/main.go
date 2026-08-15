//go:build wasip1

// wavetune is an mmokit test fixture: a Position-column system with one tunable
// (Offset) written into every entity's Y. Used to verify the full host↔guest
// tunable bridge through the tune.* verbs.
package main

import (
	comp "github.com/mmokit/mmokit/pkg/component"
	"github.com/mmokit/mmokit/pkg/wasmsys"
)

type wavetune struct {
	Offset float32 `tune:"default=10,min=0,max=100,step=5"`
}

func (w *wavetune) Query() wasmsys.Query { return wasmsys.ReadWrite[comp.Position]() }
func (w *wavetune) Update(ctx *wasmsys.Ctx, dt float32) {
	pos := wasmsys.Column[comp.Position](ctx)
	for i := range pos {
		pos[i].Y = w.Offset
	}
}

func init() { wasmsys.Register(&wavetune{}) }
func main() {}
