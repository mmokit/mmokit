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
// Returns a non-nil frame synchronously for:
//   - RouteGatewayLocal handlers (handler runs inline on the caller goroutine)
//   - Framework-level failures (unknown typeID, decode error, handler error)
//
// Returns nil for:
//   - Structurally invalid frames (truncated header / body) — dropped silently
//     because there is no request_id to correlate the error with
//   - Successful RoutePlayerCell dispatches — the response frame is sent
//     asynchronously via the cell engine's connection manager once the
//     handler completes on the cell loop. The caller MUST treat nil as
//     "response will arrive later, do not send" rather than "dispatch
//     failed silently"
//
// router is the cell-routing path used for RoutePlayerCell entries; pass
// nil from tests / stage-only paths and RoutePlayerCell ops will produce
// an OperationError synchronously.
//
// Universe cannot import pkg/mmokit (circular dep) — the typed-op
// registry, RouteKind enum, and OperationError encoder are reached via
// the TypedOpHooks indirection populated by mmokit's init().
func DispatchTypedOpInbound(payload []byte, ctx *ops.OpContext, router CellOpRouter) []byte {
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
		// RoutePlayerCell: defer to the process-level router. router
		// returns nil for successful async dispatch (response sent later
		// via cell engine connMgr) or a synchronous OperationError frame
		// when the cell isn't reachable on this process.
		if router == nil {
			return encodeOpErrorViaHooks(requestID, opErrorHandlerFailed,
				fmt.Sprintf("op %s requires cell routing; no router wired", reqType.String()))
		}
		// Copy body — the underlying buffer is owned by the caller
		// (router poll loop) and may be reused once we return. The cell-
		// routed dispatch runs asynchronously on the engine loop and
		// must not alias the inbound buffer.
		bodyCopy := append([]byte(nil), body...)
		return router.DispatchCellRoutedOp(reqType, resTypeID, requestID, bodyCopy, handler, ctx)
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
	resBody, err := ReflectMarshal(resPtr.Interface())
	if err != nil {
		// The handler succeeded but its response does not fit the wire. Answer
		// with a typed error rather than dropping the frame — the client is
		// blocked on this requestID either way.
		return encodeOpErrorViaHooks(requestID, opErrorHandlerFailed,
			fmt.Sprintf("encode response: %v", err))
	}
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
