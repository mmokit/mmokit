package udpcrypto

import (
	"bytes"
	"errors"
	"testing"
)

func testKey(b byte) Key {
	var k Key
	for i := range k {
		k[i] = b ^ byte(i)
	}
	return k
}

// pair returns a client and a server session sharing one master key, the way
// POST /auth/udp-key will hand them out.
func pair(t *testing.T) (client, server *Session) {
	t.Helper()
	k := testKey(0x5a)
	salt := []byte("session-salt")
	c, err := NewSession(k, RoleClient, salt)
	if err != nil {
		t.Fatalf("client session: %v", err)
	}
	s, err := NewSession(k, RoleServer, salt)
	if err != nil {
		t.Fatalf("server session: %v", err)
	}
	return c, s
}

func TestRoundTripBothDirections(t *testing.T) {
	c, s := pair(t)
	aad := []byte{0x00, 0x01, 0xde, 0xad, 0xbe, 0xef}

	ctr, ct, err := c.Seal(nil, []byte("client to server"), aad)
	if err != nil {
		t.Fatalf("client seal: %v", err)
	}
	got, err := s.Open(nil, ctr, ct, aad)
	if err != nil {
		t.Fatalf("server open: %v", err)
	}
	if string(got) != "client to server" {
		t.Fatalf("c2s payload = %q", got)
	}

	ctr, ct, err = s.Seal(nil, []byte("server to client"), aad)
	if err != nil {
		t.Fatalf("server seal: %v", err)
	}
	got, err = c.Open(nil, ctr, ct, aad)
	if err != nil {
		t.Fatalf("client open: %v", err)
	}
	if string(got) != "server to client" {
		t.Fatalf("s2c payload = %q", got)
	}
}

// The two directions must not share a key. If they did, an attacker could
// reflect a client's own packet back at it and have it authenticate — and the
// same counter would be used under one key in both directions, which is the
// nonce-reuse condition this package exists to prevent.
func TestDirectionsUseDistinctKeys(t *testing.T) {
	c, _ := pair(t)
	aad := []byte("hdr")
	ctr, ct, err := c.Seal(nil, []byte("payload"), aad)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if _, err := c.Open(nil, ctr, ct, aad); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("client opened its own c2s packet (err=%v) — directions share a key", err)
	}
}

func TestReplayRejected(t *testing.T) {
	c, s := pair(t)
	aad := []byte("hdr")
	ctr, ct, err := c.Seal(nil, []byte("once"), aad)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if _, err := s.Open(nil, ctr, ct, aad); err != nil {
		t.Fatalf("first open: %v", err)
	}
	if _, err := s.Open(nil, ctr, ct, aad); !errors.Is(err, ErrReplay) {
		t.Fatalf("replay accepted, err=%v", err)
	}
}

func TestOutOfOrderWithinWindowAccepted(t *testing.T) {
	c, s := pair(t)
	aad := []byte("hdr")

	type pkt struct {
		ctr uint64
		ct  []byte
	}
	var pkts []pkt
	for i := range 10 {
		ctr, ct, err := c.Seal(nil, []byte{byte(i)}, aad)
		if err != nil {
			t.Fatalf("seal %d: %v", i, err)
		}
		pkts = append(pkts, pkt{ctr, ct})
	}
	// Deliver the newest first, then the rest backwards — every one must land.
	for i := len(pkts) - 1; i >= 0; i-- {
		got, err := s.Open(nil, pkts[i].ctr, pkts[i].ct, aad)
		if err != nil {
			t.Fatalf("reordered open of ctr=%d: %v", pkts[i].ctr, err)
		}
		if len(got) != 1 || got[0] != byte(i) {
			t.Fatalf("ctr=%d payload=%v want %d", pkts[i].ctr, got, i)
		}
	}
}

func TestTooOldRejected(t *testing.T) {
	c, s := pair(t)
	aad := []byte("hdr")

	first, firstCT, err := c.Seal(nil, []byte("old"), aad)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	// Advance well past the window before delivering the first packet.
	for i := range ReplayWindowSize + 8 {
		ctr, ct, err := c.Seal(nil, []byte{byte(i)}, aad)
		if err != nil {
			t.Fatalf("seal: %v", err)
		}
		if _, err := s.Open(nil, ctr, ct, aad); err != nil {
			t.Fatalf("open %d: %v", ctr, err)
		}
	}
	if _, err := s.Open(nil, first, firstCT, aad); !errors.Is(err, ErrReplay) {
		t.Fatalf("packet %d older than the window was accepted, err=%v", first, err)
	}
}

func TestAADMismatchRejected(t *testing.T) {
	c, s := pair(t)
	ctr, ct, err := c.Seal(nil, []byte("payload"), []byte{0x00, 0x11})
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	// Same ciphertext, different header — the packet must not be movable onto
	// another header (e.g. a different token or packet type).
	if _, err := s.Open(nil, ctr, ct, []byte{0x00, 0x22}); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("AAD substitution accepted, err=%v", err)
	}
}

