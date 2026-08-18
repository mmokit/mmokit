package facadetest

import (
	"reflect"
	"slices"
	"testing"

	"github.com/mmokit/mmokit"
)

// testBroadcastableMsg is a typed message used for broadcast-registry tests.
// Production code marks broadcast-eligibility by registering the type via
// HandleAll; tests can call RegisterBroadcastType directly.
type testBroadcastableMsg struct {
	Caster mmokit.Entity
	Slot   uint8
}

// testInternalMsg stands in for a server-internal type — registered via
// HandleAllInternal in production, never added to the broadcast registry.
type testInternalMsg struct {
	Currency uint32
}

func TestTypeIDOf_Stable(t *testing.T) {
	a := mmokit.TypeIDOf(reflect.TypeFor[testBroadcastableMsg]())
	b := mmokit.TypeIDOf(reflect.TypeFor[testBroadcastableMsg]())
	if a != b {
		t.Fatalf("TypeIDOf is not stable: %d vs %d", a, b)
	}
	if a == 0 {
		t.Fatal("TypeIDOf returned 0 (suspicious)")
	}
}

func TestTypeIDOf_DistinguishesTypes(t *testing.T) {
	a := mmokit.TypeIDOf(reflect.TypeFor[testBroadcastableMsg]())
	b := mmokit.TypeIDOf(reflect.TypeFor[testInternalMsg]())
	if a == b {
		t.Fatalf("TypeIDOf collision between distinct types: %d", a)
	}
}

// TestHandleAllInternal_NoBroadcastRegistration pins that HandleAllInternal
// does NOT add the message type to the broadcast registry. Production
// KillCredit (registered via HandleAllInternal) relies on this — the
// registry membership is the single source of truth for broadcast
// eligibility now that the ServerOnly marker is gone.
func TestHandleAllInternal_NoBroadcastRegistration(t *testing.T) {
	type internalOnlyMsg struct{ X uint32 }

	p := mmokit.New(mmokit.Config{
		Mode:     "all",
		CellsX:   1,
		CellsY:   1,
		Headless: true,
	})
	mmokit.HandleAllInternal(p, func(target mmokit.Entity, msg *internalOnlyMsg) {})

	for _, ty := range p.Wire().BroadcastTypes() {
		if ty == reflect.TypeFor[internalOnlyMsg]() {
			t.Fatal("HandleAllInternal incorrectly registered internalOnlyMsg in broadcast registry")
		}
	}
}

// TestHandleAll_RegistersInBroadcastRegistry is the positive case: HandleAll
// adds the type to the registry so AoI auto-broadcast fires post-handler.
// Pinned alongside the negative test above so a future refactor that
// accidentally swaps the call sites can't pass silently.
func TestHandleAll_RegistersInBroadcastRegistry(t *testing.T) {
	type broadcastableMsg struct{ X uint32 }

	p := mmokit.New(mmokit.Config{
		Mode:     "all",
		CellsX:   1,
		CellsY:   1,
		Headless: true,
	})
	mmokit.HandleAll(p, func(target mmokit.Entity, msg *broadcastableMsg) {})

	if !slices.Contains(p.Wire().BroadcastTypes(), reflect.TypeFor[broadcastableMsg]()) {
		t.Fatal("HandleAll did not register broadcastableMsg in broadcast registry")
	}
}

func TestRegisterBroadcastType_Idempotent(t *testing.T) {
	p := newFacadeProcess(t)
	mmokit.RegisterBroadcastType(p, reflect.TypeFor[testBroadcastableMsg]())
	mmokit.RegisterBroadcastType(p, reflect.TypeFor[testBroadcastableMsg]())

	types := p.Wire().BroadcastTypes()
	count := 0
	for _, ty := range types {
		if ty == reflect.TypeFor[testBroadcastableMsg]() {
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
