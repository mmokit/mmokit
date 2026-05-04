package universe

import (
	"github.com/mlange-42/ark/ecs"
	"github.com/zenion/mmoserver/pkg/engine"
)

// GameWorld is the interface a game must implement to use the generic server meshing infrastructure.
// Embed *Stage to get working default implementations for all methods.
type GameWorld interface {
	Init()
	Hooks() engine.Hooks
	Shutdown()

	SerializeEntity(entity ecs.Entity) ([]byte, error)
	SpawnFromTransfer(data []byte) (netID uint32, connID uint32, err error)

	DispatchChat(username, text string)

	SetBridge(bridge Bridge)
	UpdateCellBounds(cell CellID, cellSize float32)
	MarkForRemoval(entity ecs.Entity)
}

// NeighborInfo describes a neighbor cell's offset relative to the current cell.
type NeighborInfo struct {
	CellID string
	DX, DY int32 // cell offset from this cell
}
