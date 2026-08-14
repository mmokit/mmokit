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

	"github.com/mmokit/mmokit/pkg/net/udpproto"
)

const (
	maxUDPPacket = 1400 // safe MTU

	defaultMaxUDPConnections    = 1024
	defaultMaxPendingHandshakes = 1024
	defaultPendingTTL           = 15 * time.Second
	dropLogInterval             = time.Second
)

// pendingHandshake is a connection request that has been accepted but whose
// peer has not yet proven return routability by sending a data packet from the
// same source address. It deliberately holds no transport and no goroutine:
// a connection request is unauthenticated and trivially spoofable, so nothing
// expensive may be allocated until the peer sends a packet carrying the token
// that only a receiver of our accept could have derived.
type pendingHandshake struct {
	clientSalt uint64
	serverSalt uint64
	token      uint32
	createdAt  time.Time
}

// UDPServer manages a single UDP socket and dispatches packets to UDPTransports.
type UDPServer struct {
	conn    *net.UDPConn
	connMgr *ConnManager
	mu      sync.RWMutex
	byToken map[uint32]*UDPTransport
	byAddr  map[netip.AddrPort]*UDPTransport // peer address → transport
	connIDs map[*UDPTransport]uint32         // transport → connMgr connID
	pending map[netip.AddrPort]pendingHandshake

	// Limits are guarded by mu because the reader goroutine consults them on
	// every connection request and promotion. Change them with SetLimits.
	maxConnections int
	maxPending     int
	pendingTTL     time.Duration
	limits         WireLimits

	sourceMismatchDrops atomic.Uint64
	capacityDrops       atomic.Uint64
	pendingFullDrops    atomic.Uint64
	lastDropLogStamp    atomic.Int64
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
		pending:        make(map[netip.AddrPort]pendingHandshake),
		maxConnections: defaultMaxUDPConnections,
		maxPending:     defaultMaxPendingHandshakes,
		pendingTTL:     defaultPendingTTL,
		limits:         DefaultWireLimits(),
	}
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

	case udpproto.TypeUnreliable:
		token, payload, err := udpproto.DecodeUnreliable(data)
		if err != nil {
			return
		}
		if t := s.routeOrPromote(token, ap); t != nil {
			t.handleUnreliable(payload)
		}

	case udpproto.TypeReliable:
		token, seq, payload, err := udpproto.DecodeReliable(data)
		if err != nil {
			return
		}
		if t := s.routeOrPromote(token, ap); t != nil {
			t.handleReliable(seq, payload)
		}

	case udpproto.TypeACK:
		token, ackSeq, ackBits, err := udpproto.DecodeACK(data)
		if err != nil {
			return
		}
		if t := s.routeOrPromote(token, ap); t != nil {
			t.handleACK(ackSeq, ackBits)
		}

	case udpproto.TypeDisconnect:
		token, err := udpproto.DecodeDisconnect(data)
		if err != nil {
			return
		}
		s.handleDisconnectPacket(token, ap)
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

// routeOrPromote resolves an established session, or promotes a pending
// handshake whose peer has now proven return routability.
func (s *UDPServer) routeOrPromote(token uint32, ap netip.AddrPort) *UDPTransport {
	if t := s.routeFor(token, ap); t != nil {
		return t
	}
	return s.promotePending(token, ap)
}

// promotePending turns an accepted handshake into a real session. It is only
// reached when a data packet arrives carrying a token that matches the pending
// entry for its own source address, which proves the peer received our accept
// at that address. Only then is a transport plus its goroutine allocated.
//
// dispatch runs on the single reader goroutine, so no double-promotion race
// exists; the lock protects against concurrent removeTransport and readers.
func (s *UDPServer) promotePending(token uint32, ap netip.AddrPort) *UDPTransport {
	s.mu.Lock()
	p, ok := s.pending[ap]
	if !ok || p.token != token {
		// A token that does not match the entry for its own source address
		// proves nothing, whether or not it matches some other session.
		s.mu.Unlock()
		return nil
	}
	if len(s.byToken) >= s.maxConnections {
		delete(s.pending, ap)
		s.mu.Unlock()
		s.noteDrop(&s.capacityDrops, "connection capacity reached", token, ap, ap)
		return nil
	}
	delete(s.pending, ap)
	limits := s.limits
	s.mu.Unlock()

	// Construct outside s.mu: AddTransport pushes onto ConnManager's bounded
	// event channel and can block on the routing goroutine.
	//
	// The peer address is recorded with the transport. Without it every
	// UDP-originated login in the cluster shares one IPRateLimiter bucket
	// (ops.ParseClientIP yields the zero netip.Addr for an empty string) and
	// every UDP audit row records a null IP.
	t := newUDPTransport(s, ap, p.token, p.clientSalt, p.serverSalt, limits)
	connID := s.connMgr.AddTransport(t, ap.String())

	s.mu.Lock()
	s.byToken[p.token] = t
	s.byAddr[ap] = t
	s.connIDs[t] = connID
	s.mu.Unlock()

	log.Printf("udp: new connection from %s (token=%08x, connID=%d)", ap, p.token, connID)
	return t
}

