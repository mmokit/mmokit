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
