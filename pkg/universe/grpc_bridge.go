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
	coord       *Process        // for cell enumeration (e.g. chat fan-out)
	host        *Host               // local host (for IsLocal shortcut)
	cellToHost  func(string) string // destCellID -> hostID
	local       *cellBridge         // fallback/delegate for colocated cells
	gatewayMode string              // "local-shortcut" | "always-proxy"
}

// newGrpcBridge constructs a grpcBridge wrapping the given local
// cellBridge. The cellToHost resolver is typically the coordinator's
// cell-ownership lookup — given a destCellID string, return the hostID
// that currently owns it (or "" if unknown).
func newGrpcBridge(cell *Cell, coord *Process, host *Host, cellToHost func(string) string, local *cellBridge, gatewayMode string) *grpcBridge {
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
	// (Process's cross-connect loop skips peer.ID == h.ID to avoid a
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
		if err := b.host.SendReliable(destHostID, frame); err != nil {
			b.cell.Log.Log(CatMeshGrpc, "[%s] grpc reliable send to %s failed: %v", b.cell.ID, destHostID, err)
			return
		}
		b.cell.Log.Log(CatMeshGrpc, "[%s] grpc reliable -> host=%s cell=%s type=%v", b.cell.ID, destHostID, destCellID, msg.Type)
		return
	}
	if ok := b.host.SendLossy(destHostID, frame); !ok {
		b.cell.Log.Log(CatMeshGrpc, "[%s] grpc lossy drop to %s (%v)", b.cell.ID, destHostID, msg.Type)
		return
	}
	b.cell.Log.Log(CatMeshGrpc, "[%s] grpc lossy -> host=%s cell=%s type=%v", b.cell.ID, destHostID, destCellID, msg.Type)
}

// dispatchOrLocal resolves the destination and either delegates to the
// local cellBridge or encodes + sends via gRPC. localFn runs the local
// path; msgFn builds the CellMessage only when the remote path is taken.
func (b *grpcBridge) dispatchOrLocal(destCellID string, reliable bool, localFn func(), msgFn func() CellMessage) {
	useLocal, destHostID := b.resolveDest(destCellID)
	if useLocal {
		localFn()
		return
	}
	b.sendViaGrpc(destHostID, destCellID, msgFn(), reliable)
}

// dispatchOrLocalBool is the bool-returning variant for methods like
// SendHandoff where the local path may fail (returning false) but the
// remote path is fire-and-forget (always true).
func (b *grpcBridge) dispatchOrLocalBool(destCellID string, reliable bool, localFn func() bool, msgFn func() CellMessage) bool {
	useLocal, destHostID := b.resolveDest(destCellID)
	if useLocal {
		return localFn()
	}
	b.sendViaGrpc(destHostID, destCellID, msgFn(), reliable)
	return true
}

// HandoffDriver returns the lazily-constructed HandoffDriver from the
// wrapped cellBridge. Delegates so cell.go handlers can reach the
// driver via the handoffDriverHost interface regardless of whether
// this is a single-host or multi-host bridge.
func (b *grpcBridge) HandoffDriver() *HandoffDriver { return b.local.HandoffDriver() }

// PreTick delegates to the wrapped cellBridge.
func (b *grpcBridge) PreTick() { b.local.PreTick() }

// PostSystems delegates to the wrapped cellBridge.
func (b *grpcBridge) PostSystems() { b.local.PostSystems() }

// CellOwner delegates to the wrapped cellBridge.
func (b *grpcBridge) CellOwner(cell CellID) string { return b.local.CellOwner(cell) }

// CellOwnerAtPos delegates to the wrapped cellBridge.
func (b *grpcBridge) CellOwnerAtPos(worldX, worldY float32) string {
	return b.local.CellOwnerAtPos(worldX, worldY)
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
			b.cell.Log.Log(CatMeshMsg, "[%s] grpcBridge: remote-host mode but coord.vcm is nil, skipping PlayerMigrated for conn=%d", b.cell.ID, connID)
		}
	} else {
		// Single-process `all` preset: call the coordinator directly
		// with the embedded gateway ID.
		b.coord.notifyPlayerMigrated(InprocGatewayID, connID, srcHost, destHost, destCellID)
	}
}

// RelayChatToOtherCells broadcasts a chat message to all other cells.
// For cells on this host the message is pushed directly via the local
// coordinator inbox. For cells on remote hosts it is dispatched via
// SendReliable (user-visible chat must not drop).
func (b *grpcBridge) RelayChatToOtherCells(username, text string) {
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
	b.dispatchOrLocal(destCellID, false,
		func() { b.local.SendBorderFrame(destCellID, fromCellID, encoded) },
		func() CellMessage {
			return CellMessage{Type: MsgBorderFrame, FromCellID: fromCellID, BorderFrame: encoded}
		})
}

// SendAction dispatches a CrossCellAction to the authoritative cell.
func (b *grpcBridge) SendAction(targetCellID string, action *CrossCellAction) {
	b.dispatchOrLocal(targetCellID, true,
		func() { b.local.SendAction(targetCellID, action) },
		func() CellMessage {
			return CellMessage{Type: MsgCrossCellAction, FromCellID: b.cell.ID, Action: action}
		})
}

// SendActionResult dispatches an ActionResult back to the originating cell.
func (b *grpcBridge) SendActionResult(targetCellID string, result *ActionResult) {
	b.dispatchOrLocal(targetCellID, true,
		func() { b.local.SendActionResult(targetCellID, result) },
		func() CellMessage {
			return CellMessage{Type: MsgActionResult, FromCellID: b.cell.ID, ActionResult: result}
		})
}

// SendHandoff sends a hard-cut authority-transfer payload. See Bridge
// interface for the false-return semantics — a false return must NOT
// demote the source entity. Cross-host path is best-effort (always
// returns true) since remote-cell existence is not verified upfront.
func (b *grpcBridge) SendHandoff(destCellID string, payload *HandoffPayload) bool {
	return b.dispatchOrLocalBool(destCellID, true,
		func() bool { return b.local.SendHandoff(destCellID, payload) },
		func() CellMessage {
			return CellMessage{Type: MsgHandoff, FromCellID: b.cell.ID, Handoff: payload}
		})
}

// SendForwardInput forwards a player input frame to the new owner cell.
func (b *grpcBridge) SendForwardInput(destCellID string, payload *ForwardInputPayload) {
	b.dispatchOrLocal(destCellID, true,
		func() { b.local.SendForwardInput(destCellID, payload) },
		func() CellMessage {
			return CellMessage{Type: MsgForwardInput, FromCellID: b.cell.ID, ForwardInput: payload}
		})
}

// newBridgeForCell creates the right Bridge for a cell: a plain cellBridge
// when the host has no HostNetwork (single-host colocated mode), or a
// grpcBridge wrapping a cellBridge when the host has a Network (multi-host).
// This eliminates the two-pass "create cellBridge then upgrade" pattern.
func newBridgeForCell(cell *Cell, coord *Process, host *Host, cellToHost func(string) string, gatewayMode string) Bridge {
	local := &cellBridge{cell: cell, coord: coord}
	if host == nil || host.Network == nil {
		return local
	}
	return newGrpcBridge(cell, coord, host, cellToHost, local, gatewayMode)
}

// compile-time interface assertion
var _ Bridge = (*grpcBridge)(nil)
