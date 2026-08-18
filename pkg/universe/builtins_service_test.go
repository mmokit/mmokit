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
// int32, not int: these stand in for real wire types, and
// ValidateMessageType rejects a platform-width int.
type authLoginRequest struct{ X int32 }
type marketBrowseRequest struct{ Y int32 }
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

// A nil registry is what a fixture with no Process behind it holds, and it
// reads as empty — the successor to the old "typed-op hooks unwired" arm.
func TestFindOpByShortName_NilRegistry(t *testing.T) {
	if _, ok := findOpByShortName(nil, "anything"); ok {
		t.Errorf("findOpByShortName on a nil registry should return false")
	}
}

func TestFindOpByShortName_Match(t *testing.T) {
	wire := NewWireRegistry()
	wire.RegisterTypedOp(RouteGatewayLocal,
		reflect.TypeFor[authLoginRequest](),
		reflect.TypeFor[opShortNameOperationError](),
		func() {})

	got, ok := findOpByShortName(wire, "authLogin")
	if !ok {
		t.Fatalf("findOpByShortName(authLogin): not found")
	}
	if want := TypeIDOf(reflect.TypeFor[authLoginRequest]()); TypeIDOf(got.RequestType) != want {
		t.Errorf("request typeID: got %#x, want %#x", TypeIDOf(got.RequestType), want)
	}

	if _, ok := findOpByShortName(wire, "nope"); ok {
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

// TestRouteKindString covers the routing label the console renders. It used
// to come from a uint8 mirror plus a cached string; TypedOpEntry now carries
// the real RouteKind, so there is exactly one source for the label.
func TestRouteKindString(t *testing.T) {
	if got := RouteGatewayLocal.String(); got != "gateway-local" {
		t.Errorf("RouteGatewayLocal.String() = %q", got)
	}
	if got := RoutePlayerCell.String(); got != "player-cell" {
		t.Errorf("RoutePlayerCell.String() = %q", got)
	}
	if got := RouteKind(99).String(); got != "unknown" {
		t.Errorf("RouteKind(99).String() = %q, want unknown", got)
	}
}
