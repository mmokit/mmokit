package replication

import (
	"encoding/hex"
	"testing"
)

// Byte-level pin for the replication frame layout.
//
// Every other test in this package is a ROUND TRIP: encode, decode, compare.
// Those pass unchanged if the encoder and decoder are altered together, which
// is exactly what a codec refactor does — so they cannot detect the failure
// they look like they are guarding against. A peer running the old build is
// the party that notices, in production. This test is the one that fails in CI
// instead.
//
// The expectation is the current encoder's output, deliberately: the contract
// being pinned is "these bytes do not change", not "these bytes are correct in
// the abstract". Regenerating it is a wire break, and must come with a version
// bump and a lockstep client redeploy.
//
// Layout, little-endian throughout, hand-derived from the hex below so a
// reader can check it without running anything:
//
//	header  viewerID   u64  0807060504030201  → 0x0102030405060708
//	        senderNode u32  44332211          → 0x11223344
//	        tick       u64  efbeadde00000000  → 0xdeadbeef
//	        count      u32  02000000          → 2
//	entry 1 netID.ID   u32  ddccbbaa          → 0xaabbccdd
//	        netID.Epoch u32 07000000          → 7
//	        kind       u16  0201              → 0x0102
//	        deltaLen   u32  02000000          → 2
//	        delta           dead
//	entry 2 netID.ID   u32  01000000          → 1
//	        netID.Epoch u32 00000000          → 0
//	        kind       u16  0000              → 0
//	        deltaLen   u32  00000000          → 0 (nil DeltaBuf encodes as empty)
const goldenFrameHex = "080706050403020144332211efbeadde0000000002000000" +
	"ddccbbaa07000000020102000000dead" +
	"0100000000000000000000000000"

func goldenFrame() *Frame {
	return &Frame{
		ViewerID:   0x0102030405060708,
		SenderNode: 0x11223344,
		Tick:       0xDEADBEEF,
		Entries: []FrameEntry{
			{NetID: NetID{ID: 0xAABBCCDD, Epoch: 7}, Kind: 0x0102, DeltaBuf: []byte{0xDE, 0xAD}},
			// A nil DeltaBuf, because "nil encodes identically to empty" is a
			// property the collapse could plausibly break.
			{NetID: NetID{ID: 1, Epoch: 0}, Kind: 0, DeltaBuf: nil},
		},
	}
}

func TestFrame_EncodeMatchesGoldenBytes(t *testing.T) {
	got := hex.EncodeToString(goldenFrame().Encode())
	if got != goldenFrameHex {
		t.Fatalf("frame encoding changed — this is a WIRE BREAK, not a test failure to paper over.\n got  %s\n want %s", got, goldenFrameHex)
	}
}

// SizeEncoded is used to pre-allocate the encode buffer and to report frame
// sizes to metrics. A drift between it and the real encoding is silent in
// every round-trip test.
func TestFrame_SizeEncodedMatchesGoldenLength(t *testing.T) {
	want := len(goldenFrameHex) / 2
	if got := goldenFrame().SizeEncoded(); got != want {
		t.Fatalf("SizeEncoded = %d, want %d (golden frame length)", got, want)
	}
}

// The decode half, pinned independently. Decoding the golden bytes must
// reproduce the exact source struct — if only the encoder is rewritten, this
// is the test that catches it.
func TestDecodeFrame_GoldenBytesReproduceSource(t *testing.T) {
	raw, err := hex.DecodeString(goldenFrameHex)
	if err != nil {
		t.Fatalf("bad golden hex: %v", err)
	}
	got, err := DecodeFrame(raw)
	if err != nil {
		t.Fatalf("DecodeFrame(golden): %v", err)
	}
	want := *goldenFrame()
	if got.ViewerID != want.ViewerID || got.SenderNode != want.SenderNode || got.Tick != want.Tick {
		t.Fatalf("header = %+v, want %+v", got, want)
	}
	if len(got.Entries) != len(want.Entries) {
		t.Fatalf("decoded %d entries, want %d", len(got.Entries), len(want.Entries))
	}
	for i := range want.Entries {
		w, g := want.Entries[i], got.Entries[i]
		if g.NetID != w.NetID || g.Kind != w.Kind {
			t.Fatalf("entry %d = %+v, want %+v", i, g, w)
		}
		if len(g.DeltaBuf) != len(w.DeltaBuf) {
			t.Fatalf("entry %d delta len = %d, want %d", i, len(g.DeltaBuf), len(w.DeltaBuf))
		}
		for j := range w.DeltaBuf {
			if g.DeltaBuf[j] != w.DeltaBuf[j] {
				t.Fatalf("entry %d delta byte %d = %#x, want %#x", i, j, g.DeltaBuf[j], w.DeltaBuf[j])
			}
		}
	}
}
