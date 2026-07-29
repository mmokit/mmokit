package universe

import (
	"testing"

	meshpb "github.com/zenion/mmoserver/gen/go/meshpb"
	"github.com/zenion/mmoserver/pkg/metrics"
	pkgnet "github.com/zenion/mmoserver/pkg/net"
)

// TestPayloadBinding_ClientInputRequiresMatchingGateway pins the arm that
// matters most: ClientInput is the client-input INJECTION primitive, so a
// gateway must not be able to inject input while claiming to be another.
func TestPayloadBinding_ClientInputRequiresMatchingGateway(t *testing.T) {
	n := newManualHostNetwork(t)
	n.vcm = NewVirtualConnManager(n, n.log)

	frame := &meshpb.MeshFrame{Msg: &meshpb.MeshFrame_ClientInput{
		ClientInput: &meshpb.ClientInput{
			GatewayId: "gw-victim",
			ConnId:    7,
			Epoch:     1,
			Data:      []byte{0x01},
		},
	}}

	before := metrics.Ingress().Rejected(metrics.SurfaceMesh, metrics.ReasonIdentityMismatch)
	if err := n.routeInboundFrame("gw-attacker", frame); err != nil {
		t.Fatalf("routeInboundFrame: %v", err)
	}
	after := metrics.Ingress().Rejected(metrics.SurfaceMesh, metrics.ReasonIdentityMismatch)

	if after != before+1 {
		t.Fatalf("a gateway impersonating another was not rejected (%d -> %d)", before, after)
	}
}

// TestPayloadBinding_ClientFrameBindsToReceiverNotSender is the rule a blanket
// "payload identity == sender" check would get catastrophically wrong.
//
// ClientFrame.GatewayId names the RECEIVING gateway and every producer is a
// host, so a sender check would drop one frame per player per tick — 100% of
// replication traffic. This asserts a host-sent frame addressed to THIS
// gateway is delivered, precisely because the sender differs.
func TestPayloadBinding_ClientFrameBindsToReceiverNotSender(t *testing.T) {
	cm := pkgnet.NewConnManager()
	transport := &clientFrameTransport{
		result: pkgnet.SendResult{Disposition: pkgnet.SendQueued, Delivery: pkgnet.DeliveryReliableOrdered},
	}
	connID := cm.AddTransport(transport, "")

	n := newManualHostNetwork(t)
	n.gw = &Gateway{
		id:      "gw-1",
		connMgr: cm,
		sessions: map[uint32]*localSession{
			connID: {connID: connID, hostID: "host-a", epoch: 1},
		},
	}

	frame := &meshpb.MeshFrame{Msg: &meshpb.MeshFrame_ClientFrame{
		ClientFrame: &meshpb.ClientFrame{
			GatewayId: "gw-1", // the receiver, not the sender
			ConnId:    connID,
			Epoch:     1,
			Data:      []byte{0x01},
		},
	}}

	// Sender is a HOST, deliberately different from the named gateway.
	if err := n.routeInboundFrame("host-a", frame); err != nil {
		t.Fatalf("routeInboundFrame: %v", err)
	}
	if transport.sent == 0 {
		t.Fatal("a legitimate host->gateway replication frame was dropped; " +
			"the ClientFrame arm must bind to the receiving gateway, not the sender")
	}
}

// TestPayloadBinding_ClientFrameRejectsWrongGateway is the other half: a frame
// addressed to a DIFFERENT gateway must not be written to this one's sockets.
func TestPayloadBinding_ClientFrameRejectsWrongGateway(t *testing.T) {
	cm := pkgnet.NewConnManager()
	transport := &clientFrameTransport{
		result: pkgnet.SendResult{Disposition: pkgnet.SendQueued, Delivery: pkgnet.DeliveryReliableOrdered},
	}
	connID := cm.AddTransport(transport, "")

	n := newManualHostNetwork(t)
	n.gw = &Gateway{
		id:      "gw-1",
		connMgr: cm,
		sessions: map[uint32]*localSession{
			connID: {connID: connID, hostID: "host-a", epoch: 1},
		},
	}

	frame := &meshpb.MeshFrame{Msg: &meshpb.MeshFrame_ClientFrame{
		ClientFrame: &meshpb.ClientFrame{
			GatewayId: "gw-somewhere-else",
			ConnId:    connID,
			Epoch:     1,
			Data:      []byte{0x01},
		},
	}}

	if err := n.routeInboundFrame("host-a", frame); err != nil {
		t.Fatalf("routeInboundFrame: %v", err)
	}
	if transport.sent != 0 {
		t.Fatal("a frame addressed to another gateway was written to this gateway's socket")
	}
}

