package universe_test

import (
	"bytes"
	"reflect"
	"testing"

	"github.com/zenion/mmoserver/pkg/mmokit"
	pkgnet "github.com/zenion/mmoserver/pkg/net"
	"github.com/zenion/mmoserver/pkg/ops"
	pkguniverse "github.com/zenion/mmoserver/pkg/universe"
)

// dispReq / dispRes are the test op pair. dispRes carries Y = X * 2 so the
// handler's effect on the wire is observable.
type dispReq struct {
	X int32
}

type dispRes struct {
	Y int32
}

func TestDispatchTypedOp_GatewayLocal_HappyPath(t *testing.T) {
	t.Cleanup(mmokit.ResetTypedOpRegistryForTest)
	mmokit.ResetTypedOpRegistryForTest()

	mmokit.RegisterOp[dispReq, dispRes](mmokit.RouteGatewayLocal,
		func(_ *mmokit.OpContext, req *dispReq) (*dispRes, error) {
			return &dispRes{Y: req.X * 2}, nil
		})

	// Build the request frame: encode the body via the reflection codec,
	// wrap in a 0x01 typed-op frame.
	reqBody := mustMarshal(t, &dispReq{X: 21})
	reqTypeID := mmokit.TypeIDOf(reflect.TypeFor[dispReq]())
	frame := pkguniverse.EncodeTypedOpFrame(reqTypeID, 1234, reqBody)

	// Strip the channel byte to get the payload the dispatcher expects.
	payload := frame[1:]
	respFrame := pkguniverse.DispatchTypedOpInbound(payload, &ops.OpContext{ConnID: 7, Username: "alice"}, nil, pkgnet.WireLimits{})
	if respFrame == nil {
		t.Fatalf("DispatchTypedOpInbound: nil response frame")
	}
	if respFrame[0] != 0x01 {
		t.Fatalf("response channel byte = %#x, want 0x01", respFrame[0])
	}

	// Decode the response.
	gotTypeID, gotReqID, gotBody, err := pkguniverse.DecodeTypedOpFrame(respFrame[1:])
	if err != nil {
		t.Fatalf("DecodeTypedOpFrame on response: %v", err)
	}
	wantResTypeID := mmokit.TypeIDOf(reflect.TypeFor[dispRes]())
	if gotTypeID != wantResTypeID {
		t.Errorf("response typeID: got %#x, want %#x", gotTypeID, wantResTypeID)
	}
	if gotReqID != 1234 {
		t.Errorf("response request_id: got %d, want 1234", gotReqID)
	}
	var got dispRes
	mustUnmarshal(t, gotBody, &got)
	if got.Y != 42 {
		t.Errorf("response Y: got %d, want 42 (handler doubles input)", got.Y)
	}
}

func TestDispatchTypedOp_UnknownTypeID_ReturnsOperationError(t *testing.T) {
	t.Cleanup(mmokit.ResetTypedOpRegistryForTest)
	mmokit.ResetTypedOpRegistryForTest()

	// Build a frame at a typeID nobody registered.
	frame := pkguniverse.EncodeTypedOpFrame(0xDEADBEEF, 99, nil)
	resp := pkguniverse.DispatchTypedOpInbound(frame[1:], &ops.OpContext{}, nil, pkgnet.WireLimits{})
	if resp == nil {
		t.Fatalf("expected OperationError frame for unknown typeID, got nil")
	}

	gotTypeID, gotReqID, gotBody, err := pkguniverse.DecodeTypedOpFrame(resp[1:])
	if err != nil {
		t.Fatalf("DecodeTypedOpFrame: %v", err)
	}
	wantTypeID := mmokit.TypeIDOf(reflect.TypeFor[mmokit.OperationError]())
	if gotTypeID != wantTypeID {
		t.Errorf("typeID: got %#x, want OperationError typeID %#x", gotTypeID, wantTypeID)
	}
	if gotReqID != 99 {
		t.Errorf("request_id: got %d, want 99", gotReqID)
	}
	var opErr mmokit.OperationError
	mustUnmarshal(t, gotBody, &opErr)
	if opErr.Code != mmokit.OpErrorUnknownTypeID {
		t.Errorf("code: got %d, want OpErrorUnknownTypeID(%d)", opErr.Code, mmokit.OpErrorUnknownTypeID)
	}
}

