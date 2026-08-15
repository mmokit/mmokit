// Package udpcrypto implements the authenticated-encryption layer for the
// client UDP transport (CE-005b Tier 2).
//
// # Cipher choice
//
// ChaCha20-Poly1305 (RFC 8439), matching the umbrella security spec.
//
// An earlier revision of roadmap §6.9.2 reversed this to AES-256-GCM on the
// grounds that netstandard2.1 — the TFM Unity consumes — ships AesGcm and not
// ChaCha20Poly1305. That reasoning checked the reference assembly rather than
// the runtime: Mono deliberately does not implement AesGcm and stubs it to
// throw PlatformNotSupportedException (mono/mono#19285), and Unity 6.5 still
// ships the Mono class library, which IL2CPP inherits. CoreCLR and its working
// .NET 10 BCL do not arrive until Unity 6.7/6.8.
//
// ChaCha20-Poly1305 is also the better primitive for a managed client
// independent of availability. The C# side must implement whichever cipher we
// pick in pure managed code, and a managed AES uses table lookups and so has
// cache-timing side channels. ChaCha20 is add-rotate-xor and is designed to be
// constant-time in software with no hardware support.
//
// # What this package does and does not do
//
// It owns key derivation, packet sealing and opening, nonce allocation and
// replay rejection. It knows nothing about packet layout: callers place the
// counter and ciphertext on the wire and hand them back. That split keeps the
// wire format in udpproto, where the fuzzers already live.
//
// # Nonce uniqueness is structural
//
// A repeated nonce under any Poly1305-based AEAD does not leak one plaintext —
// it leaks the one-time MAC key, and every packet in both directions becomes
// forgeable.
// So Session is the only thing that can produce a nonce: Seal allocates the
// counter itself from an atomic, and no exported function accepts a
// caller-supplied nonce. Send and receive use keys derived under different
// HKDF labels, so the same counter value in both directions is safe by
// construction rather than by convention.
package udpcrypto

import (
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"golang.org/x/crypto/chacha20poly1305"
)

const (
	// KeySize is the master key length issued by POST /auth/udp-key, and also
	// the derived per-direction key length (AES-256).
	KeySize = 32
	// NonceSize is the RFC 8439 nonce width.
	NonceSize = chacha20poly1305.NonceSize
	// TagSize is the Poly1305 authentication tag width. Callers budget this per
	// packet.
	TagSize = 16
	// CounterSize is the width of the explicit per-packet counter on the wire.
	//
	// Explicit, not implicit: the unreliable channel drops and reorders by
	// design, so a receiver-side counter would desynchronise on the first lost
	// packet and never recover.
	CounterSize = 8

	// ReplayWindowSize is how far out of order a packet may arrive and still be
	// accepted, measured in counter values behind the highest one seen. 64 is
	// the IPsec and DTLS default and is ample for a 20 Hz tick: it tolerates
	// three full ticks of reordering even when a tick emits twenty packets.
	ReplayWindowSize = 64

	// maxCounter leaves headroom below the uint64 ceiling so exhaustion is a
	// clean error at a predictable point rather than a wrap into reuse.
	maxCounter = ^uint64(0) - 1024
)

// HKDF info labels. Changing either string changes the derived keys and is a
// wire break; they are versioned so a future rotation is explicit.
const (
	labelC2S = "mmokit/udp/v1 client-to-server"
	labelS2C = "mmokit/udp/v1 server-to-client"
)

// Errors returned by this package. They are deliberately coarse: a peer must
// not learn from a rejection whether it failed authentication, replay or
// length, since that distinction is an oracle.
var (
	ErrReplay           = errors.New("udpcrypto: replayed or too-old counter")
	ErrAuthentication   = errors.New("udpcrypto: authentication failed")
	ErrCounterExhausted = errors.New("udpcrypto: send counter exhausted, rekey required")
	ErrZeroCounter      = errors.New("udpcrypto: counter 0 is never valid")
)

// Key is the 32-byte master secret shared between one client session and the
// server, issued over HTTPS by POST /auth/udp-key.
type Key [KeySize]byte

// Role selects which derived key a Session sends with. The two directions use
// different keys, so both peers derive both and simply disagree about which is
// which.
type Role uint8

const (
	// RoleClient sends under the client-to-server key.
	RoleClient Role = iota
	// RoleServer sends under the server-to-client key.
	RoleServer
)

// Session holds one connection's directional keys, its send counter and its
// receive replay window.
//
// Seal is safe for concurrent use. Open is not: it is called from the single
// goroutine that reads the socket, and the replay window is guarded by a mutex
// only so that Stats can be read from elsewhere without a race.
type Session struct {
	send cipher.AEAD
	recv cipher.AEAD

	sendCtr atomic.Uint64

	mu  sync.Mutex
	win replayWindow

	accepted atomic.Uint64
	replayed atomic.Uint64
	failed   atomic.Uint64
}

