package universe_test

import (
	"bytes"
	"reflect"
	"testing"

	"github.com/zenion/mmoserver/pkg/mmokit"
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
	reqBody := pkguniverse.ReflectMarshal(&dispReq{X: 21})
	reqTypeID := mmokit.TypeIDOf(reflect.TypeFor[dispReq]())
	frame := pkguniverse.EncodeTypedOpFrame(reqTypeID, 1234, reqBody)

	// Strip the channel byte to get the payload the dispatcher expects.
	payload := frame[1:]
	respFrame := pkguniverse.DispatchTypedOpInbound(payload, &ops.OpContext{ConnID: 7, Username: "alice"}, nil)
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
	pkguniverse.ReflectUnmarshal(gotBody, &got)
	if got.Y != 42 {
		t.Errorf("response Y: got %d, want 42 (handler doubles input)", got.Y)
	}
}

func TestDispatchTypedOp_UnknownTypeID_ReturnsOperationError(t *testing.T) {
	t.Cleanup(mmokit.ResetTypedOpRegistryForTest)
	mmokit.ResetTypedOpRegistryForTest()

	// Build a frame at a typeID nobody registered.
	frame := pkguniverse.EncodeTypedOpFrame(0xDEADBEEF, 99, nil)
	resp := pkguniverse.DispatchTypedOpInbound(frame[1:], &ops.OpContext{}, nil)
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
	pkguniverse.ReflectUnmarshal(gotBody, &opErr)
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

	reqBody := pkguniverse.ReflectMarshal(&dispReq{X: 1})
	reqTypeID := mmokit.TypeIDOf(reflect.TypeFor[dispReq]())
	frame := pkguniverse.EncodeTypedOpFrame(reqTypeID, 5, reqBody)
	resp := pkguniverse.DispatchTypedOpInbound(frame[1:], &ops.OpContext{}, nil)
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
	pkguniverse.ReflectUnmarshal(gotBody, &opErr)
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

	reqBody := pkguniverse.ReflectMarshal(&dispReq{X: 1})
	reqTypeID := mmokit.TypeIDOf(reflect.TypeFor[dispReq]())
	frame := pkguniverse.EncodeTypedOpFrame(reqTypeID, 7, reqBody)
	resp := pkguniverse.DispatchTypedOpInbound(frame[1:], &ops.OpContext{}, nil)
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
	pkguniverse.ReflectUnmarshal(gotBody, &opErr)
	if opErr.Code != mmokit.OpErrorHandlerFailed {
		t.Errorf("code: got %d, want OpErrorHandlerFailed", opErr.Code)
	}
}

func TestDispatchTypedOp_TruncatedFrame_ReturnsNil(t *testing.T) {
	resp := pkguniverse.DispatchTypedOpInbound([]byte{0x01, 0x02}, &ops.OpContext{}, nil, )
	if resp != nil {
		t.Fatalf("truncated frame should drop silently (nil response), got %x", resp)
	}
}

// fakeCellOpRouter records the parameters DispatchTypedOpInbound forwards
// to it and returns a configurable response. Used to verify the dispatcher
// hands off RoutePlayerCell entries to the router with the right typeID,
// requestID, body, and OpContext (and that the body slice is a copy, not
// an alias of the inbound buffer).
type fakeCellOpRouter struct {
	called   bool
	reqType  reflect.Type
	resTID   uint32
	reqID    uint64
	body     []byte
	handler  any
	ctx      *ops.OpContext
	resp     []byte // returned synchronously
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

	reqBody := pkguniverse.ReflectMarshal(&dispReq{X: 5})
	reqTypeID := mmokit.TypeIDOf(reflect.TypeFor[dispReq]())
	wantResTID := mmokit.TypeIDOf(reflect.TypeFor[dispRes]())
	frame := pkguniverse.EncodeTypedOpFrame(reqTypeID, 99, reqBody)

	router := &fakeCellOpRouter{} // returns nil → "scheduled async"
	resp := pkguniverse.DispatchTypedOpInbound(frame[1:], &ops.OpContext{ConnID: 7, Username: "alice"}, router)

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

	reqBody := pkguniverse.ReflectMarshal(&dispReq{X: 1})
	reqTypeID := mmokit.TypeIDOf(reflect.TypeFor[dispReq]())
	frame := pkguniverse.EncodeTypedOpFrame(reqTypeID, 11, reqBody)

	resp := pkguniverse.DispatchTypedOpInbound(
		frame[1:],
		&ops.OpContext{ConnID: 1, Username: "ghost"},
		p, // *Process implements CellOpRouter
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
	pkguniverse.ReflectUnmarshal(gotBody, &opErr)
	if opErr.Code != mmokit.OpErrorHandlerFailed {
		t.Errorf("code = %d, want OpErrorHandlerFailed", opErr.Code)
	}
}

type fakeHandlerErr struct{}

func (fakeHandlerErr) Error() string { return "fake handler error" }

var errFakeHandler = fakeHandlerErr{}
