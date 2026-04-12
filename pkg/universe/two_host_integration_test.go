package universe

import (
	"testing"
	"time"
)

// TestTwoHostHandoffPrepareRoundTrip stands up a 2x2 coordinator distributed
// across two in-process Host instances via TestHosts config and exercises the
// full cross-host gRPC dispatch path: source cell's grpcBridge encodes the
// HandoffPrepare payload through the meshpb codec, HostNetwork sender goroutine
// pushes it on the bidi stream, the peer's server receives it,
// routeInboundFrame decodes and delivers to the destination cell's Inbox.
//
// Cell assignment (round-robin):
//
//	cell_0_0 -> host-a
//	cell_1_0 -> host-b
//	cell_0_1 -> host-a
//	cell_1_1 -> host-b
//
// We send from cell_0_0 (host-a) to cell_1_0 (host-b) — a cross-host path.
func TestTwoHostHandoffPrepareRoundTrip(t *testing.T) {
	cfg := Config{
		CellsX:    2,
		CellsY:    2,
		TestHosts: []string{"host-a", "host-b"},
	}
	c, _ := newTestCoordinator(cfg)
	t.Cleanup(func() { c.Shutdown() })

	// Sanity: confirm the cells landed on different hosts.
	srcID := MeshCellID(CellID{X: 0, Y: 0})
	dstID := MeshCellID(CellID{X: 1, Y: 0})
	if c.cellToHostMap[srcID] == c.cellToHostMap[dstID] {
		t.Fatalf("expected src and dst on different hosts; both on %q", c.cellToHostMap[srcID])
	}

	src := c.Cells[srcID]
	dst := c.Cells[dstID]

	payload := &HandoffPreparePayload{
		NetID:        789,
		Epoch:        5,
		Kind:         1,
		TransferBlob: []byte("two-host handoff blob"),
		ExpectedTick: 1000,
		OldEpoch:     4,
	}

	// Send through the source cell's Bridge. In multi-host mode this is a
	// grpcBridge wrapping a cellBridge; shouldUseLocal returns false because
	// dstHost != srcHost, so sendViaGrpc encodes and dispatches via
	// host.Network.SendReliable.
	src.Bridge.SendHandoffPrepare(dstID, payload)

	// Wait for the message to arrive on the destination cell's inbox.
	// The gRPC path is async: sender goroutine -> stream.Send -> wire ->
	// server.Recv -> routeInboundFrame -> dst.Inbox.
	select {
	case msg := <-dst.Inbox:
		if msg.Type != MsgHandoffPrepare {
			t.Fatalf("expected MsgHandoffPrepare, got %d", msg.Type)
		}
		if msg.FromCellID != srcID {
			t.Errorf("FromCellID = %q, want %q", msg.FromCellID, srcID)
		}
		if msg.HandoffPrepare == nil {
			t.Fatal("HandoffPrepare payload is nil")
		}
		if msg.HandoffPrepare.NetID != 789 {
			t.Errorf("NetID = %d, want 789", msg.HandoffPrepare.NetID)
		}
		if msg.HandoffPrepare.Epoch != 5 {
			t.Errorf("Epoch = %d, want 5", msg.HandoffPrepare.Epoch)
		}
		if string(msg.HandoffPrepare.TransferBlob) != "two-host handoff blob" {
			t.Errorf("TransferBlob = %q, want %q", msg.HandoffPrepare.TransferBlob, "two-host handoff blob")
		}
		if msg.HandoffPrepare.OldEpoch != 4 {
			t.Errorf("OldEpoch = %d, want 4", msg.HandoffPrepare.OldEpoch)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for handoff prepare on destination inbox")
	}
}

// TestTwoHostGrpcBridgeRoutesLocal asserts that a colocated destination on the
// same host takes the local shortcut (delegates to the wrapped cellBridge,
// skipping gRPC). Confirms shouldUseLocal returns true for same-host
// destinations in "local-shortcut" mode.
func TestTwoHostGrpcBridgeRoutesLocal(t *testing.T) {
	cfg := Config{
		CellsX:    2,
		CellsY:    2,
		TestHosts: []string{"host-a", "host-b"},
	}
	c, _ := newTestCoordinator(cfg)
	t.Cleanup(func() { c.Shutdown() })

	// Find two cells on the same host.
	var srcID, dstID string
	seen := make(map[string]string) // hostID -> first cellID seen on that host
	for cellID, hostID := range c.cellToHostMap {
		if other, ok := seen[hostID]; ok {
			srcID, dstID = other, cellID
			break
		}
		seen[hostID] = cellID
	}
	if srcID == "" || dstID == "" {
		t.Fatal("could not find two colocated cells")
	}

	src := c.Cells[srcID]
	dst := c.Cells[dstID]

	payload := &HandoffPreparePayload{NetID: 555, Epoch: 1, TransferBlob: []byte("local")}
	src.Bridge.SendHandoffPrepare(dstID, payload)

	// Colocated local-shortcut delivery is synchronous (direct channel send),
	// but a select with a tight timeout is still safer than a bare receive.
	select {
	case msg := <-dst.Inbox:
		if msg.HandoffPrepare == nil || msg.HandoffPrepare.NetID != 555 {
			t.Fatalf("unexpected msg: %+v", msg)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timeout — colocated local-shortcut delivery should be near-instant")
	}
}

// TestTwoHostAlwaysProxySelfRoute asserts that GatewayMode=always-proxy
// still delivers same-host messages through the codec + routeInboundFrame
// path. Hosts don't connect to themselves in the peer cross-connect loop,
// so sendViaGrpc has a self-route shortcut that hands the encoded frame
// straight to HostNetwork.routeInboundFrame when destHostID == host.ID.
// This exercises the codec end-to-end (the whole point of always-proxy)
// without requiring a self-loop gRPC client stream.
func TestTwoHostAlwaysProxySelfRoute(t *testing.T) {
	cfg := Config{
		CellsX:      2,
		CellsY:      2,
		TestHosts:   []string{"host-a", "host-b"},
		GatewayMode: "always-proxy",
	}
	c, _ := newTestCoordinator(cfg)
	t.Cleanup(func() { c.Shutdown() })

	// Find two cells on the same host so we exercise the self-route path.
	var srcID, dstID string
	seen := make(map[string]string)
	for cellID, hostID := range c.cellToHostMap {
		if other, ok := seen[hostID]; ok {
			srcID, dstID = other, cellID
			break
		}
		seen[hostID] = cellID
	}
	if srcID == "" || dstID == "" {
		t.Fatal("could not find two colocated cells")
	}

	src := c.Cells[srcID]
	dst := c.Cells[dstID]

	payload := &HandoffPreparePayload{
		NetID:        111,
		Epoch:        2,
		Kind:         1,
		TransferBlob: []byte("always-proxy self-route"),
		OldEpoch:     1,
	}
	src.Bridge.SendHandoffPrepare(dstID, payload)

	select {
	case msg := <-dst.Inbox:
		if msg.Type != MsgHandoffPrepare {
			t.Fatalf("expected MsgHandoffPrepare, got %d", msg.Type)
		}
		if msg.FromCellID != srcID {
			t.Errorf("FromCellID = %q, want %q", msg.FromCellID, srcID)
		}
		if msg.HandoffPrepare == nil || msg.HandoffPrepare.NetID != 111 {
			t.Fatalf("unexpected payload: %+v", msg.HandoffPrepare)
		}
		if string(msg.HandoffPrepare.TransferBlob) != "always-proxy self-route" {
			t.Errorf("TransferBlob = %q, want %q", msg.HandoffPrepare.TransferBlob, "always-proxy self-route")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timeout — always-proxy self-route should still be synchronous (in-process encode/decode)")
	}
}