// NewSession derives both directional keys from master and returns a Session
// that sends under the direction implied by role.
//
// salt is bound into the derivation so that two sessions issued the same master
// key — which must not happen, but is cheap to defend against — still produce
// different traffic keys. Pass the connection's salts or any per-session value
// both peers agree on; an empty salt is permitted and matches HKDF's own
// treatment of it.
func NewSession(master Key, role Role, salt []byte) (*Session, error) {
	c2s, err := deriveKey(master, salt, labelC2S)
	if err != nil {
		return nil, err
	}
	s2c, err := deriveKey(master, salt, labelS2C)
	if err != nil {
		return nil, err
	}

	sendKey, recvKey := c2s, s2c
	if role == RoleServer {
		sendKey, recvKey = s2c, c2s
	}
	sendAEAD, err := newAEAD(sendKey)
	if err != nil {
		return nil, err
	}
	recvAEAD, err := newAEAD(recvKey)
	if err != nil {
		return nil, err
	}
	return &Session{send: sendAEAD, recv: recvAEAD}, nil
}

func deriveKey(master Key, salt []byte, label string) ([]byte, error) {
	k, err := hkdf.Key(sha256.New, master[:], salt, label, KeySize)
	if err != nil {
		return nil, fmt.Errorf("udpcrypto: derive %q: %w", label, err)
	}
	return k, nil
}

func newAEAD(key []byte) (cipher.AEAD, error) {
	aead, err := chacha20poly1305.New(key)
	if err != nil {
		return nil, fmt.Errorf("udpcrypto: chacha20poly1305: %w", err)
	}
	return aead, nil
}

// Seal encrypts and authenticates plaintext, returning the counter the caller
// must put on the wire alongside the ciphertext.
//
// aad is authenticated but not encrypted — pass the packet header bytes that
// precede the payload (type, version, token) so an attacker cannot move a valid
// payload onto a different header.
//
// The counter is allocated here and nowhere else. Counters start at 1, so a
// zero counter on the wire is always invalid.
func (s *Session) Seal(dst, plaintext, aad []byte) (uint64, []byte, error) {
	ctr := s.sendCtr.Add(1)
	if ctr > maxCounter {
		return 0, nil, ErrCounterExhausted
	}
	nonce := nonceFor(ctr)
	return ctr, s.send.Seal(dst, nonce[:], plaintext, aad), nil
}

// Open authenticates and decrypts a packet received with the given counter.
//
// Ordering is deliberate and is the classic trap in this code: the window is
// pre-checked to reject obvious duplicates cheaply, then the packet is
// authenticated, and the window is advanced ONLY after authentication
// succeeds. Advancing on an unauthenticated packet would let an off-path
// attacker fast-forward the window with forged counters and cause the receiver
// to discard the peer's real traffic.
func (s *Session) Open(dst []byte, ctr uint64, ciphertext, aad []byte) ([]byte, error) {
	if ctr == 0 {
		s.replayed.Add(1)
		return nil, ErrZeroCounter
	}
	s.mu.Lock()
	ok := s.win.check(ctr)
	s.mu.Unlock()
	if !ok {
		s.replayed.Add(1)
		return nil, ErrReplay
	}

	nonce := nonceFor(ctr)
	out, err := s.recv.Open(dst, nonce[:], ciphertext, aad)
	if err != nil {
		s.failed.Add(1)
		return nil, ErrAuthentication
	}

	s.mu.Lock()
	s.win.commit(ctr)
	s.mu.Unlock()
	s.accepted.Add(1)
	return out, nil
}

// Stats returns aggregate counters for this session. Bounded cardinality by
// construction — three totals, no per-counter or per-peer breakdown — matching
// the rule in pkg/metrics/cell_metrics.go.
func (s *Session) Stats() (accepted, replayed, authFailed uint64) {
	return s.accepted.Load(), s.replayed.Load(), s.failed.Load()
}

// SendCounter reports the last counter handed out. Test and metrics use only.
func (s *Session) SendCounter() uint64 { return s.sendCtr.Load() }

// nonceFor maps a counter to an AEAD nonce. The leading four bytes are zero:
// the counter alone is unique per key, and the two directions use different
// keys, so no direction or session field is needed to separate them.
func nonceFor(ctr uint64) [NonceSize]byte {
	var n [NonceSize]byte
	binary.BigEndian.PutUint64(n[NonceSize-CounterSize:], ctr)
	return n
}

// replayWindow is a sliding bitmap over accepted counters. bit i corresponds to
// counter (highest - i), so bit 0 is always the highest counter seen.
type replayWindow struct {
	highest uint64
	bitmap  uint64
}

// check reports whether ctr may be accepted, without recording anything.
func (w *replayWindow) check(ctr uint64) bool {
	if ctr > w.highest {
		return true
	}
	diff := w.highest - ctr
	if diff >= ReplayWindowSize {
		return false // too old to prove unseen
	}
	return w.bitmap&(1<<diff) == 0
}

// commit records ctr as accepted. Caller must have had check(ctr) return true.
func (w *replayWindow) commit(ctr uint64) {
	if ctr > w.highest {
		shift := ctr - w.highest
		if shift >= ReplayWindowSize {
			w.bitmap = 0
		} else {
			w.bitmap <<= shift
		}
		w.bitmap |= 1
		w.highest = ctr
		return
	}
	if diff := w.highest - ctr; diff < ReplayWindowSize {
		w.bitmap |= 1 << diff
	}
}
