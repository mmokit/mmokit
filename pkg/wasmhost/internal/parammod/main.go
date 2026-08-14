//go:build wasip1

// parammod is a wasmhost test fixture: a system with two tunable fields and a
// trivial column, used to exercise the params ABI from the host.
package main

import "github.com/mmokit/mmokit/pkg/wasmsys"

type sys struct {
	Gain   float32 `tune:"default=2,min=0,max=10,step=0.5"`
	Enable bool    `tune:"default=true"`
}

func (s *sys) Query() wasmsys.Query { return wasmsys.ReadWrite[float32]() }
func (s *sys) Update(ctx *wasmsys.Ctx, dt float32) {
	col := wasmsys.Column[float32](ctx)
	g := s.Gain
	if !s.Enable {
		g = 0
	}
	for i := range col {
		col[i] *= g
	}
}

func init() { wasmsys.Register(&sys{}) }
func main() {}
