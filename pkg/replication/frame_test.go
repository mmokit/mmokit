package replication

import (
	"bytes"
	"testing"
)

func TestFrame_RoundTrip(t *testing.T) {
	original := Frame{
		ViewerID:   42,
		SenderNode: 7,
		Tick:       100,
		Entries: []FrameEntry{
			{NetID: NetID{ID: 1, Epoch: 0}, Kind: 2, DeltaBuf: []byte{0xAA, 0xBB}},
			{NetID: NetID{ID: 5, Epoch: 3}, Kind: 4, DeltaBuf: []byte{0xCC}},
		},
	}
	encoded := original.Encode()
	sz := original.SizeEncoded()
	if sz != len(encoded) {
		t.Fatalf("SizeEncoded=%d actual=%d", sz, len(encoded))
	}
	decoded, err := DecodeFrame(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.ViewerID != original.ViewerID || decoded.SenderNode != original.SenderNode {
		t.Fatalf("header mismatch: %+v vs %+v", decoded, original)
	}
	if decoded.Tick != original.Tick {
		t.Fatalf("tick mismatch: %d vs %d", decoded.Tick, original.Tick)
	}
	if len(decoded.Entries) != 2 {
		t.Fatalf("got %d entries", len(decoded.Entries))
	}
	if decoded.Entries[0].NetID.ID != 1 || decoded.Entries[0].NetID.Epoch != 0 {
		t.Fatalf("entry 0 netid: %+v", decoded.Entries[0].NetID)
	}
	if decoded.Entries[0].Kind != 2 {
		t.Fatalf("entry 0 kind: %d", decoded.Entries[0].Kind)
	}
	if !bytes.Equal(decoded.Entries[0].DeltaBuf, []byte{0xAA, 0xBB}) {
		t.Fatalf("entry 0 delta: %x", decoded.Entries[0].DeltaBuf)
	}
	if decoded.Entries[1].NetID.ID != 5 || decoded.Entries[1].NetID.Epoch != 3 {
		t.Fatalf("entry 1 netid: %+v", decoded.Entries[1].NetID)
	}
	if decoded.Entries[1].Kind != 4 {
		t.Fatalf("entry 1 kind: %d", decoded.Entries[1].Kind)
	}
	if !bytes.Equal(decoded.Entries[1].DeltaBuf, []byte{0xCC}) {
		t.Fatalf("entry 1 delta: %x", decoded.Entries[1].DeltaBuf)
	}
}

func TestFrame_EmptyEntries(t *testing.T) {
	f := Frame{ViewerID: 1, SenderNode: 2, Tick: 3}
	enc := f.Encode()
	if len(enc) == 0 {
		t.Fatal("empty frame should still have header")
	}
	if len(enc) != frameHeaderBytes {
		t.Fatalf("empty frame size: got %d, want %d", len(enc), frameHeaderBytes)
	}
	dec, err := DecodeFrame(enc)
	if err != nil {
		t.Fatal(err)
	}
	if dec.ViewerID != 1 || dec.SenderNode != 2 || dec.Tick != 3 {
		t.Fatalf("header lost: %+v", dec)
	}
	if len(dec.Entries) != 0 {
		t.Fatalf("expected 0 entries, got %d", len(dec.Entries))
	}
}

func TestFrame_SizeEncodedMatchesEncode(t *testing.T) {
	// Verify SizeEncoded is exact for a variety of shapes.
	cases := []Frame{
		{},
		{ViewerID: 1, SenderNode: 2, Tick: 3},
		{Entries: []FrameEntry{{NetID: NetID{ID: 1}, Kind: 1, DeltaBuf: nil}}},
		{Entries: []FrameEntry{{NetID: NetID{ID: 1}, Kind: 1, DeltaBuf: make([]byte, 1000)}}},
		{Entries: []FrameEntry{
			{NetID: NetID{ID: 1}, DeltaBuf: []byte{0x01}},
			{NetID: NetID{ID: 2}, DeltaBuf: []byte{0x02, 0x03}},
			{NetID: NetID{ID: 3}, DeltaBuf: make([]byte, 100)},
		}},
	}
	for i, f := range cases {
		sz := f.SizeEncoded()
		enc := f.Encode()
		if sz != len(enc) {
			t.Errorf("case %d: SizeEncoded=%d actual=%d", i, sz, len(enc))
		}
	}
}

func TestDecodeFrame_TruncatedHeader(t *testing.T) {
	_, err := DecodeFrame([]byte{0x01, 0x02, 0x03})
	if err == nil {
		t.Fatal("expected error for truncated header")
	}
}

func TestDecodeFrame_TruncatedEntry(t *testing.T) {
	// Valid header announcing 1 entry, but the entry bytes are cut off.
	f := Frame{
		ViewerID: 1,
		Entries:  []FrameEntry{{NetID: NetID{ID: 1}, DeltaBuf: []byte{0xAA, 0xBB, 0xCC}}},
	}
	enc := f.Encode()
	// Chop off the last two bytes of the delta payload.
	_, err := DecodeFrame(enc[:len(enc)-2])
	if err == nil {
		t.Fatal("expected error for truncated entry payload")
	}
}

func TestFrame_NilDeltaBufRoundTrip(t *testing.T) {
	// A FrameEntry with nil DeltaBuf must round-trip correctly.
	// The decoded value may be nil or a zero-length slice; both are
	// acceptable, but the length must be zero and subsequent entries
	// must still decode correctly.
	original := Frame{
		ViewerID: 1,
		Entries: []FrameEntry{
			{NetID: NetID{ID: 1}, Kind: 1, DeltaBuf: nil},
			{NetID: NetID{ID: 2}, Kind: 2, DeltaBuf: []byte{0xAA}},
		},
	}
	enc := original.Encode()
	dec, err := DecodeFrame(enc)
	if err != nil {
		t.Fatal(err)
	}
	if len(dec.Entries) != 2 {
		t.Fatalf("entries: got %d want 2", len(dec.Entries))
	}
	if len(dec.Entries[0].DeltaBuf) != 0 {
		t.Fatalf("entry 0 DeltaBuf should be zero-length, got %x", dec.Entries[0].DeltaBuf)
	}
	if len(dec.Entries[1].DeltaBuf) != 1 || dec.Entries[1].DeltaBuf[0] != 0xAA {
		t.Fatalf("entry 1 DeltaBuf lost: %x", dec.Entries[1].DeltaBuf)
	}
}

func TestDecodeFrame_TruncatedFixedPortion(t *testing.T) {
	// A header that announces 1 entry, but the entry's fixed 14-byte
	// portion is cut off mid-stream. Should return an error, not panic.
	f := Frame{
		ViewerID: 1,
		Entries:  []FrameEntry{{NetID: NetID{ID: 99}, Kind: 5, DeltaBuf: []byte{0xAA, 0xBB}}},
	}
	enc := f.Encode()
	// Trim to header + half of the fixed portion.
	truncated := enc[:frameHeaderBytes+7]
	_, err := DecodeFrame(truncated)
	if err == nil {
		t.Fatal("expected error for truncated fixed portion")
	}
}

func TestDecodeFrame_ImplausibleCount(t *testing.T) {
	// A valid 24-byte header announcing 4 billion entries would cause
	// the old code to attempt an enormous preallocation. Verify the
	// sanity check rejects it cleanly.
	var data [24]byte
	// ViewerID, SenderNode, Tick all zero; count at offset 20-23.
	data[20] = 0xFF
	data[21] = 0xFF
	data[22] = 0xFF
	data[23] = 0xFF
	_, err := DecodeFrame(data[:])
	if err == nil {
		t.Fatal("expected error for implausible entry count")
	}
}
