package universe

import (
	"github.com/mlange-42/ark/ecs"
	"github.com/zenion/mmoserver/pkg/engine"
)

// GameWorld is the interface a game must implement to use the generic server meshing infrastructure.
// Embed *WorldBase to get working default implementations for all methods.
type GameWorld interface {
	Init()
	Hooks() engine.Hooks
	Shutdown()

	SerializeEntity(entity ecs.Entity) ([]byte, error)
	SpawnFromTransfer(data []byte) (netID uint32, connID uint32, err error)

	HandleCrossNodeAction(action *CrossNodeAction) *ActionResult
	HandleActionResult(result *ActionResult)

	DispatchChat(username, text string)

	SetBridge(bridge NodeBridge)
	UpdateCellBounds(cell CellID, cellSize float32)
	MarkForRemoval(entity ecs.Entity)
}

// NeighborInfo describes a neighbor node's cell offset relative to the current node.
type NeighborInfo struct {
	NodeID string
	DX, DY int32 // cell offset from this node
}
