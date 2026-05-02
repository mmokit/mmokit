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
	b.cell.DrainInbox()
}

func (b *cellBridge) PostSystems() {
	b.ensureBorderDispatcher()
	b.ensureHandoffDriver()
	// Cluster-coherent tick index for hard-cut handoff CommitTick
	// arithmetic. Both source (HandoffDriver.Tick) and destination
	// (Cell.drainPendingPromotes) derive commit ticks from this shared
	// axis so a CommitTick value commutes across asynchronously-ticking
	// cells. Falls back to local engine tick when the cluster clock is
	// missing (test WorldBases without a Process wire a pre-observed
	// clock so this is normally always available).
	var clusterTick uint64
	if cc := b.cell.Stage.clusterClock; cc != nil {
		clusterTick = cc.ClusterTick(b.cell.Engine.TickIntervalMs())
	} else {
		clusterTick = uint64(b.cell.Engine.Tick)
	}
	// Drain destination-side promotes FIRST so the promoted entity is
	// included in this tick's outbound border frame and outbound client
	// replication — the first authoritative sample from the new owner.
	b.cell.drainPendingPromotes(clusterTick)
	if b.borderDispatcher != nil {
		b.borderDispatcher.Tick(clusterTick)
	}
	if b.handoffDriver != nil {
		b.handoffDriver.Tick(clusterTick)
	}
	b.cell.Stage.ExpireReplicas()
}

// ensureHandoffDriver lazily constructs the HandoffDriver on first
// PostSystems call. The driver drains the Stage crossing-event
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
	if b.cell == nil || b.cell.Stage == nil {
		return
	}
	outer := b.cell.Bridge
	if outer == nil {
		outer = b
	}
	b.handoffDriver = NewHandoffDriver(b.cell.Stage, outer)
}

// ensureBorderDispatcher lazily constructs the BorderDispatcher on first
// PostSystems call, once the neighbor map is populated. It is also
// re-invoked implicitly after invalidateBorderDispatcher nils the field
// (e.g., after a cell split/merge rewires the Cell.Neighbors map).
func (b *cellBridge) ensureBorderDispatcher() {
	if b.borderDispatcher != nil {
		return
	}
	if b.cell == nil || b.cell.Stage == nil {
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
	b.borderDispatcher = NewBorderDispatcher(b.cell.Stage, viewers)
}

// HandoffDriver returns the lazily-constructed HandoffDriver for this
// bridge, or nil if it has not yet been created. Kept for the
// handoffDriverHost interface used by grpcBridge — Phase G of the
// Replication Timeline Redesign collapses both when the Bridge
// interface shrinks to a single SendHandoff method.
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
	return string(b.coord.CellOwner[cell])
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
			return string(cellID)
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
	b.cell.Log.Log(CatMeshTransfer, "[%s] player transfer: conn=%d -> %s", b.cell.MeshID, connID, destCellID)
}

func (b *cellBridge) RelayChatToOtherCells(username, text string) {
	b.cell.Log.Log(CatMeshMsg, "[%s] relaying chat from %s to %d cells", b.cell.MeshID, username, len(b.coord.Cells)-1)
	for _, other := range b.coord.Cells {
		if other.MeshID == b.cell.MeshID {
			continue
		}
		other.Inbox <- CellMessage{
			Type:       MsgChat,
			FromCellID: string(b.cell.MeshID),
			Chat:       &ChatRelay{Username: username, Text: text},
		}
	}
}

func (b *cellBridge) RequestRespawn(connID uint32, username string) {
	b.cell.Log.Log(CatMeshMsg, "[%s] requesting respawn: conn=%d user=%s", b.cell.MeshID, connID, username)
	b.coord.mu.RLock()
	resolver := b.coord.spawnResolver
	defaultSpawn := b.coord.cfg.DefaultSpawn
	b.coord.mu.RUnlock()

	loc := defaultSpawn
	if resolver != nil {
		if resolved, ok := resolver(username); ok {
			loc = resolved
		}
	}

	targetCellID := b.coord.CellAtPosition(loc.X, loc.Y)
	if targetCellID == "" {
		b.cell.Log.Log(CatMeshMsg,
			"[%s] respawn rejected: location (%f,%f) outside world bounds (user=%s)",
			b.cell.MeshID, loc.X, loc.Y, username)
		return
	}
	dest, ok := b.coord.Cells[MeshCellID(targetCellID)]
	if !ok {
		b.cell.Log.Log(CatMeshMsg,
			"[%s] respawn rejected: cell %s no longer owned (user=%s)",
			b.cell.MeshID, targetCellID, username)
		return
	}
	dest.Inbox <- CellMessage{
		Type:       MsgSpawnTransfer,
		FromCellID: string(b.cell.MeshID),
		Spawn: &SpawnTransfer{
			ConnID:        connID,
			Username:      username,
			SpawnLocation: loc,
		},
	}
	b.coord.setPlayerNode(connID, targetCellID)
}

func (b *cellBridge) SendAction(targetCellID string, action *CrossCellAction) {
	b.cell.Log.Log(CatMeshAction, "[%s] sending action type=%d targetNetID=%d -> %s", b.cell.MeshID, action.Type, action.TargetNetID, targetCellID)
	if dest, ok := b.coord.Cells[MeshCellID(targetCellID)]; ok {
		dest.Inbox <- CellMessage{
			Type:       MsgCrossCellAction,
			FromCellID: string(b.cell.MeshID),
			Action:     action,
		}
	}
}

func (b *cellBridge) SendActionResult(targetCellID string, result *ActionResult) {
	if dest, ok := b.coord.Cells[MeshCellID(targetCellID)]; ok {
		dest.Inbox <- CellMessage{
			Type:         MsgActionResult,
			FromCellID:   string(b.cell.MeshID),
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
	dest, ok := b.coord.Cells[MeshCellID(destCellID)]
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

func (b *cellBridge) SendHandoff(destCellID string, payload *HandoffPayload) bool {
	b.cell.Log.Log(CatMeshTransfer, "[%s] sending handoff: netID=%d -> %s epoch=%d commitTick=%d", b.cell.MeshID, payload.NetID, destCellID, payload.Epoch, payload.CommitTick)
	b.coord.mu.RLock()
	dest, ok := b.coord.Cells[MeshCellID(destCellID)]
	b.coord.mu.RUnlock()
	if !ok {
		b.cell.Log.Log(CatMeshTransfer, "[%s] handoff dest gone: netID=%d -> %s (cell deleted from coord.Cells, source will retry next tick)",
			b.cell.MeshID, payload.NetID, destCellID)
		return false
	}
	dest.Inbox <- CellMessage{
		Type:       MsgHandoff,
		FromCellID: string(b.cell.MeshID),
		Handoff:    payload,
	}
	return true
}

func (b *cellBridge) SendForwardInput(destCellID string, payload *ForwardInputPayload) {
	b.coord.mu.RLock()
	dest, ok := b.coord.Cells[MeshCellID(destCellID)]
	b.coord.mu.RUnlock()
	if !ok {
		return
	}
	dest.Inbox <- CellMessage{
		Type:         MsgForwardInput,
		FromCellID:   string(b.cell.MeshID),
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
