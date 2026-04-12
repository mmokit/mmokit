package universe

// Bridge abstracts multi-cell coordination so the game world doesn't need
// nil-checked function pointers. In single-cell mode, use NoopBridge.
type Bridge interface {
	// PreTick is called at the start of each tick to drain inter-cell messages.
	PreTick()
	// PostSystems is called after all systems run (replica replication/expiration).
	PostSystems()
	// NodeOwner returns the cellID that owns the given cell, or "" if unowned.
	NodeOwner(cell CellID) string
	// NodeOwnerAtPos returns the cellID that owns the given world-space position,
	// or "" if unowned. Used by BoundarySystem for cross-depth cell lookups.
	NodeOwnerAtPos(worldX, worldY float32) string
	// SendTransfer delivers a serialized transfer payload to the destination cell.
	// netID is the entity's network ID, used to remove pre-existing replicas on arrival.
	SendTransfer(destCellID string, data []byte, netID uint32)
	// SendArrivalConfirm notifies the source cell that a transferred entity arrived.
	SendArrivalConfirm(destCellID string, confirm *ArrivalConfirmMsg)
	// OnPlayerTransfer notifies the coordinator that a player moved to another cell.
	OnPlayerTransfer(connID uint32, destCellID string)
	// RelayChatToOtherNodes relays a chat message to all other cells.
	RelayChatToOtherNodes(username, text string)
	// RequestRespawn asks the coordinator to route a player respawn.
	// The coordinator calls PlayerRouter to determine the target cell.
	RequestRespawn(connID uint32, username string)
	// SendAction sends a cross-cell action to the authoritative cell for an entity.
	SendAction(targetCellID string, action *CrossNodeAction)
	// SendActionResult sends the result of a cross-cell action back to the originator.
	SendActionResult(targetCellID string, result *ActionResult)
}

// NoopBridge is a no-op implementation for single-cell mode.
type NoopBridge struct{}

func (NoopBridge) PreTick()                                    {}
func (NoopBridge) PostSystems()                                {}
func (NoopBridge) NodeOwner(CellID) string                     { return "" }
func (NoopBridge) NodeOwnerAtPos(float32, float32) string      { return "" }
func (NoopBridge) SendTransfer(string, []byte, uint32)         {}
func (NoopBridge) SendArrivalConfirm(string, *ArrivalConfirmMsg) {}
func (NoopBridge) OnPlayerTransfer(uint32, string)             {}
func (NoopBridge) RelayChatToOtherNodes(string, string)        {}
func (NoopBridge) RequestRespawn(uint32, string)               {}
func (NoopBridge) SendAction(string, *CrossNodeAction)         {}
func (NoopBridge) SendActionResult(string, *ActionResult)      {}
