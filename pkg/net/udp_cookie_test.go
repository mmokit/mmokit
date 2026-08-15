package net

import (
	"net/netip"
	"testing"
	"time"
)

func cookieSigner(t *testing.T) *HandshakeCookieSigner {
	t.Helper()
	s, err := NewHandshakeCookieSigner()
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	return s
}

func ap(s string) netip.AddrPort { return netip.MustParseAddrPort(s) }

func TestHandshakeCookieRoundTrip(t *testing.T) {
	s := cookieSigner(t)
	now := time.Unix(1_700_000_000, 0)
	peer := ap("203.0.113.7:44444")

	c := s.Mint(peer, 0x1122334455667788, 0x99aabbccddeeff00, now)
	if !s.Verify(c[:], peer, 0x1122334455667788, 0x99aabbccddeeff00, now) {
		t.Fatal("freshly minted cookie failed to verify")
	}
}

// The property the whole construction rests on: a cookie minted for one source
// address must be worthless from another. Otherwise an attacker could request a
// cookie against a victim's address, or reuse a captured one from elsewhere.
func TestHandshakeCookieIsAddressBound(t *testing.T) {
	s := cookieSigner(t)
	now := time.Unix(1_700_000_000, 0)
	minted := ap("203.0.113.7:44444")
	c := s.Mint(minted, 1, 2, now)

	for _, other := range []string{
		"203.0.113.8:44444",   // different address, same port
		"203.0.113.7:44445",   // same address, different port
		"[2001:db8::1]:44444", // different family
	} {
		if s.Verify(c[:], ap(other), 1, 2, now) {
			t.Fatalf("cookie minted for %s verified from %s", minted, other)
		}
	}
}

func TestHandshakeCookieIsSaltBound(t *testing.T) {
	s := cookieSigner(t)
	now := time.Unix(1_700_000_000, 0)
	peer := ap("203.0.113.7:44444")
	c := s.Mint(peer, 1, 2, now)

	if s.Verify(c[:], peer, 999, 2, now) {
		t.Fatal("cookie verified with a tampered client salt")
	}
	if s.Verify(c[:], peer, 1, 999, now) {
		t.Fatal("cookie verified with a tampered server salt — the client could " +
			"substitute a serverSalt and change the derived token")
	}
}

// A cookie must remain usable across a slot boundary, or a handshake that
// straddles one fails for no reason the client can see.
func TestHandshakeCookieAcceptsPreviousSlot(t *testing.T) {
	s := cookieSigner(t)
	now := time.Unix(1_700_000_000, 0)
	peer := ap("203.0.113.7:44444")
	c := s.Mint(peer, 1, 2, now)

	later := now.Add(defaultCookieSlot)
	if !s.Verify(c[:], peer, 1, 2, later) {
		t.Fatal("cookie rejected one slot later; a handshake straddling a slot " +
			"boundary would fail")
	}
}

func TestHandshakeCookieExpires(t *testing.T) {
	s := cookieSigner(t)
	now := time.Unix(1_700_000_000, 0)
	peer := ap("203.0.113.7:44444")
	c := s.Mint(peer, 1, 2, now)

	tooLate := now.Add(3 * defaultCookieSlot)
	if s.Verify(c[:], peer, 1, 2, tooLate) {
		t.Fatal("cookie still valid three slots later — the replay horizon is unbounded")
	}
}

func TestHandshakeCookieRejectsWrongLength(t *testing.T) {
	s := cookieSigner(t)
	now := time.Unix(1_700_000_000, 0)
	peer := ap("203.0.113.7:44444")
	c := s.Mint(peer, 1, 2, now)

	if s.Verify(c[:HandshakeCookieSize-1], peer, 1, 2, now) {
		t.Fatal("truncated cookie verified")
	}
	if s.Verify(append(c[:], 0x00), peer, 1, 2, now) {
		t.Fatal("over-long cookie verified")
	}
	if s.Verify(nil, peer, 1, 2, now) {
		t.Fatal("nil cookie verified")
	}
	if s.Verify([]byte{}, peer, 1, 2, now) {
		t.Fatal("empty cookie verified")
	}
}

func TestHandshakeCookieRejectsBitFlips(t *testing.T) {
	s := cookieSigner(t)
	now := time.Unix(1_700_000_000, 0)
	peer := ap("203.0.113.7:44444")
	c := s.Mint(peer, 1, 2, now)

	for i := range c {
		bad := c
		bad[i] ^= 0x01
		if s.Verify(bad[:], peer, 1, 2, now) {
			t.Fatalf("cookie with byte %d flipped verified", i)
		}
	}
}

// Two processes must not accept each other's cookies: the secret is what makes
// a cookie unforgeable, so an independently-seeded signer must reject.
func TestHandshakeCookieSecretsAreIndependent(t *testing.T) {
	a, b := cookieSigner(t), cookieSigner(t)
	now := time.Unix(1_700_000_000, 0)
	peer := ap("203.0.113.7:44444")
	c := a.Mint(peer, 1, 2, now)
	if b.Verify(c[:], peer, 1, 2, now) {
		t.Fatal("a cookie minted by one signer verified under another's secret")
	}
}

// An IPv4 peer and its IPv4-mapped IPv6 spelling are the same peer and must
// share one cookie, or a dual-stack listener rejects its own valid handshakes.
func TestHandshakeCookieNormalisesMappedIPv4(t *testing.T) {
	s := cookieSigner(t)
	now := time.Unix(1_700_000_000, 0)
	v4 := ap("203.0.113.7:44444")
	mapped := netip.AddrPortFrom(netip.AddrFrom16(v4.Addr().As16()), v4.Port())

	c := s.Mint(v4, 1, 2, now)
	if !s.Verify(c[:], mapped, 1, 2, now) {
		t.Fatal("IPv4-mapped IPv6 form rejected a cookie minted for the IPv4 form")
	}
}

// The table this replaces was the DoS surface. Nothing here may allocate
// per-peer state, so a flood of distinct source addresses must cost nothing
// that persists.
func TestHandshakeCookieIsStateless(t *testing.T) {
	s := cookieSigner(t)
	now := time.Unix(1_700_000_000, 0)
	for i := range 10_000 {
		peer := netip.AddrPortFrom(
			netip.AddrFrom4([4]byte{10, byte(i >> 16), byte(i >> 8), byte(i)}),
			uint16(1024+i%60000),
		)
		c := s.Mint(peer, uint64(i), uint64(i*7), now)
		if !s.Verify(c[:], peer, uint64(i), uint64(i*7), now) {
			t.Fatalf("cookie %d failed to verify", i)
		}
	}
	// The signer is two fixed-size fields; there is no table to assert on, and
	// that absence is the point. This test exists so a future change that adds
	// one has to delete this comment deliberately.
}

func BenchmarkHandshakeCookieMint(b *testing.B) {
	s, err := NewHandshakeCookieSigner()
	if err != nil {
		b.Fatal(err)
	}
	peer := ap("203.0.113.7:44444")
	now := time.Unix(1_700_000_000, 0)
	b.ReportAllocs()
	for b.Loop() {
		_ = s.Mint(peer, 1, 2, now)
	}
}
