package universe

import (
	"reflect"

	"github.com/mmokit/mmokit/pkg/ops"
)

// TypedOpInfo is one row of the typed-op registry, surfaced through the
// ListTypedOps hook for diagnostics surfaces (admin console, sdkgen).
// Kind is the route discriminator (uint8 mirror of mmokit.RouteKind, see
// TypedOpHooks.RouteGatewayLocal); KindName is the human-readable form
// ("gateway-local" / "player-cell") for direct console rendering.
type TypedOpInfo struct {
	Kind         uint8
	KindName     string
	RequestType  reflect.Type
	ResponseType reflect.Type
	RequestID    uint32
}

// TypedOpHooks is the import-cycle indirection that lets the gateway-side
// inbound 0x01 dispatch consult the mmokit-owned typed-op registry without
// importing pkg/mmokit (which would be a circular dependency — pkg/mmokit
// already depends on pkg/universe).
//
// pkg/mmokit populates these callbacks in its init(). When the hooks are
// nil (e.g. tests that build a Process standalone without importing mmokit)
// the dispatcher returns an "unknown typeID" OperationError for every
// inbound typed-op frame — handlers can't possibly be registered without
// the mmokit init having run.
//
// Same pattern as BroadcastHooks / ClientInputHooks / ServerEventHooks.
var TypedOpHooks struct {
	// LookupTypedOp returns the registered typed-op for the given request
	// typeID, or (nil, false) if none. Returned values are kind, request
	// type, response type, response typeID, and the raw handler `any`
	// (signature func(*OpContext, *Req) (*Res, error)). The dispatcher
	// invokes the handler via reflect.Call.
	LookupTypedOp func(reqTypeID uint32) (kind uint8, reqType reflect.Type, resType reflect.Type, resTypeID uint32, handler any, ok bool)

	// ListTypedOps returns every registered typed-op for introspection
	// surfaces (admin console, sdkgen). Each row carries the kind, the
	// request type, the response type, and the request typeID. Order is
	// deterministic (mmokit sorts by request type name).
	ListTypedOps func() []TypedOpInfo

	// OperationErrorTypeID is the wire-stable typeID for
	// mmokit.OperationError, computed once at init via
	// TypeIDOf(reflect.TypeFor[OperationError]()). Cached so the
	// dispatcher's error path does not repeatedly walk the reflection
	// stack.
	OperationErrorTypeID uint32

	// MakeOperationErrorBody encodes an OperationError{code, message}
	// via the reflection codec. The dispatcher wraps the result in a
	// 0x01 typed-op frame keyed at OperationErrorTypeID.
	MakeOperationErrorBody func(code uint32, message string) []byte

	// RouteGatewayLocal is the kind value for gateway-local routing,
	// mirrored on the universe side as a uint8 to keep the hooks
	// dependency-free (the enum lives in pkg/mmokit).
	RouteGatewayLocal uint8
}

// CellOpRouter resolves a typed-op request to the player's authoritative
// cell on this process and dispatches the handler on that cell's engine
// loop.
//
// Implementations return a non-nil response frame synchronously when the
// op cannot be routed (no active session, player on a remote host, etc.) —
// typically an OperationError frame. They return nil when the dispatch is
// scheduled successfully; the response frame is sent asynchronously via
// the cell engine's connection manager once the handler completes.
//
// Process implements this via Process.dispatchCellRoutedOp; tests that
// build a Stage standalone without a Process can pass nil and the
// dispatcher returns OperationError for every RoutePlayerCell inbound.
type CellOpRouter interface {
	DispatchCellRoutedOp(reqType reflect.Type, resTypeID uint32, requestID uint64, body []byte, handler any, ctx *ops.OpContext) []byte
}
