package mmokit

import (
	"reflect"
	"time"

	"github.com/mmokit/mmokit/pkg/component"
	pkguniverse "github.com/mmokit/mmokit/pkg/universe"
)

// bootstrapWire populates a freshly-constructed Process's wire registry with
// everything the framework itself puts on the wire, and installs the encoders
// pkg/universe needs for the framework's own types. Called once from New.
//
// This is what five package-global hook structs and a root init() used to be.
// They existed because the registries were package globals in this package and
// pkg/universe had to call back up into them; the registries belong to a
// Process now, so the only thing left pointing this direction is the handful of
// wire types the facade declares and pkg/universe frames but cannot construct:
// OperationError, PlayerEntityAssigned, ServerConfig. Their wire identity is
// fnv32a("mmokit.OperationError") and friends, so moving the declarations down
// to remove the indirection would rename every one of them on the wire.
//
// Nothing here is sync.Once-guarded. Registration is idempotent per registry,
// and a process-global guard would mean the second Process built by a binary
// received none of it — which is precisely the class of bug this unit exists
// to close.
func bootstrapWire(p *Process) {
	wire := p.Wire()

	// Engine-level typed server events (Pong, DebugInfo, WorldDelta,
	// PlayerEntityAssigned, CellChange, ServerConfig) and the framework's
	// typed-op error response. Every game gets them for free.
	registerEngineTypedEvents(wire)
	registerFrameworkOps(wire)

	wire.SetFrameworkEncoders(pkguniverse.FrameworkEncoders{
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
			return pkguniverse.BuildTypedEventFrameRaw(wire, &PlayerEntityAssigned{
				EntityNetID: netID,
				WorldX:      worldX,
				WorldY:      worldY,
			})
		},
		ServerConfig: func(tickRate uint32) []byte {
			return pkguniverse.BuildTypedEventFrameRaw(wire, &ServerConfig{TickRate: tickRate})
		},
	})

	// Engine-default client inputs. Games never wire these themselves.
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

	// Explicit replication frame ACK. Dispatch is per-Stage and resolves the
	// handler against the player entity owned by the sending connection on THAT
	// stage, so the ack always reaches the ReplicationSystem of the cell that
	// currently holds authority.
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
