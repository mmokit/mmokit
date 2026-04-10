package quantize

import (
	"bytes"
	"testing"
)

func TestWireformatRoundTrip(t *testing.T) {
	enc := NewFrameEncoder(256)

	full := []FullEntry{
		{NetID: 1, EntityType: 0, Snapshot: []byte{0x01, 0x02, 0x03}, InitialData: []byte("alice")},
		{NetID: 2, EntityType: 1, Snapshot: []byte{0x04, 0x05}, InitialData: nil},
	}
	deltas := []DeltaEntry{
		{NetID: 3, EntityType: 0, Data: []byte{0xFF, 0x10, 0x20}},
	}
	removed := []uint32{100, 200}
	exited := []uint32{300}

	data := enc.Encode(42, 7, full, deltas, removed, exited)

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
	data := enc.Encode(1, 1, nil, nil, nil, nil)

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

	data1 := enc.Encode(1, 1, []FullEntry{{NetID: 1, Snapshot: []byte{0x01}}}, nil, nil, nil)
	len1 := len(data1)

	data2 := enc.Encode(2, 2, nil, nil, nil, nil)
	len2 := len(data2)

	// data1 slice is invalidated by reuse, but len should differ.
	if len1 == len2 {
		t.Fatal("expected different lengths for different frames")
	}
	if len2 != frameHeaderSize {
		t.Fatalf("empty frame should be %d bytes, got %d", frameHeaderSize, len2)
	}
}
