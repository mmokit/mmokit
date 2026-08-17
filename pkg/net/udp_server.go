package net

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"log"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mmokit/mmokit/pkg/net/udpcrypto"
	"github.com/mmokit/mmokit/pkg/net/udpproto"
)

const (
	maxUDPPacket = 1400 // safe MTU

	defaultMaxUDPConnections = 1024
	dropLogInterval          = time.Second
)

// The pending-handshake table that used to live here is gone. Return
// routability is now proven by a stateless HMAC cookie (HandshakeCookieSigner):
// the server mints a MAC over the peer's address and both salts, the peer
// echoes it in ConnConfirm, and the server recomputes it from the echoing
// packet's own source address. Nothing is stored between the two packets, so
// there is nothing for a flood of spoofed addresses to exhaust — which was the
// last open Tier 1 residual.

// UDPServer manages a single UDP socket and dispatches packets to UDPTransports.
type UDPServer struct {
	conn    *net.UDPConn
	connMgr *ConnManager
	mu      sync.RWMutex
	byToken map[uint32]*UDPTransport
	byAddr  map[netip.AddrPort]*UDPTransport // peer address → transport
	connIDs map[*UDPTransport]uint32         // transport → connMgr connID

	// cookies mints and verifies the stateless handshake cookie. keys resolves
	// the key IDs clients present in ConnConfirm; a nil registry means this
	// listener cannot authenticate anyone and refuses every handshake.
	cookies *HandshakeCookieSigner
	keys    *UDPKeyRegistry

	// Limits are guarded by mu because the reader goroutine consults them on
	// every connection request and promotion. Change them with SetLimits.
	maxConnections int
	limits         WireLimits

	sourceMismatchDrops  atomic.Uint64
	capacityDrops        atomic.Uint64
	handshakeRejectDrops atomic.Uint64
	lastDropLogStamp     atomic.Int64
}

// NewUDPServer creates a new UDP server bound to the given address.
func NewUDPServer(addr string, connMgr *ConnManager) (*UDPServer, error) {
	udpAddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return nil, err
	}
	conn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		return nil, err
	}

	s := &UDPServer{
		conn:           conn,
		connMgr:        connMgr,
		byToken:        make(map[uint32]*UDPTransport),
		byAddr:         make(map[netip.AddrPort]*UDPTransport),
		connIDs:        make(map[*UDPTransport]uint32),
		maxConnections: defaultMaxUDPConnections,
		limits:         DefaultWireLimits(),
	}
	signer, err := NewHandshakeCookieSigner()
	if err != nil {
		conn.Close()
		return nil, err
	}
	s.cookies = signer
	return s, nil
}

// Run starts the read loop. Blocks until context is cancelled.
func (s *UDPServer) Run(ctx context.Context) {
	go func() {
		<-ctx.Done()
		s.conn.Close()
	}()

	buf := make([]byte, maxUDPPacket)
	for {
		// ReadFromUDPAddrPort yields the peer address as a comparable value.
		// It is stored and compared raw; see UDPTransport.addr for why it is
		// never converted to *net.UDPAddr and back.
		n, ap, err := s.conn.ReadFromUDPAddrPort(buf)
		if err != nil {
			select {
			case <-ctx.Done():
				return
			default:
				log.Printf("udp: read error: %v", err)
				continue
			}
		}
		if n == 0 {
			continue
		}

		// Copy packet data so buf can be reused
		data := make([]byte, n)
		copy(data, buf[:n])

		s.dispatch(data, ap)
	}
}

func (s *UDPServer) dispatch(data []byte, ap netip.AddrPort) {
	pktType := data[0]

	switch pktType {
	case udpproto.TypeConnReq:
		s.handleConnReq(data, ap)

	case udpproto.TypeConnConfirm:
		s.handleConnConfirm(data, ap)

	// Data packets no longer promote anything. A session exists only after a
	// ConnConfirm proved return routability AND named a key, so an unknown
	// token is simply dropped rather than being a promotion opportunity.
	case udpproto.TypeUnreliable:
		token, counter, aad, sealed, err := udpproto.DecodeUnreliable(data)
		if err != nil {
			return
		}
		if t := s.routeFor(token, ap); t != nil {
			t.handleUnreliable(counter, aad, sealed)
		}

	case udpproto.TypeReliable:
		token, seq, counter, aad, sealed, err := udpproto.DecodeReliable(data)
		if err != nil {
			return
		}
		if t := s.routeFor(token, ap); t != nil {
			t.handleReliable(seq, counter, aad, sealed)
		}

	case udpproto.TypeACK:
		token, counter, aad, sealed, err := udpproto.DecodeACK(data)
		if err != nil {
			return
		}
		if t := s.routeFor(token, ap); t != nil {
			t.handleACK(counter, aad, sealed)
		}

	case udpproto.TypeDisconnect:
		token, counter, aad, sealed, err := udpproto.DecodeDisconnect(data)
		if err != nil {
			return
		}
		s.handleDisconnectPacket(token, counter, aad, sealed, ap)
	}
}

