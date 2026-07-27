package mmokit

import (
	"reflect"
	"time"

	"github.com/zenion/mmoserver/pkg/component"
	pkguniverse "github.com/zenion/mmoserver/pkg/universe"
)

// init wires:
//
//  1. The universe-side BroadcastHooks callbacks that let Stage.maybeBroadcast
//     consult the mmokit-owned broadcast registry (eligibility, typeID,
//     anchor extraction) without an import cycle.
//  2. The universe-side ClientInputHooks callbacks that let the gateway-
//     side dispatchClientInput phase consult the mmokit-owned
//     HandleClient registry (eligibility, typeID, typeID→Type lookup)
//     without an import cycle.
//  3. The universe-side ServerEventHooks callbacks that let
//     universe.SendEventTyped consult the mmokit-owned server-event
//     registry (RegisterEvent[T]) without an import cycle.
//  4. The universe-side TypedOpHooks callbacks that let
//     universe.DispatchTypedOpInbound consult the mmokit-owned
//     typed-op registry (RegisterOp[Req, Res]) without an import
//     cycle.
//
// All universe.Process instances built after package init see all
// wirings. Tests that build engines/stages outside the mmokit stack and
// never import mmokit get nil hooks: BroadcastHooks and ClientInputHooks
// silent no-op, but ServerEventHooks panics in SendEventTyped — silently
// dropping a typed event is worse than failing loudly, so unregistered
// (or unwired) types must surface as a panic. TypedOpHooks unwired
// returns an "unknown typeID" OperationError for every inbound 0x01
// typed-op frame, since no handlers can be registered without mmokit.
func init() {
	pkguniverse.BroadcastHooks.Eligible = brIsRegistered
	pkguniverse.BroadcastHooks.TypeIDOf = TypeIDOf
	pkguniverse.BroadcastHooks.ExtractAnchors = func(msgPtr any, targetNetID uint32, stage *pkguniverse.Stage) []uint32 {
		return ExtractAnchors(msgPtr, EntityByNetID(stage, targetNetID))
	}

	pkguniverse.ClientInputHooks.IsRegistered = ciIsRegistered
	pkguniverse.ClientInputHooks.TypeIDOf = TypeIDOf
	pkguniverse.ClientInputHooks.TypeOfTypeID = ciTypeOfTypeID

	pkguniverse.ServerEventHooks.IsRegistered = func(t reflect.Type) bool {
		_, ok := LookupServerEventType(TypeIDOf(t))
		return ok
	}
	pkguniverse.ServerEventHooks.TypeIDOf = TypeIDOf

	// Engine-default typed events. Universe + engine call these helpers
	// to emit the framework events without importing pkg/mmokit. Register
	// the types here at package-init time so the build helpers are usable
	// before any NewProtocol call (the engine's PlayerManager.OnConnect
	// hook may fire before user code runs NewProtocol).
	registerEngineTypedEvents()
	pkguniverse.EngineDefaultFrameHooks.PlayerEntityAssigned = func(netID uint32, worldX, worldY float32) []byte {
		return pkguniverse.BuildTypedEventFrameRaw(&PlayerEntityAssigned{
			EntityNetID: netID,
			WorldX:      worldX,
			WorldY:      worldY,
		})
	}
	pkguniverse.EngineDefaultFrameHooks.ServerConfig = func(tickRate uint32) []byte {
		return pkguniverse.BuildTypedEventFrameRaw(&ServerConfig{TickRate: tickRate})
	}

	// Engine-default HandleClient handlers — installed once per Process by
	// universe.New via this hook. Further engine-default client inputs would
	// register here.
	pkguniverse.EngineDefaultClientHandlers = func(p *pkguniverse.Process) {
		HandleClient(p, func(player Entity, msg *Ping) {
			conn := Get[component.PlayerConn](player)
			if conn == nil {
				return
			}
			SendEvent(player.Stage(), conn.ConnID, &Pong{
				ClientTime: msg.ClientTime,
				ServerTime: time.Now().UnixMilli(),
			})
		})

		// Explicit replication frame ACK. Dispatch is per-Stage and resolves
		// the handler against the player entity owned by the sending
		// connection on THAT stage, so the ack always reaches the
		// ReplicationSystem of the cell that currently holds authority.
		HandleClient(p, func(player Entity, msg *ReplicationAck) {
			conn := Get[component.PlayerConn](player)
			if conn == nil {
				return
			}
			stage := player.Stage()
			if stage == nil {
				return
			}
			if eng := stage.Engine(); eng != nil {
				eng.AckReplicationFrame(conn.ConnID, msg.StreamEpoch, msg.Seq)
			}
		})
	}

	pkguniverse.TypedOpHooks.LookupTypedOp = func(reqTypeID uint32) (uint8, reflect.Type, reflect.Type, uint32, any, bool) {
		e, ok := LookupTypedOp(reqTypeID)
		if !ok {
			return 0, nil, nil, 0, nil, false
		}
		return uint8(e.Kind), e.RequestType, e.ResponseType, e.ResponseTypeID, e.Handler, true
	}
	pkguniverse.TypedOpHooks.ListTypedOps = func() []pkguniverse.TypedOpInfo {
		entries := RegisteredTypedOps()
		out := make([]pkguniverse.TypedOpInfo, 0, len(entries))
		for _, e := range entries {
			out = append(out, pkguniverse.TypedOpInfo{
				Kind:         uint8(e.Kind),
				KindName:     e.Kind.String(),
				RequestType:  e.RequestType,
				ResponseType: e.ResponseType,
				RequestID:    TypeIDOf(e.RequestType),
			})
		}
		return out
	}
	pkguniverse.TypedOpHooks.OperationErrorTypeID = TypeIDOf(reflect.TypeFor[OperationError]())
	pkguniverse.TypedOpHooks.MakeOperationErrorBody = func(code uint32, message string) []byte {
		body, err := pkguniverse.ReflectMarshal(&OperationError{Code: code, Message: message})
		if err != nil {
			// Only reachable when a handler's error string exceeds the u16
			// wire prefix. Returning nil would drop the response and leave the
			// client blocked on its requestID, so re-encode with the message
			// replaced rather than the frame lost.
			body, err = pkguniverse.ReflectMarshal(&OperationError{
				Code:    code,
				Message: "error message too large for the wire",
			})
			if err != nil {
				return nil
			}
		}
		return body
	}
	pkguniverse.TypedOpHooks.RouteGatewayLocal = uint8(RouteGatewayLocal)
}
