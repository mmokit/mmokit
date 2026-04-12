package universe

// grpcBridge is a Bridge implementation for multi-host coordinators.
// It wraps a local *cellBridge (which still handles colocated-cell
// direct-channel dispatch) and picks per destination: if the destCellID
// maps to this host, fall through to the local bridge; otherwise
// encode the CellMessage and dispatch via HostNetwork.SendLossy or
// SendReliable according to the policy matrix in the S3 plan.
//
// GatewayMode controls the shortcut decision:
//   - "local-shortcut" (default): colocated destinations take the local
//     path, exercising zero gRPC machinery.
//   - "always-proxy": every destination goes through the gRPC codec and
//     HostNetwork.Send* path, even for colocated cells. Used in tests
//     to prove the wire format round-trips end-to-end.
type grpcBridge struct {
	cell        *Cell               // source cell (for logging + FromCellID)
	coord       *Coordinator        // for cell enumeration (e.g. chat fan-out)
	host        *Host               // local host (for IsLocal shortcut)
	cellToHost  func(string) string // destCellID -> hostID
	local       *cellBridge         // fallback/delegate for colocated cells
	gatewayMode string              // "local-shortcut" | "always-proxy"
}

// newGrpcBridge constructs a grpcBridge wrapping the given local
// cellBridge. The cellToHost resolver is typically the coordinator's
// cell-ownership lookup — given a destCellID string, return the hostID
// that currently owns it (or "" if unknown).
func newGrpcBridge(cell *Cell, coord *Coordinator, host *Host, cellToHost func(string) string, local *cellBridge, gatewayMode string) *grpcBridge {
	if gatewayMode == "" {
		gatewayMode = "local-shortcut"
	}
	return &grpcBridge{
		cell:        cell,
		coord:       coord,
		host:        host,
		cellToHost:  cellToHost,
		local:       local,
		gatewayMode: gatewayMode,
	}
}

// resolveDest returns (useLocal, destHostID) in a single cellToHost lookup,
// so routing decisions and downstream dispatch share one topology snapshot.
// If useLocal is true, the caller should delegate to b.local; otherwise
// sendViaGrpc uses destHostID.
func (b *grpcBridge) resolveDest(destCellID string) (useLocal bool, destHostID string) {
	destHostID = b.cellToHost(destCellID)
	if b.gatewayMode == "always-proxy" {
		return false, destHostID
	}
	if destHostID == "" || b.host.IsLocal(destHostID) {
		return true, destHostID
	}
	return false, destHostID
}

// sendViaGrpc is the shared remote-dispatch helper. reliable=true blocks
// with deadline; reliable=false is fire-and-forget drop-on-full.
// logCat controls which log category is used for dispatch errors so that
// callers can surface handoff traffic under CatMeshTransfer, actions under
// CatMeshAction, and chat under CatMeshMsg.
func (b *grpcBridge) sendViaGrpc(destHostID, destCellID string, msg CellMessage, reliable bool, logCat string) {
	if destHostID == "" {
		b.cell.Log.Log(logCat, "[%s] grpc send: no host for cell %s", b.cell.ID, destCellID)
		return
	}
	if b.host.Network == nil {
		b.cell.Log.Log(logCat, "[%s] grpc send: local host has no Network", b.cell.ID)
		return
	}
	frame, err := encodeCellMessage(msg, destCellID)
	if err != nil {
		b.cell.Log.Log(logCat, "[%s] grpc encode %v failed: %v", b.cell.ID, msg.Type, err)
		return
	}
	if reliable {
		if err := b.host.Network.SendReliable(destHostID, frame); err != nil {
			b.cell.Log.Log(logCat, "[%s] grpc reliable send to %s failed: %v", b.cell.ID, destHostID, err)
		}
		return
	}
	if ok := b.host.Network.SendLossy(destHostID, frame); !ok {
		b.cell.Log.Log(logCat, "[%s] grpc lossy drop to %s (%v)", b.cell.ID, destHostID, msg.Type)
	}
}

// PreTick delegates to the wrapped cellBridge.
func (b *grpcBridge) PreTick() { b.local.PreTick() }

// PostSystems delegates to the wrapped cellBridge.
func (b *grpcBridge) PostSystems() { b.local.PostSystems() }

// NodeOwner delegates to the wrapped cellBridge.
func (b *grpcBridge) NodeOwner(cell CellID) string { return b.local.NodeOwner(cell) }

// NodeOwnerAtPos delegates to the wrapped cellBridge.
func (b *grpcBridge) NodeOwnerAtPos(worldX, worldY float32) string {
	return b.local.NodeOwnerAtPos(worldX, worldY)
}

// OnPlayerTransfer is a control-plane call that stays in-process via
// the local Coordinator in S3. S4 will route this over MeshControl.
func (b *grpcBridge) OnPlayerTransfer(connID uint32, destCellID string) {
	b.local.OnPlayerTransfer(connID, destCellID)
}

