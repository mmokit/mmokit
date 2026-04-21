package universe

import "github.com/zenion/mmoserver/pkg/coords"

// cellBridge implements Bridge for multi-cell mode.
type cellBridge struct {
	cell             *Cell
	coord            *Process
	borderDispatcher *BorderDispatcher
	handoffDriver    *HandoffDriver
}

func (b *cellBridge) PreTick() {
	b.cell.Base.ClearReplicaUpdateFlags()
	b.cell.DrainInbox()
}

func (b *cellBridge) PostSystems() {
	b.ensureBorderDispatcher()
	b.ensureHandoffDriver()
	currentTick := uint64(b.cell.Engine.Tick)
	if b.borderDispatcher != nil {
		b.borderDispatcher.Tick(currentTick)
	}
	if b.handoffDriver != nil {
		b.handoffDriver.Tick(currentTick)
	}
	b.cell.Base.ExpireReplicas()
}

// ensureHandoffDriver lazily constructs the HandoffDriver on first
// PostSystems call. The driver drains the WorldBase crossing-event
// queue and emits Prepare+Commit messages to destination cells.
//
// Captures the cell's CURRENT outer Bridge (which may be a grpcBridge
// wrapping this cellBridge in multi-host mode) so handoff dispatches
// go through the outer bridge's routing decisions instead of
// short-circuiting back into cellBridge. In single-host mode
// b.cell.Bridge == b and the behavior is unchanged.
func (b *cellBridge) ensureHandoffDriver() {
	if b.handoffDriver != nil {
		return
	}
	if b.cell == nil || b.cell.Base == nil {
		return
	}
	outer := b.cell.Bridge
	if outer == nil {
		outer = b
	}
	b.handoffDriver = NewHandoffDriver(b.cell.Base, outer)
}

// ensureBorderDispatcher lazily constructs the BorderDispatcher on first
// PostSystems call, once the neighbor map is populated. It is also
// re-invoked implicitly after invalidateBorderDispatcher nils the field
// (e.g., after a cell split/merge rewires the Cell.Neighbors map).
func (b *cellBridge) ensureBorderDispatcher() {
	if b.borderDispatcher != nil {
		return
	}
	if b.cell == nil || b.cell.Base == nil {
		return
	}
	neighbors := b.cell.Neighbors
	if len(neighbors) == 0 {
		return
	}
	viewers := make(map[string]*CellViewer, len(neighbors))
	info := b.neighborInfo()
	for destID, destCell := range neighbors {
		ni, ok := info[destID]
		if !ok {
			continue
		}
		bx, by := neighborBoundaryMidpoint(b.cell.Cell, ni.DX, ni.DY)
		nv := NewCellViewer(destID, CellViewerID(destID), bx, by, nil, b.cell, destCell)
		nv.SetDirection(ni.DX, ni.DY)
		viewers[destID] = nv
	}
	b.borderDispatcher = NewBorderDispatcher(b.cell.Base, viewers)
}

// HandoffDriver returns the lazily-constructed HandoffDriver for this
// bridge, or nil if it has not yet been created. Used by cell.go's
// MsgHandoffCancel handler to release stuck state-machine entries on
// the source cell when a dest-side watchdog cancel arrives.
func (b *cellBridge) HandoffDriver() *HandoffDriver {
	return b.handoffDriver
}

// invalidateBorderDispatcher drops the cached dispatcher and its
// CellViewer set so the next PostSystems tick will rebuild them from
// the current Cell.Neighbors map. Called by the coordinator after
// cell split/merge topology changes rewire neighbor relationships.
// Without this call, the cached viewers would keep pointing at stale
// neighbors and miss newly-split siblings.
func (b *cellBridge) invalidateBorderDispatcher() {
	b.borderDispatcher = nil
}

// neighborBoundaryMidpoint computes the world-space midpoint of the shared
// edge between a cell and its neighbor in direction (dx, dy).
func neighborBoundaryMidpoint(cell CellID, dx, dy int32) (float32, float32) {
	minX, minY, maxX, maxY := cell.WorldBounds(coords.CellSize)
	cx := (minX + maxX) / 2
	cy := (minY + maxY) / 2
	halfW := (maxX - minX) / 2
	halfH := (maxY - minY) / 2
	return cx + float32(dx)*halfW, cy + float32(dy)*halfH
}

func (b *cellBridge) CellOwner(cell CellID) string {
	b.coord.mu.RLock()
	defer b.coord.mu.RUnlock()
	return b.coord.CellOwner[cell]
}

