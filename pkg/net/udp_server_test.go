package net

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/mmokit/mmokit/pkg/net/udpcrypto"
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
	srv.SetMaxConnections(2)

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

// testKeyRegistry installs a registry holding one known key and returns the
// key ID plus the key, so a test can complete a real authenticated handshake.
func testKeyRegistry(t *testing.T, s *UDPServer) (UDPKeyID, udpcrypto.Key) {
	t.Helper()
	reg := NewUDPKeyRegistry(0, time.Minute)
	id, entry, err := reg.Issue("test-user", "tester", time.Now())
	if err != nil {
		t.Fatalf("issue udp key: %v", err)
	}
	s.SetKeyRegistry(reg)
	return id, entry.Key
}

// handshake performs the full v2 handshake — ConnReq, ConnAccept, ConnConfirm —
// and returns the negotiated token plus the client-side crypto session.
//
// There is no shortcut past ConnConfirm any more: a session exists only once the
// stateless cookie has been echoed from this source address AND a key ID has
// resolved.
func handshake(t *testing.T, c *net.UDPConn, clientSalt uint64, keyID UDPKeyID, key udpcrypto.Key) (uint32, *udpcrypto.Session) {
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
	gotClient, serverSalt, cookie, err := udpproto.DecodeConnAccept(buf[:n])
	if err != nil {
		t.Fatalf("DecodeConnAccept: %v", err)
	}
	if gotClient != clientSalt {
		t.Fatalf("ConnAccept clientSalt=%d, want %d", gotClient, clientSalt)
	}
	if _, err := c.Write(udpproto.EncodeConnConfirm(uint64(keyID), clientSalt, serverSalt, cookie)); err != nil {
		t.Fatalf("write ConnConfirm: %v", err)
	}
	sess, err := udpcrypto.NewSession(key, udpcrypto.RoleClient,
		udpproto.SessionSalt(clientSalt, serverSalt))
	if err != nil {
		t.Fatalf("client session: %v", err)
	}
	return udpproto.MakeToken(clientSalt, serverSalt), sess
}

// sealedUnreliable builds a v2 unreliable packet under sess.
func sealedUnreliable(t *testing.T, sess *udpcrypto.Session, token uint32, payload []byte) []byte {
	t.Helper()
	hdr, sealed, err := sess.SealWithHeader(payload, func(ctr uint64) []byte {
		return udpproto.EncodeUnreliableHeader(token, ctr)
	})
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	return append(append([]byte{}, hdr...), sealed...)
}

