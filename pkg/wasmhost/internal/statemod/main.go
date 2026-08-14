//go:build wasip1

// statemod is a wasmhost fixture with NO Stateful impl and an exported,
// untagged Count field — so the framework must AUTO-snapshot it. Count
// increments each Update and is mirrored into the int32 column.
package main

import "github.com/zenion/mmokit/pkg/wasmsys"

type st struct {
	Count int32 // exported + untagged => auto-snapshot state
}

func (s *st) Query() wasmsys.Query { return wasmsys.ReadWrite[int32]() }
func (s *st) Update(ctx *wasmsys.Ctx, dt float32) {
	s.Count++
	col := wasmsys.Column[int32](ctx)
	for i := range col {
		col[i] = s.Count
	}
}

func init() { wasmsys.Register(&st{}) }
func main() {}
