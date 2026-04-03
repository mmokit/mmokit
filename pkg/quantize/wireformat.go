package quantize

import (
	"encoding/binary"
	"math"
)

// Delta World Update binary wire format.
//
// Header (24 bytes):
//   [4] tick        (uint32 big-endian)
//   [4] seq         (uint32 big-endian) — frame sequence for client ack
//   [4] viewerX     (float32 big-endian) — viewer's absolute position
//   [4] viewerY     (float32 big-endian) — viewer's absolute position
//   [2] fullCount   (uint16 big-endian)
//   [2] deltaCount  (uint16 big-endian)
//   [2] removedCount(uint16 big-endian)
//   [2] exitedCount (uint16 big-endian)
//
// Full entities (fullCount):
//   [4] netID       (uint32)
//   [1] entityType  (uint8)
//   [2] snapshotLen (uint16)
//   [N] snapshot bytes
//   [2] initialLen  (uint16, 0 if none)
//   [M] initial data bytes
//
// Delta entities (deltaCount):
//   [4] netID       (uint32)
//   [1] entityType  (uint8)
//   [2] deltaLen    (uint16)
//   [D] bitmask + changed fields
//
// Removed IDs: [4] * removedCount
// Exited IDs:  [4] * exitedCount

const frameHeaderSize = 24

// FrameHeader is the decoded header of a delta world update frame.
type FrameHeader struct {
	Tick         uint32
	Seq          uint32
	ViewerX      float32 // viewer's absolute position
	ViewerY      float32
	FullCount    uint16
	DeltaCount   uint16
	RemovedCount uint16
	ExitedCount  uint16
}

// FullEntry is a decoded full-snapshot entity from the wire format.
type FullEntry struct {
	NetID       uint32
	EntityType  uint8
	Snapshot    []byte
	InitialData []byte // nil if length was 0
}

// DeltaEntry is a decoded delta-encoded entity from the wire format.
type DeltaEntry struct {
	NetID      uint32
	EntityType uint8
	Data       []byte // bitmask + changed fields
}

// EncodeFrame builds a binary delta world update frame.
type FrameEncoder struct {
	buf []byte
}

// NewFrameEncoder creates a frame encoder with a pre-allocated buffer.
func NewFrameEncoder(initialCap int) *FrameEncoder {
	return &FrameEncoder{buf: make([]byte, 0, initialCap)}
}

// Encode builds the complete binary frame.
func (e *FrameEncoder) Encode(
	tick, seq uint32,
	viewerX, viewerY float32,
	full []FullEntry,
	deltas []DeltaEntry,
	removed []uint32,
	exited []uint32,
) []byte {
	e.buf = e.buf[:0]

	// Header.
	e.buf = e.appendUint32(e.buf, tick)
	e.buf = e.appendUint32(e.buf, seq)
	e.buf = e.appendFloat32(e.buf, viewerX)
	e.buf = e.appendFloat32(e.buf, viewerY)
	e.buf = e.appendUint16(e.buf, uint16(len(full)))
	e.buf = e.appendUint16(e.buf, uint16(len(deltas)))
	e.buf = e.appendUint16(e.buf, uint16(len(removed)))
	e.buf = e.appendUint16(e.buf, uint16(len(exited)))

	// Full entities.
	for i := range full {
		f := &full[i]
		e.buf = e.appendUint32(e.buf, f.NetID)
		e.buf = append(e.buf, f.EntityType)
		e.buf = e.appendUint16(e.buf, uint16(len(f.Snapshot)))
		e.buf = append(e.buf, f.Snapshot...)
		if len(f.InitialData) > 0 {
			e.buf = e.appendUint16(e.buf, uint16(len(f.InitialData)))
			e.buf = append(e.buf, f.InitialData...)
		} else {
			e.buf = e.appendUint16(e.buf, 0)
		}
	}

	// Delta entities.
	for i := range deltas {
		d := &deltas[i]
		e.buf = e.appendUint32(e.buf, d.NetID)
		e.buf = append(e.buf, d.EntityType)
		e.buf = e.appendUint16(e.buf, uint16(len(d.Data)))
		e.buf = append(e.buf, d.Data...)
	}

	// Removed IDs.
	for _, id := range removed {
		e.buf = e.appendUint32(e.buf, id)
	}

	// Exited IDs.
	for _, id := range exited {
		e.buf = e.appendUint32(e.buf, id)
	}

	return e.buf
}

func (e *FrameEncoder) appendFloat32(b []byte, v float32) []byte {
	bits := math.Float32bits(v)
	return append(b, byte(bits>>24), byte(bits>>16), byte(bits>>8), byte(bits))
}

func (e *FrameEncoder) appendUint16(b []byte, v uint16) []byte {
	return append(b, byte(v>>8), byte(v))
}

func (e *FrameEncoder) appendUint32(b []byte, v uint32) []byte {
	return append(b, byte(v>>24), byte(v>>16), byte(v>>8), byte(v))
}

// FrameDecoder decodes a binary delta world update frame.
type FrameDecoder struct {
	data []byte
	pos  int
}

// NewFrameDecoder creates a decoder for the given binary data.
func NewFrameDecoder(data []byte) *FrameDecoder {
	return &FrameDecoder{data: data}
}

// Header decodes the frame header.
func (d *FrameDecoder) Header() FrameHeader {
	return FrameHeader{
		Tick:         d.readUint32(),
		Seq:          d.readUint32(),
		ViewerX:      d.readFloat32(),
		ViewerY:      d.readFloat32(),
		FullCount:    d.readUint16(),
		DeltaCount:   d.readUint16(),
		RemovedCount: d.readUint16(),
		ExitedCount:  d.readUint16(),
	}
}

// NextFull decodes the next full entity entry.
func (d *FrameDecoder) NextFull() FullEntry {
	netID := d.readUint32()
	entityType := d.readUint8()
	snapLen := int(d.readUint16())
	snapshot := d.readBytes(snapLen)
	initLen := int(d.readUint16())
	var initData []byte
	if initLen > 0 {
		initData = d.readBytes(initLen)
	}
	return FullEntry{
		NetID:       netID,
		EntityType:  entityType,
		Snapshot:    snapshot,
		InitialData: initData,
	}
}

// NextDelta decodes the next delta entity entry.
func (d *FrameDecoder) NextDelta() DeltaEntry {
	netID := d.readUint32()
	entityType := d.readUint8()
	deltaLen := int(d.readUint16())
	data := d.readBytes(deltaLen)
	return DeltaEntry{
		NetID:      netID,
		EntityType: entityType,
		Data:       data,
	}
}

// NextUint32 reads the next uint32 (for removed/exited IDs).
func (d *FrameDecoder) NextUint32() uint32 {
	return d.readUint32()
}

func (d *FrameDecoder) readUint8() uint8 {
	v := d.data[d.pos]
	d.pos++
	return v
}

func (d *FrameDecoder) readFloat32() float32 {
	v := math.Float32frombits(binary.BigEndian.Uint32(d.data[d.pos:]))
	d.pos += 4
	return v
}

func (d *FrameDecoder) readUint16() uint16 {
	v := binary.BigEndian.Uint16(d.data[d.pos:])
	d.pos += 2
	return v
}

func (d *FrameDecoder) readUint32() uint32 {
	v := binary.BigEndian.Uint32(d.data[d.pos:])
	d.pos += 4
	return v
}

func (d *FrameDecoder) readBytes(n int) []byte {
	v := d.data[d.pos : d.pos+n]
	d.pos += n
	return v
}
