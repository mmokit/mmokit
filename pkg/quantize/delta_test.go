package quantize

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// Food layout: posX(2) posY(2) value(1) colorIdx(1) = 6 bytes, 4 fields.
var foodSizes = []int{2, 2, 1, 1}

// Snake layout: 7 fixed fields + var tail.
// posX(2) posY(2) dirX(2) dirY(2) speed(2) skin(1) flags(1) + var tail
var snakeSizes = []int{2, 2, 2, 2, 2, 1, 1, -1}

func makeFood(posX, posY uint16, value, color byte) []byte {
	b := make([]byte, 6)
	binary.BigEndian.PutUint16(b[0:2], posX)
	binary.BigEndian.PutUint16(b[2:4], posY)
	b[4] = value
	b[5] = color
	return b
}

func makeSnake(posX, posY, dirX, dirY, speed uint16, skin, flags byte, tail []byte) []byte {
	fixed := 12 // 2+2+2+2+2+1+1
	b := make([]byte, fixed+2+len(tail))
	off := 0
	binary.BigEndian.PutUint16(b[off:], posX)
	off += 2
	binary.BigEndian.PutUint16(b[off:], posY)
	off += 2
	binary.BigEndian.PutUint16(b[off:], dirX)
	off += 2
	binary.BigEndian.PutUint16(b[off:], dirY)
	off += 2
	binary.BigEndian.PutUint16(b[off:], speed)
	off += 2
	b[off] = skin
	off++
	b[off] = flags
	off++
	binary.BigEndian.PutUint16(b[off:], uint16(len(tail)))
	off += 2
	copy(b[off:], tail)
	return b
}

func TestIdenticalReturnsNil(t *testing.T) {
	enc := NewDeltaEncoder(foodSizes...)
	snap := makeFood(100, 200, 5, 3)
	prev := make([]byte, len(snap))
	copy(prev, snap)

	delta := enc.Encode(prev, snap, nil)
	if delta != nil {
		t.Fatalf("expected nil for identical snapshots, got %v", delta)
	}
}

func TestSingleFieldChange(t *testing.T) {
	enc := NewDeltaEncoder(foodSizes...)
	prev := makeFood(100, 200, 5, 3)
	curr := makeFood(100, 200, 9, 3) // value changed (field 2)

	delta := enc.Encode(prev, curr, nil)
	if delta == nil {
		t.Fatal("expected non-nil delta")
	}

	// Bitmask: 1 byte, field 2 set → 0b00000100 = 0x04
	if delta[0] != 0x04 {
		t.Fatalf("expected bitmask 0x04, got 0x%02x", delta[0])
	}
	// Changed value: single byte = 9
	if len(delta) != 2 || delta[1] != 9 {
		t.Fatalf("expected [0x04, 9], got %v", delta)
	}
}

func TestMultipleFieldChanges(t *testing.T) {
	enc := NewDeltaEncoder(foodSizes...)
	prev := makeFood(100, 200, 5, 3)
	curr := makeFood(150, 200, 5, 7) // posX (field 0) and colorIdx (field 3) changed

	delta := enc.Encode(prev, curr, nil)
	if delta == nil {
		t.Fatal("expected non-nil delta")
	}

	// Bitmask: field 0 and field 3 → 0b00001001 = 0x09
	if delta[0] != 0x09 {
		t.Fatalf("expected bitmask 0x09, got 0x%02x", delta[0])
	}
	// posX (2 bytes) + colorIdx (1 byte) = 3 bytes payload
	if len(delta) != 1+3 {
		t.Fatalf("expected 4 bytes total, got %d", len(delta))
	}
	gotPosX := binary.BigEndian.Uint16(delta[1:3])
	if gotPosX != 150 {
		t.Fatalf("expected posX 150, got %d", gotPosX)
	}
	if delta[3] != 7 {
		t.Fatalf("expected colorIdx 7, got %d", delta[3])
	}
}

func TestAllFieldsChanged(t *testing.T) {
	enc := NewDeltaEncoder(foodSizes...)
	prev := makeFood(100, 200, 5, 3)
	curr := makeFood(150, 250, 9, 7)

	delta := enc.Encode(prev, curr, nil)
	if delta == nil {
		t.Fatal("expected non-nil delta")
	}

	// All 4 fields changed → 0b00001111 = 0x0F
	if delta[0] != 0x0F {
		t.Fatalf("expected bitmask 0x0F, got 0x%02x", delta[0])
	}
	// Full payload: 2+2+1+1 = 6 bytes
	if len(delta) != 1+6 {
		t.Fatalf("expected 7 bytes total, got %d", len(delta))
	}
}

func TestEncodeDecodeRoundTrip(t *testing.T) {
	enc := NewDeltaEncoder(foodSizes...)
	prev := makeFood(100, 200, 5, 3)
	curr := makeFood(150, 200, 9, 3)

	delta := enc.Encode(prev, curr, nil)
	if delta == nil {
		t.Fatal("expected non-nil delta")
	}

	base := make([]byte, len(prev))
	copy(base, prev)
	result := enc.Decode(base, delta)

	if !bytes.Equal(result, curr) {
		t.Fatalf("round-trip failed: expected %v, got %v", curr, result)
	}
}

