package universe

// Bridge abstracts multi-cell coordination so the game world doesn't need
// nil-checked function pointers. In single-cell mode, use NoopBridge.
type Bridge interface {
	// PreTick is called at the start of each tick to drain inter-cell messages.
	PreTick()
	// PostSystems is called after all systems run (replica replication/expiration).
	PostSystems()
	// CellOwner returns the cellID that owns the given cell, or "" if unowned.
	CellOwner(cell CellID) string
	// CellOwnerAtPos returns the cellID that owns the given world-space position,
	// or "" if unowned. Used by BoundarySystem for cross-depth cell lookups.
	CellOwnerAtPos(worldX, worldY float32) string
	// OnPlayerTransfer notifies the coordinator that a player moved to another cell.
	OnPlayerTransfer(connID uint32, destCellID MeshCellID)
	// RequestRespawn asks the coordinator to route a player respawn.
	// The coordinator calls PlayerRouter to determine the target cell.
	RequestRespawn(connID uint32, username string)
	// SendAction sends a cross-cell action to the authoritative cell for an entity.
	SendAction(targetCellID MeshCellID, action *CrossCellAction)
	// SendBorderFrame dispatches an encoded border replication frame to
	// a neighbor cell. The encoded bytes are a pkg/replication.Frame.
	// Lossy delivery: if the destination inbox is full (single-host) or
	// the gRPC outbound queue is full (multi-host), the frame is dropped.
	// The 30-tick forced resync recovers the receiver automatically.
	SendBorderFrame(destCellID, fromCellID MeshCellID, encoded []byte)
	// SendHandoff sends an authority-transfer message to the destination
	// cell. Returns true on successful enqueue (in-process inbox or
	// outbound gRPC stream); returns false only if the destination cell
	// no longer exists on this process — typically because a concurrent
	// merge commit just deleted it. The caller (HandoffDriver) MUST NOT
	// demote the source on a false return; the next BoundarySystem tick
	// will re-detect the crossing and retry.
	SendHandoff(destCellID MeshCellID, payload *HandoffPayload) bool
	// SendForwardInput forwards a player input frame to the new owner cell and
	// reports whether the destination path accepted it. A false result must be
	// retried; draining a source queue is not itself delivery.
	SendForwardInput(destCellID MeshCellID, payload *ForwardInputPayload) bool
}

// NoopBridge is a no-op implementation for single-cell mode.
type NoopBridge struct{}

func (NoopBridge) PreTick()                                               {}
func (NoopBridge) PostSystems()                                           {}
func (NoopBridge) CellOwner(CellID) string                                { return "" }
func (NoopBridge) CellOwnerAtPos(float32, float32) string                 { return "" }
func (NoopBridge) OnPlayerTransfer(uint32, MeshCellID)                    {}
func (NoopBridge) RequestRespawn(uint32, string)                          {}
func (NoopBridge) SendAction(MeshCellID, *CrossCellAction)                {}
func (NoopBridge) SendBorderFrame(MeshCellID, MeshCellID, []byte)         {}
func (NoopBridge) SendHandoff(MeshCellID, *HandoffPayload) bool           { return true }
func (NoopBridge) SendForwardInput(MeshCellID, *ForwardInputPayload) bool { return true }
