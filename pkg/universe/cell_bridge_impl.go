package universe

import (
	"sync"

	"github.com/zenion/mmoserver/pkg/coords"
	"github.com/zenion/mmoserver/pkg/engine"
)

// cellBridge implements Bridge for multi-cell mode.
type cellBridge struct {
	cell             *Cell
	coord            *Process
	borderMu         sync.Mutex
	borderDispatcher *BorderDispatcher
	handoffDriver    *HandoffDriver
}

func (b *cellBridge) PreTick() {
	b.cell.DrainInbox()
}

func (b *cellBridge) PostSystems() {
	borderDispatcher := b.ensureBorderDispatcher()
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
	if borderDispatcher != nil {
		borderDispatcher.Tick(clusterTick)
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
func (b *cellBridge) ensureBorderDispatcher() *BorderDispatcher {
	if b.cell == nil || b.cell.Stage == nil {
		return nil
	}
	b.borderMu.Lock()
	cached := b.borderDispatcher
	b.borderMu.Unlock()
	if cached != nil {
		return cached
	}

	// Neighbors is owned by Process.mu. Take locks in the repository-wide
	// Process-before-child order also used by reconcileCellNeighbors, then
	// build/install one immutable dispatcher snapshot. If topology changes
	// immediately afterward, invalidation clears the cached pointer while this
	// tick may safely finish using the returned snapshot.
	b.coord.mu.RLock()
	defer b.coord.mu.RUnlock()
	b.borderMu.Lock()
	defer b.borderMu.Unlock()
	if b.borderDispatcher != nil {
		return b.borderDispatcher
	}
	neighbors := b.cell.Neighbors
	if len(neighbors) == 0 {
		return nil
	}
	viewers := make(map[string]*CellViewer, len(neighbors))
	baseCellSize := b.coord.baseCellSize()
	for destID, destCell := range neighbors {
		destStr := string(destID)
		if destCell == nil {
			continue
		}
		dx, dy := CellDirection(b.cell.Cell, destCell.Cell, baseCellSize)
		bx, by := neighborBoundaryMidpoint(b.cell.Cell, dx, dy)
		nv := NewCellViewer(MeshCellID(destStr), CellViewerID(destStr), bx, by, nil, b.cell, destCell)
		nv.SetDirection(dx, dy)
		viewers[destStr] = nv
	}
	b.borderDispatcher = NewBorderDispatcher(b.cell.Stage, viewers)
	return b.borderDispatcher
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
	b.borderMu.Lock()
	b.borderDispatcher = nil
	b.borderMu.Unlock()
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

func (b *cellBridge) OnPlayerTransfer(connID uint32, destCellID MeshCellID) {
	gatewayConnID, newRouteEpoch := b.advancePlayerRoute(connID, destCellID)
	// Refresh the gateway's cached localSession.cellID so the next WS
	// disconnect routes its event to the destination cell (not the stale
	// origin). Without this, the disconnect event is dispatched to the
	// pre-transfer cell, which already scrubbed the connID via
	// RemoveTransferred — the destination cell never sees the disconnect,
	// the session stays StateActive instead of StateDisconnected, and
	// the next refresh is treated as a fresh login (kick + spawn duplicate).
	// Cross-host transfers go through notifyPlayerMigrated which already
	// fires OnUpstreamSwitch; this branch covers the same-host path that
	// grpcBridge.OnPlayerTransfer routes through us.
	if b.coord != nil && b.coord.gateway != nil {
		if sess := b.coord.gateway.lookupSession(gatewayConnID); sess != nil {
			b.coord.gateway.OnUpstreamSwitch(gatewayConnID, sess.hostID, destCellID, newRouteEpoch)
		}
	}
	b.cell.Log.Log(CatMeshTransfer, "[%s] player transfer: conn=%d -> %s", b.cell.MeshID, connID, destCellID)
}

// advancePlayerRoute advances the coordinator's transport-fencing route for a
// real same-host source change. Direct/in-process sessions use connID as-is.
// When a VCM coexists (for example embedded always-proxy mode), connID is
// node-local: resolve the stable gateway key, advance that composite route,
// then re-register the returned route epoch in the VCM. StreamGeneration is a
// separate replication counter and is intentionally not read or written here.
func (b *cellBridge) advancePlayerRoute(connID uint32, destCellID MeshCellID) (gatewayConnID uint32, newRouteEpoch uint64) {
	if b.coord != nil && b.coord.vcm != nil {
		if key, vcmEpoch, ok := b.coord.vcm.LookupRouteByLocal(connID); ok {
			newRouteEpoch = b.coord.sessionRoutes.AdvanceCellFrom(key, destCellID, vcmEpoch)
			username := ""
			if sess := b.cell.Engine.Players.ByConnID(connID); sess != nil {
				username = sess.Username
			}
			b.coord.vcm.RegisterSession(key, username, newRouteEpoch, destCellID)
			return key.ConnID, newRouteEpoch
		}
	}
	return connID, b.coord.setPlayerNode(connID, destCellID)
}

func (b *cellBridge) RequestRespawn(connID uint32, username string) {
	b.cell.Log.Log(CatMeshMsg, "[%s] requesting respawn: conn=%d user=%s", b.cell.MeshID, connID, username)
	b.coord.mu.RLock()
	resolver := b.coord.spawnResolver
	b.coord.mu.RUnlock()

	// A cross-cell respawn creates a fresh replication source. Carry N+1 to
	// the destination while leaving the source session pinned at N. Same-cell
	// respawns keep the existing stream because the cell's ReplicationSystem
	// and its per-viewer sequence continue.
	streamGeneration := uint32(0)
	if sourceSession := b.cell.Engine.Players.ByConnID(connID); sourceSession != nil {
		streamGeneration = sourceSession.StreamGeneration
	}

	var loc coords.Location
	if resolver != nil {
		// Respawn: we don't have UserID at this layer (the cell bridge takes
		// only connID + username). The resolver still gets a session with the
		// fields available; games that need UserID for respawn should add it
		// at a higher level.
		session := &engine.PlayerSession{ConnID: connID, Username: username}
		loc = resolver(session)
	} else {
		loc = defaultSpawnLocation()
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
	targetMeshID := MeshCellID(targetCellID)
	if targetMeshID != b.cell.MeshID {
		streamGeneration++
		gatewayConnID, newRouteEpoch := b.advancePlayerRoute(connID, targetMeshID)
		if b.coord.gateway != nil {
			if sess := b.coord.gateway.lookupSession(gatewayConnID); sess != nil {
				b.coord.gateway.OnUpstreamSwitch(gatewayConnID, sess.hostID, targetMeshID, newRouteEpoch)
			}
		}
	} else {
		// Preserve route establishment for tests and local setups that invoke
		// respawn before an assignment route exists, without advancing an
		// existing same-cell route or restarting the replication stream.
		key := SessionKey{GatewayID: InprocGatewayID, ConnID: connID}
		epoch := uint64(1)
		if b.coord.vcm != nil {
			if vcmKey, vcmEpoch, ok := b.coord.vcm.LookupRouteByLocal(connID); ok {
				key = vcmKey
				epoch = vcmEpoch
			}
		}
		if _, ok := b.coord.sessionRoutes.Get(key); !ok {
			b.coord.sessionRoutes.Set(&SessionRoute{Key: key, CellID: targetMeshID, Epoch: epoch})
		}
	}
	dest.Inbox <- CellMessage{
		Type:       MsgSpawnTransfer,
		FromCellID: b.cell.MeshID,
		Spawn: &SpawnTransfer{
			ConnID:           connID,
			Username:         username,
			StreamGeneration: streamGeneration,
			SpawnLocation:    loc,
		},
	}
}

func (b *cellBridge) SendAction(targetCellID MeshCellID, action *CrossCellAction) {
	b.cell.Log.Log(CatMeshAction, "[%s] sending action type=%d targetNetID=%d -> %s", b.cell.MeshID, action.Type, action.TargetNetID, targetCellID)
	if dest, ok := b.coord.Cells[targetCellID]; ok {
		dest.Inbox <- CellMessage{
			Type:       MsgCrossCellAction,
			FromCellID: b.cell.MeshID,
			Action:     action,
		}
	}
}

// SendBorderFrame wraps an encoded replication.Frame in a CellMessage
// and pushes it into the destination cell's inbox. Non-blocking: drops
// on full inbox. This is the direct-channel path used by both
// single-host colocated mode and the local-shortcut fall-through in
// multi-host mode (when grpcBridge.resolveDest says the destination
// is local).
func (b *cellBridge) SendBorderFrame(destCellID, fromCellID MeshCellID, encoded []byte) {
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

func (b *cellBridge) SendHandoff(destCellID MeshCellID, payload *HandoffPayload) bool {
	b.cell.Log.Log(CatMeshTransfer, "[%s] sending handoff: netID=%d -> %s epoch=%d commitTick=%d", b.cell.MeshID, payload.NetID, destCellID, payload.Epoch, payload.CommitTick)
	b.coord.mu.RLock()
	dest, ok := b.coord.Cells[destCellID]
	b.coord.mu.RUnlock()
	if !ok {
		b.cell.Log.Log(CatMeshTransfer, "[%s] handoff dest gone: netID=%d -> %s (cell deleted from coord.Cells, source will retry next tick)",
			b.cell.MeshID, payload.NetID, destCellID)
		return false
	}
	dest.Inbox <- CellMessage{
		Type:       MsgHandoff,
		FromCellID: b.cell.MeshID,
		Handoff:    payload,
	}
	return true
}

func (b *cellBridge) SendForwardInput(destCellID MeshCellID, payload *ForwardInputPayload) bool {
	b.coord.mu.RLock()
	dest, ok := b.coord.Cells[destCellID]
	b.coord.mu.RUnlock()
	if !ok {
		return false
	}
	dest.Inbox <- CellMessage{
		Type:         MsgForwardInput,
		FromCellID:   b.cell.MeshID,
		ForwardInput: payload,
	}
	return true
}
