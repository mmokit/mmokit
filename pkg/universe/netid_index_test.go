package universe

import (
	"testing"

	"github.com/mlange-42/ark/ecs"
)

func TestNetIDIndex_Transitions(t *testing.T) {
	type tc struct {
		name     string
		existing EntityPresence
		incoming EntityPresence
		wantAct  TransitionAction
	}
	// Policy table from Spec 2 § Component 4.
	cases := []tc{
		// current: None
		{"none_to_live", PresenceNone, PresenceLive, ActionInstalled},
		{"none_to_shadow", PresenceNone, PresenceShadow, ActionInstalled},
		{"none_to_replica", PresenceNone, PresenceReplica, ActionInstalled},
		// current: Live
		{"live_to_live", PresenceLive, PresenceLive, ActionDuplicate},
		{"live_to_shadow", PresenceLive, PresenceShadow, ActionRejected},
		{"live_to_replica", PresenceLive, PresenceReplica, ActionRejected},
		// current: Shadow
		{"shadow_to_live", PresenceShadow, PresenceLive, ActionPromoted},
		{"shadow_to_shadow", PresenceShadow, PresenceShadow, ActionRejected},
		{"shadow_to_replica", PresenceShadow, PresenceReplica, ActionReplaced},
		// current: Replica
		{"replica_to_live", PresenceReplica, PresenceLive, ActionReplaced},
		{"replica_to_shadow", PresenceReplica, PresenceShadow, ActionReplaced},
		{"replica_to_replica", PresenceReplica, PresenceReplica, ActionUpdated},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			idx := newNetIDIndex()
			existingEnt := ecs.Entity{} // zero is fine for table-driven logic
			incomingEnt := ecs.Entity{}
			if c.existing != PresenceNone {
				idx.Enter(1, existingEnt, c.existing)
			}
			got := idx.Enter(1, incomingEnt, c.incoming)
			if got.Action != c.wantAct {
				t.Errorf("got %v, want %v", got.Action, c.wantAct)
			}
		})
	}
}

func TestNetIDIndex_LiveToReplicaDemote(t *testing.T) {
	idx := newNetIDIndex()
	ent := ecs.Entity{} // zero is fine for the policy check
	// Install as Live.
	idx.Enter(1, ent, PresenceLive)

	// Unsolicited Enter(Replica) on a Live slot must still be rejected —
	// no silent downgrade. Only the explicit Demote path may transition.
	res := idx.Enter(1, ent, PresenceReplica)
	if res.Action != ActionRejected {
		t.Fatalf("Enter(Replica) on Live must return ActionRejected, got %d", res.Action)
	}

	// Explicit Demote flips the slot and returns ActionUpdated, keeping
	// the same entity.
	res = idx.Demote(1, ent)
	if res.Action != ActionUpdated {
		t.Fatalf("Demote on Live must return ActionUpdated, got %d", res.Action)
	}
	_, presence, ok := idx.Lookup(1)
	if !ok || presence != PresenceReplica {
		t.Fatalf("after Demote, slot presence = %v ok=%v, want PresenceReplica true", presence, ok)
	}
}

func TestNetIDIndex_DemoteNonLiveRejected(t *testing.T) {
	idx := newNetIDIndex()
	ent := ecs.Entity{}

	// Demote on an empty slot rejects.
	if idx.Demote(42, ent).Action != ActionRejected {
		t.Fatal("Demote on empty slot must reject")
	}

	// Demote on a Shadow slot rejects (Shadow is pre-authority; the
	// source cell shouldn't see a Shadow for its own netID).
	idx.Enter(42, ent, PresenceShadow)
	if idx.Demote(42, ent).Action != ActionRejected {
		t.Fatal("Demote on Shadow slot must reject")
	}
}
