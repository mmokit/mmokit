package quantize

import (
	"bytes"
	"encoding/binary"
)

// DeltaEncoder compares two fixed-layout snapshots field-by-field and produces
// compact delta encoding: a bitmask of changed fields + only changed field values.
type DeltaEncoder struct {
	fieldOffsets []int // cumulative byte offset of each field start
	fieldSizes   []int // byte size of each field
	fixedSize    int   // total fixed portion
	fieldCount   int
	bitmaskSize  int  // ceil(fieldCount / 8)
	hasVarTail   bool // last "field" is variable-length
}

// NewDeltaEncoder creates a DeltaEncoder from field size descriptors.
// Positive values represent fixed-size fields. A single -1 as the last element
// indicates a variable-length tail (preceded by a uint16 big-endian length prefix
// in the snapshot).
func NewDeltaEncoder(fieldSizes ...int) *DeltaEncoder {
	hasVarTail := false
	fixedFields := fieldSizes
	if len(fieldSizes) > 0 && fieldSizes[len(fieldSizes)-1] == -1 {
		hasVarTail = true
		fixedFields = fieldSizes[:len(fieldSizes)-1]
	}

	offsets := make([]int, len(fixedFields))
	sizes := make([]int, len(fixedFields))
	offset := 0
	for i, s := range fixedFields {
		offsets[i] = offset
		sizes[i] = s
		offset += s
	}

	fieldCount := len(fixedFields)
	if hasVarTail {
		fieldCount++ // var tail counts as one logical field
	}

	return &DeltaEncoder{
		fieldOffsets: offsets,
		fieldSizes:   sizes,
		fixedSize:    offset,
		fieldCount:   fieldCount,
		bitmaskSize:  (fieldCount + 7) / 8,
		hasVarTail:   hasVarTail,
	}
}

// FixedSize returns the total byte size of the fixed portion of a snapshot.
func (d *DeltaEncoder) FixedSize() int {
	return d.fixedSize
}

// BitmaskSize returns the number of bytes used for the field-change bitmask.
func (d *DeltaEncoder) BitmaskSize() int {
	return d.bitmaskSize
}

// Encode compares prev and curr snapshots field-by-field and appends a compact
// delta to out. Returns the extended out slice, or nil if the snapshots are identical.
//
// Wire format: [bitmaskSize bytes bitmask][changed field values in order]
// Bit ordering: field 0 = bit 0 of byte 0, field 8 = bit 0 of byte 1.
func (d *DeltaEncoder) Encode(prev, curr, out []byte) []byte {
	start := len(out)
	// Reserve space for bitmask.
	for i := 0; i < d.bitmaskSize; i++ {
		out = append(out, 0)
	}

	anyChanged := false

	// Compare fixed fields.
	fixedFieldCount := len(d.fieldOffsets)
	for i := 0; i < fixedFieldCount; i++ {
		off := d.fieldOffsets[i]
		sz := d.fieldSizes[i]
		if !bytes.Equal(prev[off:off+sz], curr[off:off+sz]) {
			out[start+i/8] |= 1 << (uint(i) % 8)
			out = append(out, curr[off:off+sz]...)
			anyChanged = true
		}
	}

	// Compare variable-length tail if present.
	if d.hasVarTail {
		tailIdx := fixedFieldCount // logical field index for the var tail
		prevTailData, _ := d.readVarTail(prev)
		currTailData, currTailLen := d.readVarTail(curr)

		if !bytes.Equal(prevTailData, currTailData) {
			out[start+tailIdx/8] |= 1 << (uint(tailIdx) % 8)
			var lenBuf [2]byte
			binary.BigEndian.PutUint16(lenBuf[:], currTailLen)
			out = append(out, lenBuf[:]...)
			out = append(out, currTailData...)
			anyChanged = true
		}
	}

	if !anyChanged {
		return nil
	}
	return out
}

// Decode reads a delta and applies changed fields to base in place.
// Returns the (possibly resized) base slice.
func (d *DeltaEncoder) Decode(base, delta []byte) []byte {
	pos := d.bitmaskSize // read cursor past bitmask

	// Apply fixed fields.
	fixedFieldCount := len(d.fieldOffsets)
	for i := 0; i < fixedFieldCount; i++ {
		if delta[i/8]&(1<<(uint(i)%8)) != 0 {
			off := d.fieldOffsets[i]
			sz := d.fieldSizes[i]
			copy(base[off:off+sz], delta[pos:pos+sz])
			pos += sz
		}
	}

	// Apply variable-length tail if present and changed.
	if d.hasVarTail {
		tailIdx := fixedFieldCount
		if delta[tailIdx/8]&(1<<(uint(tailIdx)%8)) != 0 {
			newLen := int(binary.BigEndian.Uint16(delta[pos : pos+2]))
			pos += 2
			newTailData := delta[pos : pos+newLen]

			// Rebuild base: fixed portion + uint16 len + tail data.
			needed := d.fixedSize + 2 + newLen
			if cap(base) >= needed {
				base = base[:needed]
			} else {
				grown := make([]byte, needed)
				copy(grown, base[:d.fixedSize])
				base = grown
			}
			binary.BigEndian.PutUint16(base[d.fixedSize:d.fixedSize+2], uint16(newLen))
			copy(base[d.fixedSize+2:], newTailData)
		}
	}

	return base
}

// readVarTail extracts the variable-length tail from a snapshot.
// Returns the tail data (after the uint16 length prefix) and the length value.
func (d *DeltaEncoder) readVarTail(snap []byte) ([]byte, uint16) {
	if len(snap) < d.fixedSize+2 {
		return nil, 0
	}
	length := binary.BigEndian.Uint16(snap[d.fixedSize : d.fixedSize+2])
	start := d.fixedSize + 2
	end := start + int(length)
	if end > len(snap) {
		return snap[start:], uint16(len(snap) - start)
	}
	return snap[start:end], length
}