// TestPayloadBinding_ClientFrameRejectsZeroEpoch pins one of the three
// stale-epoch bypasses. Reaching connMgr.Send is an arbitrary write to any
// client socket, and Epoch==0 previously skipped the authority check entirely.
func TestPayloadBinding_ClientFrameRejectsZeroEpoch(t *testing.T) {
	cm := pkgnet.NewConnManager()
	transport := &clientFrameTransport{
		result: pkgnet.SendResult{Disposition: pkgnet.SendQueued, Delivery: pkgnet.DeliveryReliableOrdered},
	}
	connID := cm.AddTransport(transport, "")

	n := newManualHostNetwork(t)
	n.gw = &Gateway{
		id:      "gw-1",
		connMgr: cm,
		sessions: map[uint32]*localSession{
			connID: {connID: connID, hostID: "host-a", epoch: 5},
		},
	}

	frame := &meshpb.MeshFrame{Msg: &meshpb.MeshFrame_ClientFrame{
		ClientFrame: &meshpb.ClientFrame{
			GatewayId: "gw-1", ConnId: connID, Epoch: 0, Data: []byte{0x01},
		},
	}}

	if err := n.routeInboundFrame("host-a", frame); err != nil {
		t.Fatalf("routeInboundFrame: %v", err)
	}
	if transport.sent != 0 {
		t.Fatal("a zero-epoch ClientFrame bypassed the authority check and reached a client socket")
	}
}

// TestPayloadBinding_ClientFrameRejectsUnknownSession pins the third bypass:
// sess == nil previously skipped the epoch comparison rather than refusing.
func TestPayloadBinding_ClientFrameRejectsUnknownSession(t *testing.T) {
	cm := pkgnet.NewConnManager()
	transport := &clientFrameTransport{
		result: pkgnet.SendResult{Disposition: pkgnet.SendQueued, Delivery: pkgnet.DeliveryReliableOrdered},
	}
	connID := cm.AddTransport(transport, "")

	n := newManualHostNetwork(t)
	n.gw = &Gateway{id: "gw-1", connMgr: cm, sessions: map[uint32]*localSession{}}

	frame := &meshpb.MeshFrame{Msg: &meshpb.MeshFrame_ClientFrame{
		ClientFrame: &meshpb.ClientFrame{
			GatewayId: "gw-1", ConnId: connID, Epoch: 3, Data: []byte{0x01},
		},
	}}

	if err := n.routeInboundFrame("host-a", frame); err != nil {
		t.Fatalf("routeInboundFrame: %v", err)
	}
	if transport.sent != 0 {
		t.Fatal("a ClientFrame for an unknown session reached a client socket")
	}
}

// TestPayloadBinding_UnattributableStreamIsRejected pins that an empty stream
// identity is treated as unattributable rather than as a wildcard.
func TestPayloadBinding_UnattributableStreamIsRejected(t *testing.T) {
	n := newManualHostNetwork(t)
	n.vcm = NewVirtualConnManager(n, n.log)

	frame := &meshpb.MeshFrame{Msg: &meshpb.MeshFrame_ClientInput{
		ClientInput: &meshpb.ClientInput{GatewayId: "gw-1", ConnId: 7, Epoch: 1, Data: []byte{0x01}},
	}}

	before := metrics.Ingress().Rejected(metrics.SurfaceMesh, metrics.ReasonIdentityMismatch)
	if err := n.routeInboundFrame("", frame); err != nil {
		t.Fatalf("routeInboundFrame: %v", err)
	}
	if metrics.Ingress().Rejected(metrics.SurfaceMesh, metrics.ReasonIdentityMismatch) == before {
		t.Fatal("a frame from a stream with no identity was accepted")
	}
}
