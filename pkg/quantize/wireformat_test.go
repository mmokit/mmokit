package quantize

import (
	"bytes"
	"testing"
)

func TestWireformatRoundTrip(t *testing.T) {
	enc := NewFrameEncoder(256)

	full := []FullEntry{
		{NetID: 1, EntityType: 0, ProducedAtMs: 1_000_000, Snapshot: []byte{0x01, 0x02, 0x03}, InitialData: []byte("alice")},
		{NetID: 2, EntityType: 1, ProducedAtMs: 2_000_000, Snapshot: []byte{0x04, 0x05}, InitialData: nil},
	}
	deltas := []DeltaEntry{
		{NetID: 3, EntityType: 0, ProducedAtMs: 3_000_000, Data: []byte{0xFF, 0x10, 0x20}},
	}
	removed := []uint32{100, 200}
	exited := []uint32{300}

	data := enc.Encode(42, 7, 0, full, deltas, removed, exited)

	dec := NewFrameDecoder(data)
	hdr := dec.Header()

	if hdr.Tick != 42 {
		t.Fatalf("tick: got %d, want 42", hdr.Tick)
	}
	if hdr.Seq != 7 {
		t.Fatalf("seq: got %d, want 7", hdr.Seq)
	}
	if hdr.FullCount != 2 {
		t.Fatalf("fullCount: got %d, want 2", hdr.FullCount)
	}
	if hdr.DeltaCount != 1 {
		t.Fatalf("deltaCount: got %d, want 1", hdr.DeltaCount)
	}
	if hdr.RemovedCount != 2 {
		t.Fatalf("removedCount: got %d, want 2", hdr.RemovedCount)
	}
	if hdr.ExitedCount != 1 {
		t.Fatalf("exitedCount: got %d, want 1", hdr.ExitedCount)
	}

	// Full entries.
	f0 := dec.NextFull()
	if f0.NetID != 1 || f0.EntityType != 0 {
		t.Fatalf("full[0]: netID=%d type=%d", f0.NetID, f0.EntityType)
	}
	if f0.ProducedAtMs != 1_000_000 {
		t.Fatalf("full[0] producedAtMs: got %d want 1_000_000", f0.ProducedAtMs)
	}
	if !bytes.Equal(f0.Snapshot, []byte{0x01, 0x02, 0x03}) {
		t.Fatalf("full[0] snapshot: %v", f0.Snapshot)
	}
	if !bytes.Equal(f0.InitialData, []byte("alice")) {
		t.Fatalf("full[0] initialData: %v", f0.InitialData)
	}

	f1 := dec.NextFull()
	if f1.NetID != 2 || f1.EntityType != 1 {
		t.Fatalf("full[1]: netID=%d type=%d", f1.NetID, f1.EntityType)
	}
	if f1.ProducedAtMs != 2_000_000 {
		t.Fatalf("full[1] producedAtMs: got %d want 2_000_000", f1.ProducedAtMs)
	}
	if !bytes.Equal(f1.Snapshot, []byte{0x04, 0x05}) {
		t.Fatalf("full[1] snapshot: %v", f1.Snapshot)
	}
	if f1.InitialData != nil {
		t.Fatalf("full[1] initialData should be nil: %v", f1.InitialData)
	}

	// Delta entries.
	d0 := dec.NextDelta()
	if d0.NetID != 3 || d0.EntityType != 0 {
		t.Fatalf("delta[0]: netID=%d type=%d", d0.NetID, d0.EntityType)
	}
	if d0.ProducedAtMs != 3_000_000 {
		t.Fatalf("delta[0] producedAtMs: got %d want 3_000_000", d0.ProducedAtMs)
	}
	if !bytes.Equal(d0.Data, []byte{0xFF, 0x10, 0x20}) {
		t.Fatalf("delta[0] data: %v", d0.Data)
	}

	// Removed IDs.
	if dec.NextUint32() != 100 {
		t.Fatal("removed[0]")
	}
	if dec.NextUint32() != 200 {
		t.Fatal("removed[1]")
	}

	// Exited IDs.
	if dec.NextUint32() != 300 {
		t.Fatal("exited[0]")
	}
}

