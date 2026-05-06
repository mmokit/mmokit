package main

import "github.com/zenion/mmoserver/pkg/mmokit"

type OscillateSystem struct {
	mmokit.SystemBase[any]
	entities mmokit.Query[struct {
		Pos *mmokit.Position
	}]
	elapsed float32 // time accumulator for direction changes
	dir     float32 // current direction: 1 or -1
}

func (s *OscillateSystem) Init() {
	s.entities.With(mmokit.IncludeAll())
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
