package net

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"hash"
	"net/netip"
	"sync"
	"time"
)

// CE-005b Tier 2 unit 3: the stateless handshake cookie.
//
// # What it replaces
//
// Tier 1 proves return routability by remembering an unproven peer: ConnReq
// records a pendingHandshake keyed by source address, and a later data packet
// carrying the matching token promotes it. That table is state held on behalf
// of peers who have proven nothing, and it is the remaining Tier 1 residual —
// sweepPendingLocked runs only when the table is already full, so 1024 spoofed
// source addresses deny new connections for up to pendingTTL.
//
// A SYN-cookie construction removes the table rather than tuning it. The server
// mints a MAC over (client address, both salts, coarse time slot) and sends it
// in ConnAccept; the client echoes it; the server recomputes the MAC from the
// echoing packet's own source address and compares. Nothing is stored between
// the two packets, so there is nothing to exhaust.
//
// # Why the tuple is echoed rather than remembered
//
// A stateless server cannot recall the serverSalt it chose. Two options exist:
// derive it from the secret, or let the client echo it and bind it into the
// MAC. This takes the second, because deriving would tie the salt to the time
// slot and therefore change the connection token across a slot boundary. The
// MAC covers the salt, so a client that tampers with it invalidates its own
// cookie.
//
// # Scope
//
// This file is the primitive only. Landing it on the wire changes ConnAccept
// and is deliberately deferred to the framing unit, so the AEAD change, this
// change and CE-009's version byte are one wire break and one golden
// regeneration rather than three.

const (
	// HandshakeCookieSize is the truncated HMAC-SHA256 length placed on the
	// wire. 16 bytes puts forgery at 2^128 work, and ConnAccept is a
	// once-per-connection packet where the bytes do not matter.
	HandshakeCookieSize = 16

	// defaultCookieSlot is the coarse time quantum a cookie is minted against.
	// Verify accepts the current and the previous slot, so a cookie is usable
	// for between one and two slots — 15 to 30 seconds — which comfortably
	// covers a round trip while keeping the replay horizon short.
	defaultCookieSlot = 15 * time.Second
)

// HandshakeCookieSigner mints and verifies stateless handshake cookies.
//
// The secret is random per process and never leaves it. Rotation is deliberately
// absent: a rotation would invalidate every in-flight handshake, and the window
// a cookie is valid for is already only tens of seconds.
type HandshakeCookieSigner struct {
	secret [32]byte
	slot   time.Duration

	// macs pools keyed HMAC states. Minting happens once per ConnReq, on an
	// unauthenticated path, so it is reachable by anyone who can send a
	// datagram: hmac.New per call cost 7 allocations, which turns a spoofed
	// ConnReq flood into a garbage-generation vector against the very DoS
	// resistance this cookie exists to provide. Reset() restores the keyed
	// state, so a pooled hash is exactly as safe as a fresh one.
	macs sync.Pool
}

// NewHandshakeCookieSigner returns a signer with a fresh random secret.
func NewHandshakeCookieSigner() (*HandshakeCookieSigner, error) {
	s := &HandshakeCookieSigner{slot: defaultCookieSlot}
	if _, err := rand.Read(s.secret[:]); err != nil {
		return nil, err
	}
	s.macs.New = func() any { return hmac.New(sha256.New, s.secret[:]) }
	return s, nil
}

// Mint returns the cookie for this handshake tuple at the given time.
func (s *HandshakeCookieSigner) Mint(ap netip.AddrPort, clientSalt, serverSalt uint64, now time.Time) [HandshakeCookieSize]byte {
	return s.compute(ap, clientSalt, serverSalt, s.slotOf(now))
}

// Verify reports whether cookie is a MAC this signer produced for exactly this
// tuple, in either the current or the previous time slot.
//
// The address comes from the packet being verified, never from the packet that
// requested the cookie — that is the whole point. A cookie minted for one
// source address proves nothing when echoed from another, so an attacker
// cannot mint against a victim's address and use it themselves, and Tier 1's
// source-address binding survives into the stateless design.
func (s *HandshakeCookieSigner) Verify(cookie []byte, ap netip.AddrPort, clientSalt, serverSalt uint64, now time.Time) bool {
	if len(cookie) != HandshakeCookieSize {
		return false
	}
	slot := s.slotOf(now)
	// Constant-time on both arms: comparing with hmac.Equal rather than
	// bytes.Equal keeps the timing independent of how many bytes matched.
	cur := s.compute(ap, clientSalt, serverSalt, slot)
	if hmac.Equal(cookie, cur[:]) {
		return true
	}
	prev := s.compute(ap, clientSalt, serverSalt, slot-1)
	return hmac.Equal(cookie, prev[:])
}

func (s *HandshakeCookieSigner) slotOf(now time.Time) int64 {
	return now.UnixNano() / int64(s.slot)
}

// compute builds the MAC over a fixed-width encoding of the tuple. Fixed width
// matters: a variable-length encoding would let two different tuples serialise
// to the same bytes and share a cookie.
func (s *HandshakeCookieSigner) compute(ap netip.AddrPort, clientSalt, serverSalt uint64, slot int64) [HandshakeCookieSize]byte {
	var buf [16 + 2 + 8 + 8 + 8]byte

	// Always the 16-byte form, so an IPv4 peer and its IPv4-mapped IPv6
	// spelling produce one cookie rather than two.
	addr := ap.Addr().As16()
	copy(buf[0:16], addr[:])
	binary.BigEndian.PutUint16(buf[16:18], ap.Port())
	binary.BigEndian.PutUint64(buf[18:26], clientSalt)
	binary.BigEndian.PutUint64(buf[26:34], serverSalt)
	binary.BigEndian.PutUint64(buf[34:42], uint64(slot))

	mac := s.macs.Get().(hash.Hash)
	mac.Reset()
	mac.Write(buf[:])
	// Sum into a stack array rather than nil, so the digest does not escape.
	var sum [sha256.Size]byte
	digest := mac.Sum(sum[:0])

	var out [HandshakeCookieSize]byte
	copy(out[:], digest[:HandshakeCookieSize])
	// Returned only after the digest has been copied out, so no reader can be
	// holding a slice into anything this hash owns.
	s.macs.Put(mac)
	return out
}
