package main

import "github.com/zenion/mmoserver/pkg/mmokit"

const (
	TickRate     int     = 20
	MeshCellsX   uint32  = 2
	MeshCellsY   uint32  = 2
	CellSize     float32 = 2000.0
	AoIRadius    float32 = 800.0
	PlayerRadius float32 = 20.0

	// Entity types
	KindPlayer uint8 = 1

)

// Cross-node replication component IDs (used in replication registry).
var (
	RepVelocity   = mmokit.ComponentID(1)
	RepName       = mmokit.ComponentID(2)
	RepMoveTarget = mmokit.ComponentID(3)
	RepDebugInfo  = mmokit.ComponentID(4)
)
