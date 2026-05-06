package universe

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestEncodeTypedOpFrame(t *testing.T) {
	body := []byte{0xAA, 0xBB, 0xCC, 0xDD}
	frame := EncodeTypedOpFrame(0xCAFEBABE, 0x0123456789ABCDEF, body)

	if frame[0] != 0x01 {
		t.Fatalf("channel byte: got %#x, want 0x01", frame[0])
	}
	if got := binary.LittleEndian.Uint32(frame[1:5]); got != 0xCAFEBABE {
		t.Fatalf("typeID: got %#x, want 0xCAFEBABE", got)
	}
	if got := binary.LittleEndian.Uint64(frame[5:13]); got != 0x0123456789ABCDEF {
		t.Fatalf("request_id: got %#x, want 0x0123456789ABCDEF", got)
	}
	if got := binary.LittleEndian.Uint32(frame[13:17]); got != uint32(len(body)) {
		t.Fatalf("body_len: got %d, want %d", got, len(body))
	}
	if !bytes.Equal(frame[17:], body) {
		t.Fatalf("body bytes mismatch: got %x, want %x", frame[17:], body)
	}
	if len(frame) != 1+4+8+4+len(body) {
		t.Fatalf("total len: got %d, want %d", len(frame), 1+4+8+4+len(body))
	}
}

func TestDecodeTypedOpFrame(t *testing.T) {
	// Build a payload (channel byte stripped) by hand.
	body := []byte{0x10, 0x20, 0x30}
	payload := make([]byte, 4+8+4+len(body))
	binary.LittleEndian.PutUint32(payload[0:4], 0xDEADBEEF)
	binary.LittleEndian.PutUint64(payload[4:12], 42)
	binary.LittleEndian.PutUint32(payload[12:16], uint32(len(body)))
	copy(payload[16:], body)

	typeID, requestID, gotBody, err := DecodeTypedOpFrame(payload)
	if err != nil {
		t.Fatalf("DecodeTypedOpFrame: unexpected error: %v", err)
	}
	if typeID != 0xDEADBEEF {
		t.Errorf("typeID: got %#x, want 0xDEADBEEF", typeID)
	}
	if requestID != 42 {
		t.Errorf("requestID: got %d, want 42", requestID)
	}
	if !bytes.Equal(gotBody, body) {
		t.Errorf("body: got %x, want %x", gotBody, body)
	}
}

func TestDecodeTypedOpFrame_RoundTrip(t *testing.T) {
	body := []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	frame := EncodeTypedOpFrame(0x55AA55AA, 9999, body)
	// Strip channel byte to feed back into Decode.
	typeID, reqID, gotBody, err := DecodeTypedOpFrame(frame[1:])
	if err != nil {
		t.Fatalf("DecodeTypedOpFrame: %v", err)
	}
	if typeID != 0x55AA55AA {
		t.Errorf("typeID round-trip mismatch: got %#x", typeID)
	}
	if reqID != 9999 {
		t.Errorf("reqID round-trip mismatch: got %d", reqID)
	}
	if !bytes.Equal(gotBody, body) {
		t.Errorf("body round-trip mismatch")
	}
}

func TestDecodeTypedOpFrame_Truncated(t *testing.T) {
	// Header is 16 bytes; anything shorter must error.
	short := []byte{0x01, 0x02, 0x03}
	_, _, _, err := DecodeTypedOpFrame(short)
	if err == nil {
		t.Fatalf("expected error for truncated header, got nil")
	}
}

func TestDecodeTypedOpFrame_BodyLenOverflow(t *testing.T) {
	// Declared body_len exceeds remaining payload.
	payload := make([]byte, 4+8+4) // header only, no body bytes
	binary.LittleEndian.PutUint32(payload[0:4], 1)
	binary.LittleEndian.PutUint64(payload[4:12], 1)
	binary.LittleEndian.PutUint32(payload[12:16], 100) // claim 100 bytes, but none follow
	_, _, _, err := DecodeTypedOpFrame(payload)
	if err == nil {
		t.Fatalf("expected error for declared body_len exceeding payload, got nil")
	}
}
