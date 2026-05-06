package universe

import (
	"fmt"
	"reflect"

	"github.com/zenion/mmoserver/pkg/ops"
)

// DispatchTypedOpInbound consumes a 0x01 typed-op payload (channel byte
// stripped), decodes the request, dispatches to the registered handler,
// and returns the encoded response frame.
//
// Always returns a frame on success or framework-level failure (unknown
// typeID, handler error, or decode failure produce OperationError-typed
// responses). Returns nil only for structurally invalid frames (truncated
// header / body) — those are dropped silently because there is no
// request_id to correlate the error with.
//
// Phase 1 of Plan 2 handles RouteGatewayLocal only. RoutePlayerCell ops
// return an OperationError with code OpErrorHandlerFailed; the cell
// routing path lands in Phase 3 via engine.RunOnLoop.
//
// Universe cannot import pkg/mmokit (circular dep) — the typed-op
// registry, RouteKind enum, and OperationError encoder are reached via
// the TypedOpHooks indirection populated by mmokit's init().
func DispatchTypedOpInbound(payload []byte, ctx *ops.OpContext) []byte {
	typeID, requestID, body, err := DecodeTypedOpFrame(payload)
	if err != nil {
		return nil
	}

	if TypedOpHooks.LookupTypedOp == nil {
		// Hooks not wired (universe-only test, no mmokit import). No
		// handlers can be registered, so every typed-op inbound is by
		// definition unknown.
		return encodeOpErrorViaHooks(requestID, opErrorUnknownTypeID,
			fmt.Sprintf("typed-op hooks unwired (typeID %#x)", typeID))
	}

	kind, reqType, _, resTypeID, handler, ok := TypedOpHooks.LookupTypedOp(typeID)
	if !ok {
		return encodeOpErrorViaHooks(requestID, opErrorUnknownTypeID,
			fmt.Sprintf("unknown typed-op typeID %#x", typeID))
	}

	if kind != TypedOpHooks.RouteGatewayLocal {
		return encodeOpErrorViaHooks(requestID, opErrorHandlerFailed,
			fmt.Sprintf("op %s requires cell routing; not yet supported in Plan 2 Phase 1", reqType.String()))
	}

	reqPtr := reflect.New(reqType)
	ReflectUnmarshal(body, reqPtr.Interface())

	handlerVal := reflect.ValueOf(handler)
	results := handlerVal.Call([]reflect.Value{reflect.ValueOf(ctx), reqPtr})

	resPtr := results[0]
	errVal := results[1]
	if !errVal.IsNil() {
		return encodeOpErrorViaHooks(requestID, opErrorHandlerFailed,
			errVal.Interface().(error).Error())
	}
	if resPtr.IsNil() {
		return EncodeTypedOpFrame(resTypeID, requestID, nil)
	}
	resBody := ReflectMarshal(resPtr.Interface())
	return EncodeTypedOpFrame(resTypeID, requestID, resBody)
}

// Mirror constants for the OperationError code values defined on the
// mmokit side. Kept in sync manually — they are wire-stable identifiers,
// not implementation details. Values must match
// pkg/mmokit/handle_op.go's OpError* constants.
const (
	opErrorUnknownTypeID uint32 = 1
	opErrorHandlerFailed uint32 = 2
	opErrorDecodeFailed  uint32 = 3
)

// encodeOpErrorViaHooks builds a typed-op response frame carrying an
// OperationError. The message body is constructed by the mmokit-side
// MakeOperationErrorBody hook so the framework type's encoder lives
// next to its definition. Returns nil if hooks are unwired (test-only
// path without the mmokit import).
func encodeOpErrorViaHooks(requestID uint64, code uint32, message string) []byte {
	if TypedOpHooks.MakeOperationErrorBody == nil {
		return nil
	}
	body := TypedOpHooks.MakeOperationErrorBody(code, message)
	return EncodeTypedOpFrame(TypedOpHooks.OperationErrorTypeID, requestID, body)
}
