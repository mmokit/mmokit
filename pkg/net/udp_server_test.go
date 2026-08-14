package net

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/mmokit/mmokit/pkg/net/udpproto"
)

// newTestUDPServer starts a server on loopback with small caps so capacity
// cases need a handful of sockets rather than the production defaults.
func newTestUDPServer(t *testing.T) (*UDPServer, *ConnManager, string, context.CancelFunc) {
	t.Helper()
	cm := NewConnManager()
	srv, err := NewUDPServer("127.0.0.1:0", cm)
	if err != nil {
		t.Fatalf("NewUDPServer: %v", err)
	}
	srv.SetLimits(2, 2, 50*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	go srv.Run(ctx)
	return srv, cm, srv.conn.LocalAddr().String(), cancel
}

// dialUDP returns a client socket connected to the server address.
func dialUDP(t *testing.T, serverAddr string) *net.UDPConn {
	t.Helper()
	raddr, err := net.ResolveUDPAddr("udp", serverAddr)
	if err != nil {
		t.Fatalf("ResolveUDPAddr: %v", err)
	}
	c, err := net.DialUDP("udp", nil, raddr)
	if err != nil {
		t.Fatalf("DialUDP: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// handshake performs a connection request and returns the negotiated token.
func handshake(t *testing.T, c *net.UDPConn, clientSalt uint64) uint32 {
	t.Helper()
	if _, err := c.Write(udpproto.EncodeConnReq(clientSalt)); err != nil {
		t.Fatalf("write ConnReq: %v", err)
	}
	buf := make([]byte, maxUDPPacket)
	c.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, err := c.Read(buf)
	if err != nil {
		t.Fatalf("read ConnAccept: %v", err)
	}
	gotClient, serverSalt, err := udpproto.DecodeConnAccept(buf[:n])
	if err != nil {
		t.Fatalf("DecodeConnAccept: %v", err)
	}
	if gotClient != clientSalt {
		t.Fatalf("ConnAccept clientSalt=%d, want %d", gotClient, clientSalt)
	}
	return udpproto.MakeToken(clientSalt, serverSalt)
}

// waitFor polls until cond holds or the deadline passes.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func (s *UDPServer) sessionCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.byToken)
}

// A connection request is unauthenticated and trivially spoofable, so it must
// not allocate a transport, a goroutine, or a ConnManager registration.
func TestUDPServer_ConnReqDoesNotAllocateSession(t *testing.T) {
	srv, cm, addr, cancel := newTestUDPServer(t)
	defer cancel()

	c := dialUDP(t, addr)
	handshake(t, c, 0x1111)

	waitFor(t, "pending handshake recorded", func() bool { return srv.PendingCount() == 1 })

	if got := srv.sessionCount(); got != 0 {
		t.Fatalf("sessions after ConnReq = %d, want 0", got)
	}
	if ids := cm.ActiveConnIDs(); len(ids) != 0 {
		t.Fatalf("ConnManager registrations after ConnReq = %d, want 0", len(ids))
	}
}

// Sending a data packet from the same address proves return routability and
// is what promotes the handshake into a real session.
func TestUDPServer_DataPacketPromotesPending(t *testing.T) {
	srv, cm, addr, cancel := newTestUDPServer(t)
	defer cancel()

	c := dialUDP(t, addr)
	token := handshake(t, c, 0x2222)
	waitFor(t, "pending handshake recorded", func() bool { return srv.PendingCount() == 1 })

	if _, err := c.Write(udpproto.EncodeUnreliable(token, []byte{0x00, 'h', 'i'})); err != nil {
		t.Fatalf("write unreliable: %v", err)
	}

	waitFor(t, "session promoted", func() bool { return srv.sessionCount() == 1 })
	if got := srv.PendingCount(); got != 0 {
		t.Fatalf("pending after promotion = %d, want 0", got)
	}
	if ids := cm.ActiveConnIDs(); len(ids) != 1 {
		t.Fatalf("ConnManager registrations after promotion = %d, want 1", len(ids))
	}
}

// The token is not a bearer credential. A packet carrying a valid token from a
// different source address must be dropped without reaching the session.
func TestUDPServer_RejectsValidTokenFromForeignAddress(t *testing.T) {
	srv, cm, addr, cancel := newTestUDPServer(t)
	defer cancel()

	victim := dialUDP(t, addr)
	token := handshake(t, victim, 0x3333)
	if _, err := victim.Write(udpproto.EncodeUnreliable(token, []byte{0x00, 'o', 'k'})); err != nil {
		t.Fatalf("victim write: %v", err)
	}
	waitFor(t, "victim session promoted", func() bool { return srv.sessionCount() == 1 })

	connIDs := cm.ActiveConnIDs()
	if len(connIDs) != 1 {
		t.Fatalf("expected 1 connection, got %d", len(connIDs))
	}
	// Drain whatever the victim legitimately sent.
	waitFor(t, "victim payload delivered", func() bool {
		return len(cm.DrainInput(connIDs[0])) > 0
	})

	// A different socket replays the victim's token.
	attacker := dialUDP(t, addr)
	if _, err := attacker.Write(udpproto.EncodeUnreliable(token, []byte{0x00, 'e', 'v', 'i', 'l'})); err != nil {
		t.Fatalf("attacker write: %v", err)
	}

	waitFor(t, "spoofed packet counted", func() bool { return srv.SourceMismatchDrops() >= 1 })

	if msgs := cm.DrainInput(connIDs[0]); len(msgs) != 0 {
		t.Fatalf("spoofed payload was injected into the victim session: %q", msgs)
	}
	if got := srv.sessionCount(); got != 1 {
		t.Fatalf("sessions after spoof = %d, want 1", got)
	}
}

// A spoofed Disconnect must not be a session-kill primitive.
func TestUDPServer_RejectsDisconnectFromForeignAddress(t *testing.T) {
	srv, _, addr, cancel := newTestUDPServer(t)
	defer cancel()

	victim := dialUDP(t, addr)
	token := handshake(t, victim, 0x4444)
	if _, err := victim.Write(udpproto.EncodeUnreliable(token, []byte{0x00, 'x'})); err != nil {
		t.Fatalf("victim write: %v", err)
	}
	waitFor(t, "victim session promoted", func() bool { return srv.sessionCount() == 1 })

	attacker := dialUDP(t, addr)
	if _, err := attacker.Write(udpproto.EncodeDisconnect(token)); err != nil {
		t.Fatalf("attacker disconnect: %v", err)
	}
	waitFor(t, "spoofed disconnect counted", func() bool { return srv.SourceMismatchDrops() >= 1 })

	if got := srv.sessionCount(); got != 1 {
		t.Fatalf("session killed by spoofed disconnect: count=%d, want 1", got)
	}
}

// A token that does not match the pending entry for its own source address
// proves nothing and must not promote.
func TestUDPServer_ForeignTokenDoesNotPromote(t *testing.T) {
	srv, _, addr, cancel := newTestUDPServer(t)
	defer cancel()

	a := dialUDP(t, addr)
	tokenA := handshake(t, a, 0x5555)
	waitFor(t, "first pending", func() bool { return srv.PendingCount() == 1 })

	b := dialUDP(t, addr)
	handshake(t, b, 0x6666)
	waitFor(t, "second pending", func() bool { return srv.PendingCount() == 2 })

	// b sends a's token from b's address.
	if _, err := b.Write(udpproto.EncodeUnreliable(tokenA, []byte{0x00, 'n', 'o'})); err != nil {
		t.Fatalf("write: %v", err)
	}
	time.Sleep(50 * time.Millisecond)

	if got := srv.sessionCount(); got != 0 {
		t.Fatalf("foreign token promoted a session: count=%d, want 0", got)
	}
}

// Retrying a connection request must resend the original salts, or the peer
// would derive a different token than the server recorded.
func TestUDPServer_ConnReqRetryIsIdempotent(t *testing.T) {
	srv, _, addr, cancel := newTestUDPServer(t)
	defer cancel()

	c := dialUDP(t, addr)
	first := handshake(t, c, 0x7777)
	second := handshake(t, c, 0x7777)

	if first != second {
		t.Fatalf("retry minted a new token: %08x then %08x", first, second)
	}
	if got := srv.PendingCount(); got != 1 {
		t.Fatalf("pending after retry = %d, want 1", got)
	}
}

// The unproven-handshake table is bounded, so a spoofed request flood cannot
// grow server memory without limit.
func TestUDPServer_PendingTableIsBounded(t *testing.T) {
	srv, _, addr, cancel := newTestUDPServer(t)
	defer cancel()
	srv.SetLimits(0, 0, time.Hour) // keep entries alive so the cap is what bites

	for i := range 2 {
		c := dialUDP(t, addr)
		handshake(t, c, uint64(0x8000+i))
	}
	waitFor(t, "pending table full", func() bool { return srv.PendingCount() == 2 })

	overflow := dialUDP(t, addr)
	if _, err := overflow.Write(udpproto.EncodeConnReq(0x9999)); err != nil {
		t.Fatalf("overflow write: %v", err)
	}
	waitFor(t, "overflow refused", func() bool { return srv.PendingFullDrops() >= 1 })

	if got := srv.PendingCount(); got > 2 {
		t.Fatalf("pending exceeded cap: %d, want <= 2", got)
	}
}

// Expired handshakes are swept so a bounded table cannot be wedged shut by a
// burst of requests that never complete.
func TestUDPServer_PendingEntriesExpire(t *testing.T) {
	srv, _, addr, cancel := newTestUDPServer(t)
	defer cancel()

	for i := range 2 {
		c := dialUDP(t, addr)
		handshake(t, c, uint64(0xA000+i))
	}
	waitFor(t, "pending table full", func() bool { return srv.PendingCount() == 2 })

	time.Sleep(70 * time.Millisecond) // > PendingTTL set in newTestUDPServer

	fresh := dialUDP(t, addr)
	token := handshake(t, fresh, 0xB000)
	if _, err := fresh.Write(udpproto.EncodeUnreliable(token, []byte{0x00, 'k'})); err != nil {
		t.Fatalf("write: %v", err)
	}
	waitFor(t, "fresh handshake promoted after sweep", func() bool { return srv.sessionCount() == 1 })
}

// Promotion respects the session cap.
func TestUDPServer_ConnectionCapEnforced(t *testing.T) {
	srv, _, addr, cancel := newTestUDPServer(t)
	defer cancel()
	srv.SetLimits(1, 8, time.Hour)

	first := dialUDP(t, addr)
	t1 := handshake(t, first, 0xC001)
	if _, err := first.Write(udpproto.EncodeUnreliable(t1, []byte{0x00, '1'})); err != nil {
		t.Fatalf("write: %v", err)
	}
	waitFor(t, "first session promoted", func() bool { return srv.sessionCount() == 1 })

	second := dialUDP(t, addr)
	t2 := handshake(t, second, 0xC002)
	if _, err := second.Write(udpproto.EncodeUnreliable(t2, []byte{0x00, '2'})); err != nil {
		t.Fatalf("write: %v", err)
	}
	waitFor(t, "second promotion refused", func() bool { return srv.CapacityDrops() >= 1 })

	if got := srv.sessionCount(); got != 1 {
		t.Fatalf("sessions exceeded cap: %d, want 1", got)
	}
}
