package mmokit_test

import (
	"reflect"
	"testing"

	"github.com/mmokit/mmokit/pkg/mmokit"
)

type testServerEventA struct {
	X int32
	Y int32
}

type testServerEventB struct {
	Name string
}

func TestRegisterEvent_TypeIDLookup(t *testing.T) {
	mmokit.ResetServerEventRegistryForTest()
	mmokit.RegisterEvent[testServerEventA]()
	mmokit.RegisterEvent[testServerEventB]()

	idA := mmokit.TypeIDOf(reflect.TypeFor[testServerEventA]())
	idB := mmokit.TypeIDOf(reflect.TypeFor[testServerEventB]())

	gotA, okA := mmokit.LookupServerEventType(idA)
	if !okA || gotA != reflect.TypeFor[testServerEventA]() {
		t.Fatalf("lookup A: got=%v ok=%v", gotA, okA)
	}
	gotB, okB := mmokit.LookupServerEventType(idB)
	if !okB || gotB != reflect.TypeFor[testServerEventB]() {
		t.Fatalf("lookup B: got=%v ok=%v", gotB, okB)
	}
}

func TestRegisterEvent_RegisteredTypes(t *testing.T) {
	mmokit.ResetServerEventRegistryForTest()
	mmokit.RegisterEvent[testServerEventA]()
	mmokit.RegisterEvent[testServerEventB]()

	types := mmokit.RegisteredServerEventTypes()
	if len(types) != 2 {
		t.Fatalf("got %d types, want 2", len(types))
	}
}
