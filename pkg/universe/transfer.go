package universe

import (
	"encoding/binary"
	"fmt"
	"math"

	"github.com/google/uuid"

	"github.com/mmokit/mmokit/pkg/component"
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
	NetworkID uint32
	// Epoch travels with the entity through cell_transfer (split/merge/
	// migrate). The destination cell-transfer executor advances it once
	// when it materializes a new Live authority. Carrying the source value
	// is still required: without it, populate-side spawns would restart at
	// epoch 0 and the next border frame from the destination cell would be
	// rejected as stale by every neighbor whose highestSeenEpoch for this
	// netID is non-zero.
	// On the handoff path, SpawnLiveFromTransfer / PromoteReplicaToLive
	// override this with the authoritative HandoffPayload.Epoch, so the
	// frame value is just a baseline for non-handoff transfer paths.
	Epoch uint32
	// StreamGeneration scopes client replication-frame ordering for a player.
	// It is zero for non-player entities. Transfer paths that materialize a
	// fresh authority advance the transferred copy while leaving the source
	// session pinned to its current generation until commit.
	StreamGeneration uint32
	EntityType       uint8
	ConnID           uint32 // source node-local; destination remaps via VCM lookup
	GatewayID        string // composite session key: which gateway terminates the client WS
	GatewayConnID    uint32 // composite session key: connID on the gateway side (client-facing)
	Username         string // player username (empty for non-player entities)
	// UserID is the canonical auth identity (uuid.UUID, 16 raw bytes on the
	// wire). Set by SerializeEntityCore from the source session; the
	// destination cell.go MsgHandoff handler writes it onto the just-
	// registered session so subsequent cell-cross handoffs out of this cell
	// don't panic in PlayerRepo.Bind on a zero UserID. Zero for non-player
	// entities.
	UserID           uuid.UUID
	PosX, PosY, PosZ float32
	VelX, VelY, VelZ float32
	Rotation         component.Rotation
	Collider         component.Collider
	CellX            int32
	CellY            int32
	// DebugFlags is the bitmask of debug capabilities for the
	// session this entity belongs to. Carried in the wire format so
	// per-player debug grants survive cross-cell and cross-host
	// handoffs without a DB query on the hot path. Zero for
	// non-player entities.
	DebugFlags uint32
	Components []ComponentSlice // game-specific optional data
}

