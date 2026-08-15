package udpcrypto

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// FuzzSessionOpen feeds attacker-controlled bytes to the receive path.
//
// Open runs on the packet-receive goroutine, on bytes that arrive from an
// unauthenticated UDP socket before anything has vouched for them, so a panic
// here is reachable by anyone who can send a datagram. Every input must be
// rejected cleanly rather than crashing the listener.
//
// No recover(): a panic is a finding, not noise to be swallowed. This matches
// FuzzUDPProtoDecode's stance in pkg/net/udpproto.
//
// The invariant is exact rather than "nothing authenticates": one seed IS a
// genuine packet, because the interesting mutations are the ones a byte away
// from valid. So a packet authenticates if and only if it is byte-identical to
// what Seal produced, under the same counter and the same AAD. Anything else
// authenticating means the tag, the counter binding or the AAD binding is not
// doing its job.
func FuzzSessionOpen(f *testing.F) {
	aad := []byte{0x00, 0x01, 0xde, 0xad, 0xbe, 0xef}
	sender, _ := fuzzPair(f)
	goodCtr, goodCT, err := sender.Seal(nil, []byte("seed payload"), aad)
	if err != nil {
		f.Fatalf("seed seal: %v", err)
	}

	f.Add(goodCtr, goodCT, aad)                            // genuine
	f.Add(goodCtr+1, goodCT, aad)                          // counter moved
	f.Add(goodCtr, goodCT, []byte{0x00, 0x02})             // header substituted
	f.Add(uint64(0), goodCT, aad)                          // counter 0
	f.Add(goodCtr, goodCT[:len(goodCT)-1], aad)            // truncated tag
	f.Add(goodCtr, append(bytes.Clone(goodCT), 0x00), aad) // extended
	f.Add(goodCtr, []byte{}, []byte{})                     // empty
	f.Add(uint64(1), make([]byte, TagSize), []byte{})      // tag-sized zeroes
	f.Add(^uint64(0), make([]byte, TagSize+1), aad)        // max counter

	f.Fuzz(func(t *testing.T, ctr uint64, ciphertext, aad2 []byte) {
		// A fresh receiver per input: the fuzzer must not be able to make one
		// call's replay-window state decide the next call's outcome.
		_, recv := fuzzPair(t)
		out, err := recv.Open(nil, ctr, ciphertext, aad2)

		genuine := ctr == goodCtr &&
			bytes.Equal(ciphertext, goodCT) &&
			bytes.Equal(aad2, aad)

		if err == nil && !genuine {
			t.Fatalf("forged packet authenticated: ctr=%d len=%d aad=%x out=%q",
				ctr, len(ciphertext), aad2, out)
		}
		if err != nil && out != nil {
			t.Fatalf("Open returned plaintext alongside error %v", err)
		}
	})
}

// FuzzReplayWindow drives the sliding window with arbitrary counter sequences.
// It must never panic — the shift arithmetic in commit is the risk, since a
// jump larger than the window width would be an out-of-range shift if the
// bounds check were dropped — and a committed counter must never afterwards
// report as unseen, which is the property replay rejection rests on.
func FuzzReplayWindow(f *testing.F) {
	f.Add([]byte{0, 0, 0, 0, 0, 0, 0, 1})
	f.Add(bytes.Repeat([]byte{0xff}, 16))
	f.Add(make([]byte, 64))

	f.Fuzz(func(t *testing.T, raw []byte) {
		var w replayWindow
		// Consume the input eight bytes at a time as counters, so the fuzzer
		// reaches the large-jump paths rather than only small increments.
		for i := 0; i+8 <= len(raw); i += 8 {
			ctr := binary.BigEndian.Uint64(raw[i : i+8])
			if ctr == 0 {
				continue
			}
			if w.check(ctr) {
				w.commit(ctr)
				if w.check(ctr) {
					t.Fatalf("counter %d still reported unseen after commit", ctr)
				}
			}
		}
	})
}

func fuzzPair(tb testing.TB) (client, server *Session) {
	tb.Helper()
	k := testKey(0x5a)
	salt := []byte("fuzz-salt")
	c, err := NewSession(k, RoleClient, salt)
	if err != nil {
		tb.Fatalf("client session: %v", err)
	}
	s, err := NewSession(k, RoleServer, salt)
	if err != nil {
		tb.Fatalf("server session: %v", err)
	}
	return c, s
}
