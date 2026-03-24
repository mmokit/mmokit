package universe

import "github.com/zenion/mmoserver/pkg/coords"

// NodeBridge abstracts multi-node coordination so the game world doesn't need
// nil-checked function pointers. In single-node mode, use NoopNodeBridge.
type NodeBridge interface {
	// PreTick is called at the start of each tick to drain inter-node messages.
	PreTick()
	// PostSystems is called after all systems run (replica replication/expiration).
	PostSystems()
	// SectorOwner returns the nodeID that owns the given sector, or "" if unowned.
	SectorOwner(sector coords.SectorCoord) string
	// SendTransfer delivers a serialized transfer payload to the destination node.
	// netID is the entity's network ID, used to remove pre-existing replicas on arrival.
	SendTransfer(destNodeID string, data []byte, netID uint32)
	// SendArrivalConfirm notifies the source node that a transferred entity arrived.
	SendArrivalConfirm(destNodeID string, confirm *ArrivalConfirmMsg)
	// OnPlayerTransfer notifies the coordinator that a player moved to another node.
	OnPlayerTransfer(connID uint32, destNodeID string)
	// RelayChatToOtherNodes relays a chat message to all other nodes.
	RelayChatToOtherNodes(username, text string)
	// RequestSpawnOnNode transfers a player spawn to the station node.
	RequestSpawnOnNode(connID uint32, username string)
	// SendAction sends a cross-node action to the authoritative node for an entity.
	SendAction(targetNodeID string, action *CrossNodeAction)
	// SendActionResult sends the result of a cross-node action back to the originator.
	SendActionResult(targetNodeID string, result *ActionResult)
}

// NoopNodeBridge is a no-op implementation for single-node mode.
type NoopNodeBridge struct{}

func (NoopNodeBridge) PreTick()                                     {}
func (NoopNodeBridge) PostSystems()                                 {}
func (NoopNodeBridge) SectorOwner(coords.SectorCoord) string        { return "" }
func (NoopNodeBridge) SendTransfer(string, []byte, uint32)          {}
func (NoopNodeBridge) SendArrivalConfirm(string, *ArrivalConfirmMsg) {}
func (NoopNodeBridge) OnPlayerTransfer(uint32, string)              {}
func (NoopNodeBridge) RelayChatToOtherNodes(string, string)         {}
func (NoopNodeBridge) RequestSpawnOnNode(uint32, string)            {}
func (NoopNodeBridge) SendAction(string, *CrossNodeAction)          {}
func (NoopNodeBridge) SendActionResult(string, *ActionResult)       {}