func TestVarTailUnchanged(t *testing.T) {
	enc := NewDeltaEncoder(snakeSizes...)
	tail := []byte{10, 20, 30, 40}
	prev := makeSnake(100, 200, 1, 0, 5, 2, 0, tail)
	curr := makeSnake(110, 200, 1, 0, 5, 2, 0, tail) // only posX changed

	delta := enc.Encode(prev, curr, nil)
	if delta == nil {
		t.Fatal("expected non-nil delta")
	}

	// 8 fields (7 fixed + 1 var tail) → 1 byte bitmask.
	// Only field 0 (posX) changed → 0x01.
	if delta[0] != 0x01 {
		t.Fatalf("expected bitmask 0x01, got 0x%02x", delta[0])
	}
	// Tail bit (field 7) should NOT be set.
	if delta[0]&0x80 != 0 {
		t.Fatal("tail bit should not be set")
	}
}

func TestVarTailChangedSameLength(t *testing.T) {
	enc := NewDeltaEncoder(snakeSizes...)
	prevTail := []byte{10, 20, 30, 40}
	currTail := []byte{10, 20, 99, 40} // same length, different content
	prev := makeSnake(100, 200, 1, 0, 5, 2, 0, prevTail)
	curr := makeSnake(100, 200, 1, 0, 5, 2, 0, currTail)

	delta := enc.Encode(prev, curr, nil)
	if delta == nil {
		t.Fatal("expected non-nil delta")
	}

	// Only tail changed → field 7 bit set → 0x80
	if delta[0] != 0x80 {
		t.Fatalf("expected bitmask 0x80, got 0x%02x", delta[0])
	}
	// Payload: uint16 len (4) + 4 bytes data = 6 bytes after bitmask
	if len(delta) != 1+2+4 {
		t.Fatalf("expected 7 bytes, got %d", len(delta))
	}

	// Round-trip.
	base := make([]byte, len(prev))
	copy(base, prev)
	result := enc.Decode(base, delta)
	if !bytes.Equal(result, curr) {
		t.Fatalf("round-trip failed: expected %v, got %v", curr, result)
	}
}

func TestVarTailLengthChange(t *testing.T) {
	enc := NewDeltaEncoder(snakeSizes...)
	prevTail := []byte{10, 20, 30}
	currTail := []byte{10, 20, 30, 40, 50, 60} // grew from 3 to 6 bytes
	prev := makeSnake(100, 200, 1, 0, 5, 2, 0, prevTail)
	curr := makeSnake(100, 200, 1, 0, 5, 2, 0, currTail)

	delta := enc.Encode(prev, curr, nil)
	if delta == nil {
		t.Fatal("expected non-nil delta")
	}

	base := make([]byte, len(prev))
	copy(base, prev)
	result := enc.Decode(base, delta)
	if !bytes.Equal(result, curr) {
		t.Fatalf("round-trip with length change failed:\nexpected %v\ngot      %v", curr, result)
	}

	// Verify base was resized.
	if len(result) != len(curr) {
		t.Fatalf("expected resized length %d, got %d", len(curr), len(result))
	}
}

func TestMultiByteBitmask(t *testing.T) {
	// 9 fields of 1 byte each → 2-byte bitmask.
	sizes := []int{1, 1, 1, 1, 1, 1, 1, 1, 1}
	enc := NewDeltaEncoder(sizes...)

	if enc.BitmaskSize() != 2 {
		t.Fatalf("expected bitmask size 2, got %d", enc.BitmaskSize())
	}

	prev := make([]byte, 9)
	curr := make([]byte, 9)
	copy(curr, prev)
	curr[8] = 42 // field 8 changed (bit 0 of byte 1)

	delta := enc.Encode(prev, curr, nil)
	if delta == nil {
		t.Fatal("expected non-nil delta")
	}

	// Bitmask byte 0: 0x00 (fields 0-7 unchanged)
	// Bitmask byte 1: 0x01 (field 8 changed)
	if delta[0] != 0x00 || delta[1] != 0x01 {
		t.Fatalf("expected bitmask [0x00, 0x01], got [0x%02x, 0x%02x]", delta[0], delta[1])
	}
	if len(delta) != 3 { // 2 bitmask + 1 value
		t.Fatalf("expected 3 bytes, got %d", len(delta))
	}

	// Round-trip.
	base := make([]byte, 9)
	result := enc.Decode(base, delta)
	if !bytes.Equal(result, curr) {
		t.Fatalf("multi-byte bitmask round-trip failed")
	}
}

func TestExactly8Fields(t *testing.T) {
	// Exactly 8 fields → exactly 1 byte bitmask.
	sizes := []int{1, 1, 1, 1, 1, 1, 1, 1}
	enc := NewDeltaEncoder(sizes...)

	if enc.BitmaskSize() != 1 {
		t.Fatalf("expected bitmask size 1, got %d", enc.BitmaskSize())
	}
	if enc.FixedSize() != 8 {
		t.Fatalf("expected fixed size 8, got %d", enc.FixedSize())
	}

	prev := make([]byte, 8)
	curr := make([]byte, 8)
	for i := range curr {
		curr[i] = byte(i + 1)
	}

	delta := enc.Encode(prev, curr, nil)
	if delta == nil {
		t.Fatal("expected non-nil delta")
	}

	// All 8 fields changed → 0xFF
	if delta[0] != 0xFF {
		t.Fatalf("expected bitmask 0xFF, got 0x%02x", delta[0])
	}
	if len(delta) != 1+8 {
		t.Fatalf("expected 9 bytes, got %d", len(delta))
	}

	base := make([]byte, 8)
	result := enc.Decode(base, delta)
	if !bytes.Equal(result, curr) {
		t.Fatalf("exactly-8-fields round-trip failed")
	}
}
