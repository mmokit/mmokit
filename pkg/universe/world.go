package universe

import (
	"github.com/mlange-42/ark/ecs"
	"github.com/zenion/mmoserver/pkg/engine"
)

// GameWorld is the interface a game must implement to use the generic server meshing infrastructure.
// Embed WorldBase to get working default implementations for all methods.
type GameWorld interface {
	// Init is called by the Coordinator after all nodes are created and bridges
	// are wired, but before game loops start. Use it for entity spawning,
	// login handler setup, replicator registration, and any initialization that
	// needs the full node context (bridge, topology, coordinator).
	Init()

	// Transfer serialization
	SerializeEntity(entity ecs.Entity) ([]byte, error)
	SpawnFromTransfer(data []byte) (netID uint32, connID uint32, err error)

	// Replica support
	ScanBorderEntities(neighbors map[string]NeighborInfo) map[string][][]byte
	ApplyReplicas(snapshots [][]byte, sourceNodeID string)
	ExpireReplicas()
	ClearReplicaUpdateFlags()
	RemoveReplicaByNetID(netID uint32)

	// Proxy support (lightweight border summaries)
	ScanBorderProxies(neighbors map[string]NeighborInfo) map[string][][]byte
	ApplyProxySummaries(summaries [][]byte, sourceNodeID string)
	ExpireProxies()
	ClearProxyUpdateFlags()
	RemoveProxyByNetID(netID uint32)

	// Proxy promotion (on-demand detail)
	RequestPromotion(netIDs []uint32)
	BuildDetailResponse(netIDs []uint32) *DetailResponseMsg
	PromoteProxy(frame *ReplicaFrame, sourceNodeID string)
	TickProxyDeadReckoning(dt float32)
	TickReplicaDeadReckoning(dt float32)
	WakeDormantEntities(wakeRadius float32)

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

	// Bridge wiring (called by Coordinator after node creation)
	SetBridge(bridge NodeBridge)

	// UpdateCellBounds updates the cell identity and size for this world.
	// Called during dynamic cell partitioning (split/merge) to resize a node's
	// coordinate space. Must be called from the game loop goroutine.
	UpdateCellBounds(cell CellID, cellSize float32)

	// Hooks returns game-specific lifecycle hooks for the game loop.
	// The Coordinator merges these with bridge hooks automatically.
	Hooks() engine.Hooks

	// Shutdown
	Shutdown()
}

// NeighborInfo describes a neighbor node's cell offset relative to the current node.
type NeighborInfo struct {
	NodeID string
	DX, DY int32 // cell offset from this node
}
