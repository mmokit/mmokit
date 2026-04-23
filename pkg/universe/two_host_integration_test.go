package universe

import (
	"sync"
	"testing"
	"time"
)

// TestTwoHostHandoffPrepareRoundTrip stands up a 2-host cluster via the
// distributed fixture and exercises the full cross-host gRPC dispatch path:
// source cell's grpcBridge encodes the HandoffPrepare payload through the
// meshpb codec, HostNetwork sender goroutine pushes it on the bidi stream,
// the peer's server receives it, routeInboundFrame decodes and delivers to
// the destination cell's Inbox.
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
	fx := newDistributedFixture(t, FixtureConfig{
		CellsX:  2,
		CellsY:  2,
		HostIDs: []string{"host-a", "host-b"},
	})

	// Sanity: confirm the cells landed on different hosts.
	srcID := MeshCellID(CellID{X: 0, Y: 0})
	dstID := MeshCellID(CellID{X: 1, Y: 0})
	srcHost := fx.CellOwner(srcID)
	dstHost := fx.CellOwner(dstID)
	if srcHost == dstHost {
		t.Fatalf("expected src and dst on different hosts; both on %q", srcHost)
	}

	src := fx.CellOn(srcHost, srcID)
	dst := fx.CellOn(dstHost, dstID)

	if src == nil {
		t.Fatalf("src cell %s not found on host %s", srcID, srcHost)
	}
	if dst == nil {
		t.Fatalf("dst cell %s not found on host %s", dstID, dstHost)
	}

	// Install a message spy on dst before sending. The spy fires inside
	// processMessage (on the game loop goroutine) — so it races neither
	// with message delivery nor with game-loop draining.
	var (
		mu       sync.Mutex
		received *CellMessage
		gotMsg   = make(chan struct{})
	)
	dst.onMessage = func(msg CellMessage) {
		if msg.Type != MsgHandoff {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if received != nil {
			return // capture only the first HandoffPrepare
		}
		cp := msg
		received = &cp
		close(gotMsg)
	}

	payload := &HandoffPayload{
		NetID:        789,
		Epoch:        5,
		CommitTick:   1000,
		TransferBlob: []byte("two-host handoff blob"),
	}

	// Send through the source cell's Bridge. In multi-host mode this is a
	// grpcBridge wrapping a cellBridge; shouldUseLocal returns false because
	// dstHost != srcHost, so sendViaGrpc encodes and dispatches via
	// host.Network.SendReliable.
	src.Bridge.SendHandoff(dstID, payload)

	// Wait for the message spy to fire. The gRPC path is async:
	// sender goroutine -> stream.Send -> wire -> server.Recv ->
	// routeInboundFrame -> dst.Inbox -> processMessage -> spy.
	select {
	case <-gotMsg:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for handoff on destination cell")
	}

	mu.Lock()
	msg := *received
	mu.Unlock()

	if msg.Type != MsgHandoff {
		t.Fatalf("expected MsgHandoff, got %d", msg.Type)
	}
	if msg.FromCellID != srcID {
		t.Errorf("FromCellID = %q, want %q", msg.FromCellID, srcID)
	}
	if msg.Handoff == nil {
		t.Fatal("Handoff payload is nil")
	}
	if msg.Handoff.NetID != 789 {
		t.Errorf("NetID = %d, want 789", msg.Handoff.NetID)
	}
	if msg.Handoff.Epoch != 5 {
		t.Errorf("Epoch = %d, want 5", msg.Handoff.Epoch)
	}
	if msg.Handoff.CommitTick != 1000 {
		t.Errorf("CommitTick = %d, want 1000", msg.Handoff.CommitTick)
	}
	if string(msg.Handoff.TransferBlob) != "two-host handoff blob" {
		t.Errorf("TransferBlob = %q, want %q", msg.Handoff.TransferBlob, "two-host handoff blob")
	}
}

