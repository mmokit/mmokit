package universe

import "encoding/binary"

// ActionHandoffAccepted is the engine-reserved opcode that carries a
// MsgHandoffAccepted over the wire inside the existing
// meshpb.CrossCellAction oneof variant. Like ActionTypedMessage (100) it
// uses a high constant value to avoid collision with game-defined
// ActionTypes (which start at 1; no game defines any today).
//
// This opcode is a WIRE CARRIER ONLY. mesh_frame_codec.go intercepts it on
// both encode and decode, so it never reaches Stage.HandleEngineAction and
// no CrossCellAction with this type is ever surfaced to a Stage. The reason
// it exists instead of a dedicated meshpb.HandoffAccepted message is the
// repository's proto convention: fields are never reserved and are
// renumbered from 1 on change, which makes every .proto edit a whole-cluster
// lockstep redeploy. Tunnelling through an existing variant keeps the
// acceptance protocol deployable incrementally.
const ActionHandoffAccepted ActionType = 101

// handoffAckPayloadSize is the fixed encoded length of a HandoffAckPayload
// tail: Epoch (u32 LE) + CommitTick (u64 LE). NetID rides in
// CrossCellAction.TargetNetId rather than the payload.
const handoffAckPayloadSize = 12

// encodeHandoffAck serializes the epoch/commit-tick tail of a
// HandoffAckPayload. NetID is carried by CrossCellAction.TargetNetId.
func encodeHandoffAck(a *HandoffAckPayload) []byte {
	buf := make([]byte, handoffAckPayloadSize)
	binary.LittleEndian.PutUint32(buf[0:4], a.Epoch)
	binary.LittleEndian.PutUint64(buf[4:12], a.CommitTick)
	return buf
}

// decodeHandoffAck rebuilds a HandoffAckPayload from the CrossCellAction
// target netID and its payload tail. Returns false on any length other than
// handoffAckPayloadSize so a malformed frame fails loudly at the codec
// boundary instead of arming a demote against a garbage commit tick.
func decodeHandoffAck(netID uint32, payload []byte) (*HandoffAckPayload, bool) {
	if len(payload) != handoffAckPayloadSize {
		return nil, false
	}
	return &HandoffAckPayload{
		NetID:      netID,
		Epoch:      binary.LittleEndian.Uint32(payload[0:4]),
		CommitTick: binary.LittleEndian.Uint64(payload[4:12]),
	}, true
}
