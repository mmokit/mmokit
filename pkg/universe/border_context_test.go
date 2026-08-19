package universe

import (
	"testing"

	"github.com/mlange-42/ark/ecs"

	meshpb "github.com/mmokit/mmokit/gen/go/meshpb"
	"github.com/mmokit/mmokit/pkg/component"
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
		Rotation:   component.RotationFromYaw(1.5),
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

// TestUpsertBorderReplicaFromTransfer_NonOriginRoot is the regression guard for
// the coordinate double-subtraction bug. TransferFrame positions are already
// base-cell-local (relative to the entity's root cell origin), so the
// materialized replica's Position must equal frame.PosX/PosY verbatim
// regardless of the destination cell's grid position — matching what the
// authoritative SpawnFromTransferCore path does. A prior version subtracted the
// root-cell world origin, which was a no-op at (0,0) but offset replicas by a
// full cell at any non-origin root (landing 4node's (1,0)/(0,1)/(1,1) context
// entities thousands of units away). This test runs at root (1,0) so the bug
// would manifest as Position.X = 300 - CellSize instead of 300.
func TestUpsertBorderReplicaFromTransfer_NonOriginRoot(t *testing.T) {
	stage := newTestStageAt(t, CellID{X: 1, Y: 0})

	frame := &TransferFrame{
		NetworkID:  7777,
		EntityType: 1,
		PosX:       300,
		PosY:       50,
		Collider:   component.Collider{Radius: 5},
	}
	stage.upsertBorderReplicaFromTransfer(frame, MeshCellID("cell_2_0"))

	ent, _, ok := stage.netIDIdx.Lookup(7777)
	if !ok {
		t.Fatalf("expected netID 7777 present after upsert")
	}
	pos := ecs.NewMap1[component.Position](stage.eng.ECS).Get(ent)
	if pos.X != 300 || pos.Y != 50 {
		t.Errorf("replica Position = (%.1f,%.1f), want (300,50) — base-cell-local coords must transfer verbatim (root origin must NOT be subtracted)", pos.X, pos.Y)
	}
}

// TestCollectBorderContextFor_SplitFilterAndSource exercises the Stage method
// in isolation: the caller's shouldInclude predicate mimics the SPLIT case
// for "this child = quadrant 0" — local-in-quadrant-0 is authoritative and
// excluded, local-in-quadrant-1 is included with a sibling source id, and the
// replica is included with its own source attribution. Verifies the filter,
// the source-id plumbing, and that each blob round-trips through
// UnmarshalTransferFrame.
func TestCollectBorderContextFor_SplitFilterAndSource(t *testing.T) {
	stage := newTestStage(t) // cell {0,0} depth 0
	half := stage.cell.Size(stage.CellSize()) / 2

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

// TestPopulateCell_MaterializesContextEntries exercises the destination-side
// consumption of the shipped border context: populateCell spawns the
// authoritative entities Live, then materializes proto.Context entries as
// PresenceReplica — UNLESS a context entry's netID collides with an
// authoritative one, in which case it is skipped (Live wins, no downgrade).
func TestPopulateCell_MaterializesContextEntries(t *testing.T) {
	coord, host, cell := newExecutorTestCoord(t)
	exec := coord.hostExecutors[host.ID]

	// Build a transfer-frame blob for a given netID/type at (x,y).
	mkBlob := func(netID uint32, etype uint8, x, y float32) []byte {
		blob, err := MarshalTransferFrame(&TransferFrame{
			NetworkID:  netID,
			Epoch:      1,
			EntityType: etype,
			PosX:       x,
			PosY:       y,
			Collider:   component.Collider{Radius: 4},
			CellX:      cell.CellID().X,
			CellY:      cell.CellID().Y,
		})
		if err != nil {
			t.Fatalf("marshal transfer frame: %v", err)
		}
		return blob
	}

	// Two authoritative entities (Live) — netIDs 1001, 1002.
	authBlobs := [][]byte{
		mkBlob(1001, 1, 100, 100),
		mkBlob(1002, 1, 200, 200),
	}

	// Context entries: 2001 + 2002 are pure context (→ Replica); 1001 collides
	// with an authoritative netID and MUST be skipped (stays Live).
	proto := &meshpb.CellTransfer{
		Entities: packRecords(authBlobs),
		Context: []*meshpb.BorderContextEntry{
			{Entity: mkBlob(2001, 1, 300, 300), SourceCellId: "cell_1_0"},
			{Entity: mkBlob(2002, 1, 400, 400), SourceCellId: "cell_9_9"},
			{Entity: mkBlob(1001, 1, 999, 999), SourceCellId: "cell_1_0"}, // collides with Live
		},
	}

	// populateCell touches ECS — run it on the cell's game loop.
	execOnLoop(t, cell, func() {
		if _, err := exec.populateCell(cell, proto); err != nil {
			t.Fatalf("populateCell: %v", err)
		}
	})

	assertPresence := func(netID uint32, want EntityPresence, label string) {
		t.Helper()
		_, got, ok := cell.Stage.netIDIdx.Lookup(netID)
		if !ok {
			t.Fatalf("%s: netID %d not present after populate", label, netID)
		}
		if got != want {
			t.Errorf("%s: netID %d presence = %v, want %v", label, netID, got, want)
		}
	}

	// Authoritative entities are Live.
	assertPresence(1001, PresenceLive, "authoritative")
	assertPresence(1002, PresenceLive, "authoritative")
	// Pure context entries are Replica.
	assertPresence(2001, PresenceReplica, "context")
	assertPresence(2002, PresenceReplica, "context")
	// The colliding context entry (1001) must NOT have downgraded the Live
	// authoritative entity — it should remain Live (asserted above) and the
	// context copy was skipped, leaving exactly one slot for that netID.
}
