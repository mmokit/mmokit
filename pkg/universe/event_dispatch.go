package universe

import (
	"encoding/binary"
	"reflect"
)

// DispatchInboundEventFrame consumes a payload (frame body, channel byte
// already stripped) carrying one or more typed-event entries:
//
//	[typeID:u32 LE][body_len:u32 LE][body] repeated
//
// For each entry, the typeID is looked up in the client-input registry
// (populated by mmokit.HandleClient) via the ClientInputHooks indirection;
// if registered, the body is decoded via the reflection codec and routed to
// the matching handler through stage.Dispatcher().Invoke. If the typeID is
// unknown, the entry is logged and skipped — a single bad entry doesn't
// abort the rest of the payload. A truncated body (declared body_len
// exceeds remaining bytes) is logged and stops parsing for the rest of the
// payload, since the stream alignment can no longer be trusted.
//
// playerNetID is the NetID of the player entity associated with the
// connection; resolved upstream by the read loop (gateway/host VCM lookup
// against the active session).
//
// Dormant until phase 5 of the events-channel-redesign plan: phases 1–4
// don't wire any caller. Phase 5 retires ChannelClientInput (0x02) and
// routes inbound 0x00 typed-event frames through this entry point so the
// channel-byte flip is a one-line forwarding change in gateway.go and
// virtual_conn_manager.go.
//
// Trust contract: same as the existing dispatchOneClientInput path —
// the framework guarantees the playerNetID is resolved against an
// active session by the caller; field-value validation, player-state
// gating, and rate limits remain handler responsibility.
func DispatchInboundEventFrame(stage *Stage, playerNetID uint32, payload []byte) {
	if stage == nil {
		return
	}
	off := 0
	for off+8 <= len(payload) {
		typeID := binary.LittleEndian.Uint32(payload[off : off+4])
		bodyLen := binary.LittleEndian.Uint32(payload[off+4 : off+8])
		off += 8
		if int(bodyLen) > len(payload)-off {
			if stage.eng != nil {
				stage.eng.Log.Log(CatClientInput,
					"[%s] DispatchInboundEventFrame: truncated body for typeID %#x (declared=%d remaining=%d)",
					stage.cellID, typeID, bodyLen, len(payload)-off)
			}
			return
		}
		body := payload[off : off+int(bodyLen)]
		off += int(bodyLen)

		// Universe cannot import mmokit — go through the pre-wired
		// ClientInputHooks indirection populated in pkg/mmokit/init.go.
		if ClientInputHooks.TypeOfTypeID == nil {
			if stage.eng != nil {
				stage.eng.Log.Log(CatClientInput,
					"[%s] DispatchInboundEventFrame: ClientInputHooks not wired (universe-only test?)",
					stage.cellID)
			}
			continue
		}
		t := ClientInputHooks.TypeOfTypeID(typeID)
		if t == nil {
			if stage.eng != nil {
				stage.eng.Log.Log(CatClientInput,
					"[%s] DispatchInboundEventFrame: unknown typeID %#x (no HandleClient registration)",
					stage.cellID, typeID)
			}
			continue
		}
		msgPtr := reflect.New(t)
		ReflectUnmarshalOnStage(stage, body, msgPtr.Interface())
		stage.Dispatcher().Invoke(playerNetID, msgPtr.Interface())
	}
}