func TestDispatchTypedOp_HandlerError_ReturnsOperationError(t *testing.T) {
	t.Cleanup(mmokit.ResetTypedOpRegistryForTest)
	mmokit.ResetTypedOpRegistryForTest()

	mmokit.RegisterOp[dispReq, dispRes](mmokit.RouteGatewayLocal,
		func(_ *mmokit.OpContext, _ *dispReq) (*dispRes, error) {
			return nil, errFakeHandler
		})

	reqBody := mustMarshal(t, &dispReq{X: 1})
	reqTypeID := mmokit.TypeIDOf(reflect.TypeFor[dispReq]())
	frame := pkguniverse.EncodeTypedOpFrame(reqTypeID, 5, reqBody)
	resp := pkguniverse.DispatchTypedOpInbound(frame[1:], &ops.OpContext{}, nil, pkgnet.WireLimits{})
	if resp == nil {
		t.Fatalf("expected OperationError frame on handler error, got nil")
	}
	gotTypeID, _, gotBody, err := pkguniverse.DecodeTypedOpFrame(resp[1:])
	if err != nil {
		t.Fatalf("DecodeTypedOpFrame: %v", err)
	}
	wantTypeID := mmokit.TypeIDOf(reflect.TypeFor[mmokit.OperationError]())
	if gotTypeID != wantTypeID {
		t.Errorf("typeID: got %#x, want OperationError %#x", gotTypeID, wantTypeID)
	}
	var opErr mmokit.OperationError
	mustUnmarshal(t, gotBody, &opErr)
	if opErr.Code != mmokit.OpErrorHandlerFailed {
		t.Errorf("code: got %d, want OpErrorHandlerFailed(%d)", opErr.Code, mmokit.OpErrorHandlerFailed)
	}
}

func TestDispatchTypedOp_RoutePlayerCell_NoRouter_ReturnsOperationError(t *testing.T) {
	// With nil CellOpRouter the dispatcher cannot resolve the player's
	// cell, so RoutePlayerCell ops fail synchronously with
	// OperationError{OpErrorHandlerFailed}. Used by tests that exercise
	// the dispatcher in isolation; production wiring passes *Process as
	// the router (see Process.DispatchCellRoutedOp).
	t.Cleanup(mmokit.ResetTypedOpRegistryForTest)
	mmokit.ResetTypedOpRegistryForTest()

	mmokit.RegisterOp[dispReq, dispRes](mmokit.RoutePlayerCell,
		func(_ *mmokit.OpContext, _ *dispReq) (*dispRes, error) { return &dispRes{}, nil })

	reqBody := mustMarshal(t, &dispReq{X: 1})
	reqTypeID := mmokit.TypeIDOf(reflect.TypeFor[dispReq]())
	frame := pkguniverse.EncodeTypedOpFrame(reqTypeID, 7, reqBody)
	resp := pkguniverse.DispatchTypedOpInbound(frame[1:], &ops.OpContext{}, nil, pkgnet.WireLimits{})
	if resp == nil {
		t.Fatalf("expected OperationError frame for cell-routed op, got nil")
	}
	gotTypeID, _, gotBody, err := pkguniverse.DecodeTypedOpFrame(resp[1:])
	if err != nil {
		t.Fatalf("DecodeTypedOpFrame: %v", err)
	}
	wantTypeID := mmokit.TypeIDOf(reflect.TypeFor[mmokit.OperationError]())
	if gotTypeID != wantTypeID {
		t.Errorf("typeID: got %#x, want OperationError %#x", gotTypeID, wantTypeID)
	}
	var opErr mmokit.OperationError
	mustUnmarshal(t, gotBody, &opErr)
	if opErr.Code != mmokit.OpErrorHandlerFailed {
		t.Errorf("code: got %d, want OpErrorHandlerFailed", opErr.Code)
	}
}

