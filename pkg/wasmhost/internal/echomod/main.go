//go:build wasip1

package main

import "github.com/mmokit/mmokit/pkg/wasmsys"

// inc adds 1.0 to every float32 in its column each tick, and counts ticks in
// internal state to exercise snapshot/restore.
type inc struct{ ticks uint32 }

func (s *inc) Query() wasmsys.Query { return wasmsys.ReadWrite[float32]() }

func (s *inc) Update(ctx *wasmsys.Ctx, dt float32) {
	col := wasmsys.Column[float32](ctx)
	for i := range col {
		col[i] += 1
	}
	s.ticks++
}

func (s *inc) Snapshot() []byte { return wasmsys.MarshalState(s.ticks) }
func (s *inc) Restore(b []byte) { wasmsys.UnmarshalState(b, &s.ticks) }

// Registration runs in init() so it fires under reactor instantiation
// (_initialize), which runs package init funcs but not main().
func init() { wasmsys.Register(&inc{}) }

func main() {}