// RelayChatToOtherNodes broadcasts a chat message to all other cells.
// For cells on this host the message is pushed directly via the local
// coordinator inbox. For cells on remote hosts it is dispatched via
// SendReliable (user-visible chat must not drop).
func (b *grpcBridge) RelayChatToOtherNodes(username, text string) {
	b.cell.Log.Log(CatMeshMsg, "[%s] relaying chat from %s to %d cells", b.cell.ID, username, len(b.coord.Cells)-1)
	for _, other := range b.coord.Cells {
		if other.ID == b.cell.ID {
			continue
		}
		msg := CellMessage{
			Type:       MsgChat,
			FromCellID: b.cell.ID,
			Chat:       &ChatRelay{Username: username, Text: text},
		}
		useLocal, destHostID := b.resolveDest(other.ID)
		if useLocal {
			other.Inbox <- msg
		} else {
			b.sendViaGrpc(destHostID, other.ID, msg, true, CatMeshMsg)
		}
	}
}

// RequestRespawn delegates to the local cellBridge. S3 coordinator
// is still in-process, so the respawn routing stays local.
func (b *grpcBridge) RequestRespawn(connID uint32, username string) {
	b.local.RequestRespawn(connID, username)
}

// SendAction dispatches a CrossNodeAction to the authoritative cell.
func (b *grpcBridge) SendAction(targetCellID string, action *CrossNodeAction) {
	useLocal, destHostID := b.resolveDest(targetCellID)
	if useLocal {
		b.local.SendAction(targetCellID, action)
		return
	}
	b.sendViaGrpc(destHostID, targetCellID, CellMessage{
		Type:       MsgCrossNodeAction,
		FromCellID: b.cell.ID,
		Action:     action,
	}, true, CatMeshAction) // reliable
}

// SendActionResult dispatches an ActionResult back to the originating cell.
func (b *grpcBridge) SendActionResult(targetCellID string, result *ActionResult) {
	useLocal, destHostID := b.resolveDest(targetCellID)
	if useLocal {
		b.local.SendActionResult(targetCellID, result)
		return
	}
	b.sendViaGrpc(destHostID, targetCellID, CellMessage{
		Type:         MsgActionResult,
		FromCellID:   b.cell.ID,
		ActionResult: result,
	}, true, CatMeshAction) // reliable
}

// SendHandoffPrepare begins a co-simulation handoff.
func (b *grpcBridge) SendHandoffPrepare(destCellID string, payload *HandoffPreparePayload) {
	useLocal, destHostID := b.resolveDest(destCellID)
	if useLocal {
		b.local.SendHandoffPrepare(destCellID, payload)
		return
	}
	b.sendViaGrpc(destHostID, destCellID, CellMessage{
		Type:           MsgHandoffPrepare,
		FromCellID:     b.cell.ID,
		HandoffPrepare: payload,
	}, true, CatMeshTransfer)
}

// SendHandoffCommit completes an authority flip to the destination cell.
func (b *grpcBridge) SendHandoffCommit(destCellID string, payload *HandoffCommitPayload) {
	useLocal, destHostID := b.resolveDest(destCellID)
	if useLocal {
		b.local.SendHandoffCommit(destCellID, payload)
		return
	}
	b.sendViaGrpc(destHostID, destCellID, CellMessage{
		Type:          MsgHandoffCommit,
		FromCellID:    b.cell.ID,
		HandoffCommit: payload,
	}, true, CatMeshTransfer)
}

// SendHandoffCancel asks the destination cell to remove a shadow entity
// created by a previously-sent HandoffPrepare.
func (b *grpcBridge) SendHandoffCancel(destCellID string, payload *HandoffCancelPayload) {
	useLocal, destHostID := b.resolveDest(destCellID)
	if useLocal {
		b.local.SendHandoffCancel(destCellID, payload)
		return
	}
	b.sendViaGrpc(destHostID, destCellID, CellMessage{
		Type:          MsgHandoffCancel,
		FromCellID:    b.cell.ID,
		HandoffCancel: payload,
	}, true, CatMeshTransfer)
}

// SendForwardInput forwards a player input frame to the new owner cell.
func (b *grpcBridge) SendForwardInput(destCellID string, payload *ForwardInputPayload) {
	useLocal, destHostID := b.resolveDest(destCellID)
	if useLocal {
		b.local.SendForwardInput(destCellID, payload)
		return
	}
	b.sendViaGrpc(destHostID, destCellID, CellMessage{
		Type:         MsgForwardInput,
		FromCellID:   b.cell.ID,
		ForwardInput: payload,
	}, true, CatMeshTransfer)
}

// compile-time interface assertion
var _ Bridge = (*grpcBridge)(nil)