func TestWireformatEmpty(t *testing.T) {
	enc := NewFrameEncoder(64)
	data := enc.Encode(1, 1, 0, nil, nil, nil, nil)

	dec := NewFrameDecoder(data)
	hdr := dec.Header()

	if hdr.Tick != 1 || hdr.Seq != 1 {
		t.Fatalf("header: tick=%d seq=%d", hdr.Tick, hdr.Seq)
	}
	if hdr.FullCount != 0 || hdr.DeltaCount != 0 || hdr.RemovedCount != 0 || hdr.ExitedCount != 0 {
		t.Fatal("expected all counts 0")
	}
	if len(data) != frameHeaderSize {
		t.Fatalf("expected %d bytes for empty frame, got %d", frameHeaderSize, len(data))
	}
}

func TestWireformatEncoderReuse(t *testing.T) {
	enc := NewFrameEncoder(64)

	data1 := enc.Encode(1, 1, 0, []FullEntry{{NetID: 1, Snapshot: []byte{0x01}}}, nil, nil, nil)
	len1 := len(data1)

	data2 := enc.Encode(2, 2, 0, nil, nil, nil, nil)
	len2 := len(data2)

	// data1 slice is invalidated by reuse, but len should differ.
	if len1 == len2 {
		t.Fatal("expected different lengths for different frames")
	}
	if len2 != frameHeaderSize {
		t.Fatalf("empty frame should be %d bytes, got %d", frameHeaderSize, len2)
	}
}

func TestFrameEncoder_CarriesEpoch(t *testing.T) {
	enc := NewFrameEncoder(256)
	full := []FullEntry{{NetID: 42, Epoch: 7, EntityType: 1, Snapshot: []byte{0xAA, 0xBB}}}
	deltas := []DeltaEntry{{NetID: 43, Epoch: 9, EntityType: 2, Data: []byte{0xCC}}}
	data := enc.Encode(1, 1, 0, full, deltas, nil, nil)

	dec := NewFrameDecoder(data)
	_ = dec.Header()
	got := dec.NextFull()
	if got.NetID != 42 || got.Epoch != 7 {
		t.Fatalf("full: got NetID=%d Epoch=%d, want 42/7", got.NetID, got.Epoch)
	}
	gotD := dec.NextDelta()
	if gotD.NetID != 43 || gotD.Epoch != 9 {
		t.Fatalf("delta: got NetID=%d Epoch=%d, want 43/9", gotD.NetID, gotD.Epoch)
	}
}

func TestFrameEncoder_FullEntryCarriesProducedAtMs(t *testing.T) {
	enc := NewFrameEncoder(128)
	produced := uint64(1_234_567_890_123)
	full := []FullEntry{{
		NetID:        42,
		Epoch:        1,
		EntityType:   1,
		ProducedAtMs: produced,
		Snapshot:     []byte{0x01, 0x02, 0x03},
		InitialData:  nil,
	}}
	buf := enc.Encode(100, 1, 0, full, nil, nil, nil)

	dec := NewFrameDecoder(buf)
	hdr := dec.Header()
	if hdr.FullCount != 1 {
		t.Fatalf("FullCount=%d, want 1", hdr.FullCount)
	}
	got := dec.NextFull()
	if got.ProducedAtMs != produced {
		t.Fatalf("ProducedAtMs round-trip: got %d, want %d", got.ProducedAtMs, produced)
	}
	if string(got.Snapshot) != string(full[0].Snapshot) {
		t.Fatal("Snapshot payload corrupted by producedAtMs insertion")
	}
}

func TestFrameEncoder_DeltaEntryCarriesProducedAtMs(t *testing.T) {
	enc := NewFrameEncoder(128)
	produced := uint64(9_999_999)
	deltas := []DeltaEntry{{
		NetID:        7,
		Epoch:        2,
		EntityType:   3,
		ProducedAtMs: produced,
		Data:         []byte{0xAA, 0xBB},
	}}
	buf := enc.Encode(200, 2, 0, nil, deltas, nil, nil)

	dec := NewFrameDecoder(buf)
	_ = dec.Header()
	got := dec.NextDelta()
	if got.ProducedAtMs != produced {
		t.Fatalf("Delta.ProducedAtMs: got %d, want %d", got.ProducedAtMs, produced)
	}
}

func TestFrameHeader_NoServerTimeMs(t *testing.T) {
	// Header shrinks from 28 → 20 bytes after serverTimeMs removal.
	enc := NewFrameEncoder(64)
	buf := enc.Encode(1, 0, 0, nil, nil, nil, nil)
	// 4 tick + 4 seq + 4 flags + 2 fullCnt + 2 deltaCnt + 2 removedCnt + 2 exitedCnt = 20
	if len(buf) != 20 {
		t.Fatalf("empty frame size = %d, want 20 (header only, no serverTimeMs)", len(buf))
	}
}
