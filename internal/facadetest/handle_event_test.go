package facadetest

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/mmokit/mmokit"
)

type testServerEventA struct {
	X int32
	Y int32
}

type testServerEventB struct {
	Name string
}

func TestRegisterEvent_TypeIDLookup(t *testing.T) {
	p := newFacadeProcess(t)
	mmokit.RegisterEvent[testServerEventA](p)
	mmokit.RegisterEvent[testServerEventB](p)

	idA := mmokit.TypeIDOf(reflect.TypeFor[testServerEventA]())
	idB := mmokit.TypeIDOf(reflect.TypeFor[testServerEventB]())

	gotA, okA := p.Wire().ServerEventType(idA)
	if !okA || gotA != reflect.TypeFor[testServerEventA]() {
		t.Fatalf("lookup A: got=%v ok=%v", gotA, okA)
	}
	gotB, okB := p.Wire().ServerEventType(idB)
	if !okB || gotB != reflect.TypeFor[testServerEventB]() {
		t.Fatalf("lookup B: got=%v ok=%v", gotB, okB)
	}
}

func TestRegisterEvent_RegisteredTypes(t *testing.T) {
	p := newFacadeProcess(t)
	mmokit.RegisterEvent[testServerEventA](p)
	mmokit.RegisterEvent[testServerEventB](p)

	// Membership, not a count: the enumeration also carries the six engine
	// events mmokit.New registers, and — until registries are per-process —
	// whatever a sibling test registered.
	types := p.Wire().ServerEventTypes()
	for _, want := range []reflect.Type{
		reflect.TypeFor[testServerEventA](),
		reflect.TypeFor[testServerEventB](),
	} {
		if !slices.Contains(types, want) {
			t.Errorf("%s missing from ServerEventTypes", want)
		}
	}
	if !slices.IsSortedFunc(types, func(a, b reflect.Type) int {
		return strings.Compare(a.String(), b.String())
	}) {
		t.Errorf("ServerEventTypes not sorted: %v", types)
	}
}
