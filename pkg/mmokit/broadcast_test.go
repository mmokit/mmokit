package mmokit_test

import (
	"reflect"
	"testing"

	"github.com/zenion/mmoserver/pkg/mmokit"
)

// testBroadcastableMsg is a typed message with no ServerOnly() marker — it
// IS broadcast-eligible.
type testBroadcastableMsg struct {
	Caster mmokit.Entity
	Slot   uint8
}

// testServerOnlyMsg has the ServerOnly() marker — it is NOT broadcast-eligible.
type testServerOnlyMsg struct {
	Currency uint32
}

func (testServerOnlyMsg) ServerOnly() {}

func TestIsServerOnly(t *testing.T) {
	if mmokit.IsServerOnly(reflect.TypeOf(testBroadcastableMsg{})) {
		t.Fatal("testBroadcastableMsg should NOT be ServerOnly")
	}
	if !mmokit.IsServerOnly(reflect.TypeOf(testServerOnlyMsg{})) {
		t.Fatal("testServerOnlyMsg SHOULD be ServerOnly")
	}
}

func TestTypeIDOf_Stable(t *testing.T) {
	a := mmokit.TypeIDOf(reflect.TypeOf(testBroadcastableMsg{}))
	b := mmokit.TypeIDOf(reflect.TypeOf(testBroadcastableMsg{}))
	if a != b {
		t.Fatalf("TypeIDOf is not stable: %d vs %d", a, b)
	}
	if a == 0 {
		t.Fatal("TypeIDOf returned 0 (suspicious)")
	}
}

func TestTypeIDOf_DistinguishesTypes(t *testing.T) {
	a := mmokit.TypeIDOf(reflect.TypeOf(testBroadcastableMsg{}))
	b := mmokit.TypeIDOf(reflect.TypeOf(testServerOnlyMsg{}))
	if a == b {
		t.Fatalf("TypeIDOf collision between distinct types: %d", a)
	}
}

func TestRegisterBroadcastType_Idempotent(t *testing.T) {
	mmokit.RegisterBroadcastType(reflect.TypeOf(testBroadcastableMsg{}))
	mmokit.RegisterBroadcastType(reflect.TypeOf(testBroadcastableMsg{}))

	types := mmokit.BroadcastTypes()
	count := 0
	for _, ty := range types {
		if ty == reflect.TypeOf(testBroadcastableMsg{}) {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("RegisterBroadcastType not idempotent: got %d entries, want 1", count)
	}
}
