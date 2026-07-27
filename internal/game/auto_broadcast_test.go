package game

import (
	"reflect"
	"testing"

	"github.com/zenion/mmoserver/pkg/mmokit"
	pkguniverse "github.com/zenion/mmoserver/pkg/universe"
)

// TestAutoBroadcast_SameCell_PostHandler verifies that target.Send(&Damage{...})
// pushes the post-handler msg into the stage's broadcast queue with the
// expected typeID, anchors (target + caster), and a body that decodes back
// to the post-handler value (Dealt populated by the handler before the
// queue push).
func TestAutoBroadcast_SameCell_PostHandler(t *testing.T) {
	gw, _ := newTestGameWorld()
	gw.stage.SetStateByName("game.GameWorld", gw)
	mmokit.Handle(gw.stage, damageHandler)

	target := newTestShip(t, gw, 101, 100, 0)
	caster := newTestShip(t, gw, 202, 100, 0)
	targetE := mmokit.EntityByNetID(gw.stage, target)
	casterE := mmokit.EntityByNetID(gw.stage, caster)

	// Drain any events queued during test setup before we exercise the path.
	gw.stage.BroadcastQueue().Drain()

	targetE.Send(&Damage{Amount: 25, Source: casterE, Slot: 0, AbilityType: 1})

	events := gw.stage.BroadcastQueue().Drain()
	if len(events) != 1 {
		t.Fatalf("broadcast queue: got %d events, want 1", len(events))
	}
	e := events[0]

	wantTypeID := mmokit.TypeIDOf(reflect.TypeFor[Damage]())
	if e.TypeID != wantTypeID {
		t.Errorf("typeID = %d, want %d", e.TypeID, wantTypeID)
	}

	if len(e.Anchors) != 2 {
		t.Errorf("anchors: got %d (%v), want 2 (target+caster)", len(e.Anchors), e.Anchors)
	}
	// First anchor must be the target/receiver.
	if len(e.Anchors) > 0 && e.Anchors[0] != target {
		t.Errorf("anchors[0] = %d, want target=%d", e.Anchors[0], target)
	}

	// Decode the body back and confirm post-handler state (Dealt populated).
	var decoded Damage
	if err := pkguniverse.ReflectUnmarshalOnStage(gw.stage, e.Body, &decoded); err != nil {
		t.Fatalf("ReflectUnmarshalOnStage: %v", err)
	}
	if decoded.Dealt <= 0 {
		t.Errorf("Dealt = %v, want >0 (handler should have populated it before broadcast push)", decoded.Dealt)
	}
}

// TestAutoBroadcast_Internal_DoesNotBroadcast verifies that KillCredit —
// registered via mmokit.HandleAllInternal in RegisterDeathVerbs — does NOT
// push to the broadcast queue. The test wires the handler with
// stage-scoped Handle, which never touches the broadcast registry; the
// production wire-up via HandleAllInternal preserves the same property by
// explicitly skipping RegisterBroadcastType.
//
// Currency rewards are server-internal accounting; clients receive a
// CurrencyUpdate event from the handler, not the message itself.
func TestAutoBroadcast_Internal_DoesNotBroadcast(t *testing.T) {
	gw, _ := newTestGameWorld()
	gw.stage.SetStateByName("game.GameWorld", gw)
	mmokit.Handle(gw.stage, killCreditHandler)

	killer := newTestPlayerShip(t, gw, 303, "alice")
	killerE := mmokit.EntityByNetID(gw.stage, killer)

	gw.stage.BroadcastQueue().Drain() // baseline

	killerE.Send(&KillCredit{Currency: 1, Amount: 50})

	events := gw.stage.BroadcastQueue().Drain()
	if len(events) != 0 {
		t.Fatalf("HandleAllInternal should suppress broadcast: got %d events (%v)", len(events), events)
	}
}
