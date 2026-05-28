package universe

import (
	"testing"

	"github.com/zenion/mmoserver/pkg/component"
)

// TestUpsertBorderReplicaFromTransfer_SeedsReplica confirms a TransferFrame
// fed through the context-materialize helper lands in the destination
// stage as a PresenceReplica entity — the synchronous-seed equivalent of
// what steady-state border replication would reach, but at commit time.
func TestUpsertBorderReplicaFromTransfer_SeedsReplica(t *testing.T) {
	stage := newTestStage(t) // cell {0,0} depth 0 → world coords == local coords

	frame := &TransferFrame{
		NetworkID:  4242,
		Epoch:      1,
		EntityType: 7,
		PosX:       100,
		PosY:       200,
		VelX:       5,
		Rotation:   1.5,
		Collider:   component.Collider{Radius: 10},
	}

	stage.upsertBorderReplicaFromTransfer(frame, MeshCellID("cell_1_0"))

	// Verify via the netID index. The Stage field is named netIDIdx and the
	// index method is Lookup(netID) (ecs.Entity, EntityPresence, bool).
	ent, presence, ok := stage.netIDIdx.Lookup(4242)
	if !ok {
		t.Fatalf("expected netID 4242 present after upsert")
	}
	if presence != PresenceReplica {
		t.Errorf("presence = %v, want PresenceReplica", presence)
	}
	// Confirm it's actually tagged as a Replica component too.
	if !stage.replicaMap.HasAll(ent) {
		t.Errorf("expected entity to carry a Replica component")
	}
	_ = ent
}
