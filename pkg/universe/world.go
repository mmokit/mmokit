package universe

import (
	"github.com/mlange-42/ark/ecs"
	"github.com/zenion/mmoserver/pkg/engine"
)

// GameWorld is the interface a game must implement to use the generic server meshing infrastructure.
// Embed WorldBase to get working default implementations for all methods.
type GameWorld interface {
	// Transfer serialization
	SerializeEntity(entity ecs.Entity) ([]byte, error)
	SpawnFromTransfer(data []byte) (netID uint32, connID uint32, err error)

	// Replica support
	ScanBorderEntities(neighbors map[string]NeighborInfo) map[string][][]byte
	ApplyReplicas(snapshots [][]byte, sourceNodeID string)
	ExpireReplicas()
	RemoveReplicaByNetID(netID uint32)

	// Entity lifecycle
	MarkForRemoval(entity ecs.Entity)
	ECSWorld() *ecs.World
	GetAoIRadius() float32

	// Ghost/transfer cooldown management
	TickGhosts()
	TickTransferCooldowns()
	RemoveGhostByNetID(netID uint32)

	// Cross-node actions (combat, status effects, etc.)
	HandleCrossNodeAction(action *CrossNodeAction) *ActionResult
	HandleActionResult(result *ActionResult)

	// Chat dispatch
	DispatchChat(username, text string)

	// Player login/spawn support
	RegisterPendingLogin(connID uint32, username string)

	// Bridge wiring (called by Coordinator after node creation)
	SetBridge(bridge NodeBridge)

	// Hooks returns game-specific lifecycle hooks for the game loop.
	// The Coordinator merges these with bridge hooks automatically.
	Hooks() engine.Hooks

	// Shutdown
	Shutdown()
}

// NeighborInfo describes a neighbor node's sector offset relative to the current node.
type NeighborInfo struct {
	NodeID string
	DX, DY int32 // sector offset from this node
}
