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

func TestExtractAnchors_Dedup(t *testing.T) {
	type Msg struct {
		Caster mmokit.Entity
		Source mmokit.Entity
		Slot   uint8
	}
	stage, _ := newTestStage(t)
	spawnTestEntity(t, stage, 100)
	spawnTestEntity(t, stage, 200)

	e100 := mmokit.EntityByNetID(stage, 100)
	e200 := mmokit.EntityByNetID(stage, 200)

	msg := Msg{Caster: e100, Source: e100, Slot: 1}
	anchors := mmokit.ExtractAnchors(&msg, e200)

	// Expect: [200 (target), 100 (Caster)]; Source dedups to 100.
	if len(anchors) != 2 || anchors[0] != 200 || anchors[1] != 100 {
		t.Fatalf("anchors: got %v, want [200 100]", anchors)
	}
}

func TestExtractAnchors_SkipZero(t *testing.T) {
	type Msg struct {
		Killer mmokit.Entity // intentionally zero (unattributed)
	}
	stage, _ := newTestStage(t)
	spawnTestEntity(t, stage, 50)
	target := mmokit.EntityByNetID(stage, 50)

	anchors := mmokit.ExtractAnchors(&Msg{}, target)
	if len(anchors) != 1 || anchors[0] != 50 {
		t.Fatalf("anchors: got %v, want [50] (zero Killer dropped)", anchors)
	}
}

func TestExtractAnchors_NestedStructs(t *testing.T) {
	type Inner struct {
		Witness mmokit.Entity
	}
	type Msg struct {
		Caster mmokit.Entity
		Inner  Inner
	}
	stage, _ := newTestStage(t)
	spawnTestEntity(t, stage, 10)
	spawnTestEntity(t, stage, 20)
	spawnTestEntity(t, stage, 30)

	e10 := mmokit.EntityByNetID(stage, 10)
	e20 := mmokit.EntityByNetID(stage, 20)
	e30 := mmokit.EntityByNetID(stage, 30)

	msg := Msg{Caster: e20, Inner: Inner{Witness: e30}}
	anchors := mmokit.ExtractAnchors(&msg, e10)

	if len(anchors) != 3 || anchors[0] != 10 || anchors[1] != 20 || anchors[2] != 30 {
		t.Fatalf("anchors: got %v, want [10 20 30]", anchors)
	}
}
