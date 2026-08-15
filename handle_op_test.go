package mmokit

import (
	"reflect"
	"testing"
)

func TestRouteKind_StringNames(t *testing.T) {
	cases := []struct {
		k    RouteKind
		want string
	}{
		{RouteGatewayLocal, "gateway-local"},
		{RoutePlayerCell, "player-cell"},
		{RouteKind(99), "unknown"},
	}
	for _, c := range cases {
		if got := c.k.String(); got != c.want {
			t.Errorf("RouteKind(%d).String() = %q, want %q", c.k, got, c.want)
		}
	}
}

type tOpReq struct {
	X int32
}

type tOpRes struct {
	Y int32
}

func TestRegisterOp_LookupByTypeID(t *testing.T) {
	t.Cleanup(ResetTypedOpRegistryForTest)
	ResetTypedOpRegistryForTest()

	RegisterOp[tOpReq, tOpRes](RouteGatewayLocal,
		func(_ *OpContext, req *tOpReq) (*tOpRes, error) {
			return &tOpRes{Y: req.X * 2}, nil
		})

	reqID := TypeIDOf(reflect.TypeFor[tOpReq]())
	resID := TypeIDOf(reflect.TypeFor[tOpRes]())

	entry, ok := LookupTypedOp(reqID)
	if !ok {
		t.Fatalf("LookupTypedOp(%#x): not found", reqID)
	}
	if entry.Kind != RouteGatewayLocal {
		t.Errorf("Kind: got %v, want RouteGatewayLocal", entry.Kind)
	}
	if entry.ResponseTypeID != resID {
		t.Errorf("ResponseTypeID: got %#x, want %#x", entry.ResponseTypeID, resID)
	}
	if entry.RequestType != reflect.TypeFor[tOpReq]() {
		t.Errorf("RequestType: got %v", entry.RequestType)
	}
	if entry.ResponseType != reflect.TypeFor[tOpRes]() {
		t.Errorf("ResponseType: got %v", entry.ResponseType)
	}
}

func TestRegisterOp_SameShapeIsIdempotent(t *testing.T) {
	// Re-registering the same Request type with the same Kind + Response
	// type is allowed — last-writer-wins on the handler closure. Mirrors
	// HandleClient's idempotent contract so games can call RegisterOp from
	// setup functions that fire many times in tests without juggling
	// reset boilerplate.
	t.Cleanup(ResetTypedOpRegistryForTest)
	ResetTypedOpRegistryForTest()

	RegisterOp[tOpReq, tOpRes](RouteGatewayLocal,
		func(_ *OpContext, _ *tOpReq) (*tOpRes, error) { return &tOpRes{Y: 1}, nil })
	// Re-register: must NOT panic, and the second handler should win.
	RegisterOp[tOpReq, tOpRes](RouteGatewayLocal,
		func(_ *OpContext, _ *tOpReq) (*tOpRes, error) { return &tOpRes{Y: 99}, nil })

	entry, ok := LookupTypedOp(TypeIDOf(reflect.TypeFor[tOpReq]()))
	if !ok {
		t.Fatal("entry missing after re-register")
	}
	// Invoke handler via reflection (same shape the dispatcher uses) and
	// confirm the SECOND handler ran — last-writer-wins.
	hv := reflect.ValueOf(entry.Handler)
	results := hv.Call([]reflect.Value{
		reflect.ValueOf(&OpContext{}), reflect.ValueOf(&tOpReq{}),
	})
	resPtr := results[0].Interface().(*tOpRes)
	if resPtr.Y != 99 {
		t.Errorf("re-register did not replace handler: got Y=%d, want 99", resPtr.Y)
	}
}

type tOpRes2 struct {
	Z int64
}

func TestRegisterOp_DifferentShapePanics(t *testing.T) {
	// Re-registering the same Request type with a different Response
	// type (or a different Kind) is a wire-schema-changing programmer
	// error — must panic.
	t.Cleanup(ResetTypedOpRegistryForTest)
	ResetTypedOpRegistryForTest()

	RegisterOp[tOpReq, tOpRes](RouteGatewayLocal,
		func(_ *OpContext, _ *tOpReq) (*tOpRes, error) { return nil, nil })

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on RegisterOp with different Response type")
		}
	}()
	RegisterOp[tOpReq, tOpRes2](RouteGatewayLocal,
		func(_ *OpContext, _ *tOpReq) (*tOpRes2, error) { return nil, nil })
}

func TestOperationError_TypeIDStable(t *testing.T) {
	// The package init() registers OperationError as a framework typed-op
	// response. LookupServerEventType must resolve its typeID back to the
	// concrete type — proves the init ran.
	id := TypeIDOf(reflect.TypeFor[OperationError]())
	gotType, ok := LookupServerEventType(id)
	if !ok {
		t.Fatalf("LookupServerEventType(%#x): not registered (init didn't run?)", id)
	}
	wantType := reflect.TypeFor[OperationError]()
	if gotType != wantType {
		t.Errorf("LookupServerEventType(%#x) = %v, want %v", id, gotType, wantType)
	}
}
