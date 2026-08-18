package universe

import (
	"fmt"
	"reflect"

	"github.com/mmokit/mmokit/pkg/metrics"
	pkgnet "github.com/mmokit/mmokit/pkg/net"
	"github.com/mmokit/mmokit/pkg/ops"
)

// DispatchTypedOpInbound consumes a 0x01 typed-op payload (channel byte
// stripped), decodes the request, dispatches to the registered handler,
// and returns the encoded response frame.
//
// Returns a non-nil frame synchronously for:
//   - RouteGatewayLocal handlers (handler runs inline on the caller goroutine)
//   - Framework-level failures (unknown typeID, request-body decode error,
//     handler error)
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
// lim is the client ingress profile the request body is decoded under —
// Process.clientWireLimits() on every production path. The zero value is
// accepted and falls back to the defaults, which is what a fixture that never
// calls BindFlags gets.
//
// The typed-op registry is the process's WireRegistry; a nil one (a fixture
// with no Process behind it) has no registrations, so every inbound is
// unknown.
func DispatchTypedOpInbound(wire *WireRegistry, payload []byte, ctx *ops.OpContext, router CellOpRouter, lim pkgnet.WireLimits) []byte {
	typeID, requestID, body, err := DecodeTypedOpFrame(payload)
	if err != nil {
		return nil
	}

	entry, ok := wire.LookupTypedOp(typeID)
	if !ok {
		// A sustained rate here is either an SDK/server version skew or
		// someone walking the registry.
		metrics.Ingress().RecordRejected(metrics.SurfaceClient, metrics.ReasonUnknownTypeID)
		return encodeOpError(wire, requestID, opErrorUnknownTypeID,
			fmt.Sprintf("unknown typed-op typeID %#x", typeID))
	}
	reqType := entry.RequestType
	resTypeID := entry.ResponseTypeID
	handler := entry.Handler

	if entry.Kind != RouteGatewayLocal {
		// RoutePlayerCell: defer to the process-level router. router
		// returns nil for successful async dispatch (response sent later
		// via cell engine connMgr) or a synchronous OperationError frame
		// when the cell isn't reachable on this process.
		if router == nil {
			return encodeOpError(wire, requestID, opErrorHandlerFailed,
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
	// Strict: the body's length is part of the contract here. A body longer
	// than reqType means the sender and this process disagree about which type
	// typeID names, which is a protocol error and not a producer appending
	// something — unlike the mesh blobs the tolerant wrapper still serves.
	if err := ReflectUnmarshalStrict(nil, body, reqPtr.Interface(), lim); err != nil {
		// Never call the handler on a body the decoder refused: reqPtr is only
		// partially written, so the handler would see a request whose refused
		// fields are indistinguishable from an intentional zero value.
		return encodeOpError(wire, requestID, opErrorDecodeFailed,
			fmt.Sprintf("decode %s: %v", reqType.String(), err))
	}

	handlerVal := reflect.ValueOf(handler)
	results := handlerVal.Call([]reflect.Value{reflect.ValueOf(ctx), reqPtr})

	resPtr := results[0]
	errVal := results[1]
	if !errVal.IsNil() {
		return encodeOpError(wire, requestID, opErrorHandlerFailed,
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
		return encodeOpError(wire, requestID, opErrorHandlerFailed,
			fmt.Sprintf("encode response: %v", err))
	}
	return EncodeTypedOpFrame(resTypeID, requestID, resBody)
}

// Mirror constants for the OperationError code values defined on the
// mmokit side. Kept in sync manually — they are wire-stable identifiers,
// not implementation details. Values must match
// handle_op.go's OpError* constants.
const (
	opErrorUnknownTypeID uint32 = 1
	opErrorHandlerFailed uint32 = 2
	opErrorDecodeFailed  uint32 = 3
)

// encodeOpError builds a typed-op response frame carrying an OperationError.
// The message body is constructed by the registry's MakeOperationErrorBody
// encoder so the framework type's encoder lives next to its definition (see
// FrameworkEncoders). Returns nil when that encoder is unset — a fixture
// without the mmokit import has no OperationError type to encode.
func encodeOpError(wire *WireRegistry, requestID uint64, code uint32, message string) []byte {
	enc := wire.FrameworkEncoders()
	if enc.MakeOperationErrorBody == nil {
		return nil
	}
	body := enc.MakeOperationErrorBody(code, message)
	return EncodeTypedOpFrame(enc.OperationErrorTypeID, requestID, body)
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