func TestDispatchTypedOp_TruncatedFrame_ReturnsNil(t *testing.T) {
	resp := pkguniverse.DispatchTypedOpInbound([]byte{0x01, 0x02}, &ops.OpContext{}, nil, pkgnet.WireLimits{})
	if resp != nil {
		t.Fatalf("truncated frame should drop silently (nil response), got %x", resp)
	}
}

// dispStrReq is shaped like the requests the decode-error path exists for: a
// leading length-prefixed string, as an auth login or a chat send has. A
// zero-valued decode of one of these reaches the handler as empty credentials
// that it cannot distinguish from a client that really sent them.
type dispStrReq struct {
	Username string
}

// TestDispatchTypedOpInbound_DecodeError verifies that a RouteGatewayLocal op
// whose body the checked decoder refuses answers with
// OperationError{OpErrorDecodeFailed} — and does not reach the handler.
//
// Both halves matter. Returning nil would leave the client's typed-op promise
// pending forever, since nil means "a response will arrive later" on this API.
// Calling the handler anyway would be the correctness regression CE-002 unit 9
// exists to prevent: bounding the decoder is worthless if a refused body is
// dispatched as a zero value.
func TestDispatchTypedOpInbound_DecodeError(t *testing.T) {
	t.Cleanup(mmokit.ResetTypedOpRegistryForTest)
	mmokit.ResetTypedOpRegistryForTest()

	handlerRan := false
	mmokit.RegisterOp[dispStrReq, dispRes](mmokit.RouteGatewayLocal,
		func(_ *mmokit.OpContext, _ *dispStrReq) (*dispRes, error) {
			handlerRan = true
			return &dispRes{}, nil
		})

	// Body declares a 64-byte Username and supplies none of it. RouteGatewayLocal
	// runs the handler inline on this goroutine, so handlerRan needs no
	// synchronization.
	reqTypeID := mmokit.TypeIDOf(reflect.TypeFor[dispStrReq]())
	frame := pkguniverse.EncodeTypedOpFrame(reqTypeID, 4242, []byte{64, 0})

	resp := pkguniverse.DispatchTypedOpInbound(frame[1:],
		&ops.OpContext{ConnID: 3, Username: "alice"}, nil, pkgnet.WireLimits{})
	if resp == nil {
		t.Fatal("expected an OperationError frame for a refused body; nil means " +
			"\"response will arrive later\" and the client would wait forever")
	}
	if handlerRan {
		t.Error("handler ran on a body the decoder refused — it would have seen an " +
			"empty Username indistinguishable from a real one")
	}

	gotTypeID, gotReqID, gotBody, err := pkguniverse.DecodeTypedOpFrame(resp[1:])
	if err != nil {
		t.Fatalf("DecodeTypedOpFrame: %v", err)
	}
	wantTypeID := mmokit.TypeIDOf(reflect.TypeFor[mmokit.OperationError]())
	if gotTypeID != wantTypeID {
		t.Errorf("typeID = %#x, want OperationError %#x", gotTypeID, wantTypeID)
	}
	if gotReqID != 4242 {
		t.Errorf("request_id = %d, want 4242 (the client correlates on it)", gotReqID)
	}
	var opErr mmokit.OperationError
	mustUnmarshal(t, gotBody, &opErr)
	if opErr.Code != mmokit.OpErrorDecodeFailed {
		t.Errorf("code = %d, want OpErrorDecodeFailed(%d)", opErr.Code, mmokit.OpErrorDecodeFailed)
	}
	if opErr.Message == "" {
		t.Error("OperationError.Message is empty; the client cannot tell which body was refused")
	}
}

