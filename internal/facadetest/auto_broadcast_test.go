// auto_broadcast_test.go — cross-cell broadcast-queue integration test.
//
// Same-cell + server-internal variants live in internal/game/auto_broadcast_test.go
// because they exercise the production Damage / KillCredit verbs. This file
// covers the cross-cell case using the loopback bridge — purely framework
// behavior, no game-side handler chain involved.
package facadetest

import (
	"reflect"
	"testing"
	"time"

	"github.com/mmokit/mmokit"
)

// abDamage is a broadcast-eligible test message with an Entity anchor field.
// Stage-scoped Handle no longer touches the broadcast registry — the test
// registers eligibility directly via RegisterBroadcastType (production code
// goes through HandleAll, which does this for you).
type abDamage struct {
	Amount float32
	Dealt  float32
	Source mmokit.Entity
}

// TestAutoBroadcast_CrossCell_BothSidesEnqueue verifies that Send to a
// replica produces:
//
//   - a source-cell pre-handler push on the sending stage (so caster-local
//     viewers see the broadcast even though the handler runs on dest)
//   - a dest-cell post-handler push after the handler runs (so target-local
//     viewers see the authoritative result fields)
func TestAutoBroadcast_CrossCell_BothSidesEnqueue(t *testing.T) {
	cellA, cellB, drain := newTwoCellLoopback(t)

	// Stage-scoped Handle no longer marks T broadcast-eligible — register
	// the type explicitly. Production wiring goes through HandleAll which
	// does this automatically.
	mmokit.RegisterBroadcastType(cellA, reflect.TypeFor[abDamage]())

	// Handler authoritative on cellB (target's home). Mutates Dealt so we
	// can verify the dest-cell push carries post-handler state.
	mmokit.Handle(cellB, func(target mmokit.Entity, msg *abDamage) {
		msg.Dealt = msg.Amount
	})

	// Spawn target on B (authoritative). Push a border replica to A so
	// A.Send routes via the bridge.
	targetNetID := uint32(101)
	spawnTestEntity(t, cellB, targetNetID)
	pushBorderReplicaTo(t, cellB, cellA, targetNetID)

	// Spawn caster on A so the Source anchor resolves locally on A.
	casterNetID := uint32(202)
	spawnTestEntity(t, cellA, casterNetID)
	casterOnA := mmokit.EntityByNetID(cellA, casterNetID)

	// Pre-spawn a replica of the caster on B too — so the dest-cell push
	// has a locally-resolvable anchor for the caster (otherwise dest only
	// resolves the target and we get a 1-anchor broadcast there; both
	// configurations are valid, but two-anchor is the more interesting
	// post-handler shape).
	pushBorderReplicaTo(t, cellA, cellB, casterNetID)

	// Drain any setup-time events.
	cellA.BroadcastQueue().Drain()
	cellB.BroadcastQueue().Drain()

	targetOnA := mmokit.EntityByNetID(cellA, targetNetID) // replica
	targetOnA.Send(&abDamage{Amount: 25, Source: casterOnA})

	// Source-cell A: pre-handler broadcast pushed BEFORE the bridge dispatch.
	eventsA := cellA.BroadcastQueue().Drain()
	if len(eventsA) != 1 {
		t.Fatalf("source cell broadcast: got %d events, want 1", len(eventsA))
	}
	wantTypeID := mmokit.TypeIDOf(reflect.TypeFor[abDamage]())
	if eventsA[0].TypeID != wantTypeID {
		t.Errorf("source typeID = %d, want %d", eventsA[0].TypeID, wantTypeID)
	}
	if len(eventsA[0].Anchors) == 0 {
		t.Errorf("source anchors empty (expected at least target=%d resolvable as replica on A)", targetNetID)
	}

	// Drive the bridge so the cross-cell action delivers to B.
	drain(50 * time.Millisecond)

	// Dest cell B: handler runs, then post-handler broadcast pushed.
	eventsB := cellB.BroadcastQueue().Drain()
	if len(eventsB) != 1 {
		t.Fatalf("dest cell broadcast: got %d events, want 1", len(eventsB))
	}
	if eventsB[0].TypeID != wantTypeID {
		t.Errorf("dest typeID = %d, want %d", eventsB[0].TypeID, wantTypeID)
	}
	if len(eventsB[0].Anchors) == 0 {
		t.Errorf("dest anchors empty (expected target=%d at minimum)", targetNetID)
	}
}
