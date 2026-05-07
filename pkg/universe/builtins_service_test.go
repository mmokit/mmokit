package universe

import (
	"reflect"
	"strings"
	"testing"
)

// Local fixtures for opShortName — covers the three variants the helper
// is shaped around: the conventional "<Verb>Request" suffix, a service-
// prefixed type, and a non-Request type (e.g. OperationError) that
// should still produce a usable handle.
type authLoginRequest struct{ X int }
type marketBrowseRequest struct{ Y int }
type opShortNameOperationError struct{ Code uint32 }

func TestOpShortName(t *testing.T) {
	cases := []struct {
		t    reflect.Type
		want string
	}{
		{reflect.TypeFor[authLoginRequest](), "authLogin"},
		{reflect.TypeFor[marketBrowseRequest](), "marketBrowse"},
		{reflect.TypeFor[opShortNameOperationError](), "opShortNameOperationError"},
	}
	for _, c := range cases {
		got := opShortName(c.t)
		if got != c.want {
			t.Errorf("opShortName(%s) = %q, want %q", c.t.String(), got, c.want)
		}
	}
}

func TestPrintStructFields(t *testing.T) {
	type sample struct {
		Foo int32
		Bar string
	}
	var sb strings.Builder
	printStructFields(&sb, reflect.TypeFor[sample](), "  ")
	out := sb.String()
	if !strings.Contains(out, "Foo: int32") || !strings.Contains(out, "Bar: string") {
		t.Errorf("printStructFields missing fields:\n%s", out)
	}

	// Empty struct path.
	sb.Reset()
	type empty struct{}
	printStructFields(&sb, reflect.TypeFor[empty](), "")
	if !strings.Contains(sb.String(), "(no fields)") {
		t.Errorf("printStructFields empty: %q", sb.String())
	}

	// Pointer path — reflect.Type.Kind() == Pointer should be unwrapped.
	sb.Reset()
	printStructFields(&sb, reflect.TypeFor[*sample](), "")
	if !strings.Contains(sb.String(), "Foo: int32") {
		t.Errorf("printStructFields pointer: %q", sb.String())
	}

	// Non-struct path.
	sb.Reset()
	printStructFields(&sb, reflect.TypeFor[int](), "")
	if !strings.Contains(sb.String(), "non-struct") {
		t.Errorf("printStructFields non-struct: %q", sb.String())
	}
}

func TestFindOpByShortName_NoHook(t *testing.T) {
	prev := TypedOpHooks.ListTypedOps
	TypedOpHooks.ListTypedOps = nil
	t.Cleanup(func() { TypedOpHooks.ListTypedOps = prev })

	if _, ok := findOpByShortName("anything"); ok {
		t.Errorf("findOpByShortName with nil hook should return false")
	}
}

func TestFindOpByShortName_Match(t *testing.T) {
	prev := TypedOpHooks.ListTypedOps
	TypedOpHooks.ListTypedOps = func() []TypedOpInfo {
		return []TypedOpInfo{
			{
				Kind:         0,
				KindName:     "gateway-local",
				RequestType:  reflect.TypeFor[authLoginRequest](),
				ResponseType: reflect.TypeFor[opShortNameOperationError](),
				RequestID:    0xAAAA,
			},
		}
	}
	t.Cleanup(func() { TypedOpHooks.ListTypedOps = prev })

	got, ok := findOpByShortName("authLogin")
	if !ok {
		t.Fatalf("findOpByShortName(authLogin): not found")
	}
	if got.RequestID != 0xAAAA {
		t.Errorf("RequestID: got %#x, want 0xAAAA", got.RequestID)
	}

	if _, ok := findOpByShortName("nope"); ok {
		t.Errorf("findOpByShortName(nope) should miss")
	}
}

// TestServiceKindOf covers the package-prefix extraction used by
// `service list` to group typed ops by service kind.
func TestServiceKindOf(t *testing.T) {
	// Local types live in package `universe` — Type.String() yields
	// "universe.authLoginRequest", so the extracted kind is "universe".
	got := serviceKindOf(reflect.TypeFor[authLoginRequest]())
	if got != "universe" {
		t.Errorf("serviceKindOf(local type) = %q, want %q", got, "universe")
	}

	// Pointer types are reported as "*pkg.Name" — LastIndex still finds
	// the dot, so the prefix is "*universe" (acceptable; nothing in the
	// real registry is a pointer-to-Request anyway).
	got = serviceKindOf(reflect.TypeFor[*authLoginRequest]())
	if !strings.Contains(got, "universe") {
		t.Errorf("serviceKindOf(pointer) = %q, want substring %q", got, "universe")
	}

	// Builtin (no package) — empty.
	got = serviceKindOf(reflect.TypeFor[int]())
	if got != "" {
		t.Errorf("serviceKindOf(int) = %q, want empty", got)
	}
}

// TestRoutingString covers the small helper that emits the routing
// label. Prefers e.KindName when populated (the live path); falls back
// to the uint8 lookup when KindName is empty.
func TestRoutingString(t *testing.T) {
	// Live path: KindName already populated by mmokit.init.
	if got := routingString(0, "gateway-local"); got != "gateway-local" {
		t.Errorf("routingString(_, gateway-local) = %q", got)
	}
	if got := routingString(1, "player-cell"); got != "player-cell" {
		t.Errorf("routingString(_, player-cell) = %q", got)
	}

	// Empty-KindName fallback: relies on TypedOpHooks.RouteGatewayLocal.
	prev := TypedOpHooks.RouteGatewayLocal
	TypedOpHooks.RouteGatewayLocal = 0
	t.Cleanup(func() { TypedOpHooks.RouteGatewayLocal = prev })

	if got := routingString(0, ""); got != "gateway-local" {
		t.Errorf("routingString(0, empty) = %q, want gateway-local", got)
	}
	if got := routingString(99, ""); got != "unknown" {
		t.Errorf("routingString(99, empty) = %q, want unknown", got)
	}
}
