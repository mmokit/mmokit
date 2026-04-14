package universe

import (
	"encoding/binary"
	"fmt"
	"math"

	"github.com/zenion/mmoserver/pkg/component"
)

// TransferFrame is the generic wire format for entity transfers between nodes.
// Core fields (position, velocity, rotation, collider, cell, IDs) are always
// present. Game-specific data is carried in the Components slice.
//
// ConnID is the SOURCE node's local ID at serialization time; the destination
// must remap it to its own local ID at decode time by consulting the VCM with
// the carried SessionKey (GatewayID + GatewayConnID). SpawnFromTransferCore
// performs this remap automatically when the destination is in node mode.
// Without the SessionKey, a cross-host handoff leaves the destination unable
// to route client inputs (gateway→node) or outputs (node→gateway) for the
// transferred player — the player appears "stuck" on the new cell.
type TransferFrame struct {
	NetworkID     uint32
	EntityType    uint8
	ConnID        uint32 // source node-local; destination remaps via VCM lookup
	GatewayID     string // composite session key: which gateway terminates the client WS
	GatewayConnID uint32 // composite session key: connID on the gateway side (client-facing)
	Username      string // player username (empty for non-player entities)
	PosX, PosY    float32
	VelX, VelY    float32
	Rotation      float32
	Collider      component.Collider
	CellX         int32
	CellY         int32
	Components    []ComponentSlice // game-specific optional data
}

// MarshalTransferFrame encodes a TransferFrame to a compact binary format.
//
// Wire format:
//
//	[4] NetworkID
//	[1] EntityType
//	[4] ConnID              // source node-local; destination remaps via VCM
//	[1] GatewayID length
//	[M] GatewayID bytes
//	[4] GatewayConnID       // client-facing connID on the gateway
//	[1] Username length
//	[N] Username bytes
//	[4] PosX
//	[4] PosY
//	[4] VelX
//	[4] VelY
//	[4] Rotation
//	[14] Collider (radius[4] + width[4] + height[4] + layer[1] + shape[1])
//	[4] CellX (int32)
//	[4] CellY (int32)
//	[2] component count
//	per component:
//	  [2] ComponentID
//	  [2] data length
//	  [N] data bytes
func MarshalTransferFrame(f *TransferFrame) ([]byte, error) {
	// headerSize is the fixed portion — the variable lengths (Username,
	// GatewayID, component tails) are added on top.
	const headerSize = 4 + 1 + 4 + 1 + 4 + 1 + 4 + 4 + 4 + 4 + 4 + 14 + 4 + 4 + 2 // 59

	size := headerSize + len(f.Username) + len(f.GatewayID)
	for _, c := range f.Components {
		size += 2 + 2 + len(c.Data)
	}

	buf := make([]byte, size)
	off := 0

	binary.LittleEndian.PutUint32(buf[off:], f.NetworkID)
	off += 4
	buf[off] = f.EntityType
	off++
	binary.LittleEndian.PutUint32(buf[off:], f.ConnID)
	off += 4
	buf[off] = uint8(len(f.GatewayID))
	off++
	copy(buf[off:], f.GatewayID)
	off += len(f.GatewayID)
	binary.LittleEndian.PutUint32(buf[off:], f.GatewayConnID)
	off += 4
	buf[off] = uint8(len(f.Username))
	off++
	copy(buf[off:], f.Username)
	off += len(f.Username)
	binary.LittleEndian.PutUint32(buf[off:], math.Float32bits(f.PosX))
	off += 4
	binary.LittleEndian.PutUint32(buf[off:], math.Float32bits(f.PosY))
	off += 4
	binary.LittleEndian.PutUint32(buf[off:], math.Float32bits(f.VelX))
	off += 4
	binary.LittleEndian.PutUint32(buf[off:], math.Float32bits(f.VelY))
	off += 4
	binary.LittleEndian.PutUint32(buf[off:], math.Float32bits(f.Rotation))
	off += 4

	// Collider: 14 bytes
	binary.LittleEndian.PutUint32(buf[off:], math.Float32bits(f.Collider.Radius))
	off += 4
	binary.LittleEndian.PutUint32(buf[off:], math.Float32bits(f.Collider.Width))
	off += 4
	binary.LittleEndian.PutUint32(buf[off:], math.Float32bits(f.Collider.Height))
	off += 4
	buf[off] = f.Collider.Layer
	off++
	buf[off] = f.Collider.Shape
	off++

	binary.LittleEndian.PutUint32(buf[off:], uint32(f.CellX))
	off += 4
	binary.LittleEndian.PutUint32(buf[off:], uint32(f.CellY))
	off += 4

	binary.LittleEndian.PutUint16(buf[off:], uint16(len(f.Components)))
	off += 2

	for _, c := range f.Components {
		binary.LittleEndian.PutUint16(buf[off:], uint16(c.ID))
		off += 2
		binary.LittleEndian.PutUint16(buf[off:], uint16(len(c.Data)))
		off += 2
		copy(buf[off:], c.Data)
		off += len(c.Data)
	}

	return buf, nil
}