func (b *cellBridge) CellOwnerAtPos(worldX, worldY float32) string {
	b.coord.mu.RLock()
	baseCellSize := b.coord.baseCellSize()
	// First check CellOwner — has full CellID structs including depth info
	// for dynamic cells. In `all` preset mode this covers every cell in the
	// grid; in remote-host mode it only covers LOCAL cells.
	for cell, cellID := range b.coord.CellOwner {
		minX, minY, maxX, maxY := cell.WorldBounds(baseCellSize)
		if worldX >= minX && worldX < maxX && worldY >= minY && worldY < maxY {
			b.coord.mu.RUnlock()
			return cellID
		}
	}
	b.coord.mu.RUnlock()
	// Node mode fallback: AllOwnedCells covers hostRegistry + cellToHostMap
	// (the full cluster view from PeerList broadcasts, including cells on peer
	// nodes). Without this, BoundarySystem would clamp players back inside the
	// local node's bounds on every cross-cell boundary crossing, breaking
	// multi-process handoffs entirely.
	var found string
	b.coord.Control.AllOwnedCells(func(cellIDStr, _ string) bool {
		cell, err := ParseCellID(cellIDStr)
		if err != nil {
			return true
		}
		minX, minY, maxX, maxY := cell.WorldBounds(baseCellSize)
		if worldX >= minX && worldX < maxX && worldY >= minY && worldY < maxY {
			found = cellIDStr
			return false
		}
		return true
	})
	return found
}

func (b *cellBridge) OnPlayerTransfer(connID uint32, destCellID string) {
	b.coord.setPlayerNode(connID, destCellID)
	b.cell.Log.Log(CatMeshTransfer, "[%s] player transfer: conn=%d -> %s", b.cell.ID, connID, destCellID)
}

func (b *cellBridge) RelayChatToOtherCells(username, text string) {
	b.cell.Log.Log(CatMeshMsg, "[%s] relaying chat from %s to %d cells", b.cell.ID, username, len(b.coord.Cells)-1)
	for _, other := range b.coord.Cells {
		if other.ID == b.cell.ID {
			continue
		}
		other.Inbox <- CellMessage{
			Type:       MsgChat,
			FromCellID: b.cell.ID,
			Chat:       &ChatRelay{Username: username, Text: text},
		}
	}
}

func (b *cellBridge) RequestRespawn(connID uint32, username string) {
	b.cell.Log.Log(CatMeshMsg, "[%s] requesting respawn: conn=%d user=%s", b.cell.ID, connID, username)
	// Resolve respawn point through the same path as login: SpawnResolver if
	// registered, else Config.DefaultSpawn. Then CellAtPosition converts the
	// world coord to the current owning cell — topology-independent.
	b.coord.mu.RLock()
	resolver := b.coord.spawnResolver
	b.coord.mu.RUnlock()
	worldX, worldY := b.coord.cfg.DefaultSpawn.X, b.coord.cfg.DefaultSpawn.Y
	if resolver != nil {
		if x, y, ok := resolver(username); ok {
			worldX, worldY = x, y
		}
	}
	targetCellID := b.coord.CellAtPosition(worldX, worldY)
	if targetCellID == "" {
		for id := range b.coord.Cells {
			targetCellID = id
			break
		}
	}
	if targetCellID == "" {
		return
	}
	dest, ok := b.coord.Cells[targetCellID]
	if !ok {
		return
	}
	dest.Inbox <- CellMessage{
		Type:       MsgSpawnTransfer,
		FromCellID: b.cell.ID,
		Spawn:      &SpawnTransfer{ConnID: connID, Username: username},
	}
	b.coord.setPlayerNode(connID, targetCellID)
}

func (b *cellBridge) SendAction(targetCellID string, action *CrossCellAction) {
	b.cell.Log.Log(CatMeshAction, "[%s] sending action type=%d targetNetID=%d -> %s", b.cell.ID, action.Type, action.TargetNetID, targetCellID)
	if dest, ok := b.coord.Cells[targetCellID]; ok {
		dest.Inbox <- CellMessage{
			Type:       MsgCrossCellAction,
			FromCellID: b.cell.ID,
			Action:     action,
		}
	}
}

func (b *cellBridge) SendActionResult(targetCellID string, result *ActionResult) {
	if dest, ok := b.coord.Cells[targetCellID]; ok {
		dest.Inbox <- CellMessage{
			Type:         MsgActionResult,
			FromCellID:   b.cell.ID,
			ActionResult: result,
		}
	}
}