// handleDisconnectPacket tears down a session only when the packet came from
// that session's own bound address. A pending handshake may also be cancelled
// by its own peer.
func (s *UDPServer) handleDisconnectPacket(token uint32, ap netip.AddrPort) {
	s.mu.Lock()
	if p, ok := s.pending[ap]; ok && p.token == token {
		delete(s.pending, ap)
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()

	if t := s.routeFor(token, ap); t != nil {
		s.removeTransport(t)
	}
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
	log.Printf("udp: dropped packet (%s) token=%08x from=%s bound=%s [mismatch=%d capacity=%d pendingFull=%d]",
		reason, token, got, want,
		s.sourceMismatchDrops.Load(), s.capacityDrops.Load(), s.pendingFullDrops.Load())
}

// SetLimits overrides the connection, pending-handshake, and handshake-expiry
// limits. A non-positive value leaves that limit unchanged. Safe to call while
// the server is running.
func (s *UDPServer) SetLimits(maxConnections, maxPending int, pendingTTL time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if maxConnections > 0 {
		s.maxConnections = maxConnections
	}
	if maxPending > 0 {
		s.maxPending = maxPending
	}
	if pendingTTL > 0 {
		s.pendingTTL = pendingTTL
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

// PendingFullDrops returns the number of connection requests refused because
// the unproven-handshake table was full.
func (s *UDPServer) PendingFullDrops() uint64 { return s.pendingFullDrops.Load() }

// PendingCount returns the number of accepted but unproven handshakes.
func (s *UDPServer) PendingCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.pending)
}

func (s *UDPServer) handleConnReq(data []byte, ap netip.AddrPort) {
	clientSalt, err := udpproto.DecodeConnReq(data)
	if err != nil {
		return
	}

	now := time.Now()

	s.mu.Lock()
	// Established peer re-requesting: resend the original accept. Minting new
	// salts would change the token the peer already derived.
	if t := s.byAddr[ap]; t != nil {
		clientSaltOut, serverSaltOut := t.clientSalt, t.serverSalt
		s.mu.Unlock()
		s.conn.WriteToUDPAddrPort(udpproto.EncodeConnAccept(clientSaltOut, serverSaltOut), ap)
		return
	}
	// Pending peer retrying: resend the same accept, same reasoning.
	if p, ok := s.pending[ap]; ok {
		s.mu.Unlock()
		s.conn.WriteToUDPAddrPort(udpproto.EncodeConnAccept(p.clientSalt, p.serverSalt), ap)
		return
	}
	if len(s.pending) >= s.maxPending {
		s.sweepPendingLocked(now)
		if len(s.pending) >= s.maxPending {
			s.mu.Unlock()
			s.noteDrop(&s.pendingFullDrops, "pending handshake table full", 0, ap, ap)
			return
		}
	}
	s.mu.Unlock()

	var saltBuf [8]byte
	if _, err := rand.Read(saltBuf[:]); err != nil {
		log.Printf("udp: failed to generate salt: %v", err)
		return
	}
	serverSalt := binary.LittleEndian.Uint64(saltBuf[:])
	token := udpproto.MakeToken(clientSalt, serverSalt)

	s.mu.Lock()
	// Re-check under the reacquired lock: another request for this address may
	// have raced in while the salt was generated.
	if _, ok := s.pending[ap]; ok {
		p := s.pending[ap]
		s.mu.Unlock()
		s.conn.WriteToUDPAddrPort(udpproto.EncodeConnAccept(p.clientSalt, p.serverSalt), ap)
		return
	}
	s.pending[ap] = pendingHandshake{
		clientSalt: clientSalt,
		serverSalt: serverSalt,
		token:      token,
		createdAt:  now,
	}
	s.mu.Unlock()

	s.conn.WriteToUDPAddrPort(udpproto.EncodeConnAccept(clientSalt, serverSalt), ap)
}

// sweepPendingLocked expires unproven handshakes. Caller holds s.mu. It runs
// only when the table is full, so its O(MaxPending) cost is bounded to
// pressure situations rather than the steady-state path.
func (s *UDPServer) sweepPendingLocked(now time.Time) {
	for addr, p := range s.pending {
		if now.Sub(p.createdAt) >= s.pendingTTL {
			delete(s.pending, addr)
		}
	}
}

func (s *UDPServer) removeTransport(t *UDPTransport) {
	s.mu.Lock()
	connID, ok := s.connIDs[t]
	delete(s.byToken, t.token)
	delete(s.byAddr, t.addr)
	// Defensive: a stale pending entry must not be able to resurrect an
	// address whose session was just torn down.
	delete(s.pending, t.addr)
	delete(s.connIDs, t)
	s.mu.Unlock()

	t.Close()

	if ok {
		s.connMgr.Unregister(connID)
	}
}
