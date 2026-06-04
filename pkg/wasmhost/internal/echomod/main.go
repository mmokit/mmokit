//go:build wasip1

package main

import "github.com/zenion/mmoserver/pkg/wasmsys"

// inc adds 1.0 to every float32 in its column each tick, and counts ticks in
// internal state to exercise snapshot/restore.
type inc struct{ ticks uint32 }

func (s *inc) Query() wasmsys.Query { return wasmsys.ReadWrite[float32](42) }

func (s *inc) Update(ctx *wasmsys.Ctx, dt float32) {
	col := wasmsys.Column[float32](ctx)
	for i := range col {
		col[i] += 1
	}
	s.ticks++
}

func (s *inc) Snapshot() []byte {
	return []byte{byte(s.ticks), byte(s.ticks >> 8), byte(s.ticks >> 16), byte(s.ticks >> 24)}
}
func (s *inc) Restore(b []byte) {
	if len(b) == 4 {
		s.ticks = uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24
	}
}

// Registration runs in init() so it fires under reactor instantiation
// (_initialize), which runs package init funcs but not main().
func init() { wasmsys.Register(&inc{}) }

func main() {}
