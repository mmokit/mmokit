// Simplest possible mmokit server. One entity oscillates left and right.
// Use the interactive console to inspect state: "entity list", "perf".
package main

import (
	"context"

	"github.com/zenion/mmoserver/pkg/mmokit"
)

// OscillateSystem is an example system that reverses all entities every 5 seconds.
type OscillateSystem struct {
	mmokit.SystemBase // the mmokit base struct for common functionality

	// Query for entities with a Position component. The struct fields define the components to access.
	entities mmokit.Query[struct {
		Pos *mmokit.Position
	}]
	elapsed float32 // time accumulator for direction changes
	dir     float32 // current direction: 1 or -1
}

// Init runs once per node after the ECS world is ready.
func (s *OscillateSystem) Init() {
	s.entities.Init(s, mmokit.IncludeAll()) // do the query for entities with Position
	s.dir = 1                               // start moving right
}

// Update runs every tick (20Hz). dt is the fixed timestep in seconds.
func (s *OscillateSystem) Update(dt float32) {
	s.elapsed += dt
	if s.elapsed >= 5.0 {
		s.elapsed = 0
		s.dir = -s.dir
	}
	// Range iterator — e.Pos points into Ark's column storage, mutations are immediate.
	for _, e := range s.entities.All() {
		e.Pos.X += 100 * s.dir * dt
	}
}

func main() {
	// Coordinator manages nodes, networking, and the interactive console.
	coord := mmokit.NewCoordinator(mmokit.Config{
		CellSize: 8192,
		TickRate: 20,
	})
	// OnInit runs once per node — use for simple games without a custom GameWorld struct.
	coord.OnInit(func(w *mmokit.WorldBase) {
		w.SpawnEntity(mmokit.Position{X: 0, Y: 0})
	})
	// Systems are registered as factories — each node gets its own instance.
	coord.AddSystem("Oscillate", func() mmokit.System { return &OscillateSystem{} })
	// Start blocks: runs game loop, console, handles SIGINT/SIGTERM.
	coord.Start(context.Background())
}