// TestDispatchTypedOpInbound_RejectsTrailingBytes pins CE-002 criterion 4's
// trailing half on the client-facing op path.
//
// The body here is a perfectly well-formed dispReq followed by four surplus
// bytes. Under the tolerant wrapper the dispatcher decoded the prefix, threw
// the rest away, and called the handler as if nothing had happened — so a
// client whose idea of dispReq has extra fields, or one probing for a type
// confusion, got a successful dispatch against a type it was not sending. The
// strict decoder answers OperationError{OpErrorDecodeFailed} instead.
//
// The op path is the one place where a length mismatch is unambiguous: unlike
// the mesh blobs the tolerant wrapper still serves, nothing appends to a typed
// op body, so surplus bytes mean the sender and this process disagree about
// what typeID names.
func TestDispatchTypedOpInbound_RejectsTrailingBytes(t *testing.T) {
	t.Cleanup(mmokit.ResetTypedOpRegistryForTest)
	mmokit.ResetTypedOpRegistryForTest()

	handlerRan := false
	mmokit.RegisterOp[dispReq, dispRes](mmokit.RouteGatewayLocal,
		func(_ *mmokit.OpContext, _ *dispReq) (*dispRes, error) {
			handlerRan = true
			return &dispRes{}, nil
		})

	body := append(mustMarshal(t, &dispReq{X: 5}), 0xAA, 0xBB, 0xCC, 0xDD)
	reqTypeID := mmokit.TypeIDOf(reflect.TypeFor[dispReq]())
	frame := pkguniverse.EncodeTypedOpFrame(reqTypeID, 777, body)

	resp := pkguniverse.DispatchTypedOpInbound(frame[1:],
		&ops.OpContext{ConnID: 3, Username: "alice"}, nil, pkgnet.WireLimits{})
	if resp == nil {
		t.Fatal("expected an OperationError frame for a body with trailing bytes")
	}
	if handlerRan {
		t.Error("handler ran on a body carrying 4 surplus bytes — the sender and " +
			"this process disagree about the request type and neither found out")
	}

	gotTypeID, gotReqID, gotBody, err := pkguniverse.DecodeTypedOpFrame(resp[1:])
	if err != nil {
		t.Fatalf("DecodeTypedOpFrame: %v", err)
	}
	if want := mmokit.TypeIDOf(reflect.TypeFor[mmokit.OperationError]()); gotTypeID != want {
		t.Errorf("typeID = %#x, want OperationError %#x", gotTypeID, want)
	}
	if gotReqID != 777 {
		t.Errorf("request_id = %d, want 777", gotReqID)
	}
	var opErr mmokit.OperationError
	mustUnmarshal(t, gotBody, &opErr)
	if opErr.Code != mmokit.OpErrorDecodeFailed {
		t.Errorf("code = %d, want OpErrorDecodeFailed(%d)", opErr.Code, mmokit.OpErrorDecodeFailed)
	}
}

// fakeCellOpRouter records the parameters DispatchTypedOpInbound forwards
// to it and returns a configurable response. Used to verify the dispatcher
// hands off RoutePlayerCell entries to the router with the right typeID,
// requestID, body, and OpContext (and that the body slice is a copy, not
// an alias of the inbound buffer).
type fakeCellOpRouter struct {
	called  bool
	reqType reflect.Type
	resTID  uint32
	reqID   uint64
	body    []byte
	handler any
	ctx     *ops.OpContext
	resp    []byte // returned synchronously
}

func (f *fakeCellOpRouter) DispatchCellRoutedOp(
	reqType reflect.Type,
	resTypeID uint32,
	requestID uint64,
	body []byte,
	handler any,
	ctx *ops.OpContext,
) []byte {
	f.called = true
	f.reqType = reqType
	f.resTID = resTypeID
	f.reqID = requestID
	f.body = body
	f.handler = handler
	f.ctx = ctx
	return f.resp
}

