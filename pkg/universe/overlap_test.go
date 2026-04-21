package universe

import (
	"testing"

	"github.com/mlange-42/ark/ecs"

	"github.com/zenion/mmoserver/pkg/component"
	"github.com/zenion/mmoserver/pkg/coords"
	"github.com/zenion/mmoserver/pkg/replication"
)

// TestOverlap_NeighborReplicaStableThroughHandoff is the F1 end-to-end
// assertion for the overlap handoff: across a simulated 3-cell mesh,
// verify that during the Prepare→Commit overlap, a shared neighbor
// keeps the SAME ECS entity representing the handoff netID through
// the entire flow. No entity is evicted and recreated. This is the
// core invariant the overlap handoff exists to preserve.
//
// Topology:
//
//	A at (0, 0) — source (owns the entity pre-handoff)
//	B at (1, 0) — destination (takes ownership at Commit)
//	P at (1, 1) — shared neighbor; sees A and B as senders
//
// The entity lives near the A/B boundary and close to both A/P and B/P
// edges, so P receives border frames for it from BOTH A (before the
// handoff completes) and B (via Shadow participation in border push).
func TestOverlap_NeighborReplicaStableThroughHandoff(t *testing.T) {
	coords.SetCellSize(1024)

	// Three WorldBase instances. newTestWorldBase already resets
	// cellSize to 1024.
	a := newTestWorldBase(t, CellID{X: 0, Y: 0})
	b := newTestWorldBase(t, CellID{X: 1, Y: 0})
	p := newTestWorldBase(t, CellID{X: 1, Y: 1})

	// The handoff netID. We pick a value not issued by the engine's
	// NetID allocator so we don't collide with auto-allocated IDs.
	const netID = uint32(42001)

	// Simulate cell A's "neighbor push" to P with netID 42001 at a
	// position near the A-B-P shared corner. cellSize = 1024; entity
	// lives at world (1000, 1000) — inside cell A's (0,0) bounds but
	// near A/B (east) and A/P (south) edges. Both B and P will see it.
	pushFromA := func(x, y float32) {
		p.ApplyBorderFrame(replication.Frame{Entries: []replication.FrameEntry{{
			NetID:    replication.NetID{ID: netID, Epoch: 1},
			Kind:     1,
			DeltaBuf: buildWireEntry(x, y, 5, 10, 0),
		}}}, "cell_0_0")
	}

	pushFromB := func(x, y float32) {
		p.ApplyBorderFrame(replication.Frame{Entries: []replication.FrameEntry{{
			NetID:    replication.NetID{ID: netID, Epoch: 1},
			Kind:     1,
			DeltaBuf: buildWireEntry(x, y, 5, 10, 0),
		}}}, "cell_1_0")
	}

	// Empty push from a source — simulates the source dropping the
	// netID from its interest set.
	emptyFromA := func() { p.ApplyBorderFrame(replication.Frame{}, "cell_0_0") }
	emptyFromB := func() { p.ApplyBorderFrame(replication.Frame{}, "cell_1_0") }

	// Tick 1 — Pre-handoff: only A broadcasts netID 42001 to P.
	// P creates the replica.
	pushFromA(1000, 1000)
	if _, ok := p.ReplicaNetIDs()[netID]; !ok {
		t.Fatal("tick 1: P did not create replica from A's push")
	}
	originalReplica := p.ReplicaNetIDs()[netID]

	// Tick 2 — Handoff Prepare: B spawns a Shadow (simulating
	// SpawnShadow from receiving MsgHandoffPrepare). A also keeps
	// broadcasting. Source A's entity is still Live in A.
	//
	// Build a Shadow in B's ECS via SpawnShadow with a minimal
	// transfer blob.
	bWorld := b.ECSWorld()
	tempEnt := bWorld.NewEntity()
	ecs.NewMap1[component.Position](bWorld).Add(tempEnt, &component.Position{X: 1000 - 1024, Y: 1000}) // local to B
	ecs.NewMap1[component.Velocity](bWorld).Add(tempEnt, &component.Velocity{X: 10})
	ecs.NewMap1[component.NetworkID](bWorld).Add(tempEnt, &component.NetworkID{ID: netID, Epoch: 1})
	ecs.NewMap1[component.EntityKind](bWorld).Add(tempEnt, &component.EntityKind{Type: 1})
	ecs.NewMap1[component.Collider](bWorld).Add(tempEnt, &component.Collider{Radius: 5})
	ecs.NewMap1[component.Rotation](bWorld).Add(tempEnt, &component.Rotation{})
	ecs.NewMap1[component.CellCoord](bWorld).Add(tempEnt, &component.CellCoord{CellX: 1, CellY: 0})
	blob, err := b.SerializeEntity(tempEnt)
	if err != nil {
		t.Fatalf("serialize: %v", err)
	}
	bWorld.RemoveEntity(tempEnt)

	_, err = b.SpawnShadow(&HandoffPreparePayload{
		NetID: netID, Epoch: 1, Kind: 1, TransferBlob: blob,
	})
	if err != nil {
		t.Fatalf("SpawnShadow on B: %v", err)
	}
	// Now B's shadow-push reaches P:
	pushFromA(1000, 1000)
	pushFromB(1000, 1000)
	if got := p.ReplicaNetIDs()[netID]; got != originalReplica {
		t.Fatalf("tick 2: P's replica entity changed on Shadow participation: was %v, got %v",
			originalReplica, got)
	}
	_ = a

	// Tick 3 — Authority flip. A stops broadcasting (its Live entity
	// was demoted to Replica; BorderDispatcher skips replicas). B
	// promotes Shadow to Live (still the same ECS entity because
	// PromoteShadow preserves the handle). P's multi-source dedup
	// should skip eviction since B still pushes.
	//
	// IMPORTANT ORDERING (from D1): B must push FIRST so its
	// borderLastSeen for cell_1_0 is populated, THEN A sends empty.
	// Otherwise A's eviction wipes B's snapshot and B's next push
	// silently re-creates the replica (false green).
	pushFromB(1000, 1000)
	emptyFromA()

	if got, ok := p.ReplicaNetIDs()[netID]; !ok || got != originalReplica {
		t.Fatalf("tick 3: P's replica evicted or recreated during authority flip (ok=%v, got=%v, want=%v)",
			ok, got, originalReplica)
	}

	// Tick 4+ — Steady state: only B broadcasts. Replica still alive.
	pushFromB(1010, 1000)
	if got, ok := p.ReplicaNetIDs()[netID]; !ok || got != originalReplica {
		t.Fatalf("tick 4: P's replica not stable in steady state (ok=%v, got=%v, want=%v)",
			ok, got, originalReplica)
	}

	// Finally, B eventually drops the entity (e.g. despawn). Replica
	// should evict because no source pushes it anymore.
	emptyFromB()
	if _, ok := p.ReplicaNetIDs()[netID]; ok {
		t.Fatal("tick 5: P's replica not evicted after both sources silent")
	}
}
