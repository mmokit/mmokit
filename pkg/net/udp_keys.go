package net

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mmokit/mmokit/pkg/net/udpcrypto"
)

// CE-005b Tier 2 unit 2: the registry of UDP session keys.
//
// A client authenticates over HTTPS, receives a key, and then proves
// possession of that key on the UDP handshake. That inverts today's order,
// where a UDP session exists first and authenticates afterwards over the op
// channel — which is what makes the current transport forgeable by anyone
// on path.
//
// Issuance and consumption are deliberately both gateway-local. The gateway
// already validates session cookies through Config.AuthResolver at WS-upgrade
// time; reusing that same seam for POST /auth/udp-key means the key never has
// to be replicated between processes, so distributed mode needs no key
// distribution channel and no shared secret beyond what already exists.

// UDPKeyID identifies an issued key on the wire. It is NOT secret — the
// handshake carries it in the clear so the server can find the matching key —
// so it is random rather than sequential only to avoid leaking issuance rate
// and to make scanning useless.
type UDPKeyID uint64

// UDPKeyEntry is one issued key and the identity it vouches for.
type UDPKeyEntry struct {
	Key       udpcrypto.Key
	UserID    string // uuid string; pkg/net stays free of the uuid dependency
	Username  string
	ExpiresAt time.Time
}

// Errors from the registry.
var (
	ErrUDPKeyRegistryFull = errors.New("net: udp key registry full")
	ErrUDPKeyUnknown      = errors.New("net: udp key unknown or expired")
)

// Default registry bounds. The table is attacker-influenced only behind a
// valid session cookie, so the cap is a backstop against a compromised or
// scripted account rather than against anonymous traffic.
const (
	DefaultUDPKeyTTL     = 5 * time.Minute
	DefaultMaxUDPKeys    = 4096
	udpKeySweepThreshold = 64 // sweep when the table has grown by this much
)

// UDPKeyRegistry holds keys issued over HTTPS and awaiting (or serving) a UDP
// handshake.
//
// Keys are multi-use until they expire, not single-use. Single-use would make
// a lost handshake packet cost a full HTTPS round trip, and would defeat the
// address-rebinding case Tier 2 exists to support: a roaming client that
// changes address must be able to re-handshake under the same identity without
// re-authenticating.
type UDPKeyRegistry struct {
	mu         sync.Mutex
	entries    map[UDPKeyID]UDPKeyEntry
	max        int
	ttl        time.Duration
	sinceSweep int

	issued   atomic.Uint64
	claimed  atomic.Uint64
	rejected atomic.Uint64
}

// NewUDPKeyRegistry returns a registry with the default bounds. Pass zero for
// either argument to take the default.
func NewUDPKeyRegistry(max int, ttl time.Duration) *UDPKeyRegistry {
	if max <= 0 {
		max = DefaultMaxUDPKeys
	}
	if ttl <= 0 {
		ttl = DefaultUDPKeyTTL
	}
	return &UDPKeyRegistry{
		entries: make(map[UDPKeyID]UDPKeyEntry),
		max:     max,
		ttl:     ttl,
	}
}

// Issue mints a fresh key for an authenticated user and records it.
//
// The caller must already have validated the session — this function trusts
// userID and username completely and is the reason the HTTP handler in front
// of it may not be mounted without an AuthResolver.
func (r *UDPKeyRegistry) Issue(userID, username string, now time.Time) (UDPKeyID, UDPKeyEntry, error) {
	var key udpcrypto.Key
	if _, err := rand.Read(key[:]); err != nil {
		return 0, UDPKeyEntry{}, err
	}
	var idBuf [8]byte
	if _, err := rand.Read(idBuf[:]); err != nil {
		return 0, UDPKeyEntry{}, err
	}
	id := UDPKeyID(binary.BigEndian.Uint64(idBuf[:]))
	if id == 0 {
		id = 1 // 0 is reserved as "absent" on the wire
	}

	entry := UDPKeyEntry{
		Key:       key,
		UserID:    userID,
		Username:  username,
		ExpiresAt: now.Add(r.ttl),
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.sinceSweep++
	if r.sinceSweep >= udpKeySweepThreshold || len(r.entries) >= r.max {
		r.sweepLocked(now)
		r.sinceSweep = 0
	}
	if len(r.entries) >= r.max {
		r.rejected.Add(1)
		return 0, UDPKeyEntry{}, ErrUDPKeyRegistryFull
	}
	r.entries[id] = entry
	r.issued.Add(1)
	return id, entry, nil
}

// Lookup returns the entry for id if it exists and has not expired. The entry
// stays in the table: see the type comment for why keys are multi-use.
func (r *UDPKeyRegistry) Lookup(id UDPKeyID, now time.Time) (UDPKeyEntry, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.entries[id]
	if !ok {
		return UDPKeyEntry{}, ErrUDPKeyUnknown
	}
	if !now.Before(e.ExpiresAt) {
		delete(r.entries, id)
		return UDPKeyEntry{}, ErrUDPKeyUnknown
	}
	r.claimed.Add(1)
	return e, nil
}

// Revoke drops a key, e.g. on logout or when its session is torn down.
func (r *UDPKeyRegistry) Revoke(id UDPKeyID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.entries, id)
}

// Len reports the live entry count. Test and metrics use.
func (r *UDPKeyRegistry) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.entries)
}

// Stats returns aggregate counters. Bounded cardinality by construction —
// three totals, no per-user or per-key breakdown — matching the rule in
// pkg/metrics/cell_metrics.go.
func (r *UDPKeyRegistry) Stats() (issued, claimed, rejected uint64) {
	return r.issued.Load(), r.claimed.Load(), r.rejected.Load()
}

// sweepLocked drops expired entries. Caller holds r.mu.
//
// Unlike the Tier 1 pending-handshake table, this sweep runs on a growth
// counter as well as on pressure, so a table that fills slowly still gets
// cleaned rather than only being cleaned at the cap.
func (r *UDPKeyRegistry) sweepLocked(now time.Time) {
	for id, e := range r.entries {
		if !now.Before(e.ExpiresAt) {
			delete(r.entries, id)
		}
	}
}
