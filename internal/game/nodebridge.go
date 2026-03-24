package game

import (
	"github.com/zenion/mmoserver/pkg/coords"
	pkguniverse "github.com/zenion/mmoserver/pkg/universe"
)

// NodeBridge abstracts multi-node coordination so GameWorld doesn't need
// nil-checked function pointers. In single-node mode, use NoopNodeBridge.
type NodeBridge interface {
	// PreTick is called at the start of each tick to drain inter-node messages.
	PreTick()
	// PostSystems is called after all systems run (replica replication/expiration).
	PostSystems()
	// SectorOwner returns the nodeID that owns the given sector, or "" if unowned.
	SectorOwner(sector coords.SectorCoord) string
	// SendTransfer delivers a transfer payload to the destination node.
	SendTransfer(destNodeID string, payload *TransferPayload)
	// SendArrivalConfirm notifies the source node that a transferred entity arrived.
	SendArrivalConfirm(destNodeID string, confirm *pkguniverse.ArrivalConfirmMsg)
	// OnPlayerTransfer notifies the coordinator that a player moved to another node.
	OnPlayerTransfer(connID uint32, destNodeID string)
	// RelayChatToOtherNodes relays a chat message to all other nodes.
	RelayChatToOtherNodes(username, text string)
	// RequestSpawnOnNode transfers a player spawn to the station node.
	RequestSpawnOnNode(connID uint32, username string)
}

// NoopNodeBridge is a no-op implementation for single-node mode.
type NoopNodeBridge struct{}

func (NoopNodeBridge) PreTick()                                                {}
func (NoopNodeBridge) PostSystems()                                            {}
func (NoopNodeBridge) SectorOwner(coords.SectorCoord) string                   { return "" }
func (NoopNodeBridge) SendTransfer(string, *TransferPayload)                   {}
func (NoopNodeBridge) SendArrivalConfirm(string, *pkguniverse.ArrivalConfirmMsg) {}
func (NoopNodeBridge) OnPlayerTransfer(uint32, string)                         {}
func (NoopNodeBridge) RelayChatToOtherNodes(string, string)                    {}
func (NoopNodeBridge) RequestSpawnOnNode(uint32, string)                       {}
