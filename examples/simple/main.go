package main

import (
	"context"

	"github.com/zenion/mmoserver/pkg/mmokit"
)

// OscillateSystem moves all entities left and right.
type OscillateSystem struct {
	mmokit.SystemBase
	entities mmokit.Query[struct {
		Pos *mmokit.Position
	}]
	elapsed float32
	dir     float32
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
	for _, b := range s.entities.All() {
		b.Pos.X += 100 * s.dir * dt
	}
}

func main() {
	coord := mmokit.NewCoordinator(mmokit.Config{
		CellSize: 8192,
		TickRate: 20,
	})
	coord.OnInit(func(w *mmokit.WorldBase) {
		w.SpawnEntity(mmokit.Position{X: 4096, Y: 4096}, mmokit.WithCollider(20))
	})
	coord.AddSystem("Oscillate", func() mmokit.System { return &OscillateSystem{} })
	coord.Start(context.Background())
}