// MarshalTransferFrame encodes a TransferFrame to a compact binary format.
//
// Wire format:
//
//	[4] NetworkID
//	[4] Epoch
//	[4] StreamGeneration
//	[1] EntityType
//	[4] ConnID              // source node-local; destination remaps via VCM
//	[1] GatewayID length
//	[M] GatewayID bytes
//	[4] GatewayConnID       // client-facing connID on the gateway
//	[1] Username length
//	[N] Username bytes
//	[16] UserID (raw uuid bytes)
//	[4] PosX
//	[4] PosY
//	[4] PosZ
//	[4] VelX
//	[4] VelY
//	[4] VelZ
//	[16] Rotation (unit quaternion: x[4] + y[4] + z[4] + w[4])
//	[18] Collider (radius[4] + width[4] + height[4] + depth[4] + layer[1] + shape[1])
//	[4] CellX (int32)
//	[4] CellY (int32)
//	[4] DebugFlags (uint32)
//	[2] component count
//	per component:
//	  [2] ComponentID
//	  [2] data length
//	  [N] data bytes
func MarshalTransferFrame(f *TransferFrame) ([]byte, error) {
	if len(f.GatewayID) > 255 {
		return nil, fmt.Errorf("transfer frame: gateway id is %d bytes, maximum is 255", len(f.GatewayID))
	}
	if len(f.Username) > 255 {
		return nil, fmt.Errorf("transfer frame: username is %d bytes, maximum is 255", len(f.Username))
	}
	if len(f.Components) > 65535 {
		return nil, fmt.Errorf("transfer frame: component count is %d, maximum is 65535", len(f.Components))
	}
	for i, c := range f.Components {
		if len(c.Data) > 65535 {
			return nil, fmt.Errorf("transfer frame: component %d data is %d bytes, maximum is 65535", i, len(c.Data))
		}
	}

	// headerSize is the fixed portion — the variable lengths (Username,
	// GatewayID, component tails) are added on top.
	const headerSize = 4 + 4 + 4 + 1 + 4 + 1 + 4 + 1 + 16 + 4 + 4 + 4 + 4 + 4 + 4 + 16 + 18 + 4 + 4 + 4 + 2 // 111

	size := headerSize + len(f.Username) + len(f.GatewayID)
	for _, c := range f.Components {
		size += 2 + 2 + len(c.Data)
	}

	buf := make([]byte, size)
	off := 0

	binary.LittleEndian.PutUint32(buf[off:], f.NetworkID)
	off += 4
	binary.LittleEndian.PutUint32(buf[off:], f.Epoch)
	off += 4
	binary.LittleEndian.PutUint32(buf[off:], f.StreamGeneration)
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
	copy(buf[off:], f.UserID[:])
	off += 16
	binary.LittleEndian.PutUint32(buf[off:], math.Float32bits(f.PosX))
	off += 4
	binary.LittleEndian.PutUint32(buf[off:], math.Float32bits(f.PosY))
	off += 4
	binary.LittleEndian.PutUint32(buf[off:], math.Float32bits(f.PosZ))
	off += 4
	binary.LittleEndian.PutUint32(buf[off:], math.Float32bits(f.VelX))
	off += 4
	binary.LittleEndian.PutUint32(buf[off:], math.Float32bits(f.VelY))
	off += 4
	binary.LittleEndian.PutUint32(buf[off:], math.Float32bits(f.VelZ))
	off += 4
	binary.LittleEndian.PutUint32(buf[off:], math.Float32bits(f.Rotation.X))
	off += 4
	binary.LittleEndian.PutUint32(buf[off:], math.Float32bits(f.Rotation.Y))
	off += 4
	binary.LittleEndian.PutUint32(buf[off:], math.Float32bits(f.Rotation.Z))
	off += 4
	binary.LittleEndian.PutUint32(buf[off:], math.Float32bits(f.Rotation.W))
	off += 4

	// Collider: 14 bytes
	binary.LittleEndian.PutUint32(buf[off:], math.Float32bits(f.Collider.Radius))
	off += 4
	binary.LittleEndian.PutUint32(buf[off:], math.Float32bits(f.Collider.Width))
	off += 4
	binary.LittleEndian.PutUint32(buf[off:], math.Float32bits(f.Collider.Height))
	off += 4
	binary.LittleEndian.PutUint32(buf[off:], math.Float32bits(f.Collider.Depth))
	off += 4
	buf[off] = f.Collider.Layer
	off++
	buf[off] = byte(f.Collider.Shape)
	off++

	binary.LittleEndian.PutUint32(buf[off:], uint32(f.CellX))
	off += 4
	binary.LittleEndian.PutUint32(buf[off:], uint32(f.CellY))
	off += 4

	binary.LittleEndian.PutUint32(buf[off:], f.DebugFlags)
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
	const headerSize = 4 + 4 + 4 + 1 + 4 + 1 + 4 + 1 + 16 + 4 + 4 + 4 + 4 + 4 + 4 + 16 + 18 + 4 + 4 + 4 + 2 // 111
	if len(data) < headerSize {
		return nil, fmt.Errorf("transfer frame: need at least %d bytes, got %d", headerSize, len(data))
	}

	off := 0
	f := &TransferFrame{}

	f.NetworkID = binary.LittleEndian.Uint32(data[off:])
	off += 4
	f.Epoch = binary.LittleEndian.Uint32(data[off:])
	off += 4
	f.StreamGeneration = binary.LittleEndian.Uint32(data[off:])
	off += 4
	f.EntityType = data[off]
	off++
	f.ConnID = binary.LittleEndian.Uint32(data[off:])
	off += 4
	gwIDLen := int(data[off])
	off++
	// The initial header check covers only the fixed-size bytes. Variable
	// strings shift every subsequent field, so each length must reserve the
	// fixed tail that follows it before any binary read or slice operation.
	if off+gwIDLen+4+1 > len(data) {
		return nil, fmt.Errorf("transfer frame: truncated gateway id (need %d bytes)", gwIDLen)
	}
	f.GatewayID = string(data[off : off+gwIDLen])
	off += gwIDLen
	f.GatewayConnID = binary.LittleEndian.Uint32(data[off:])
	off += 4
	nameLen := int(data[off])
	off++
	// postUsernameSize is the SOLE bounds guard for every read after the
	// variable-length username. It must be updated in lockstep with headerSize:
	// leaving it stale under-reserves, and every subsequent unguarded
	// binary.LittleEndian.Uint32(data[off:]) becomes an out-of-bounds read on
	// peer-supplied mesh bytes. The marshal-side header test does not cover it.
	const postUsernameSize = 16 + 4 + 4 + 4 + 4 + 4 + 4 + 16 + 18 + 4 + 4 + 4 + 2
	if off+nameLen+postUsernameSize > len(data) {
		return nil, fmt.Errorf("transfer frame: truncated username (need %d bytes)", nameLen)
	}
	f.Username = string(data[off : off+nameLen])
	off += nameLen
	copy(f.UserID[:], data[off:off+16])
	off += 16
	f.PosX = math.Float32frombits(binary.LittleEndian.Uint32(data[off:]))
	off += 4
	f.PosY = math.Float32frombits(binary.LittleEndian.Uint32(data[off:]))
	off += 4
	f.PosZ = math.Float32frombits(binary.LittleEndian.Uint32(data[off:]))
	off += 4
	f.VelX = math.Float32frombits(binary.LittleEndian.Uint32(data[off:]))
	off += 4
	f.VelY = math.Float32frombits(binary.LittleEndian.Uint32(data[off:]))
	off += 4
	f.VelZ = math.Float32frombits(binary.LittleEndian.Uint32(data[off:]))
	off += 4
	f.Rotation.X = math.Float32frombits(binary.LittleEndian.Uint32(data[off:]))
	off += 4
	f.Rotation.Y = math.Float32frombits(binary.LittleEndian.Uint32(data[off:]))
	off += 4
	f.Rotation.Z = math.Float32frombits(binary.LittleEndian.Uint32(data[off:]))
	off += 4
	f.Rotation.W = math.Float32frombits(binary.LittleEndian.Uint32(data[off:]))
	off += 4

	// Collider: 14 bytes
	f.Collider.Radius = math.Float32frombits(binary.LittleEndian.Uint32(data[off:]))
	off += 4
	f.Collider.Width = math.Float32frombits(binary.LittleEndian.Uint32(data[off:]))
	off += 4
	f.Collider.Height = math.Float32frombits(binary.LittleEndian.Uint32(data[off:]))
	off += 4
	f.Collider.Depth = math.Float32frombits(binary.LittleEndian.Uint32(data[off:]))
	off += 4
	f.Collider.Layer = data[off]
	off++
	f.Collider.Shape = validShape(data[off])
	off++

	f.CellX = int32(binary.LittleEndian.Uint32(data[off:]))
	off += 4
	f.CellY = int32(binary.LittleEndian.Uint32(data[off:]))
	off += 4

	f.DebugFlags = binary.LittleEndian.Uint32(data[off:])
	off += 4

	count := int(binary.LittleEndian.Uint16(data[off:]))
	off += 2
	// Every component needs at least its four-byte ID/length header. Reject
	// impossible counts before allocating attacker-controlled capacity.
	if count > (len(data)-off)/4 {
		return nil, fmt.Errorf("transfer frame: component count %d exceeds remaining payload", count)
	}

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

// PeekTransferPlayer extracts the source's ConnID, replication stream
// generation, SessionKey fields (GatewayID, GatewayConnID), Username, and
// UserID from raw transfer bytes without performing a full deserialization.
// Returns zero values if the data is too short or the entity is not a player
// (source ConnID == 0).
//
// The destination node uses the returned SessionKey to register a VCM entry
// before SpawnLiveFromTransfer / PromoteReplicaToLive runs, so subsequent
// ClientInput frames from the gateway route to the freshly transferred
// player on the destination side. UserID is propagated onto the destination
// session so the next cell-cross handoff doesn't panic in PlayerRepo.Bind.
func PeekTransferPlayer(data []byte) (srcConnID uint32, streamGeneration uint32, gatewayID string, gatewayConnID uint32, username string, userID uuid.UUID) {
	const streamGenerationOffset = 4 + 4 // after NetworkID + Epoch
	const connIDOffset = streamGenerationOffset + 4 + 1
	// Minimum fixed layout up through Username length byte.
	if len(data) < connIDOffset+4+1+4+1 {
		return 0, 0, "", 0, "", uuid.Nil
	}
	streamGeneration = binary.LittleEndian.Uint32(data[streamGenerationOffset:])
	srcConnID = binary.LittleEndian.Uint32(data[connIDOffset:])
	if srcConnID == 0 {
		return 0, 0, "", 0, "", uuid.Nil
	}
	off := connIDOffset + 4
	gwIDLen := int(data[off])
	off++
	if off+gwIDLen+4+1 > len(data) {
		return srcConnID, streamGeneration, "", 0, "", uuid.Nil
	}
	gatewayID = string(data[off : off+gwIDLen])
	off += gwIDLen
	gatewayConnID = binary.LittleEndian.Uint32(data[off:])
	off += 4
	nameLen := int(data[off])
	off++
	if off+nameLen+16 > len(data) {
		return srcConnID, streamGeneration, gatewayID, gatewayConnID, "", uuid.Nil
	}
	username = string(data[off : off+nameLen])
	off += nameLen
	copy(userID[:], data[off:off+16])
	return srcConnID, streamGeneration, gatewayID, gatewayConnID, username, userID
}
