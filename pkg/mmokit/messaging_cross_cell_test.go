package mmokit_test

import (
	"testing"
	"time"

	"github.com/zenion/mmoserver/pkg/mmokit"
)

type crossDamage struct {
	Amount float32
	Dealt  float32
}

// Two-cell loopback: spawn an entity on cell A, push a border replica onto
// cell B, then call Send from cell B targeting the replica's NetID. Verify
// the handler runs on cell A (the authoritative cell), NOT on cell B.
func TestCrossCellSend_RoutesToAuthoritativeCell(t *testing.T) {
	cellA, cellB, drain := newTwoCellLoopback(t)

	registerTestKind(t, cellA)
	registerTestKind(t, cellB)

	// Spawn live on cell A.
	a := mmokit.Spawn(cellA, testKindID, mmokit.Pos{X: 0, Y: 0})

	// Push a border replica of `a` onto cell B.
	pushBorderReplicaTo(t, cellA, cellB, a.NetID())

	// Register handlers on BOTH cells. Only A's should fire.
	var aDealt, bDealt float32
	mmokit.Handle(cellA, func(target mmokit.Entity, msg *crossDamage) {
		msg.Dealt = msg.Amount
		aDealt = msg.Amount
	})
	mmokit.Handle(cellB, func(target mmokit.Entity, msg *crossDamage) {
		bDealt = msg.Amount
	})

	// Lookup from cell B (target is a replica there).
	eOnB := mmokit.EntityByNetID(cellB, a.NetID())
	if !eOnB.Alive() {
		t.Fatal("replica should resolve as Alive on cell B")
	}
	if eOnB.Local() {
		t.Fatal("replica should NOT report Local() == true on cell B")
	}

	// Send from cell B — should route cross-cell to cell A.
	eOnB.Send(&crossDamage{Amount: 25})

	drain(100 * time.Millisecond)

	if aDealt != 25 {
		t.Fatalf("cell A handler did not run: aDealt=%v, want 25", aDealt)
	}
	if bDealt != 0 {
		t.Fatalf("cell B handler ran (bDealt=%v); Send should have routed cross-cell only", bDealt)
	}
}
