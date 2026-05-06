package universe

import (
	"encoding/binary"

	pkgnet "github.com/zenion/mmoserver/pkg/net"
)

// EncodeTypedEventFrame produces a single-event 0x00 frame:
//
//	[0x00][typeID:u32 BE][body_len:u32 BE][body]
func EncodeTypedEventFrame(typeID uint32, body []byte) []byte {
	frame := make([]byte, 1+4+4+len(body))
	frame[0] = pkgnet.ChannelEvent
	binary.BigEndian.PutUint32(frame[1:5], typeID)
	binary.BigEndian.PutUint32(frame[5:9], uint32(len(body)))
	copy(frame[9:], body)
	return frame
}

// EncodeBatchedTypedEventFrame packs multiple events into a single 0x00 frame.
// Returns nil for an empty list — callers must skip writing empty frames.
func EncodeBatchedTypedEventFrame(events []BroadcastEvent) []byte {
	if len(events) == 0 {
		return nil
	}
	total := 1
	for _, e := range events {
		total += 4 + 4 + len(e.Body)
	}
	frame := make([]byte, total)
	frame[0] = pkgnet.ChannelEvent
	off := 1
	for _, e := range events {
		binary.BigEndian.PutUint32(frame[off:off+4], e.TypeID)
		off += 4
		binary.BigEndian.PutUint32(frame[off:off+4], uint32(len(e.Body)))
		off += 4
		copy(frame[off:off+len(e.Body)], e.Body)
		off += len(e.Body)
	}
	return frame
}