// SendBorderFrame wraps an encoded replication.Frame in a CellMessage
// and pushes it into the destination cell's inbox. Non-blocking: drops
// on full inbox. This is the direct-channel path used by both
// single-host colocated mode and the local-shortcut fall-through in
// multi-host mode (when grpcBridge.resolveDest says the destination
// is local).
func (b *cellBridge) SendBorderFrame(destCellID, fromCellID string, encoded []byte) {
	b.coord.mu.RLock()
	dest, ok := b.coord.Cells[destCellID]
	b.coord.mu.RUnlock()
	if !ok || dest == nil {
		return
	}
	msg := CellMessage{
		Type:        MsgBorderFrame,
		FromCellID:  fromCellID,
		BorderFrame: encoded,
	}
	select {
	case dest.Inbox <- msg:
	default:
		// inbox full; drop — 30-tick resync recovers
	}
}

func (b *cellBridge) SendHandoffPrepare(destCellID string, payload *HandoffPreparePayload) bool {
	b.cell.Log.Log(CatMeshTransfer, "[%s] sending handoff prepare: netID=%d -> %s epoch=%d", b.cell.ID, payload.NetID, destCellID, payload.Epoch)
	b.coord.mu.RLock()
	dest, ok := b.coord.Cells[destCellID]
	b.coord.mu.RUnlock()
	if !ok {
		b.cell.Log.Log(CatMeshTransfer, "[%s] handoff dest gone: prepare netID=%d -> %s (cell deleted from coord.Cells, source will retry next tick)",
			b.cell.ID, payload.NetID, destCellID)
		return false
	}
	dest.Inbox <- CellMessage{
		Type:           MsgHandoffPrepare,
		FromCellID:     b.cell.ID,
		HandoffPrepare: payload,
	}
	return true
}

func (b *cellBridge) SendHandoffCommit(destCellID string, payload *HandoffCommitPayload) bool {
	b.cell.Log.Log(CatMeshTransfer, "[%s] sending handoff commit: netID=%d -> %s epoch=%d tick=%d", b.cell.ID, payload.NetID, destCellID, payload.Epoch, payload.CommitTick)
	b.coord.mu.RLock()
	dest, ok := b.coord.Cells[destCellID]
	b.coord.mu.RUnlock()
	if !ok {
		b.cell.Log.Log(CatMeshTransfer, "[%s] handoff dest gone: commit netID=%d -> %s (cell deleted from coord.Cells)",
			b.cell.ID, payload.NetID, destCellID)
		return false
	}
	dest.Inbox <- CellMessage{
		Type:          MsgHandoffCommit,
		FromCellID:    b.cell.ID,
		HandoffCommit: payload,
	}
	return true
}

func (b *cellBridge) SendHandoffCancel(destCellID string, payload *HandoffCancelPayload) {
	b.cell.Log.Log(CatMeshTransfer, "[%s] sending handoff cancel: netID=%d -> %s epoch=%d", b.cell.ID, payload.NetID, destCellID, payload.Epoch)
	b.coord.mu.RLock()
	dest, ok := b.coord.Cells[destCellID]
	b.coord.mu.RUnlock()
	if !ok {
		return
	}
	select {
	case dest.Inbox <- CellMessage{
		Type:          MsgHandoffCancel,
		FromCellID:    b.cell.ID,
		HandoffCancel: payload,
	}:
	default:
		// inbox full, drop (matches existing patterns)
	}
}

func (b *cellBridge) SendForwardInput(destCellID string, payload *ForwardInputPayload) {
	b.coord.mu.RLock()
	dest, ok := b.coord.Cells[destCellID]
	b.coord.mu.RUnlock()
	if !ok {
		return
	}
	dest.Inbox <- CellMessage{
		Type:         MsgForwardInput,
		FromCellID:   b.cell.ID,
		ForwardInput: payload,
	}
}

// neighborInfo builds the neighbor map used by border scanning.
// Computes DX/DY from actual cell world bounds so direction-based replica
// scanning works correctly across any depth mix, including siblings within
// the same root cell after a split.
func (b *cellBridge) neighborInfo() map[string]NeighborInfo {
	b.coord.mu.RLock()
	defer b.coord.mu.RUnlock()

	baseCellSize := b.coord.baseCellSize()
	neighbors := make(map[string]NeighborInfo, len(b.cell.Neighbors))
	for nID, neighbor := range b.cell.Neighbors {
		dx, dy := CellDirection(b.cell.Cell, neighbor.Cell, baseCellSize)
		neighbors[nID] = NeighborInfo{
			CellID: nID,
			DX:     dx,
			DY:     dy,
		}
	}
	return neighbors
}
