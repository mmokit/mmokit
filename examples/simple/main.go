// Package main demonstrates the simplest possible mmokit server.
// A single entity oscillates left and right. Use the interactive
// console to inspect entities (type "entity list" or "perf").
package main

import (
	"context"

	"github.com/mlange-42/ark/ecs"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// OscillateSystem moves all entities left and right.
type OscillateSystem struct {
	mmokit.SystemBase
	filter  *ecs.Filter1[mmokit.Position] // filter for entities with Position component
	elapsed float32                       // time accumulator for direction changes
	dir     float32                       // 1 for right, -1 for left
}

func (s *OscillateSystem) Init() {
	s.filter = ecs.NewFilter1[mmokit.Position](s.ECSWorld())
	s.dir = 1
}

func (s *OscillateSystem) Update(dt float32) {
	s.elapsed += dt
	if s.elapsed >= 5.0 { // reverse every 5 seconds
		s.elapsed = 0
		s.dir = -s.dir
	}
	query := s.filter.Query()
	for query.Next() {
		pos := query.Get()
		pos.X += 100 * s.dir * dt
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
