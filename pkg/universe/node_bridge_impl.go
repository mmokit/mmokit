package universe

import "github.com/zenion/mmoserver/pkg/coords"

// nodeBridge implements NodeBridge for multi-node mode.
type nodeBridge struct {
	node             *Node
	coord            *Coordinator
	borderDispatcher *BorderDispatcher
}

func (b *nodeBridge) PreTick() {
	b.node.Base.ClearReplicaUpdateFlags()
	b.node.DrainInbox()
}

func (b *nodeBridge) PostSystems() {
	b.ensureBorderDispatcher()
	if b.borderDispatcher != nil {
		currentTick := uint64(b.node.Engine.Tick)
		b.borderDispatcher.Tick(currentTick)
	}
	b.node.Base.ExpireReplicas()
}

// ensureBorderDispatcher lazily constructs the BorderDispatcher on first
// PostSystems call, once the neighbor map is populated. It is also
// re-invoked implicitly after invalidateBorderDispatcher nils the field
// (e.g., after a cell split/merge rewires the Node.Neighbors map).
func (b *nodeBridge) ensureBorderDispatcher() {
	if b.borderDispatcher != nil {
		return
	}
	if b.node == nil || b.node.Base == nil {
		return
	}
	neighbors := b.node.Neighbors
	if len(neighbors) == 0 {
		return
	}
	viewers := make(map[string]*NodeViewer, len(neighbors))
	info := b.neighborInfo()
	for destID, destNode := range neighbors {
		ni, ok := info[destID]
		if !ok {
			continue
		}
		bx, by := neighborBoundaryMidpoint(b.node.Cell, ni.DX, ni.DY)
		nv := NewNodeViewer(destID, NodeViewerID(destID), bx, by, nil, b.node, destNode)
		nv.SetDirection(ni.DX, ni.DY)
		viewers[destID] = nv
	}
	b.borderDispatcher = NewBorderDispatcher(b.node.Base, viewers)
}

// invalidateBorderDispatcher drops the cached dispatcher and its
// NodeViewer set so the next PostSystems tick will rebuild them from
// the current Node.Neighbors map. Called by the coordinator after
// cell split/merge topology changes rewire neighbor relationships.
// Without this call, the cached viewers would keep pointing at stale
// neighbors and miss newly-split siblings.
func (b *nodeBridge) invalidateBorderDispatcher() {
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

func (b *nodeBridge) NodeOwner(cell CellID) string {
	b.coord.mu.RLock()
	defer b.coord.mu.RUnlock()
	return b.coord.NodeOwner[cell]
}

func (b *nodeBridge) NodeOwnerAtPos(worldX, worldY float32) string {
	b.coord.mu.RLock()
	defer b.coord.mu.RUnlock()
	for cell, nodeID := range b.coord.NodeOwner {
		minX, minY, maxX, maxY := cell.WorldBounds(b.coord.baseCellSize())
		if worldX >= minX && worldX < maxX && worldY >= minY && worldY < maxY {
			return nodeID
		}
	}
	return ""
}

func (b *nodeBridge) SendTransfer(destNodeID string, data []byte, netID uint32) {
	b.node.Log.Log(CatMeshTransfer, "[%s] sending transfer: netID=%d -> %s (%d bytes)", b.node.ID, netID, destNodeID, len(data))
	b.coord.mu.RLock()
	dest, ok := b.coord.Nodes[destNodeID]
	b.coord.mu.RUnlock()
	if ok {
		dest.Inbox <- NodeMessage{
			Type:          MsgTransfer,
			FromNodeID:    b.node.ID,
			TransferNetID: netID,
			Transfer:      data,
		}
	}
}

func (b *nodeBridge) SendArrivalConfirm(destNodeID string, confirm *ArrivalConfirmMsg) {
	if dest, ok := b.coord.Nodes[destNodeID]; ok {
		dest.Inbox <- NodeMessage{
			Type:           MsgArrivalConfirm,
			FromNodeID:     b.node.ID,
			ArrivalConfirm: confirm,
		}
	}
}

func (b *nodeBridge) OnPlayerTransfer(connID uint32, destNodeID string) {
	b.coord.setPlayerNode(connID, destNodeID)
	b.node.Log.Log(CatMeshTransfer, "[%s] player transfer: conn=%d -> %s", b.node.ID, connID, destNodeID)
}

func (b *nodeBridge) RelayChatToOtherNodes(username, text string) {
	b.node.Log.Log(CatMeshMsg, "[%s] relaying chat from %s to %d nodes", b.node.ID, username, len(b.coord.Nodes)-1)
	for _, other := range b.coord.Nodes {
		if other.ID == b.node.ID {
			continue
		}
		other.Inbox <- NodeMessage{
			Type:       MsgChat,
			FromNodeID: b.node.ID,
			Chat:       &ChatRelay{Username: username, Text: text},
		}
	}
}

func (b *nodeBridge) RequestRespawn(connID uint32, username string) {
	b.node.Log.Log(CatMeshMsg, "[%s] requesting respawn: conn=%d user=%s", b.node.ID, connID, username)
	var targetNodeID string
	if b.coord.playerRouter != nil {
		targetNodeID = b.coord.playerRouter(username)
	}
	if targetNodeID == "" {
		for id := range b.coord.Nodes {
			targetNodeID = id
			break
		}
	}
	if targetNodeID == "" {
		return
	}
	dest, ok := b.coord.Nodes[targetNodeID]
	if !ok {
		return
	}
	dest.Inbox <- NodeMessage{
		Type:       MsgSpawnTransfer,
		FromNodeID: b.node.ID,
		Spawn:      &SpawnTransfer{ConnID: connID, Username: username},
	}
	b.coord.setPlayerNode(connID, targetNodeID)
}

func (b *nodeBridge) SendAction(targetNodeID string, action *CrossNodeAction) {
	b.node.Log.Log(CatMeshAction, "[%s] sending action type=%d targetNetID=%d -> %s", b.node.ID, action.Type, action.TargetNetID, targetNodeID)
	if dest, ok := b.coord.Nodes[targetNodeID]; ok {
		dest.Inbox <- NodeMessage{
			Type:       MsgCrossNodeAction,
			FromNodeID: b.node.ID,
			Action:     action,
		}
	}
}

func (b *nodeBridge) SendActionResult(targetNodeID string, result *ActionResult) {
	if dest, ok := b.coord.Nodes[targetNodeID]; ok {
		dest.Inbox <- NodeMessage{
			Type:         MsgActionResult,
			FromNodeID:   b.node.ID,
			ActionResult: result,
		}
	}
}

// neighborInfo builds the neighbor map used by border scanning.
// Computes DX/DY from actual cell world bounds so direction-based replica
// scanning works correctly across any depth mix, including siblings within
// the same root cell after a split.
func (b *nodeBridge) neighborInfo() map[string]NeighborInfo {
	b.coord.mu.RLock()
	defer b.coord.mu.RUnlock()

	baseCellSize := b.coord.baseCellSize()
	neighbors := make(map[string]NeighborInfo, len(b.node.Neighbors))
	for nID, neighbor := range b.node.Neighbors {
		dx, dy := CellDirection(b.node.Cell, neighbor.Cell, baseCellSize)
		neighbors[nID] = NeighborInfo{
			NodeID: nID,
			DX:     dx,
			DY:     dy,
		}
	}
	return neighbors
}