// TestTwoHostGrpcBridgeRoutesLocal asserts that a colocated destination on the
// same host takes the local shortcut (delegates to the wrapped cellBridge,
// skipping gRPC). Confirms shouldUseLocal returns true for same-host
// destinations in "local-shortcut" mode.
func TestTwoHostGrpcBridgeRoutesLocal(t *testing.T) {
	fx := newDistributedFixture(t, FixtureConfig{
		CellsX:  2,
		CellsY:  2,
		HostIDs: []string{"host-a", "host-b"},
	})

	// Find two cells on the same host. With a 2x2 grid and 2 hosts the
	// default round-robin gives cell_0_0 + cell_0_1 to host-a and
	// cell_1_0 + cell_1_1 to host-b.
	srcID := MeshCellID(CellID{X: 0, Y: 0}) // host-a
	dstID := MeshCellID(CellID{X: 0, Y: 1}) // host-a (same host)

	srcHost := fx.CellOwner(srcID)
	dstHost := fx.CellOwner(dstID)
	if srcHost != dstHost {
		t.Fatalf("expected src and dst on the same host; src=%q dst=%q", srcHost, dstHost)
	}

	src := fx.CellOn(srcHost, srcID)
	dst := fx.CellOn(dstHost, dstID)

	if src == nil {
		t.Fatalf("src cell %s not found on host %s", srcID, srcHost)
	}
	if dst == nil {
		t.Fatalf("dst cell %s not found on host %s", dstID, dstHost)
	}

	var (
		mu       sync.Mutex
		received *CellMessage
		gotMsg   = make(chan struct{})
	)
	dst.onMessage = func(msg CellMessage) {
		if msg.Type != MsgHandoff {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if received != nil {
			return
		}
		cp := msg
		received = &cp
		close(gotMsg)
	}

	payload := &HandoffPayload{NetID: 555, Epoch: 1, CommitTick: 1, TransferBlob: []byte("local")}
	src.Bridge.SendHandoff(dstID, payload)

	// Colocated local-shortcut delivery is synchronous (direct channel send
	// via cellBridge), so the game loop drains it on its next tick.
	select {
	case <-gotMsg:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timeout — colocated local-shortcut delivery should be near-instant")
	}

	mu.Lock()
	msg := *received
	mu.Unlock()

	if msg.Handoff == nil || msg.Handoff.NetID != 555 {
		t.Fatalf("unexpected msg: %+v", msg)
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
	fx := newDistributedFixture(t, FixtureConfig{
		CellsX:      2,
		CellsY:      2,
		HostIDs:     []string{"host-a", "host-b"},
		GatewayMode: "always-proxy",
	})

	// Find two cells on the same host to exercise the self-route path.
	srcID := MeshCellID(CellID{X: 0, Y: 0}) // host-a
	dstID := MeshCellID(CellID{X: 0, Y: 1}) // host-a (same host)

	srcHost := fx.CellOwner(srcID)
	dstHost := fx.CellOwner(dstID)
	if srcHost != dstHost {
		t.Fatalf("expected src and dst on the same host; src=%q dst=%q", srcHost, dstHost)
	}

	src := fx.CellOn(srcHost, srcID)
	dst := fx.CellOn(dstHost, dstID)

	if src == nil {
		t.Fatalf("src cell %s not found on host %s", srcID, srcHost)
	}
	if dst == nil {
		t.Fatalf("dst cell %s not found on host %s", dstID, dstHost)
	}

	var (
		mu       sync.Mutex
		received *CellMessage
		gotMsg   = make(chan struct{})
	)
	dst.onMessage = func(msg CellMessage) {
		if msg.Type != MsgHandoff {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if received != nil {
			return
		}
		cp := msg
		received = &cp
		close(gotMsg)
	}

	payload := &HandoffPayload{
		NetID:        111,
		Epoch:        2,
		CommitTick:   1,
		TransferBlob: []byte("always-proxy self-route"),
	}
	src.Bridge.SendHandoff(dstID, payload)

	select {
	case <-gotMsg:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timeout — always-proxy self-route should still deliver via codec path")
	}

	mu.Lock()
	msg := *received
	mu.Unlock()

	if msg.Type != MsgHandoff {
		t.Fatalf("expected MsgHandoff, got %d", msg.Type)
	}
	if msg.FromCellID != srcID {
		t.Errorf("FromCellID = %q, want %q", msg.FromCellID, srcID)
	}
	if msg.Handoff == nil || msg.Handoff.NetID != 111 {
		t.Fatalf("unexpected payload: %+v", msg.Handoff)
	}
	if string(msg.Handoff.TransferBlob) != "always-proxy self-route" {
		t.Errorf("TransferBlob = %q, want %q", msg.Handoff.TransferBlob, "always-proxy self-route")
	}
}
