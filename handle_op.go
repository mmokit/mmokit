package mmokit

import (
	"reflect"

	pkguniverse "github.com/mmokit/mmokit/pkg/universe"
)

// OperationError is the framework-defined response the typed-op dispatcher
// returns when an op fails before reaching, or inside, a registered handler.
// Codes are stable; the message is informational and may change. Clients
// should branch on Code, not on Message.
type OperationError struct {
	Code    uint32
	Message string
}

// OperationError codes. Values are stable wire identifiers.
const (
	OpErrorUnknownTypeID uint32 = 1 // no handler registered for the request typeID
	OpErrorHandlerFailed uint32 = 2 // handler returned a non-nil error
	OpErrorDecodeFailed  uint32 = 3 // request body failed to decode via the reflection codec
)

// registerFrameworkOps registers framework-owned typed-op response types on
// wire so the dispatcher can encode them by typeID.
//
// This used to be sync.Once-guarded "because RegisterEvent panics on a
// duplicate". It does not — RegisterServerEvent returns early on a type
// already in the set. The guard was load-bearing in the wrong direction once
// registries became per-process: a process-global Once means the SECOND
// registry never gets the type.
func registerFrameworkOps(wire *WireRegistry) {
	wire.RegisterServerEvent(reflect.TypeFor[OperationError]())
}

// RegisterOp registers a typed operation handler. The wire typeID is
// derived from the Request type via TypeIDOf; the response typeID is
// derived from Res. Each handler signature is
//
//	func(*OpContext, *Req) (*Res, error)
//
// — the dispatcher allocates a fresh *Req, decodes the body via the
// reflection codec, calls the handler, and encodes (*Res) on the
// outbound 0x01 frame. A nil response (with a nil error) emits a frame
// with an empty body.
//
// Re-registration of the same Request type with the same Kind +
// Response type is idempotent (last-writer-wins on the handler closure)
// so games can call RegisterOp from a setup function that runs many
// times across tests without juggling reset boilerplate. Mirrors
// HandleClient's idempotent behavior. Registries are per-Process, so that
// last write can no longer land in a sibling Process's handler slot — which
// it did, silently, for every service whose handlers close over their own
// *Process.
//
// Panics on:
//   - Re-registration of a type with a different Kind or different
//     Response type (genuine programmer error — the wire schema would
//     change silently).
//   - typeID collision between two distinct Request types (extremely
//     unlikely at codebase scale; rename one type if it triggers).
func RegisterOp[Req any, Res any](src WireSource, kind RouteKind, handler func(*OpContext, *Req) (*Res, error)) {
	src.Wire().RegisterTypedOp(kind,
		reflect.TypeFor[Req](), reflect.TypeFor[Res](), handler)
}

// OpContextStage returns the *Stage on which a RoutePlayerCell typed-op
// handler is currently running. RoutePlayerCell handlers can rely on a
// non-nil return; RouteGatewayLocal handlers (no cell context) get nil.
//
// Use this when a typed-op handler needs to access cell-level state
// (entities, NetworkID map, broadcast helpers) without threading the
// stage through the handler signature.
func OpContextStage(ctx *OpContext) *Stage {
	return pkguniverse.OpContextStage(ctx)
}
