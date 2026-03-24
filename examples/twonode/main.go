// Package main demonstrates a minimal 2-node server meshing setup using the
// mmokit engine. Two sectors (0,0) and (1,0) run side by side. An entity
// spawns near the right edge of node_0_0 and drifts right until it crosses
// the sector boundary, at which point it automatically transfers to node_1_0.
//
// The output shows replication (border entity visibility) and the full transfer
// lifecycle: serialize → ghost → spawn on dest → arrival confirm → ghost removed.
package main

import (
	"context"
	"fmt"
	"time"

	"github.com/zenion/mmoserver/pkg/mmokit"
)

// myWorld embeds WorldBase to get all GameWorld defaults for free.
// Override methods like SerializeEntity/DeserializeEntity to add custom
// components. For this minimal example, the defaults handle everything.
type myWorld struct {
	mmokit.WorldBase
}

func main() {
	// Create a coordinator that manages a 2x1 grid of sector nodes:
	//   node_0_0 (sector 0,0)  |  node_1_0 (sector 1,0)
	// Each sector is 8192x8192 units by default (configurable via SetSectorSize).
	coord := mmokit.NewCoordinator(
		// Grid spans sector X=[0,1], Y=[0,0] — two columns, one row.
		mmokit.GridConfig{MinSX: 0, MaxSX: 1, MinSY: 0, MaxSY: 0},
		// 20 ticks per second — the engine runs a fixed-timestep game loop.
		mmokit.EngineConfig{TickRate: 20},
		// Factory function: called once per node to create the game world and
		// register systems. Each node gets its own ECS world and system pipeline.
		func(base *mmokit.WorldBase) (mmokit.GameWorld, []mmokit.System) {
			w := &myWorld{WorldBase: *base}
			return w, []mmokit.System{
				// PhysicsSystem applies velocity to position each tick.
				// This is what makes the entity drift across the boundary.
				mmokit.NewPhysicsSystem(base.ECSWorld()),
			}
		},
	)

	// Start all nodes. Each node begins running its game loop in a goroutine.
	ctx, cancel := context.WithCancel(context.Background())
	coord.Start(ctx)

	// Spawn an entity on node_0_0 near the right boundary (X=8192), moving right.
	// PendingAdminCmds is a channel of closures that execute on the game loop
	// goroutine, ensuring thread-safe ECS access.
	coord.DefaultNode().Engine.PendingAdminCmds <- func() {
		w := coord.DefaultNode().World.(*myWorld)
		// Position X=7900 is ~292 units from the right edge of sector 0,0.
		// Velocity X=200 means it will cross the boundary in ~1.5 seconds.
		w.SpawnEntity(mmokit.Position{X: 7900, Y: 4096}, mmokit.WithVelocity(200, 0))
	}

	// Wait for the entity to drift across the boundary and complete the transfer.
	time.Sleep(3 * time.Second)

	// Count real entities per node (excluding replicas and ghosts).
	// After the transfer, node_0_0 should have 0 and node_1_0 should have 1.
	//   - Replica: a read-only copy of a border entity from a neighboring node.
	//   - Ghost: a placeholder left behind during an in-progress transfer.
	for id, n := range coord.Nodes {
		fmt.Printf("[%s] real entities: %d\n", id, mmokit.CountRealEntities(n.Engine.ECS))
	}

	// Gracefully shut down all nodes.
	cancel()
	coord.Shutdown()
}
