package universe

import (
	"bytes"
	"context"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	meshpb "github.com/zenion/mmoserver/gen/go/meshpb"
	"github.com/zenion/mmoserver/pkg/logger"
)

// testHostNetworkLogger returns a default logger suitable for tests.
func testHostNetworkLogger(t *testing.T) *logger.Logger {
	t.Helper()
	return logger.New()
}

// newManualHostNetwork constructs a HostNetwork without binding a listener or
// starting a gRPC server. Used for tests that only need to exercise the
// channel/queue semantics (SendLossy, SendReliable backpressure, dropPeer).
func newManualHostNetwork(t *testing.T) *HostNetwork {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	n := &HostNetwork{
		hostID: "test-manual",
		peers:  make(map[string]*hostPeer),
		log:    testHostNetworkLogger(t),
		ctx:    ctx,
		cancel: cancel,
	}
	t.Cleanup(cancel)
	return n
}

// newIdlePeer creates a hostPeer with a channel of the standard capacity but
// no sender goroutine — the queue never drains.
// The conn field uses a lazy grpc.ClientConn (grpc.NewClient is non-blocking)
// so conn.Close() is safe to call.
func newIdlePeer(t *testing.T, hostID string) *hostPeer {
	t.Helper()
	// Non-routable sentinel address — grpc.NewClient is lazy so this never
	// actually dials. The idle peer has no sender/receiver goroutines.
	conn, err := grpc.NewClient("localhost:1", grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("newIdlePeer grpc.NewClient: %v", err)
	}
	streamCtx, cancelStream := context.WithCancel(context.Background())
	_ = streamCtx
	p := &hostPeer{
		hostID: hostID,
		outQ:   make(chan outboundFrame, peerOutQueueSize),
		done:   make(chan struct{}),
		conn:   conn,
		cancel: cancelStream,
	}
	// Close done immediately — no sender goroutine will close it.
	close(p.done)
	t.Cleanup(func() { _ = conn.Close() })
	return p
}

// TestHostNetworkTwoPeersRoundTrip stands up two real HostNetworks in-process
// via ":0", cross-connects them, encodes a MsgBorderFrame, sends it reliably
// from A to B, and asserts it arrives in the destination cell's Inbox on B.
func TestHostNetworkTwoPeersRoundTrip(t *testing.T) {
	hostA := NewHost("host-a")
	hostB := NewHost("host-b")

	netA, err := NewHostNetwork(hostA, ":0", testHostNetworkLogger(t))
	if err != nil {
		t.Fatalf("NewHostNetwork A: %v", err)
	}
	netB, err := NewHostNetwork(hostB, ":0", testHostNetworkLogger(t))
	if err != nil {
		t.Fatalf("NewHostNetwork B: %v", err)
	}
	t.Cleanup(func() {
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); _ = netA.Shutdown() }()
		go func() { defer wg.Done(); _ = netB.Shutdown() }()
		wg.Wait()
	})

	// Register a destination cell on host B.
	cellB := &Cell{
		ID:    "cell_0_0",
		Inbox: make(chan CellMessage, 16),
	}
	hostB.AddCell(CellID{X: 0, Y: 0}, cellB)

	// Cross-connect.
	if err := netA.ConnectPeer("host-b", netB.Addr()); err != nil {
		t.Fatalf("A.ConnectPeer(B): %v", err)
	}
	if err := netB.ConnectPeer("host-a", netA.Addr()); err != nil {
		t.Fatalf("B.ConnectPeer(A): %v", err)
	}

	if err := netA.WaitPeersReady([]string{"host-b"}, 2*time.Second); err != nil {
		t.Fatalf("A WaitPeersReady: %v", err)
	}
	if err := netB.WaitPeersReady([]string{"host-a"}, 2*time.Second); err != nil {
		t.Fatalf("B WaitPeersReady: %v", err)
	}

	// Encode a MsgBorderFrame targeting "cell_0_0" on host B.
	msg := CellMessage{
		Type:        MsgBorderFrame,
		FromCellID:  "cell_1_0",
		BorderFrame: []byte{0x01, 0x02, 0x03},
	}
	frame, err := encodeCellMessage(msg, "cell_0_0")
	if err != nil {
		t.Fatalf("encodeCellMessage: %v", err)
	}

	if err := netA.SendReliable("host-b", frame); err != nil {
		t.Fatalf("SendReliable A→B: %v", err)
	}

	select {
	case got := <-cellB.Inbox:
		if got.Type != MsgBorderFrame {
			t.Errorf("got Type=%v, want MsgBorderFrame", got.Type)
		}
		if !bytes.Equal(got.BorderFrame, []byte{0x01, 0x02, 0x03}) {
			t.Errorf("BorderFrame payload = %v, want %v", got.BorderFrame, []byte{0x01, 0x02, 0x03})
		}
		if got.FromCellID != "cell_1_0" {
			t.Errorf("FromCellID = %q, want %q", got.FromCellID, "cell_1_0")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout: frame did not arrive in cellB.Inbox")
	}
}

// TestHostNetworkSendLossyDropsOnFullQueue verifies that SendLossy returns
// false once the peer's outbound queue is at capacity.
func TestHostNetworkSendLossyDropsOnFullQueue(t *testing.T) {
	n := newManualHostNetwork(t)
	peer := newIdlePeer(t, "peer")
	n.peers["peer"] = peer

	frame := &meshpb.MeshFrame{DestCellId: "cell_0_0"}

	for i := 0; i < peerOutQueueSize; i++ {
		if ok := n.SendLossy("peer", frame); !ok {
			t.Fatalf("send %d unexpectedly failed; queue should not be full yet", i)
		}
	}

	if got := len(peer.outQ); got != cap(peer.outQ) {
		t.Fatalf("queue not full: len=%d cap=%d", got, cap(peer.outQ))
	}

	if ok := n.SendLossy("peer", frame); ok {
		t.Fatal("expected drop on full queue, got true")
	}
}

