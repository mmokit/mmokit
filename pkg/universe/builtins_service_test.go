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
