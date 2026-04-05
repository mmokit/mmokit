// Package main demonstrates the simplest possible mmokit server.
// A single entity oscillates left and right. Connect via WebSocket
// at ws://localhost:8080/ws to observe the replication stream.
package main

import (
	"context"
	"fmt"
	"log"
	"net/http"

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
	// Capture the default node's world for console commands.
	var world *MyWorld

	cfg := mmokit.Config{
		CellsX:   1,
		CellsY:   1,
		CellSize: 8192,
		TickRate:  20,
		AoIRadius: 3000,
		WorldFactory: func(base *mmokit.WorldBase, coord *mmokit.Coordinator) mmokit.GameWorld {
			gw := &MyWorld{WorldBase: *base}
			world = gw

			// Spawn an entity that moves back and forth
			gw.SpawnEntity(mmokit.Position{X: 4096, Y: 4096},
				mmokit.WithVelocity(100, 0),
				mmokit.WithCollider(20),
			)

			return gw
		},
		OnConsoleReady: func(c *mmokit.Console) {
			c.Register(mmokit.Command{
				Name:        "entities",
				Aliases:     []string{"ents"},
				Category:    "game",
				Description: "list all entities with position and velocity",
				Fn: func(args []string) {
					result := c.ExecOnGameLoop(func() string {
						w := world.ECSWorld()
						filter := ecs.NewFilter3[mmokit.Position, mmokit.Velocity, mmokit.NetworkID](w)
						query := filter.Query()
						out := ""
						for query.Next() {
							pos, vel, nid := query.Get()
							out += fmt.Sprintf("  #%d  pos=(%.1f, %.1f)  vel=(%.1f, %.1f)\n", nid.ID, pos.X, pos.Y, vel.X, vel.Y)
						}
						if out == "" {
							return "no entities"
						}
						return out
					})
					c.Print(result)
				},
			})
		},
	}
	coord := mmokit.NewCoordinator(cfg)

	coord.AddSystem("Oscillate", func() mmokit.System { return &OscillateSystem{} })
	coord.AddSystem("Physics", mmokit.NewPhysicsSystem())
	coord.AddSystem("Spatial", mmokit.NewSpatialSystem())
	coord.AddSystem("Network", mmokit.NewNetworkSystem())

	cm := coord.ConnManager()
	coord.Build()

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", cm.HandleWebSocket)
	mux.Handle("/metrics", coord.MetricsHandler())

	log.Println("listening on :8080")
	go func() { log.Fatal(http.ListenAndServe(":8080", mux)) }()

	coord.Start(context.Background())
}
