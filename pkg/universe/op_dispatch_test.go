package universe_test

import (
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
	respFrame := pkguniverse.DispatchTypedOpInbound(payload, &ops.OpContext{ConnID: 7, Username: "alice"})
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
	resp := pkguniverse.DispatchTypedOpInbound(frame[1:], &ops.OpContext{})
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
	resp := pkguniverse.DispatchTypedOpInbound(frame[1:], &ops.OpContext{})
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

func TestDispatchTypedOp_RoutePlayerCell_NotYetSupported(t *testing.T) {
	t.Cleanup(mmokit.ResetTypedOpRegistryForTest)
	mmokit.ResetTypedOpRegistryForTest()

	mmokit.RegisterOp[dispReq, dispRes](mmokit.RoutePlayerCell,
		func(_ *mmokit.OpContext, _ *dispReq) (*dispRes, error) { return &dispRes{}, nil })

	reqBody := pkguniverse.ReflectMarshal(&dispReq{X: 1})
	reqTypeID := mmokit.TypeIDOf(reflect.TypeFor[dispReq]())
	frame := pkguniverse.EncodeTypedOpFrame(reqTypeID, 7, reqBody)
	resp := pkguniverse.DispatchTypedOpInbound(frame[1:], &ops.OpContext{})
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
	resp := pkguniverse.DispatchTypedOpInbound([]byte{0x01, 0x02}, &ops.OpContext{})
	if resp != nil {
		t.Fatalf("truncated frame should drop silently (nil response), got %x", resp)
	}
}

type fakeHandlerErr struct{}

func (fakeHandlerErr) Error() string { return "fake handler error" }

var errFakeHandler = fakeHandlerErr{}