// TestDispatchTypedOp_RoutePlayerCell_ForwardsToRouter verifies that the
// dispatcher hands RoutePlayerCell entries off to the supplied router with
// the decoded reqType, resTypeID, requestID, body, handler, and context.
// The router's returned bytes pass straight through (so async-success vs
// synchronous-error is the router's call to make).
func TestDispatchTypedOp_RoutePlayerCell_ForwardsToRouter(t *testing.T) {
	t.Cleanup(mmokit.ResetTypedOpRegistryForTest)
	mmokit.ResetTypedOpRegistryForTest()

	mmokit.RegisterOp[dispReq, dispRes](mmokit.RoutePlayerCell,
		func(_ *mmokit.OpContext, req *dispReq) (*dispRes, error) {
			return &dispRes{Y: req.X * 2}, nil
		})

	reqBody := mustMarshal(t, &dispReq{X: 5})
	reqTypeID := mmokit.TypeIDOf(reflect.TypeFor[dispReq]())
	wantResTID := mmokit.TypeIDOf(reflect.TypeFor[dispRes]())
	frame := pkguniverse.EncodeTypedOpFrame(reqTypeID, 99, reqBody)

	router := &fakeCellOpRouter{} // returns nil → "scheduled async"
	resp := pkguniverse.DispatchTypedOpInbound(frame[1:], &ops.OpContext{ConnID: 7, Username: "alice"}, router, pkgnet.WireLimits{})

	if !router.called {
		t.Fatal("router.DispatchCellRoutedOp was not invoked")
	}
	if resp != nil {
		t.Fatalf("expected nil response (async), got %x", resp)
	}
	if router.reqType != reflect.TypeFor[dispReq]() {
		t.Errorf("reqType = %v, want %v", router.reqType, reflect.TypeFor[dispReq]())
	}
	if router.resTID != wantResTID {
		t.Errorf("resTypeID = %#x, want %#x", router.resTID, wantResTID)
	}
	if router.reqID != 99 {
		t.Errorf("requestID = %d, want 99", router.reqID)
	}
	if !bytes.Equal(router.body, reqBody) {
		t.Errorf("body = %x, want %x", router.body, reqBody)
	}
	// Body MUST be a copy, not an alias of the inbound payload — the
	// router goroutine returns to the next op as soon as Dispatch
	// returns and the buffer is reused.
	if &router.body[0] == &frame[1+4+8+4] {
		t.Error("body aliased the inbound frame buffer; expected a copy")
	}
	if router.ctx.ConnID != 7 || router.ctx.Username != "alice" {
		t.Errorf("ctx = %+v, want ConnID=7 Username=alice", router.ctx)
	}
}

// TestDispatchCellRoutedOp_NoActiveSession returns OperationError when the
// player has no active session on this Process — neither offline players
// nor remote-host players can be served from this dispatcher's Process.
func TestDispatchCellRoutedOp_NoActiveSession(t *testing.T) {
	t.Cleanup(mmokit.ResetTypedOpRegistryForTest)
	mmokit.ResetTypedOpRegistryForTest()

	cfg := mmokit.Config{Headless: true, ConnManager: mmokit.NewConnManager()}
	p := mmokit.New(cfg)

	mmokit.RegisterOp[dispReq, dispRes](mmokit.RoutePlayerCell,
		func(_ *mmokit.OpContext, _ *dispReq) (*dispRes, error) { return &dispRes{}, nil })

	reqBody := mustMarshal(t, &dispReq{X: 1})
	reqTypeID := mmokit.TypeIDOf(reflect.TypeFor[dispReq]())
	frame := pkguniverse.EncodeTypedOpFrame(reqTypeID, 11, reqBody)

	resp := pkguniverse.DispatchTypedOpInbound(
		frame[1:],
		&ops.OpContext{ConnID: 1, Username: "ghost"},
		p, // *Process implements CellOpRouter
		pkgnet.WireLimits{},
	)
	if resp == nil {
		t.Fatal("expected synchronous OperationError frame for offline user")
	}
	gotTypeID, _, gotBody, err := pkguniverse.DecodeTypedOpFrame(resp[1:])
	if err != nil {
		t.Fatalf("DecodeTypedOpFrame: %v", err)
	}
	wantTypeID := mmokit.TypeIDOf(reflect.TypeFor[mmokit.OperationError]())
	if gotTypeID != wantTypeID {
		t.Errorf("typeID = %#x, want OperationError %#x", gotTypeID, wantTypeID)
	}
	var opErr mmokit.OperationError
	mustUnmarshal(t, gotBody, &opErr)
	if opErr.Code != mmokit.OpErrorHandlerFailed {
		t.Errorf("code = %d, want OpErrorHandlerFailed", opErr.Code)
	}
}

type fakeHandlerErr struct{}

func (fakeHandlerErr) Error() string { return "fake handler error" }

var errFakeHandler = fakeHandlerErr{}
