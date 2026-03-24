package game

import "github.com/zenion/mmoserver/pkg/coords"

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
	SendArrivalConfirm(destNodeID string, confirm *ArrivalConfirmMsg)
	// OnPlayerTransfer notifies the coordinator that a player moved to another node.
	OnPlayerTransfer(connID uint32, destNodeID string)
	// ChatRelay relays a chat message to all other nodes.
	ChatRelay(username, text string)
	// RespawnTransfer transfers a player respawn to the station node.
	RespawnTransfer(connID uint32, username string)
}

// NoopNodeBridge is a no-op implementation for single-node mode.
type NoopNodeBridge struct{}

func (NoopNodeBridge) PreTick()                                      {}
func (NoopNodeBridge) PostSystems()                                  {}
func (NoopNodeBridge) SectorOwner(coords.SectorCoord) string         { return "" }
func (NoopNodeBridge) SendTransfer(string, *TransferPayload)         {}
func (NoopNodeBridge) SendArrivalConfirm(string, *ArrivalConfirmMsg) {}
func (NoopNodeBridge) OnPlayerTransfer(uint32, string)               {}
func (NoopNodeBridge) ChatRelay(string, string)                      {}
func (NoopNodeBridge) RespawnTransfer(uint32, string)                {}