// TestHostNetworkSendReliableQueueBackpressureDeadline fills a peer queue to
// capacity then asserts SendReliable returns a "queue backpressure" error
// within peerSendDeadline + reasonable slack.
func TestHostNetworkSendReliableQueueBackpressureDeadline(t *testing.T) {
	n := newManualHostNetwork(t)
	peer := newIdlePeer(t, "peer")
	n.peers["peer"] = peer

	frame := &meshpb.MeshFrame{DestCellId: "cell_0_0"}

	// Pre-fill the queue via the channel directly so we skip the mutex path.
	for i := 0; i < peerOutQueueSize; i++ {
		peer.outQ <- outboundFrame{frame: frame}
	}

	start := time.Now()
	err := n.SendReliable("peer", frame)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected error from SendReliable on full queue, got nil")
	}
	if !strings.Contains(err.Error(), "queue backpressure") {
		t.Errorf("error %q does not contain \"queue backpressure\"", err.Error())
	}

	const slack = time.Second
	if elapsed > peerSendDeadline+slack {
		t.Errorf("SendReliable took %v, want <= %v", elapsed, peerSendDeadline+slack)
	}
}

// TestHostNetworkSendReliableUnknownPeer verifies SendReliable returns a
// "no peer" error when the target hostID is not registered.
func TestHostNetworkSendReliableUnknownPeer(t *testing.T) {
	n := newManualHostNetwork(t)

	frame := &meshpb.MeshFrame{DestCellId: "cell_0_0"}
	err := n.SendReliable("nope", frame)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "no peer") {
		t.Errorf("error %q does not contain \"no peer\"", err.Error())
	}
}

// TestHostNetworkShutdownIsClean stands up two real HostNetworks, cross-
// connects them, then shuts both down and checks for goroutine leaks.
func TestHostNetworkShutdownIsClean(t *testing.T) {
	before := runtime.NumGoroutine()

	hostA := NewHost("shutdown-a")
	hostB := NewHost("shutdown-b")

	netA, err := NewHostNetwork(hostA, ":0", testHostNetworkLogger(t))
	if err != nil {
		t.Fatalf("NewHostNetwork A: %v", err)
	}
	netB, err := NewHostNetwork(hostB, ":0", testHostNetworkLogger(t))
	if err != nil {
		t.Fatalf("NewHostNetwork B: %v", err)
	}

	if err := netA.ConnectPeer("shutdown-b", netB.Addr()); err != nil {
		t.Fatalf("A.ConnectPeer: %v", err)
	}
	if err := netB.ConnectPeer("shutdown-a", netA.Addr()); err != nil {
		t.Fatalf("B.ConnectPeer: %v", err)
	}

	if err := netA.WaitPeersReady([]string{"shutdown-b"}, 2*time.Second); err != nil {
		t.Fatalf("A WaitPeersReady: %v", err)
	}
	if err := netB.WaitPeersReady([]string{"shutdown-a"}, 2*time.Second); err != nil {
		t.Fatalf("B WaitPeersReady: %v", err)
	}

	done := make(chan error, 2)
	go func() { done <- netA.Shutdown() }()
	go func() { done <- netB.Shutdown() }()

	deadline := time.After(10 * time.Second)
	for i := 0; i < 2; i++ {
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Shutdown error: %v", err)
			}
		case <-deadline:
			t.Fatal("Shutdown did not complete within 10 seconds")
		}
	}

	// Allow grpc-go internal goroutines to drain.
	time.Sleep(300 * time.Millisecond)
	after := runtime.NumGoroutine()

	// Generous slack for any grpc-go internal goroutines that may linger.
	const slack = 10
	if after > before+slack {
		t.Errorf("possible goroutine leak: before=%d after=%d (allowed slack=%d)", before, after, slack)
	}
}

// TestHostNetworkDropPeerIdempotent verifies that dropPeer is idempotent
// and that pointer-identity prevents a stale peer from evicting a replacement.
func TestHostNetworkDropPeerIdempotent(t *testing.T) {
	n := newManualHostNetwork(t)

	peer1 := newIdlePeer(t, "peer")
	peer2 := newIdlePeer(t, "peer")

	// Subtest 1: double-drop the same peer must not panic.
	n.peers["peer"] = peer1
	n.dropPeer(peer1)
	if _, ok := n.peers["peer"]; ok {
		t.Error("peer still in map after first dropPeer")
	}
	n.dropPeer(peer1) // second call — must be a no-op
	if _, ok := n.peers["peer"]; ok {
		t.Error("peer re-appeared in map after second dropPeer")
	}

	// Subtest 2: pointer-identity guard — peer2 replaces peer1 in the map;
	// a stale dropPeer(peer1) must not evict peer2.
	n.peers["peer"] = peer2
	n.dropPeer(peer1)
	got, ok := n.peers["peer"]
	if !ok || got != peer2 {
		t.Error("pointer-identity guard failed: peer2 was incorrectly removed by dropPeer(peer1)")
	}

	// Clean up peer2.
	n.dropPeer(peer2)
	if _, ok := n.peers["peer"]; ok {
		t.Error("peer2 still in map after explicit dropPeer(peer2)")
	}
}
