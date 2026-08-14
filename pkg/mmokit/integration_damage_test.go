// integration_damage_test.go — end-to-end demonstration of the foundation
// API: Damage as its own message type, applied via Send, mutating Health,
// with cross-cell routing proven via the loopback bridge. No game-side
// cross-cell awareness anywhere.
package mmokit_test

import (
	"reflect"
	"testing"
	"time"

	"github.com/mlange-42/ark/ecs"
	"github.com/mmokit/mmokit/pkg/mmokit"
	pkguniverse "github.com/mmokit/mmokit/pkg/universe"
)

type intHealth struct {
	Current float32
	Max     float32
}

type intDamage struct {
	Amount float32
	Dealt  float32
}

const intKind uint8 = 200

// registerIntKind registers intKind with intHealth as a kind component.
func registerIntKind(t *testing.T, stage *pkguniverse.Stage) {
	t.Helper()
	def := pkguniverse.EntityKindDef{Kind: uint8(intKind), Name: "IntKind"}
	w := stage.ECSWorld()
	pkguniverse.KindComponentByID(
		&def, w,
		ecs.ComponentID[intHealth](w),
		reflect.TypeFor[intHealth](),
		pkguniverse.KindComponentRequired,
	)
	stage.RegisterEntityKind(def)
}

func TestIntegration_Damage_SameCell(t *testing.T) {
	stage, _ := newTestStage(t)
	registerIntKind(t, stage)

	mmokit.Handle(stage, func(target mmokit.Entity, msg *intDamage) {
		h := mmokit.Get[intHealth](target)
		if h == nil {
			return
		}
		h.Current -= msg.Amount
		msg.Dealt = msg.Amount
	})

	target := stage.Spawn(
		mmokit.Position{},
		mmokit.EntityKind{Type: intKind},
		intHealth{Current: 100, Max: 100},
	)

	target.Send(&intDamage{Amount: 25})

	if got := mmokit.Get[intHealth](target).Current; got != 75 {
		t.Fatalf("Health.Current = %v, want 75", got)
	}
}

func TestIntegration_Damage_CrossCell(t *testing.T) {
	cellA, cellB, drain := newTwoCellLoopback(t)
	registerIntKind(t, cellA)
	registerIntKind(t, cellB)

	// Authoritative on cell A.
	mmokit.Handle(cellA, func(target mmokit.Entity, msg *intDamage) {
		h := mmokit.Get[intHealth](target)
		if h == nil {
			return
		}
		h.Current -= msg.Amount
		msg.Dealt = msg.Amount
	})

	target := cellA.Spawn(
		mmokit.Position{},
		mmokit.EntityKind{Type: intKind},
		intHealth{Current: 100, Max: 100},
	)

	pushBorderReplicaTo(t, cellA, cellB, target.NetID())

	// Caller on cell B has the replica; Send routes to cell A.
	eOnB := mmokit.EntityByNetID(cellB, target.NetID())
	eOnB.Send(&intDamage{Amount: 25})

	drain(100 * time.Millisecond)

	if got := mmokit.Get[intHealth](target).Current; got != 75 {
		t.Fatalf("after cross-cell Send: Health.Current = %v, want 75", got)
	}
}
