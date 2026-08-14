package universe

import (
	"encoding/binary"
	"errors"

	pkgnet "github.com/mmokit/mmokit/pkg/net"
)

// EncodeTypedOpFrame produces a single-op 0x01 frame:
//
//	[0x01][typeID:u32 LE][request_id:u64 LE][body_len:u32 LE][body]
//
// Same shape in both directions. The receiver determines request-vs-response
// from the typeID — each operation registers a Request → Response type pair,
// so a known typeID is unambiguously one or the other.
func EncodeTypedOpFrame(typeID uint32, requestID uint64, body []byte) []byte {
	frame := make([]byte, 1+4+8+4+len(body))
	frame[0] = pkgnet.ChannelOperation
	binary.LittleEndian.PutUint32(frame[1:5], typeID)
	binary.LittleEndian.PutUint64(frame[5:13], requestID)
	binary.LittleEndian.PutUint32(frame[13:17], uint32(len(body)))
	copy(frame[17:], body)
	return frame
}

// DecodeTypedOpFrame parses a 0x01 typed-op payload (channel byte already
// stripped). Returns the typeID, request_id, and body slice (a view into
// the payload — caller must copy if needed past payload lifetime). Errors
// if the payload is structurally invalid.
func DecodeTypedOpFrame(payload []byte) (typeID uint32, requestID uint64, body []byte, err error) {
	const headerLen = 4 + 8 + 4
	if len(payload) < headerLen {
		return 0, 0, nil, errors.New("typed-op frame: truncated header")
	}
	typeID = binary.LittleEndian.Uint32(payload[0:4])
	requestID = binary.LittleEndian.Uint64(payload[4:12])
	bodyLen := binary.LittleEndian.Uint32(payload[12:16])
	if int(bodyLen) > len(payload)-headerLen {
		return 0, 0, nil, errors.New("typed-op frame: declared body_len exceeds payload")
	}
	body = payload[headerLen : headerLen+int(bodyLen)]
	return typeID, requestID, body, nil
}
