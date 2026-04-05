// Package main demonstrates the simplest possible mmokit server.
// A single entity oscillates left and right. Use the interactive
// console to inspect entities (type "entity list" or "perf").
package main

import (
	"context"

	"github.com/mlange-42/ark/ecs"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// MyWorld embeds WorldBase for automatic server meshing support.
type MyWorld struct {
	mmokit.WorldBase
}

// OscillateSystem reverses all entities' velocity every 5 seconds.
type OscillateSystem struct {
	mmokit.SystemBase
	filter  *ecs.Filter1[mmokit.Velocity]
	elapsed float32
	speed   float32
}

func (s *OscillateSystem) Init() {
	s.filter = ecs.NewFilter1[mmokit.Velocity](s.ECSWorld())
	s.speed = 100
}

func (s *OscillateSystem) Update(dt float32) {
	s.elapsed += dt
	if s.elapsed < 5.0 {
		return
	}
	s.elapsed = 0
	s.speed = -s.speed
	query := s.filter.Query()
	for query.Next() {
		vel := query.Get()
		vel.X = s.speed
	}
}

func main() {
	cfg := mmokit.Config{
		CellsX:   1,
		CellsY:   1,
		CellSize: 8192,
		TickRate:  20,
		WorldFactory: func(base *mmokit.WorldBase, coord *mmokit.Coordinator) mmokit.GameWorld {
			gw := &MyWorld{WorldBase: *base}

			// Spawn an entity that moves back and forth
			gw.SpawnEntity(mmokit.Position{X: 4096, Y: 4096},
				mmokit.WithVelocity(100, 0),
				mmokit.WithCollider(20),
			)

			return gw
		},
	}
	coord := mmokit.NewCoordinator(cfg)

	coord.AddSystem("Oscillate", func() mmokit.System { return &OscillateSystem{} })
	coord.AddSystem("Physics", mmokit.NewPhysicsSystem()) // integrates velocity → position

	coord.Start(context.Background())
}
