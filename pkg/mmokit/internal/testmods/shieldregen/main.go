//go:build wasip1

package main

import (
	"github.com/zenion/mmokit/pkg/mmokit/internal/testmods/podcomp"
	"github.com/zenion/mmokit/pkg/wasmsys"
)

type shieldRegen struct{ ticks uint64 }

func (s *shieldRegen) Query() wasmsys.Query {
	return wasmsys.ReadWrite[podcomp.Shield]()
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
func (s *shieldRegen) Snapshot() []byte { return wasmsys.MarshalState(s.ticks) }
func (s *shieldRegen) Restore(b []byte) { wasmsys.UnmarshalState(b, &s.ticks) }
func init()                             { wasmsys.Register(&shieldRegen{}) }
func main()                             {}
