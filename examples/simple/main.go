// Simplest possible mmokit server. One entity oscillates left and right.
// Use the interactive console to inspect state: "entity list", "perf".
package main

import (
	"github.com/zenion/mmoserver/pkg/mmokit"
)

type OscillateSystem struct {
	mmokit.SystemBase[any]
	entities mmokit.Query[struct {
		Pos *mmokit.Position
	}]
	elapsed float32 // time accumulator for direction changes
	dir     float32 // current direction: 1 or -1
}

func (s *OscillateSystem) Init() {
	s.entities.Init(s, mmokit.IncludeAll())
	s.dir = 1
}

func (s *OscillateSystem) Update(dt float32) {
	s.elapsed += dt
	if s.elapsed >= 5.0 {
		s.elapsed = 0
		s.dir = -s.dir
	}
	for _, e := range s.entities.Iter {
		e.Pos.X += 100 * s.dir * dt
	}
}

func main() {
	mmo := mmokit.New(mmokit.Config{
		OnInit: func(w *mmokit.WorldBase) {
			w.SpawnEntity(mmokit.Position{X: 0, Y: 0})
		},
	})
	mmo.AddSystem(mmokit.NewSystem(&OscillateSystem{}))
	mmo.Start()
}
