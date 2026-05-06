package universe

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestEncodeTypedEventFrame_SingleEvent(t *testing.T) {
	body := []byte{0x01, 0x02, 0x03, 0x04, 0x05}
	frame := EncodeTypedEventFrame(0xDEADBEEF, body)

	if frame[0] != 0x00 {
		t.Fatalf("channel byte: got %#x, want 0x00", frame[0])
	}
	gotTypeID := binary.LittleEndian.Uint32(frame[1:5])
	if gotTypeID != 0xDEADBEEF {
		t.Fatalf("typeID: got %#x, want 0xDEADBEEF", gotTypeID)
	}
	gotLen := binary.LittleEndian.Uint32(frame[5:9])
	if gotLen != uint32(len(body)) {
		t.Fatalf("body_len: got %d, want %d", gotLen, len(body))
	}
	if !bytes.Equal(frame[9:], body) {
		t.Fatalf("body bytes mismatch")
	}
	if len(frame) != 1+4+4+len(body) {
		t.Fatalf("total len: got %d, want %d", len(frame), 1+4+4+len(body))
	}
}

func TestEncodeBatchedTypedEventFrame_TwoEvents(t *testing.T) {
	events := []BroadcastEvent{
		{TypeID: 0xAAAA0001, Body: []byte{0x10, 0x20}},
		{TypeID: 0xBBBB0002, Body: []byte{0x30, 0x40, 0x50}},
	}
	frame := EncodeBatchedTypedEventFrame(events)

	if frame[0] != 0x00 {
		t.Fatalf("channel byte: got %#x, want 0x00", frame[0])
	}
	// Entry 1: typeID=AAAA0001 len=2 body=10,20
	if binary.LittleEndian.Uint32(frame[1:5]) != 0xAAAA0001 {
		t.Fatal("entry 1 typeID")
	}
	if binary.LittleEndian.Uint32(frame[5:9]) != 2 {
		t.Fatal("entry 1 body_len")
	}
	if !bytes.Equal(frame[9:11], []byte{0x10, 0x20}) {
		t.Fatal("entry 1 body")
	}
	// Entry 2: typeID=BBBB0002 len=3 body=30,40,50
	if binary.LittleEndian.Uint32(frame[11:15]) != 0xBBBB0002 {
		t.Fatal("entry 2 typeID")
	}
	if binary.LittleEndian.Uint32(frame[15:19]) != 3 {
		t.Fatal("entry 2 body_len")
	}
	if !bytes.Equal(frame[19:22], []byte{0x30, 0x40, 0x50}) {
		t.Fatal("entry 2 body")
	}
	if len(frame) != 22 {
		t.Fatalf("total len: got %d, want 22", len(frame))
	}
}

func TestEncodeBatchedTypedEventFrame_Empty(t *testing.T) {
	frame := EncodeBatchedTypedEventFrame(nil)
	if frame != nil {
		t.Fatalf("empty batch should return nil, got %v", frame)
	}
}