func TestTamperedCiphertextRejected(t *testing.T) {
	c, s := pair(t)
	aad := []byte("hdr")
	ctr, ct, err := c.Seal(nil, []byte("payload"), aad)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	for i := range ct {
		bad := bytes.Clone(ct)
		bad[i] ^= 0x01
		if _, err := s.Open(nil, ctr, bad, aad); !errors.Is(err, ErrAuthentication) {
			t.Fatalf("flipping bit in byte %d was accepted, err=%v", i, err)
		}
	}
}

// The property that makes the check/authenticate/commit ordering load-bearing.
//
// An off-path attacker who can guess counters must not be able to advance the
// receiver's replay window with forged packets. If Open committed before
// authenticating, one forged packet at a high counter would push the window
// past every counter the real peer is about to use, and the session would go
// silent while both ends believed they were healthy.
func TestForgedPacketDoesNotAdvanceWindow(t *testing.T) {
	c, s := pair(t)
	aad := []byte("hdr")

	// Attacker forges a far-future counter with garbage that will not authenticate.
	forged := make([]byte, 64)
	if _, err := s.Open(nil, 100_000, forged, aad); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("forged packet authenticated, err=%v", err)
	}

	// The legitimate peer's very next packet — counter 1 — must still land.
	ctr, ct, err := c.Seal(nil, []byte("legit"), aad)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	got, err := s.Open(nil, ctr, ct, aad)
	if err != nil {
		t.Fatalf("legitimate packet rejected after a forgery (ctr=%d): %v — "+
			"the replay window was advanced before authentication", ctr, err)
	}
	if string(got) != "legit" {
		t.Fatalf("payload = %q", got)
	}
}

func TestCountersStartAtOneAndIncrease(t *testing.T) {
	c, _ := pair(t)
	var prev uint64
	for range 100 {
		ctr, _, err := c.Seal(nil, []byte("x"), nil)
		if err != nil {
			t.Fatalf("seal: %v", err)
		}
		if ctr == 0 {
			t.Fatal("counter 0 was handed out; 0 must never be a valid counter")
		}
		if ctr <= prev {
			t.Fatalf("counter did not increase: %d after %d", ctr, prev)
		}
		prev = ctr
	}
}

func TestZeroCounterRejected(t *testing.T) {
	c, s := pair(t)
	_, ct, err := c.Seal(nil, []byte("payload"), nil)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if _, err := s.Open(nil, 0, ct, nil); !errors.Is(err, ErrZeroCounter) {
		t.Fatalf("counter 0 accepted, err=%v", err)
	}
}

func TestCounterExhaustion(t *testing.T) {
	c, _ := pair(t)
	c.sendCtr.Store(maxCounter)
	if _, _, err := c.Seal(nil, []byte("x"), nil); !errors.Is(err, ErrCounterExhausted) {
		t.Fatalf("exhausted counter did not error, err=%v", err)
	}
}

func TestSaltSeparatesSessions(t *testing.T) {
	k := testKey(0x11)
	a, err := NewSession(k, RoleClient, []byte("salt-a"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewSession(k, RoleServer, []byte("salt-b"))
	if err != nil {
		t.Fatal(err)
	}
	ctr, ct, err := a.Seal(nil, []byte("payload"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Open(nil, ctr, ct, nil); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("sessions with different salts interoperated, err=%v", err)
	}
}

func TestStats(t *testing.T) {
	c, s := pair(t)
	ctr, ct, _ := c.Seal(nil, []byte("x"), nil)
	_, _ = s.Open(nil, ctr, ct, nil)  // accepted
	_, _ = s.Open(nil, ctr, ct, nil)  // replayed
	_, _ = s.Open(nil, 9999, ct, nil) // auth failure
	acc, rep, fail := s.Stats()
	if acc != 1 || rep != 1 || fail != 1 {
		t.Fatalf("stats = (%d,%d,%d) want (1,1,1)", acc, rep, fail)
	}
}

func TestReplayWindowUnit(t *testing.T) {
	var w replayWindow

	if !w.check(1) {
		t.Fatal("fresh window rejected counter 1")
	}
	w.commit(1)
	if w.check(1) {
		t.Fatal("committed counter still checks as unseen")
	}

	w.commit(10)
	if w.check(10) {
		t.Fatal("counter 10 still unseen after commit")
	}
	if !w.check(5) {
		t.Fatal("counter 5 inside the window should be acceptable")
	}
	w.commit(5)
	if w.check(5) {
		t.Fatal("counter 5 still unseen after commit")
	}

	// A jump larger than the window clears history entirely.
	w.commit(10 + ReplayWindowSize*2)
	if w.check(10) {
		t.Fatal("counter 10 should be too old after a large jump")
	}
	if !w.check(10 + ReplayWindowSize*2 - 1) {
		t.Fatal("counter just behind the new highest should be acceptable")
	}
}