// routeFor resolves a token to its transport and requires the packet to have
// arrived from the address that session is bound to.
//
// This is the single identity chokepoint for established sessions. Without it
// a token is a bearer credential: anyone who observes or guesses one can
// inject input into, or tear down, another player's session from any source
// address. Returning nil on mismatch means the caller neither replies (a reply
// would confirm a guessed token and make the server a reflector) nor touches
// the session's liveness state (a mismatched packet must not keep a dead
// session alive, and must not be able to kill a live one).
//
// The compared field is immutable after construction, so no per-packet lock on
// the transport is needed. Address rebinding for roaming clients is
// deliberately not supported here: it requires a cryptographic proof of
// ownership rather than possession of a 32-bit token, and belongs with the
// authenticated handshake work.
func (s *UDPServer) routeFor(token uint32, ap netip.AddrPort) *UDPTransport {
	s.mu.RLock()
	t := s.byToken[token]
	s.mu.RUnlock()
	if t == nil {
		return nil
	}
	if t.addr != ap {
		s.noteDrop(&s.sourceMismatchDrops, "source address mismatch", token, ap, t.addr)
		return nil
	}
	return t
}

// promoteConfirmed turns a verified ConnConfirm into a live session. It is
// reached only after the stateless cookie proved the peer received our accept
// at this address AND the named key resolved, so this is the first point at
// which a transport plus its goroutine is allocated.
//
// dispatch runs on the single reader goroutine, so no double-promotion race
// exists; the lock guards concurrent removeTransport and readers. A repeated
// ConnConfirm — the client retries if its first is lost — returns the existing
// session rather than building a second one.
func (s *UDPServer) promoteConfirmed(token uint32, ap netip.AddrPort, clientSalt, serverSalt uint64, sess *udpcrypto.Session) *UDPTransport {
	s.mu.Lock()
	if existing := s.byToken[token]; existing != nil {
		s.mu.Unlock()
		return existing
	}
	if len(s.byToken) >= s.maxConnections {
		s.mu.Unlock()
		s.noteDrop(&s.capacityDrops, "connection capacity reached", token, ap, ap)
		return nil
	}
	limits := s.limits
	s.mu.Unlock()

	// Construct outside s.mu: AddTransport pushes onto ConnManager's bounded
	// event channel and can block on the routing goroutine.
	t := newUDPTransport(s, ap, token, clientSalt, serverSalt, limits, sess)
	connID := s.connMgr.AddTransport(t, ap.String())

	s.mu.Lock()
	s.byToken[token] = t
	s.byAddr[ap] = t
	s.connIDs[t] = connID
	s.mu.Unlock()

	log.Printf("udp: new authenticated connection from %s (token=%08x, connID=%d)", ap, token, connID)
	return t
}

// handleDisconnectPacket tears down a session only when the packet came from
// that session's bound address AND authenticates under its key.
//
// v1 let anyone holding a token kill a session; Tier 1 narrowed that to anyone
// at the bound address. The sealed empty body closes it: only a peer holding
// the session key can produce a valid tag, so teardown is a cryptographic
// proof rather than an addressing coincidence.
func (s *UDPServer) handleDisconnectPacket(token uint32, counter uint64, aad, sealed []byte, ap netip.AddrPort) {
	t := s.routeFor(token, ap)
	if t == nil {
		return
	}
	if _, err := t.crypto.Open(nil, counter, sealed, aad); err != nil {
		t.noteInboundDrop(&t.authFailures, "disconnect failed authentication")
		return
	}
	s.removeTransport(t)
}

// noteDrop counts a dropped packet and logs at most one line per interval.
// Unthrottled logging would itself be an amplification vector: a spoofing
// source at packet-flood rates could otherwise drive log I/O without limit.
func (s *UDPServer) noteDrop(counter *atomic.Uint64, reason string, token uint32, got, want netip.AddrPort) {
	counter.Add(1)
	now := udpClockStamp(time.Now())
	previous := s.lastDropLogStamp.Load()
	if now-previous < int64(dropLogInterval) {
		return
	}
	if !s.lastDropLogStamp.CompareAndSwap(previous, now) {
		return
	}
	log.Printf("udp: dropped packet (%s) token=%08x from=%s bound=%s [mismatch=%d capacity=%d handshakeReject=%d]",
		reason, token, got, want,
		s.sourceMismatchDrops.Load(), s.capacityDrops.Load(), s.handshakeRejectDrops.Load())
}

// SetMaxConnections overrides the session cap. Non-positive leaves it
// unchanged. Safe to call while the server is running.
//
// The pending-handshake cap and TTL that used to sit alongside it are gone with
// the table they bounded.
func (s *UDPServer) SetMaxConnections(maxConnections int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if maxConnections > 0 {
		s.maxConnections = maxConnections
	}
}

