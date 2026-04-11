package replication

import (
	"encoding/binary"
	"errors"
)

// NetID mirrors component.NetworkID without creating an import cycle.
// The replication package must stay a leaf (no internal imports) so
// both pkg/system and pkg/universe can depend on it. Callers convert
// component.NetworkID into this local struct at the boundary.
type NetID struct {
	ID    uint32
	Epoch uint32
}

// Frame is a batch of replication updates from one sender to one viewer
// for one tick. Used both for client-facing frames (viewer = player conn)
// and for inter-node border frames (viewer = neighbor node).
type Frame struct {
	ViewerID   uint64
	SenderNode uint32
	Tick       uint64
	Entries    []FrameEntry
}

// FrameEntry is one entity's delta in a Frame.
type FrameEntry struct {
	NetID    NetID
	Kind     uint16
	DeltaBuf []byte
}

// Wire format (little-endian):
//
//   Header (24 bytes):
//     [8]  ViewerID
//     [4]  SenderNode
//     [8]  Tick
//     [4]  EntryCount
//
//   For each entry (14 + N bytes):
//     [4]  NetID.ID
//     [4]  NetID.Epoch
//     [2]  Kind
//     [4]  DeltaBuf length (uint32, allows payloads up to 4 GiB)
//     [N]  DeltaBuf bytes

const (
	frameHeaderBytes     = 24
	frameEntryFixedBytes = 14
)

// SizeEncoded returns the exact number of bytes Encode() will produce
// without running the encode. Used by metrics and buffer pre-allocation.
func (f *Frame) SizeEncoded() int {
	n := frameHeaderBytes
	for i := range f.Entries {
		n += frameEntryFixedBytes + len(f.Entries[i].DeltaBuf)
	}
	return n
}

// Encode serializes the Frame to a fresh byte slice. The slice is owned
// by the caller — Encode does not retain or reuse buffers.
func (f *Frame) Encode() []byte {
	buf := make([]byte, f.SizeEncoded())
	pos := 0
	binary.LittleEndian.PutUint64(buf[pos:], f.ViewerID)
	pos += 8
	binary.LittleEndian.PutUint32(buf[pos:], f.SenderNode)
	pos += 4
	binary.LittleEndian.PutUint64(buf[pos:], f.Tick)
	pos += 8
	binary.LittleEndian.PutUint32(buf[pos:], uint32(len(f.Entries)))
	pos += 4
	for i := range f.Entries {
		e := &f.Entries[i]
		binary.LittleEndian.PutUint32(buf[pos:], e.NetID.ID)
		pos += 4
		binary.LittleEndian.PutUint32(buf[pos:], e.NetID.Epoch)
		pos += 4
		binary.LittleEndian.PutUint16(buf[pos:], e.Kind)
		pos += 2
		binary.LittleEndian.PutUint32(buf[pos:], uint32(len(e.DeltaBuf)))
		pos += 4
		copy(buf[pos:], e.DeltaBuf)
		pos += len(e.DeltaBuf)
	}
	return buf
}

// DecodeFrame parses a Frame from wire bytes. Returns an error on truncation.
// Decoded byte slices are freshly allocated copies — the caller may retain
// them safely after the source buffer is reused.
func DecodeFrame(data []byte) (Frame, error) {
	if len(data) < frameHeaderBytes {
		return Frame{}, errors.New("replication: frame too short for header")
	}
	var f Frame
	f.ViewerID = binary.LittleEndian.Uint64(data[0:8])
	f.SenderNode = binary.LittleEndian.Uint32(data[8:12])
	f.Tick = binary.LittleEndian.Uint64(data[12:20])
	count := binary.LittleEndian.Uint32(data[20:24])
	pos := 24
	// DoS guard: a valid frame with `count` entries requires at least
	// count * frameEntryFixedBytes remaining bytes (plus zero-length deltas).
	// Reject counts that can't possibly fit before allocating.
	remaining := uint64(len(data) - pos)
	if uint64(count)*uint64(frameEntryFixedBytes) > remaining {
		return Frame{}, errors.New("replication: frame entry count exceeds remaining bytes")
	}
	if count > 0 {
		f.Entries = make([]FrameEntry, 0, count)
	}
	for i := uint32(0); i < count; i++ {
		if pos+frameEntryFixedBytes > len(data) {
			return Frame{}, errors.New("replication: frame truncated mid-entry")
		}
		var e FrameEntry
		e.NetID.ID = binary.LittleEndian.Uint32(data[pos : pos+4])
		e.NetID.Epoch = binary.LittleEndian.Uint32(data[pos+4 : pos+8])
		e.Kind = binary.LittleEndian.Uint16(data[pos+8 : pos+10])
		dlen := binary.LittleEndian.Uint32(data[pos+10 : pos+14])
		pos += frameEntryFixedBytes
		if dlen > uint32(len(data)-pos) {
			return Frame{}, errors.New("replication: frame truncated in delta payload")
		}
		if dlen > 0 {
			e.DeltaBuf = make([]byte, dlen)
			copy(e.DeltaBuf, data[pos:pos+int(dlen)])
		}
		pos += int(dlen)
		f.Entries = append(f.Entries, e)
	}
	return f, nil
}
