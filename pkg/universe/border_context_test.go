package universe

import (
	"testing"

	"github.com/mlange-42/ark/ecs"

	"github.com/zenion/mmoserver/pkg/component"
	"github.com/zenion/mmoserver/pkg/coords"
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

// TestCollectBorderContextFor_SplitFilterAndSource exercises the Stage method
// in isolation: the caller's shouldInclude predicate mimics the SPLIT case
// for "this child = quadrant 0" — local-in-quadrant-0 is authoritative and
// excluded, local-in-quadrant-1 is included with a sibling source id, and the
// replica is included with its own source attribution. Verifies the filter,
// the source-id plumbing, and that each blob round-trips through
// UnmarshalTransferFrame.
func TestCollectBorderContextFor_SplitFilterAndSource(t *testing.T) {
	stage := newTestStage(t) // cell {0,0} depth 0; size = coords.CellSize
	half := stage.cell.Size(coords.CellSize) / 2

	// Local in quadrant 0 (BL) — authoritative on this child, must be excluded.
	q0 := stage.Spawn(component.Position{X: 10, Y: 10})
	if q0.NetID() == 0 {
		t.Fatal("expected q0 spawn to succeed")
	}
	// Local in quadrant 1 (BR) — sibling-quadrant context, must be included.
	q1 := stage.Spawn(component.Position{X: half + 100, Y: 10})
	if q1.NetID() == 0 {
		t.Fatal("expected q1 spawn to succeed")
	}

	// A replica from a far-off neighbor cell — outer-neighbor context.
	stage.upsertBorderReplicaFromTransfer(&TransferFrame{
		NetworkID:  9001,
		EntityType: 1,
		PosX:       5,
		PosY:       5,
		Collider:   component.Collider{Radius: 5},
	}, MeshCellID("cell_9_9"))

	const siblingID = "cell_d1_1_0"
	shouldInclude := func(e ecs.Entity, px, py float32, isRep bool, repSrc string) (bool, string) {
		if isRep {
			return true, repSrc
		}
		// Local authoritative-on-this-child (quadrant 0) → exclude.
		xi := 0
		if px >= half {
			xi = 1
		}
		yi := 0
		if py >= half {
			yi = 1
		}
		if (xi | (yi << 1)) == 0 {
			return false, ""
		}
		return true, siblingID
	}

	entries, err := stage.collectBorderContextFor(0, shouldInclude)
	if err != nil {
		t.Fatalf("collectBorderContextFor: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 context entries (q1 local + replica), got %d", len(entries))
	}

	// Decode each entry; index by netID so order doesn't matter.
	bySource := map[uint32]string{}
	for _, ent := range entries {
		frame, derr := UnmarshalTransferFrame(ent.Entity)
		if derr != nil {
			t.Fatalf("decode context entry: %v", derr)
		}
		bySource[frame.NetworkID] = ent.SourceCellId
	}

	repSrc, ok := bySource[9001]
	if !ok {
		t.Fatalf("expected replica netID 9001 in context entries; got %v", bySource)
	}
	if repSrc != "cell_9_9" {
		t.Errorf("replica SourceCellId = %q, want cell_9_9", repSrc)
	}

	localSrc, ok := bySource[q1.NetID()]
	if !ok {
		t.Fatalf("expected q1 local netID %d in context entries; got %v", q1.NetID(), bySource)
	}
	if localSrc != siblingID {
		t.Errorf("q1 local SourceCellId = %q, want %q", localSrc, siblingID)
	}

	if _, present := bySource[q0.NetID()]; present {
		t.Errorf("q0 local netID %d should have been excluded (authoritative)", q0.NetID())
	}
}