// UnmarshalTransferFrame decodes a TransferFrame from binary data.
func UnmarshalTransferFrame(data []byte) (*TransferFrame, error) {
	const headerSize = 4 + 1 + 4 + 1 + 4 + 1 + 4 + 4 + 4 + 4 + 4 + 14 + 4 + 4 + 2 // 59
	if len(data) < headerSize {
		return nil, fmt.Errorf("transfer frame: need at least %d bytes, got %d", headerSize, len(data))
	}

	off := 0
	f := &TransferFrame{}

	f.NetworkID = binary.LittleEndian.Uint32(data[off:])
	off += 4
	f.EntityType = data[off]
	off++
	f.ConnID = binary.LittleEndian.Uint32(data[off:])
	off += 4
	gwIDLen := int(data[off])
	off++
	if off+gwIDLen > len(data) {
		return nil, fmt.Errorf("transfer frame: truncated gateway id (need %d bytes)", gwIDLen)
	}
	f.GatewayID = string(data[off : off+gwIDLen])
	off += gwIDLen
	f.GatewayConnID = binary.LittleEndian.Uint32(data[off:])
	off += 4
	nameLen := int(data[off])
	off++
	if off+nameLen > len(data) {
		return nil, fmt.Errorf("transfer frame: truncated username (need %d bytes)", nameLen)
	}
	f.Username = string(data[off : off+nameLen])
	off += nameLen
	f.PosX = math.Float32frombits(binary.LittleEndian.Uint32(data[off:]))
	off += 4
	f.PosY = math.Float32frombits(binary.LittleEndian.Uint32(data[off:]))
	off += 4
	f.VelX = math.Float32frombits(binary.LittleEndian.Uint32(data[off:]))
	off += 4
	f.VelY = math.Float32frombits(binary.LittleEndian.Uint32(data[off:]))
	off += 4
	f.Rotation = math.Float32frombits(binary.LittleEndian.Uint32(data[off:]))
	off += 4

	// Collider: 14 bytes
	f.Collider.Radius = math.Float32frombits(binary.LittleEndian.Uint32(data[off:]))
	off += 4
	f.Collider.Width = math.Float32frombits(binary.LittleEndian.Uint32(data[off:]))
	off += 4
	f.Collider.Height = math.Float32frombits(binary.LittleEndian.Uint32(data[off:]))
	off += 4
	f.Collider.Layer = data[off]
	off++
	f.Collider.Shape = data[off]
	off++

	f.CellX = int32(binary.LittleEndian.Uint32(data[off:]))
	off += 4
	f.CellY = int32(binary.LittleEndian.Uint32(data[off:]))
	off += 4

	count := int(binary.LittleEndian.Uint16(data[off:]))
	off += 2

	f.Components = make([]ComponentSlice, 0, count)
	for i := 0; i < count; i++ {
		if off+4 > len(data) {
			return nil, fmt.Errorf("transfer frame: truncated component header at index %d", i)
		}
		id := ComponentID(binary.LittleEndian.Uint16(data[off:]))
		off += 2
		dlen := int(binary.LittleEndian.Uint16(data[off:]))
		off += 2
		if off+dlen > len(data) {
			return nil, fmt.Errorf("transfer frame: truncated component data at index %d", i)
		}
		f.Components = append(f.Components, ComponentSlice{
			ID:   id,
			Data: data[off : off+dlen],
		})
		off += dlen
	}

	return f, nil
}

// PeekTransferPlayer extracts the source's ConnID, the SessionKey fields
// (GatewayID, GatewayConnID), and Username from raw transfer bytes without
// performing a full deserialization. Returns zero values if the data is too
// short or the entity is not a player (source ConnID == 0).
//
// The destination node uses the returned SessionKey to register a VCM entry
// before SpawnShadow runs, so subsequent ClientInput frames from the gateway
// route to the freshly transferred player on the destination side.
func PeekTransferPlayer(data []byte) (srcConnID uint32, gatewayID string, gatewayConnID uint32, username string) {
	const connIDOffset = 4 + 1 // after NetworkID + EntityType
	// Minimum fixed layout up through Username length byte.
	if len(data) < connIDOffset+4+1+4+1 {
		return 0, "", 0, ""
	}
	srcConnID = binary.LittleEndian.Uint32(data[connIDOffset:])
	if srcConnID == 0 {
		return 0, "", 0, ""
	}
	off := connIDOffset + 4 // past NetworkID + EntityType + ConnID
	gwIDLen := int(data[off])
	off++
	if off+gwIDLen+4+1 > len(data) {
		return srcConnID, "", 0, ""
	}
	gatewayID = string(data[off : off+gwIDLen])
	off += gwIDLen
	gatewayConnID = binary.LittleEndian.Uint32(data[off:])
	off += 4
	nameLen := int(data[off])
	off++
	if off+nameLen > len(data) {
		return srcConnID, gatewayID, gatewayConnID, ""
	}
	username = string(data[off : off+nameLen])
	return srcConnID, gatewayID, gatewayConnID, username
}
