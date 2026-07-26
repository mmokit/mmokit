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
	cell        *Cell                   // source cell (for logging + FromCellID)
	coord       *Process                // for cell enumeration (e.g. chat fan-out)
	host        *Host                   // local host (for IsLocal shortcut)
	cellToHost  func(MeshCellID) string // destCellID -> hostID
	local       *cellBridge             // fallback/delegate for colocated cells
	gatewayMode string                  // "local-shortcut" | "always-proxy"
}

// newGrpcBridge constructs a grpcBridge wrapping the given local
// cellBridge. The cellToHost resolver is typically the coordinator's
// cell-ownership lookup — given a destCellID, return the hostID that
// currently owns it (or "" if unknown).
func newGrpcBridge(cell *Cell, coord *Process, host *Host, cellToHost func(MeshCellID) string, local *cellBridge, gatewayMode string) *grpcBridge {
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
func (b *grpcBridge) resolveDest(destCellID MeshCellID) (useLocal bool, destHostID string) {
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
func (b *grpcBridge) sendViaGrpc(destHostID string, destCellID MeshCellID, msg CellMessage, reliable bool) bool {
	if destHostID == "" {
		b.cell.Log.Log(CatMeshGrpc, "[%s] grpc send: no host for cell %s", b.cell.MeshID, destCellID)
		return false
	}
	if b.host.Network == nil {
		b.cell.Log.Log(CatMeshGrpc, "[%s] grpc send: local host has no Network", b.cell.MeshID)
		return false
	}
	frame, err := encodeCellMessage(msg, destCellID)
	if err != nil {
		b.cell.Log.Log(CatMeshGrpc, "[%s] grpc encode %v failed: %v", b.cell.MeshID, msg.Type, err)
		return false
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
			b.cell.Log.Log(CatMeshGrpc, "[%s] grpc self-route to %s failed: %v", b.cell.MeshID, destCellID, err)
			return false
		}
		b.cell.Log.Log(CatMeshGrpc, "[%s] grpc self-route -> cell=%s type=%v", b.cell.MeshID, destCellID, msg.Type)
		return true
	}
	if reliable {
		if err := b.host.SendReliable(destHostID, frame); err != nil {
			b.cell.Log.Log(CatMeshGrpc, "[%s] grpc reliable send to %s failed: %v", b.cell.MeshID, destHostID, err)
			return false
		}
		b.cell.Log.Log(CatMeshGrpc, "[%s] grpc reliable -> host=%s cell=%s type=%v", b.cell.MeshID, destHostID, destCellID, msg.Type)
		return true
	}
	if ok := b.host.SendLossy(destHostID, frame); !ok {
		b.cell.Log.Log(CatMeshGrpc, "[%s] grpc lossy drop to %s (%v)", b.cell.MeshID, destHostID, msg.Type)
		return false
	}
	b.cell.Log.Log(CatMeshGrpc, "[%s] grpc lossy -> host=%s cell=%s type=%v", b.cell.MeshID, destHostID, destCellID, msg.Type)
	return true
}

// dispatchOrLocal resolves the destination and either delegates to the
// local cellBridge or encodes + sends via gRPC. localFn runs the local
// path; msgFn builds the CellMessage only when the remote path is taken.
func (b *grpcBridge) dispatchOrLocal(destCellID MeshCellID, reliable bool, localFn func(), msgFn func() CellMessage) {
	useLocal, destHostID := b.resolveDest(destCellID)
	if useLocal {
		localFn()
		return
	}
	b.sendViaGrpc(destHostID, destCellID, msgFn(), reliable)
}

// dispatchOrLocalBool is the bool-returning variant for critical messages
// whose local or remote enqueue failure must be visible to the caller.
func (b *grpcBridge) dispatchOrLocalBool(destCellID MeshCellID, reliable bool, localFn func() bool, msgFn func() CellMessage) bool {
	useLocal, destHostID := b.resolveDest(destCellID)
	if useLocal {
		return localFn()
	}
	return b.sendViaGrpc(destHostID, destCellID, msgFn(), reliable)
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
// For same-host destinations, delegates to the local cellBridge which advances
// sessionRoutes via setPlayerNode. For cross-host destinations, skips the
// local cellBridge entirely and hands sessionRoutes ownership to
// notifyPlayerMigrated's atomic Migrate path.
func (b *grpcBridge) OnPlayerTransfer(connID uint32, destCellID MeshCellID) {
	useLocal, destHost := b.resolveDest(destCellID)
	srcHost := b.host.ID

	// Single-process all-mode: b.coord IS the real coordinator. Same-host
	// transfers can use the local setPlayerNode epoch bump; cross-host
	// transfers go through the full Migrate path.
	if b.coord.controlClient == nil {
		if useLocal {
			b.local.OnPlayerTransfer(connID, destCellID)
			return
		}
		b.coord.notifyPlayerMigrated(InprocGatewayID, connID, srcHost, destHost, destCellID)
		return
	}

	// Multi-process mode: b.coord is this host's local Process, not the
	// authoritative coordinator. The local sessionRoutes is unused here —
	// only the real coordinator's sessionRoutes matters for gateway routing
	// and cell-transfer commits. Notify the coordinator about EVERY boundary
	// handoff (same-host or cross-host) via PlayerMigrated.
	//
	// Without same-host notification, a player crossing between two cells
	// on the same host leaves coord.sessionRoutes pointing at the OLD cell.
	// A subsequent cell migrate of the new cell then can't find the player's
	// session route to remap — the gateway keeps routing input to the
	// torn-down source cell and the player loses connectivity.
	vcm := b.coord.vcm
	if vcm == nil {
		b.cell.Log.Log(CatMeshMsg, "[%s] grpcBridge: remote-host mode but coord.vcm is nil, skipping PlayerMigrated for conn=%d", b.cell.MeshID, connID)
		return
	}
	// Resolve the real {gatewayID, connID} from the VirtualConnManager so
	// the coordinator can route UpstreamSwitch to the correct gateway.
	key, ok := vcm.LookupByLocal(connID)
	if !ok {
		b.cell.Log.Log(CatMeshMsg, "[%s] grpcBridge: no VCM session for localID=%d, skipping PlayerMigrated", b.cell.MeshID, connID)
		return
	}
	if err := b.coord.controlClient.send(&meshpb.HostMessage{
		Msg: &meshpb.HostMessage_PlayerMigrated{
			PlayerMigrated: &meshpb.PlayerMigrated{
				GatewayId:  key.GatewayID,
				ConnId:     key.ConnID, // gateway-side connID, not node-local
				FromHostId: srcHost,
				ToHostId:   destHost,
				ToCellId:   string(destCellID),
			},
		},
	}); err != nil {
		b.cell.Log.Log(CatMeshMsg,
			"[%s] PlayerMigrated send failed for conn=%d dest=%s: %v",
			b.cell.MeshID, connID, destCellID, err)
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
func (b *grpcBridge) SendBorderFrame(destCellID, fromCellID MeshCellID, encoded []byte) {
	b.dispatchOrLocal(destCellID, false,
		func() { b.local.SendBorderFrame(destCellID, fromCellID, encoded) },
		func() CellMessage {
			return CellMessage{Type: MsgBorderFrame, FromCellID: fromCellID, BorderFrame: encoded}
		})
}

// SendAction dispatches a CrossCellAction to the authoritative cell.
func (b *grpcBridge) SendAction(targetCellID MeshCellID, action *CrossCellAction) {
	b.dispatchOrLocal(targetCellID, true,
		func() { b.local.SendAction(targetCellID, action) },
		func() CellMessage {
			return CellMessage{Type: MsgCrossCellAction, FromCellID: b.cell.MeshID, Action: action}
		})
}

// SendHandoff sends a hard-cut authority-transfer payload. See Bridge
// interface for the false-return semantics — a false return must NOT
// demote the source entity. The cross-host path reports local routing,
// encoding, and stream-enqueue failures. A true return means only that the
// send was enqueued; destination acceptance arrives separately as
// MsgHandoffAccepted and is what actually arms the source's demote.
func (b *grpcBridge) SendHandoff(destCellID MeshCellID, payload *HandoffPayload) bool {
	return b.dispatchOrLocalBool(destCellID, true,
		func() bool { return b.local.SendHandoff(destCellID, payload) },
		func() CellMessage {
			return CellMessage{Type: MsgHandoff, FromCellID: b.cell.MeshID, Handoff: payload}
		})
}

// SendHandoffAccepted returns the destination's acceptance to the source
// cell. Reliable so a cross-host stream failure is reported to the caller,
// which then skips the promote and lets the source retry the Handoff.
func (b *grpcBridge) SendHandoffAccepted(destCellID MeshCellID, ack *HandoffAckPayload) bool {
	return b.dispatchOrLocalBool(destCellID, true,
		func() bool { return b.local.SendHandoffAccepted(destCellID, ack) },
		func() CellMessage {
			return CellMessage{Type: MsgHandoffAccepted, FromCellID: b.cell.MeshID, HandoffAck: ack}
		})
}

// SendForwardInput forwards a player input frame to the new owner cell.
func (b *grpcBridge) SendForwardInput(destCellID MeshCellID, payload *ForwardInputPayload) bool {
	useLocal, destHostID := b.resolveDest(destCellID)
	if useLocal {
		return b.local.SendForwardInput(destCellID, payload)
	}

	// A node-local connID has no meaning on another host. Resolve it back to
	// the stable gateway session key; the destination VCM maps that key to its
	// own local connID before injecting the frame.
	if b.coord == nil || b.coord.vcm == nil {
		b.cell.Log.Log(CatMeshMsg, "[%s] cannot forward input cross-host without VCM: conn=%d dest=%s",
			b.cell.MeshID, payload.ConnID, destCellID)
		return false
	}
	key, sessionEpoch, ok := b.coord.vcm.LookupRouteByLocal(payload.ConnID)
	if !ok {
		b.cell.Log.Log(CatMeshMsg, "[%s] cannot resolve forwarded input session: localConn=%d dest=%s",
			b.cell.MeshID, payload.ConnID, destCellID)
		return false
	}
	wirePayload := *payload
	wirePayload.GatewayID = key.GatewayID
	wirePayload.ConnID = key.ConnID
	wirePayload.SessionEpoch = sessionEpoch
	return b.sendViaGrpc(destHostID, destCellID, CellMessage{
		Type:         MsgForwardInput,
		FromCellID:   b.cell.MeshID,
		ForwardInput: &wirePayload,
	}, true)
}

// RequiresInputForwarding reports whether source-side grace forwarding is
// needed. Colocated cells share one ConnSender, so the destination drains the
// existing queue directly; only a real cross-host VCM boundary needs replay.
func (b *grpcBridge) RequiresInputForwarding(destCellID MeshCellID) bool {
	useLocal, _ := b.resolveDest(destCellID)
	return !useLocal && b.coord != nil && b.coord.vcm != nil
}

// newBridgeForCell creates the right Bridge for a cell: a plain cellBridge
// when the host has no HostNetwork (single-host colocated mode), or a
// grpcBridge wrapping a cellBridge when the host has a Network (multi-host).
// This eliminates the two-pass "create cellBridge then upgrade" pattern.
func newBridgeForCell(cell *Cell, coord *Process, host *Host, cellToHost func(MeshCellID) string, gatewayMode string) Bridge {
	local := &cellBridge{cell: cell, coord: coord}
	if host == nil || host.Network == nil {
		return local
	}
	return newGrpcBridge(cell, coord, host, cellToHost, local, gatewayMode)
}

// compile-time interface assertion
var _ Bridge = (*grpcBridge)(nil)
