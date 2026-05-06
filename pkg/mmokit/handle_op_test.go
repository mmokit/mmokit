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

func TestRegisterOp_DuplicatePanics(t *testing.T) {
	t.Cleanup(ResetTypedOpRegistryForTest)
	ResetTypedOpRegistryForTest()

	RegisterOp[tOpReq, tOpRes](RouteGatewayLocal,
		func(_ *OpContext, _ *tOpReq) (*tOpRes, error) { return nil, nil })

	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("expected panic on duplicate RegisterOp[tOpReq], got nil")
		}
	}()
	RegisterOp[tOpReq, tOpRes](RouteGatewayLocal,
		func(_ *OpContext, _ *tOpReq) (*tOpRes, error) { return nil, nil })
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
