package universe

import (
	"encoding/binary"
	"reflect"

	"github.com/mmokit/mmokit/pkg/metrics"
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
// unknown, or the body fails to decode, the entry is logged and skipped — a
// single bad entry doesn't abort the rest of the payload. A truncated body
// (declared body_len exceeds remaining bytes) is logged and stops parsing for
// the rest of the payload, since the stream alignment can no longer be trusted.
//
// playerNetID is the NetID of the player entity associated with the
// connection; resolved upstream by the caller.
//
// Channel 0x00 carries every typed-event entry. The production drain
// path lives on Stage.dispatchInboundEventFrame, which adds
// entity-alive validation before invoking handlers. This standalone
// helper is retained as a low-level utility for tests and future call
// sites that pre-resolve the netID.
//
// Trust contract: same as the existing dispatchOneClientInput path —
// the framework guarantees the playerNetID is resolved against an
// active session by the caller; field-value validation, player-state
// gating, and rate limits remain handler responsibility.
func DispatchInboundEventFrame(stage *Stage, playerNetID uint32, payload []byte) {
	if stage == nil {
		return
	}
	lim := stage.clientWireLimits()
	off := 0
	for off+8 <= len(payload) {
		typeID := binary.LittleEndian.Uint32(payload[off : off+4])
		bodyLen := binary.LittleEndian.Uint32(payload[off+4 : off+8])
		off += 8
		if int(bodyLen) > len(payload)-off {
			metrics.Ingress().RecordRejected(metrics.SurfaceClient, metrics.ReasonTruncated)
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
			metrics.Ingress().RecordRejected(metrics.SurfaceClient, metrics.ReasonUnknownTypeID)
			if stage.eng != nil {
				stage.eng.Log.Log(CatClientInput,
					"[%s] DispatchInboundEventFrame: unknown typeID %#x (no HandleClient registration)",
					stage.cellID, typeID)
			}
			continue
		}
		msgPtr := reflect.New(t)
		// Strict, under the stage's client profile: bodyLen already fixed how
		// many bytes this entry owns, so anything left over after t is decoded
		// means the client packed a different type than typeID names.
		if err := ReflectUnmarshalStrict(stage, body, msgPtr.Interface(), lim); err != nil {
			// Skip the entry but keep walking: bodyLen was honored above, so
			// the stream is still aligned even though this body is not a
			// well-formed t. Invoking on a partial decode would hand the
			// handler a message whose refused fields look like deliberate
			// zeros.
			if stage.eng != nil {
				stage.eng.Log.Log(CatClientInput,
					"[%s] DispatchInboundEventFrame: dropping typeID %#x (%s): %v",
					stage.cellID, typeID, t.String(), err)
			}
			continue
		}
		stage.Dispatcher().Invoke(playerNetID, msgPtr.Interface())
	}
}
