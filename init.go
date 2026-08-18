package mmokit

import (
	"reflect"
	"time"

	"github.com/mmokit/mmokit/pkg/component"
	pkguniverse "github.com/mmokit/mmokit/pkg/universe"
)

// init installs the two things pkg/universe cannot do for itself, now that
// the wire registries live down there rather than up here.
//
// This file used to be five hook-struct wirings — thirteen callbacks whose
// only purpose was to let a package-global registry in pkg/universe call back
// up into the facade that owned the maps. The maps moved down; twelve of the
// thirteen callbacks were answers to questions pkg/universe can now ask
// directly, and are gone.
//
// What remains is the half that genuinely points the other way:
//
//  1. FrameworkEncoders — the framework's own wire types (OperationError,
//     PlayerEntityAssigned, ServerConfig) are declared in THIS package, and
//     their wire identity is fnv32a("mmokit.OperationError") and friends.
//     pkg/universe frames and routes them but cannot construct them, and
//     moving the declarations down to remove the indirection would rename
//     every one of them on the wire.
//  2. EngineDefaultClientHandlers — not a question at all: it asks the facade
//     to REGISTER Ping and ReplicationAck handlers against a Process that
//     universe.New has just built.
func init() {
	registerEngineTypedEvents()

	pkguniverse.GlobalWire().SetFrameworkEncoders(pkguniverse.FrameworkEncoders{
		OperationErrorTypeID: TypeIDOf(reflect.TypeFor[OperationError]()),
		MakeOperationErrorBody: func(code uint32, message string) []byte {
			body, err := pkguniverse.ReflectMarshal(&OperationError{Code: code, Message: message})
			if err != nil {
				// Only reachable when a handler's error string exceeds the u16
				// wire prefix. Returning nil would drop the response and leave
				// the client blocked on its requestID, so re-encode with the
				// message replaced rather than the frame lost.
				body, err = pkguniverse.ReflectMarshal(&OperationError{
					Code:    code,
					Message: "error message too large for the wire",
				})
				if err != nil {
					return nil
				}
			}
			return body
		},
		PlayerEntityAssigned: func(netID uint32, worldX, worldY float32) []byte {
			return pkguniverse.BuildTypedEventFrameRaw(&PlayerEntityAssigned{
				EntityNetID: netID,
				WorldX:      worldX,
				WorldY:      worldY,
			})
		},
		ServerConfig: func(tickRate uint32) []byte {
			return pkguniverse.BuildTypedEventFrameRaw(&ServerConfig{TickRate: tickRate})
		},
	})

	// Engine-default client inputs — installed once per Process by
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
}
