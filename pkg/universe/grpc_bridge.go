package universe

import meshpb "github.com/zenion/mmoserver/gen/go/meshpb"

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

// unwrapCellBridge returns the inner *cellBridge for a Bridge that may be
// a plain *cellBridge or a *grpcBridge wrapping one. Returns nil if the
// bridge is neither — tests use fakeBridges for which there is no inner
// cellBridge and the caller should skip.
func unwrapCellBridge(b Bridge) *cellBridge {
	switch x := b.(type) {
	case *cellBridge:
		return x
	case *grpcBridge:
		return x.local
	}
	return nil
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
//
// All routing-decision log lines land in CatMeshGrpc so operators can
// tail "mesh:grpc" to see every bridge dispatch without drowning in
// mesh:replica or mesh:transfer noise.
func (b *grpcBridge) sendViaGrpc(destHostID, destCellID string, msg CellMessage, reliable bool) {
	if destHostID == "" {
		b.cell.Log.Log(CatMeshGrpc, "[%s] grpc send: no host for cell %s", b.cell.ID, destCellID)
		return
	}
	if b.host.Network == nil {
		b.cell.Log.Log(CatMeshGrpc, "[%s] grpc send: local host has no Network", b.cell.ID)
		return
	}
	frame, err := encodeCellMessage(msg, destCellID)
	if err != nil {
		b.cell.Log.Log(CatMeshGrpc, "[%s] grpc encode %v failed: %v", b.cell.ID, msg.Type, err)
		return
	}
	// Self-route shortcut: when gatewayMode=always-proxy routes a same-host
	// destination through sendViaGrpc, the peer map has no self entry
	// (Coordinator's cross-connect loop skips peer.ID == h.ID to avoid a
	// self-loop gRPC stream). Hand the already-encoded frame directly to
	// routeInboundFrame — this still exercises the codec end-to-end (which
	// is the whole point of always-proxy) while avoiding a wasted network
	// round-trip through loopback TCP.
	if destHostID == b.host.ID {
		if err := b.host.Network.routeInboundFrame(frame); err != nil {
			b.cell.Log.Log(CatMeshGrpc, "[%s] grpc self-route to %s failed: %v", b.cell.ID, destCellID, err)
			return
		}
		b.cell.Log.Log(CatMeshGrpc, "[%s] grpc self-route -> cell=%s type=%v", b.cell.ID, destCellID, msg.Type)
		return
	}
	if reliable {
		if err := b.host.Network.SendReliable(destHostID, frame); err != nil {
			b.cell.Log.Log(CatMeshGrpc, "[%s] grpc reliable send to %s failed: %v", b.cell.ID, destHostID, err)
			return
		}
		b.cell.Log.Log(CatMeshGrpc, "[%s] grpc reliable -> host=%s cell=%s type=%v", b.cell.ID, destHostID, destCellID, msg.Type)
		return
	}
	if ok := b.host.Network.SendLossy(destHostID, frame); !ok {
		b.cell.Log.Log(CatMeshGrpc, "[%s] grpc lossy drop to %s (%v)", b.cell.ID, destHostID, msg.Type)
		return
	}
	b.cell.Log.Log(CatMeshGrpc, "[%s] grpc lossy -> host=%s cell=%s type=%v", b.cell.ID, destHostID, destCellID, msg.Type)
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

// OnPlayerTransfer handles a player session transfer to destCellID.
// For same-host destinations, delegates to the local cellBridge which updates
// sessionRoutes via setPlayerNode. For cross-host destinations, skips the
// local cellBridge entirely — calling it would reset the sessionRoute's Epoch
// to 1 and clear HostID, creating a race window before notifyPlayerMigrated's
// atomic Migrate call re-populates both fields. The cross-host branch hands
// off sessionRoutes ownership entirely to notifyPlayerMigrated.
func (b *grpcBridge) OnPlayerTransfer(connID uint32, destCellID string) {
	useLocal, destHost := b.resolveDest(destCellID)
	if useLocal {
		// Same host: local cellBridge handles sessionRoutes via setPlayerNode.
		b.local.OnPlayerTransfer(connID, destCellID)
		return
	}
	// Cross-host: notifyPlayerMigrated owns the sessionRoutes mutation via
	// its atomic Migrate call. Do NOT call b.local.OnPlayerTransfer here —
	// that would reset Epoch to 1 and clear HostID before Migrate fixes it.
	srcHost := b.host.ID
	if b.coord.controlClient != nil {
		// Node mode: resolve the real {gatewayID, connID} from the
		// VirtualConnManager so the coordinator can route UpstreamSwitch to
		// the correct gateway. The VCM stores the wire-format SessionKey
		// (original gateway connID) under the node-local connID.
		if vcm := b.coord.vcm; vcm != nil {
			key, ok := vcm.LookupByLocal(connID)
			if !ok {
				b.cell.Log.Log(CatMeshMsg, "[%s] grpcBridge: no VCM session for localID=%d, skipping PlayerMigrated", b.cell.ID, connID)
				return
			}
			_ = b.coord.controlClient.send(&meshpb.HostMessage{
				Msg: &meshpb.HostMessage_PlayerMigrated{
					PlayerMigrated: &meshpb.PlayerMigrated{
						GatewayId:  key.GatewayID,
						ConnId:     key.ConnID, // gateway-side connID, not node-local
						FromHostId: srcHost,
						ToHostId:   destHost,
						ToCellId:   destCellID,
					},
				},
			})
		} else {
			// No VCM in this node — should not happen in production but
			// log clearly rather than silently dropping.
			b.cell.Log.Log(CatMeshMsg, "[%s] grpcBridge: node mode but coord.vcm is nil, skipping PlayerMigrated for conn=%d", b.cell.ID, connID)
		}
	} else {
		// Single-process all-in-one with multiple TestHosts: call the
		// coordinator directly with the embedded gateway ID.
		b.coord.notifyPlayerMigrated(InprocGatewayID, connID, srcHost, destHost, destCellID)
	}
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
			b.sendViaGrpc(destHostID, other.ID, msg, true)
		}
	}
}

// RequestRespawn delegates to the local cellBridge. S3 coordinator
// is still in-process, so the respawn routing stays local.
func (b *grpcBridge) RequestRespawn(connID uint32, username string) {
	b.local.RequestRespawn(connID, username)
}

// SendBorderFrame dispatches an encoded border replication frame to a
// neighbor cell. Lossy: tick-driven and the 30-tick resync recovers
// the receiver, so drops are acceptable.
func (b *grpcBridge) SendBorderFrame(destCellID, fromCellID string, encoded []byte) {
	useLocal, destHostID := b.resolveDest(destCellID)
	if useLocal {
		b.local.SendBorderFrame(destCellID, fromCellID, encoded)
		return
	}
	b.sendViaGrpc(destHostID, destCellID, CellMessage{
		Type:        MsgBorderFrame,
		FromCellID:  fromCellID,
		BorderFrame: encoded,
	}, false) // lossy
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
	}, true) // reliable
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
	}, true) // reliable
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
	}, true)
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
	}, true)
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
	}, true)
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
	}, true)
}

// compile-time interface assertion
var _ Bridge = (*grpcBridge)(nil)
