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