// SetWireLimits installs the ingress limits applied to sessions promoted from
// this point on. Non-positive fields fall back to their defaults. Sessions
// already established keep the limits they were built with.
func (s *UDPServer) SetWireLimits(l WireLimits) {
	s.mu.Lock()
	s.limits = l.Normalized()
	s.mu.Unlock()
}

// SourceMismatchDrops returns the number of packets dropped because their
// source address did not match the bound address of the token's session.
func (s *UDPServer) SourceMismatchDrops() uint64 { return s.sourceMismatchDrops.Load() }

// CapacityDrops returns the number of handshakes refused at the connection cap.
func (s *UDPServer) CapacityDrops() uint64 { return s.capacityDrops.Load() }

// the unproven-handshake table was full.
func (s *UDPServer) HandshakeRejectDrops() uint64 { return s.handshakeRejectDrops.Load() }

// SetKeyRegistry installs the registry that resolves the key IDs clients
// present in ConnConfirm. Until it is set the listener refuses every handshake.
func (s *UDPServer) SetKeyRegistry(r *UDPKeyRegistry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.keys = r
}

func (s *UDPServer) handleConnReq(data []byte, ap netip.AddrPort) {
	clientSalt, err := udpproto.DecodeConnReq(data)
	if err != nil {
		return
	}

	// Established peer re-requesting: resend the original accept. Minting new
	// salts would change the token the peer already derived.
	s.mu.RLock()
	t := s.byAddr[ap]
	s.mu.RUnlock()
	if t != nil {
		cookie := s.cookies.Mint(ap, t.clientSalt, t.serverSalt, time.Now())
		s.conn.WriteToUDPAddrPort(
			udpproto.EncodeConnAccept(t.clientSalt, t.serverSalt, cookie[:]), ap)
		return
	}

	var saltBuf [8]byte
	if _, err := rand.Read(saltBuf[:]); err != nil {
		log.Printf("udp: failed to generate salt: %v", err)
		return
	}
	serverSalt := binary.LittleEndian.Uint64(saltBuf[:])

	// No table write and nothing retained: an unauthenticated ConnReq costs one
	// HMAC and one datagram. A retry mints a fresh salt and cookie, and the
	// client uses whichever accept reaches it.
	cookie := s.cookies.Mint(ap, clientSalt, serverSalt, time.Now())
	s.conn.WriteToUDPAddrPort(udpproto.EncodeConnAccept(clientSalt, serverSalt, cookie[:]), ap)
}

// handleConnConfirm is the one place a session can come into existence.
//
// Three things must hold, in this order: the cookie must verify against THIS
// packet's source address (return routability, and what carries Tier 1's
// address binding into a stateless design), the named key must resolve, and the
// AEAD session must build. Every failure is silent — a reply would confirm a
// guessed key ID or make the server a reflector.
func (s *UDPServer) handleConnConfirm(data []byte, ap netip.AddrPort) {
	keyID, clientSalt, serverSalt, cookie, err := udpproto.DecodeConnConfirm(data)
	if err != nil {
		return
	}
	if !s.cookies.Verify(cookie, ap, clientSalt, serverSalt, time.Now()) {
		s.noteDrop(&s.handshakeRejectDrops, "handshake cookie invalid", 0, ap, ap)
		return
	}

	s.mu.RLock()
	keys := s.keys
	s.mu.RUnlock()
	if keys == nil {
		// No registry means this listener cannot authenticate anyone. Refusing
		// is the secure default: the alternative is an anonymous session, which
		// is exactly the forgeable transport Tier 2 exists to remove.
		s.noteDrop(&s.handshakeRejectDrops, "no udp key registry configured", 0, ap, ap)
		return
	}
	entry, err := keys.Lookup(UDPKeyID(keyID), time.Now())
	if err != nil {
		s.noteDrop(&s.handshakeRejectDrops, "udp key unknown or expired", 0, ap, ap)
		return
	}

	sess, err := udpcrypto.NewSession(entry.Key, udpcrypto.RoleServer,
		udpproto.SessionSalt(clientSalt, serverSalt))
	if err != nil {
		s.noteDrop(&s.handshakeRejectDrops, "udp session key derivation failed", 0, ap, ap)
		return
	}

	s.promoteConfirmed(udpproto.MakeToken(clientSalt, serverSalt), ap, clientSalt, serverSalt, sess)
}

func (s *UDPServer) removeTransport(t *UDPTransport) {
	s.mu.Lock()
	connID, ok := s.connIDs[t]
	delete(s.byToken, t.token)
	delete(s.byAddr, t.addr)
	delete(s.connIDs, t)
	s.mu.Unlock()

	t.Close()

	if ok {
		s.connMgr.Unregister(connID)
	}
}
