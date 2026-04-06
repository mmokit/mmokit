// Simplest possible mmokit server. One entity oscillates left and right.
// Use the interactive console to inspect state: "entity list", "perf".
package main

import (
	"context"

	"github.com/zenion/mmoserver/pkg/mmokit"
)

// OscillateSystem reverses all entities every 5 seconds.
type OscillateSystem struct {
	mmokit.SystemBase // embed for ECSWorld(), Engine(), GameWorld() access

	// Query[T] matches entities with the bundle's component types.
	// Fields are pointers to components — populated each iteration.
	entities mmokit.Query[struct {
		Pos *mmokit.Position
	}]
	elapsed float32
	dir     float32
}

// Init runs once per node after the ECS world is ready.
func (s *OscillateSystem) Init() {
	// IncludeAll: no default Ghost/Replica exclusions (not needed here).
	s.entities.Init(s, mmokit.IncludeAll())
	s.dir = 1
}

// Update runs every tick (20Hz). dt is the fixed timestep in seconds.
func (s *OscillateSystem) Update(dt float32) {
	s.elapsed += dt
	if s.elapsed >= 5.0 {
		s.elapsed = 0
		s.dir = -s.dir
	}
	// Range iterator — b.Pos points into Ark's column storage, mutations are immediate.
	for _, b := range s.entities.All() {
		b.Pos.X += 100 * s.dir * dt
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
		w.SpawnEntity(mmokit.Position{X: 4096, Y: 4096}, mmokit.WithCollider(20))
	})
	// Systems are registered as factories — each node gets its own instance.
	coord.AddSystem("Oscillate", func() mmokit.System { return &OscillateSystem{} })
	// Start blocks: runs game loop, console, handles SIGINT/SIGTERM.
	coord.Start(context.Background())
}