// sealedDisconnect builds a v2 disconnect packet under sess.
func sealedDisconnect(t *testing.T, sess *udpcrypto.Session, token uint32) []byte {
	t.Helper()
	hdr, sealed, err := sess.SealWithHeader(nil, func(ctr uint64) []byte {
		return udpproto.EncodeDisconnectHeader(token, ctr)
	})
	if err != nil {
		t.Fatalf("seal disconnect: %v", err)
	}
	return append(append([]byte{}, hdr...), sealed...)
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
func TestUDPServer_ConnReqAllocatesNothing(t *testing.T) {
	srv, cm, addr, cancel := newTestUDPServer(t)
	defer cancel()
	testKeyRegistry(t, srv)

	c := dialUDP(t, addr)
	if _, err := c.Write(udpproto.EncodeConnReq(0x1111)); err != nil {
		t.Fatalf("write ConnReq: %v", err)
	}
	time.Sleep(50 * time.Millisecond)

	if got := srv.sessionCount(); got != 0 {
		t.Fatalf("sessions after ConnReq = %d, want 0", got)
	}
	if ids := cm.ActiveConnIDs(); len(ids) != 0 {
		t.Fatalf("ConnManager registrations after ConnReq = %d, want 0", len(ids))
	}
}

// The property that replaced the bounded pending table: an unauthenticated
// request flood leaves NO state behind at all, so there is nothing to size, to
// sweep, or to exhaust. This is the last Tier 1 residual, closed.
func TestUDPServer_HandshakeFloodAllocatesNothing(t *testing.T) {
	srv, cm, addr, cancel := newTestUDPServer(t)
	defer cancel()
	testKeyRegistry(t, srv)

	c := dialUDP(t, addr)
	for i := range 500 {
		if _, err := c.Write(udpproto.EncodeConnReq(uint64(i))); err != nil {
			t.Fatalf("flood write %d: %v", i, err)
		}
	}
	time.Sleep(100 * time.Millisecond)

	if got := srv.sessionCount(); got != 0 {
		t.Fatalf("500 connection requests created %d sessions, want 0", got)
	}
	if ids := cm.ActiveConnIDs(); len(ids) != 0 {
		t.Fatalf("500 connection requests created %d registrations, want 0", len(ids))
	}
}

// A completed handshake — ConnReq, ConnAccept, ConnConfirm — is what creates a
// session. Data packets no longer promote anything.
func TestUDPServer_ConnConfirmEstablishesSession(t *testing.T) {
	srv, cm, addr, cancel := newTestUDPServer(t)
	defer cancel()
	keyID, key := testKeyRegistry(t, srv)

	c := dialUDP(t, addr)
	token, sess := handshake(t, c, 0x2222, keyID, key)
	waitFor(t, "session established", func() bool { return srv.sessionCount() == 1 })

	if _, err := c.Write(sealedUnreliable(t, sess, token, []byte{0x00, 'h', 'i'})); err != nil {
		t.Fatalf("write unreliable: %v", err)
	}
	waitFor(t, "registered with ConnManager", func() bool { return len(cm.ActiveConnIDs()) == 1 })

	ids := cm.ActiveConnIDs()
	waitFor(t, "payload delivered", func() bool { return len(cm.DrainInput(ids[0])) > 0 })
}

// Data packets are inert before a ConnConfirm: without one there is no session
// to route to, whatever token they carry.
func TestUDPServer_DataPacketDoesNotPromote(t *testing.T) {
	srv, _, addr, cancel := newTestUDPServer(t)
	defer cancel()
	testKeyRegistry(t, srv)

	c := dialUDP(t, addr)
	if _, err := c.Write(udpproto.EncodeConnReq(0x3333)); err != nil {
		t.Fatalf("ConnReq: %v", err)
	}
	buf := make([]byte, maxUDPPacket)
	c.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, err := c.Read(buf)
	if err != nil {
		t.Fatalf("read accept: %v", err)
	}
	_, serverSalt, _, err := udpproto.DecodeConnAccept(buf[:n])
	if err != nil {
		t.Fatalf("decode accept: %v", err)
	}
	token := udpproto.MakeToken(0x3333, serverSalt)

	// Skip ConnConfirm entirely and send data.
	hdr := udpproto.EncodeUnreliableHeader(token, 1)
	pkt := append(append([]byte{}, hdr...), make([]byte, udpproto.TagSize)...)
	if _, err := c.Write(pkt); err != nil {
		t.Fatalf("write: %v", err)
	}
	time.Sleep(50 * time.Millisecond)

	if got := srv.sessionCount(); got != 0 {
		t.Fatalf("data packet created a session without ConnConfirm: %d", got)
	}
}

// A cookie is bound to the address it was minted for, so one peer cannot use
// another's — and a fabricated cookie proves nothing.
func TestUDPServer_ForgedCookieRejected(t *testing.T) {
	srv, _, addr, cancel := newTestUDPServer(t)
	defer cancel()
	keyID, _ := testKeyRegistry(t, srv)

	c := dialUDP(t, addr)
	if _, err := c.Write(udpproto.EncodeConnReq(0x4444)); err != nil {
		t.Fatalf("ConnReq: %v", err)
	}
	buf := make([]byte, maxUDPPacket)
	c.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, err := c.Read(buf)
	if err != nil {
		t.Fatalf("read accept: %v", err)
	}
	_, serverSalt, cookie, err := udpproto.DecodeConnAccept(buf[:n])
	if err != nil {
		t.Fatalf("decode accept: %v", err)
	}

	bad := append([]byte{}, cookie...)
	bad[0] ^= 0x01
	if _, err := c.Write(udpproto.EncodeConnConfirm(uint64(keyID), 0x4444, serverSalt, bad)); err != nil {
		t.Fatalf("write confirm: %v", err)
	}
	waitFor(t, "forged cookie counted", func() bool { return srv.HandshakeRejectDrops() >= 1 })

	if got := srv.sessionCount(); got != 0 {
		t.Fatalf("forged cookie established a session: %d", got)
	}
}

// A cookie proves return routability, not identity. Without a key that resolves,
// the handshake still fails.
func TestUDPServer_UnknownKeyRejected(t *testing.T) {
	srv, _, addr, cancel := newTestUDPServer(t)
	defer cancel()
	testKeyRegistry(t, srv)

	c := dialUDP(t, addr)
	if _, err := c.Write(udpproto.EncodeConnReq(0x5555)); err != nil {
		t.Fatalf("ConnReq: %v", err)
	}
	buf := make([]byte, maxUDPPacket)
	c.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, _ := c.Read(buf)
	_, serverSalt, cookie, err := udpproto.DecodeConnAccept(buf[:n])
	if err != nil {
		t.Fatalf("decode accept: %v", err)
	}

	if _, err := c.Write(udpproto.EncodeConnConfirm(0xDEADBEEF, 0x5555, serverSalt, cookie)); err != nil {
		t.Fatalf("write confirm: %v", err)
	}
	waitFor(t, "unknown key counted", func() bool { return srv.HandshakeRejectDrops() >= 1 })

	if got := srv.sessionCount(); got != 0 {
		t.Fatalf("unknown key established a session: %d", got)
	}
}

// A listener with no key registry cannot authenticate anyone, and must refuse
// rather than fall back to an anonymous session.
func TestUDPServer_NoKeyRegistryRefuses(t *testing.T) {
	srv, _, addr, cancel := newTestUDPServer(t)
	defer cancel() // deliberately no SetKeyRegistry

	c := dialUDP(t, addr)
	if _, err := c.Write(udpproto.EncodeConnReq(0x6666)); err != nil {
		t.Fatalf("ConnReq: %v", err)
	}
	buf := make([]byte, maxUDPPacket)
	c.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, _ := c.Read(buf)
	_, serverSalt, cookie, err := udpproto.DecodeConnAccept(buf[:n])
	if err != nil {
		t.Fatalf("decode accept: %v", err)
	}
	if _, err := c.Write(udpproto.EncodeConnConfirm(1, 0x6666, serverSalt, cookie)); err != nil {
		t.Fatalf("write confirm: %v", err)
	}
	waitFor(t, "refusal counted", func() bool { return srv.HandshakeRejectDrops() >= 1 })

	if got := srv.sessionCount(); got != 0 {
		t.Fatalf("session created without a key registry: %d", got)
	}
}

// The token is not a bearer credential. A packet carrying a valid token from a
// different source address must be dropped without reaching the session.
func TestUDPServer_RejectsValidTokenFromForeignAddress(t *testing.T) {
	srv, cm, addr, cancel := newTestUDPServer(t)
	defer cancel()
	keyID, key := testKeyRegistry(t, srv)

	victim := dialUDP(t, addr)
	token, sess := handshake(t, victim, 0x3333, keyID, key)
	waitFor(t, "victim session established", func() bool { return srv.sessionCount() == 1 })
	if _, err := victim.Write(sealedUnreliable(t, sess, token, []byte{0x00, 'o', 'k'})); err != nil {
		t.Fatalf("victim write: %v", err)
	}

	connIDs := cm.ActiveConnIDs()
	if len(connIDs) != 1 {
		t.Fatalf("expected 1 connection, got %d", len(connIDs))
	}
	waitFor(t, "victim payload delivered", func() bool {
		return len(cm.DrainInput(connIDs[0])) > 0
	})

	attacker := dialUDP(t, addr)
	if _, err := attacker.Write(sealedUnreliable(t, sess, token, []byte{0x00, 'e', 'v', 'i', 'l'})); err != nil {
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

// Even from the RIGHT address, a packet that does not authenticate must not be
// delivered. This is what the token stopped being able to prove.
func TestUDPServer_ForgedPayloadRejected(t *testing.T) {
	srv, cm, addr, cancel := newTestUDPServer(t)
	defer cancel()
	keyID, key := testKeyRegistry(t, srv)

	c := dialUDP(t, addr)
	token, sess := handshake(t, c, 0x7A7A, keyID, key)
	waitFor(t, "session established", func() bool { return srv.sessionCount() == 1 })
	if _, err := c.Write(sealedUnreliable(t, sess, token, []byte{0x00, 'o', 'k'})); err != nil {
		t.Fatalf("write: %v", err)
	}
	waitFor(t, "registered", func() bool { return len(cm.ActiveConnIDs()) == 1 })
	ids := cm.ActiveConnIDs()
	waitFor(t, "real payload delivered", func() bool { return len(cm.DrainInput(ids[0])) > 0 })

	// Correct header, garbage body: the tag cannot verify.
	hdr := udpproto.EncodeUnreliableHeader(token, 9999)
	forged := append(append([]byte{}, hdr...), make([]byte, udpproto.TagSize+4)...)
	if _, err := c.Write(forged); err != nil {
		t.Fatalf("forged write: %v", err)
	}
	time.Sleep(80 * time.Millisecond)

	if msgs := cm.DrainInput(ids[0]); len(msgs) != 0 {
		t.Fatalf("unauthenticated payload was delivered: %q", msgs)
	}
	if got := srv.sessionCount(); got != 1 {
		t.Fatalf("forged packet disturbed the session: %d", got)
	}
}

// A Disconnect must authenticate. In v1 a token was enough to kill a session.
func TestUDPServer_RejectsUnauthenticatedDisconnect(t *testing.T) {
	srv, _, addr, cancel := newTestUDPServer(t)
	defer cancel()
	keyID, key := testKeyRegistry(t, srv)

	victim := dialUDP(t, addr)
	token, _ := handshake(t, victim, 0x4444, keyID, key)
	waitFor(t, "victim session established", func() bool { return srv.sessionCount() == 1 })

	// Right token, right address, but a body that cannot authenticate.
	hdr := udpproto.EncodeDisconnectHeader(token, 1)
	forged := append(append([]byte{}, hdr...), make([]byte, udpproto.TagSize)...)
	if _, err := victim.Write(forged); err != nil {
		t.Fatalf("write forged disconnect: %v", err)
	}
	time.Sleep(80 * time.Millisecond)

	if got := srv.sessionCount(); got != 1 {
		t.Fatalf("session killed by an unauthenticated disconnect: %d, want 1", got)
	}
}

// Retrying a connection request while established must resend the original
// salts, or the peer would derive a different token than its session uses.
func TestUDPServer_ConnReqRetryIsIdempotentWhenEstablished(t *testing.T) {
	srv, _, addr, cancel := newTestUDPServer(t)
	defer cancel()
	keyID, key := testKeyRegistry(t, srv)

	c := dialUDP(t, addr)
	first, _ := handshake(t, c, 0x7777, keyID, key)
	waitFor(t, "session established", func() bool { return srv.sessionCount() == 1 })

	second, _ := handshake(t, c, 0x7777, keyID, key)
	if first != second {
		t.Fatalf("retry minted a new token for an established session: %08x then %08x", first, second)
	}
	if got := srv.sessionCount(); got != 1 {
		t.Fatalf("retry created a second session: %d", got)
	}
}

// Promotion respects the session cap.
func TestUDPServer_ConnectionCapEnforced(t *testing.T) {
	srv, _, addr, cancel := newTestUDPServer(t)
	defer cancel()
	keyID, key := testKeyRegistry(t, srv)
	srv.SetMaxConnections(1)

	first := dialUDP(t, addr)
	handshake(t, first, 0xC001, keyID, key)
	waitFor(t, "first session established", func() bool { return srv.sessionCount() == 1 })

	second := dialUDP(t, addr)
	handshake(t, second, 0xC002, keyID, key)
	waitFor(t, "second promotion refused", func() bool { return srv.CapacityDrops() >= 1 })

	if got := srv.sessionCount(); got != 1 {
		t.Fatalf("sessions exceeded cap: %d, want 1", got)
	}
}

// authBinding records what SetOnAuthenticated hands back.
type authBinding struct {
	mu    sync.Mutex
	calls []struct {
		connID   uint32
		userID   string
		username string
	}
	block chan struct{} // when non-nil, the callback waits on it
}

func (a *authBinding) install(s *UDPServer) {
	s.SetOnAuthenticated(func(connID uint32, userID, username string) {
		if a.block != nil {
			<-a.block
		}
		a.mu.Lock()
		defer a.mu.Unlock()
		a.calls = append(a.calls, struct {
			connID   uint32
			userID   string
			username string
		}{connID, userID, username})
	})
}

func (a *authBinding) count() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.calls)
}

// A completed handshake must hand the gateway the identity the presented key
// vouches for. Without this the session is encrypted but anonymous, and no
// PlayerAssignment is ever dispatched — the client sits in a working session
// with no entity.
func TestUDPServer_HandshakeBindsIdentity(t *testing.T) {
	srv, _, addr, cancel := newTestUDPServer(t)
	defer cancel()
	var bind authBinding
	bind.install(srv)
	keyID, key := testKeyRegistry(t, srv)

	c := dialUDP(t, addr)
	handshake(t, c, 0xA001, keyID, key)
	waitFor(t, "identity bound", func() bool { return bind.count() == 1 })

	bind.mu.Lock()
	got := bind.calls[0]
	bind.mu.Unlock()
	if got.userID != "test-user" || got.username != "tester" {
		t.Fatalf("bound identity = %+v, want test-user/tester", got)
	}
	if got.connID == 0 {
		t.Fatal("bound connID 0; the transport must be registered before the identity is announced")
	}
	// The connID must be the one the ConnManager issued, or replies to the
	// PlayerAssignment would route to a different connection.
	srv.mu.RLock()
	var known bool
	for _, id := range srv.connIDs {
		if id == got.connID {
			known = true
		}
	}
	srv.mu.RUnlock()
	if !known {
		t.Fatalf("bound connID %d matches no registered transport", got.connID)
	}
}

// A client retries ConnConfirm when its first is lost. The retry must not bind
// a second time: dispatching a second PlayerAssignment for a player who
// already has one would spawn a duplicate entity.
func TestUDPServer_RepeatedConnConfirmBindsOnce(t *testing.T) {
	srv, _, addr, cancel := newTestUDPServer(t)
	defer cancel()
	var bind authBinding
	bind.install(srv)
	keyID, key := testKeyRegistry(t, srv)

	c := dialUDP(t, addr)
	handshake(t, c, 0xB001, keyID, key)
	waitFor(t, "identity bound", func() bool { return bind.count() == 1 })

	// Same salts → same token → the idempotent re-confirm path.
	handshake(t, c, 0xB001, keyID, key)
	waitFor(t, "session still single", func() bool { return srv.sessionCount() == 1 })

	// Give a spurious second binding time to land before asserting its absence.
	time.Sleep(50 * time.Millisecond)
	if got := bind.count(); got != 1 {
		t.Fatalf("re-confirm bound the identity %d times, want 1", got)
	}
}

// A handshake that fails to resolve a key must bind nothing: an unauthenticated
// peer must not reach the gateway with any identity at all.
func TestUDPServer_RejectedHandshakeBindsNothing(t *testing.T) {
	srv, _, addr, cancel := newTestUDPServer(t)
	defer cancel()
	var bind authBinding
	bind.install(srv)
	_, key := testKeyRegistry(t, srv)

	c := dialUDP(t, addr)
	// A key ID that was never issued.
	handshake(t, c, 0xD001, UDPKeyID(0xdeadbeefcafe), key)
	waitFor(t, "handshake rejected", func() bool { return srv.HandshakeRejectDrops() >= 1 })

	time.Sleep(50 * time.Millisecond)
	if got := bind.count(); got != 0 {
		t.Fatalf("rejected handshake bound %d identities, want 0", got)
	}
}

// The identity callback ends in resolveSpawn, which on a standalone gateway is
// an RPC with a two-second deadline. dispatch runs on the ONE reader goroutine
// serving every session on the socket, so a callback that ran inline would
// stall all UDP ingress for the duration of one client's spawn lookup. This
// asserts the callback cannot do that.
func TestUDPServer_SlowIdentityBindingDoesNotStallIngress(t *testing.T) {
	srv, _, addr, cancel := newTestUDPServer(t)
	defer cancel()
	bind := authBinding{block: make(chan struct{})}
	bind.install(srv)
	defer close(bind.block)
	keyID, key := testKeyRegistry(t, srv)

	// First client's binding is now wedged in the callback.
	first := dialUDP(t, addr)
	handshake(t, first, 0xE001, keyID, key)
	waitFor(t, "first session established", func() bool { return srv.sessionCount() == 1 })

	// A second client must still be able to complete a handshake. If the
	// callback held the reader goroutine, this would time out.
	second := dialUDP(t, addr)
	handshake(t, second, 0xE002, keyID, key)
	waitFor(t, "second session established despite a blocked binding",
		func() bool { return srv.sessionCount() == 2 })
}
