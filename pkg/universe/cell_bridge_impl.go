package universe

import "github.com/zenion/mmoserver/pkg/coords"

// cellBridge implements Bridge for multi-cell mode.
type cellBridge struct {
	cell             *Cell
	coord            *Coordinator
	borderDispatcher *BorderDispatcher
}

func (b *cellBridge) PreTick() {
	b.cell.Base.ClearReplicaUpdateFlags()
	b.cell.DrainInbox()
}

func (b *cellBridge) PostSystems() {
	b.ensureBorderDispatcher()
	if b.borderDispatcher != nil {
		currentTick := uint64(b.cell.Engine.Tick)
		b.borderDispatcher.Tick(currentTick)
	}
	b.cell.Base.ExpireReplicas()
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

func (b *cellBridge) NodeOwner(cell CellID) string {
	b.coord.mu.RLock()
	defer b.coord.mu.RUnlock()
	return b.coord.CellOwner[cell]
}

func (b *cellBridge) NodeOwnerAtPos(worldX, worldY float32) string {
	b.coord.mu.RLock()
	defer b.coord.mu.RUnlock()
	for cell, cellID := range b.coord.CellOwner {
		minX, minY, maxX, maxY := cell.WorldBounds(b.coord.baseCellSize())
		if worldX >= minX && worldX < maxX && worldY >= minY && worldY < maxY {
			return cellID
		}
	}
	return ""
}

func (b *cellBridge) SendTransfer(destCellID string, data []byte, netID uint32) {
	b.cell.Log.Log(CatMeshTransfer, "[%s] sending transfer: netID=%d -> %s (%d bytes)", b.cell.ID, netID, destCellID, len(data))
	b.coord.mu.RLock()
	dest, ok := b.coord.Cells[destCellID]
	b.coord.mu.RUnlock()
	if ok {
		dest.Inbox <- CellMessage{
			Type:          MsgTransfer,
			FromCellID:    b.cell.ID,
			TransferNetID: netID,
			Transfer:      data,
		}
	}
}

func (b *cellBridge) SendArrivalConfirm(destCellID string, confirm *ArrivalConfirmMsg) {
	if dest, ok := b.coord.Cells[destCellID]; ok {
		dest.Inbox <- CellMessage{
			Type:           MsgArrivalConfirm,
			FromCellID:     b.cell.ID,
			ArrivalConfirm: confirm,
		}
	}
}

func (b *cellBridge) OnPlayerTransfer(connID uint32, destCellID string) {
	b.coord.setPlayerNode(connID, destCellID)
	b.cell.Log.Log(CatMeshTransfer, "[%s] player transfer: conn=%d -> %s", b.cell.ID, connID, destCellID)
}

func (b *cellBridge) RelayChatToOtherNodes(username, text string) {
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
	var targetCellID string
	if b.coord.playerRouter != nil {
		targetCellID = b.coord.playerRouter(username)
	}
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

func (b *cellBridge) SendAction(targetCellID string, action *CrossNodeAction) {
	b.cell.Log.Log(CatMeshAction, "[%s] sending action type=%d targetNetID=%d -> %s", b.cell.ID, action.Type, action.TargetNetID, targetCellID)
	if dest, ok := b.coord.Cells[targetCellID]; ok {
		dest.Inbox <- CellMessage{
			Type:       MsgCrossNodeAction,
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
			NodeID: nID,
			DX:     dx,
			DY:     dy,
		}
	}
	return neighbors
}
