//go:build wasip1

package main

import (
	"github.com/zenion/mmoserver/pkg/mmokit/internal/testmods/podcomp"
	"github.com/zenion/mmoserver/pkg/wasmsys"
)

const shieldTypeID uint32 = 1

type shieldRegen struct{ ticks uint64 }

func (s *shieldRegen) Query() wasmsys.Query {
	return wasmsys.ReadWrite[podcomp.Shield](shieldTypeID)
}
func (s *shieldRegen) Update(ctx *wasmsys.Ctx, dt float32) {
	shields := wasmsys.Column[podcomp.Shield](ctx)
	for i := range shields {
		sh := &shields[i]
		if sh.DamageCooldown > 0 {
			sh.DamageCooldown -= dt
			continue
		}
		if sh.Current < sh.Max {
			sh.Current = min(sh.Current+sh.RegenRate*dt, sh.Max)
		}
	}
	s.ticks++
}
func (s *shieldRegen) Snapshot() []byte {
	b := make([]byte, 8)
	for i := 0; i < 8; i++ {
		b[i] = byte(s.ticks >> (8 * i))
	}
	return b
}
func (s *shieldRegen) Restore(b []byte) {
	if len(b) == 8 {
		var v uint64
		for i := 0; i < 8; i++ {
			v |= uint64(b[i]) << (8 * i)
		}
		s.ticks = v
	}
}
func init() { wasmsys.Register(&shieldRegen{}) }
func main() {}
