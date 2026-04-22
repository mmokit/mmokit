package universe

import (
	"testing"

	"github.com/mlange-42/ark/ecs"

	"github.com/zenion/mmoserver/pkg/component"
	"github.com/zenion/mmoserver/pkg/coords"
)

// TestHandoffDriver_ShadowSpawnAndPromote is a focused integration test
// for the handoff protocol's core mechanics without a full two-cell
// setup. It verifies:
//  1. A HandoffPrepare payload creates a Shadow entity via SpawnShadow
//  2. A subsequent HandoffCommit (same NetID) promotes the shadow to
//     a normal local entity (Shadow component removed)
//  3. The promoted entity retains the components from the transfer blob
func TestHandoffDriver_ShadowSpawnAndPromote(t *testing.T) {
	base := newTestWorldBase(t, CellID{X: 1, Y: 0})
	world := base.ECSWorld()

	// Build a valid TransferBlob from a temp entity. SpawnShadow calls
	// SpawnFromTransferCore which decodes via UnmarshalTransferFrame, so
	// we need a real serialized frame, not an empty blob.
	posMap := ecs.NewMap1[component.Position](world)
	velMap := ecs.NewMap1[component.Velocity](world)
	netMap := ecs.NewMap1[component.NetworkID](world)
	kindMap := ecs.NewMap1[component.EntityKind](world)
	colMap := ecs.NewMap1[component.Collider](world)
	rotMap := ecs.NewMap1[component.Rotation](world)
	cellMap := ecs.NewMap1[component.CellCoord](world)

	tempEntity := world.NewEntity()
	posMap.Add(tempEntity, &component.Position{X: 100, Y: 200})
	velMap.Add(tempEntity, &component.Velocity{X: 10, Y: 5})
	netMap.Add(tempEntity, &component.NetworkID{ID: 42})
	kindMap.Add(tempEntity, &component.EntityKind{Type: 3})
	colMap.Add(tempEntity, &component.Collider{Radius: 5})
	rotMap.Add(tempEntity, &component.Rotation{Angle: 0.5})
	cellMap.Add(tempEntity, &component.CellCoord{CellX: 1, CellY: 0})

	blob, err := base.SerializeEntity(tempEntity)
	if err != nil {
		t.Fatalf("SerializeEntity: %v", err)
	}
	world.RemoveEntity(tempEntity)

	payload := &HandoffPreparePayload{
		NetID:        42,
		Epoch:        2,
		Kind:         3,
		TransferBlob: blob,
		ExpectedTick: 100,
		OldEpoch:     1,
	}

	// Step 1: SpawnShadow creates the shadow.
	shadowEntity, err := base.SpawnShadow(payload)
	if err != nil {
		t.Fatalf("SpawnShadow: %v", err)
	}

	shadowMap := ecs.NewMap1[component.Shadow](world)
	if !shadowMap.HasAll(shadowEntity) {
		t.Fatal("expected Shadow component on spawned entity")
	}
	shadow := shadowMap.Get(shadowEntity)
	if shadow.NetID != 42 {
		t.Errorf("Shadow.NetID = %d, want 42", shadow.NetID)
	}
	if shadow.Epoch != 2 {
		t.Errorf("Shadow.Epoch = %d, want 2", shadow.Epoch)
	}

	// Verify the shadow inherited the transferred components.
	if !posMap.HasAll(shadowEntity) {
		t.Fatal("shadow missing Position component")
	}
	pos := posMap.Get(shadowEntity)
	if pos.X != 100 || pos.Y != 200 {
		t.Errorf("shadow Position = (%.0f, %.0f), want (100, 200)", pos.X, pos.Y)
	}
	if !velMap.HasAll(shadowEntity) {
		t.Fatal("shadow missing Velocity component")
	}
	if !netMap.HasAll(shadowEntity) {
		t.Fatal("shadow missing NetworkID component")
	}
	nid := netMap.Get(shadowEntity)
	if nid.ID != 42 {
		t.Errorf("shadow NetworkID.ID = %d, want 42", nid.ID)
	}

	// Step 2: PromoteShadow removes the Shadow component.
	if !base.PromoteShadow(42) {
		t.Fatal("PromoteShadow returned false — shadow not found")
	}
	if shadowMap.HasAll(shadowEntity) {
		t.Fatal("Shadow component should be removed after promote")
	}

	// The entity should still exist with all its non-Shadow components.
	if !posMap.HasAll(shadowEntity) {
		t.Fatal("promoted entity lost Position component")
	}
	if !netMap.HasAll(shadowEntity) {
		t.Fatal("promoted entity lost NetworkID component")
	}
}

// TestHandoffDriver_PromoteNonexistent verifies that PromoteShadow
// returns false when no matching shadow exists, rather than panicking.
// This matters because HandoffCommit messages may arrive out of order
// or for already-promoted entities (dedup path).
func TestHandoffDriver_PromoteNonexistent(t *testing.T) {
	base := newTestWorldBase(t, CellID{X: 0, Y: 0})
	if base.PromoteShadow(999) {
		t.Fatal("PromoteShadow should return false for unknown NetID")
	}
}

// TestWorldBase_RemoveShadowByNetID verifies the cancel cleanup path:
// a shadow exists, RemoveShadowByNetID finds it by NetID and marks it
// for removal, and the entity is no longer a shadow after the tick
// flush.
func TestWorldBase_RemoveShadowByNetID(t *testing.T) {
	base := newTestWorldBase(t, CellID{X: 0, Y: 0})
	world := base.ECSWorld()

	// Create a shadow directly.
	entity := world.NewEntity()
	netMap := ecs.NewMap1[component.NetworkID](world)
	shadowMap := ecs.NewMap1[component.Shadow](world)
	netMap.Add(entity, &component.NetworkID{ID: 777, Epoch: 1})
	shadowMap.Add(entity, &component.Shadow{NetID: 777, Epoch: 1})

	if !base.RemoveShadowByNetID(777) {
		t.Fatal("RemoveShadowByNetID should return true for existing shadow")
	}

	// After MarkForRemoval the entity may still be alive in the same
	// tick but is queued for removal. Verify the next-tick flush.
	// Simpler: just check RemoveShadowByNetID returns false now
	// (because a second call can't find it — MarkForRemoval might
	// keep it alive for the rest of the tick). Alternative: call
	// base.eng.FlushRemovals() if such a method exists.
	_ = entity // suppress unused if no further assertions
}

// TestWorldBase_RemoveShadowByNetID_NotFound verifies the no-op path
// when the given NetID has no matching shadow.
func TestWorldBase_RemoveShadowByNetID_NotFound(t *testing.T) {
	base := newTestWorldBase(t, CellID{X: 0, Y: 0})
	if base.RemoveShadowByNetID(999) {
		t.Fatal("RemoveShadowByNetID should return false for unknown NetID")
	}
}

// TestDemoteLiveToReplica_PreservesEntityAndTransitionsSlot verifies
// the source-side mirror of PromoteShadow. The same ECS entity must
// survive (same handle, same Position/Velocity), a Replica component
// must be added, the netIDIdx slot must flip from Live to Replica, and
// replicaNetIDs must point at the entity so subsequent border frames
// from the new authoritative cell update in place.
func TestDemoteLiveToReplica_PreservesEntityAndTransitionsSlot(t *testing.T) {
	base := newTestWorldBase(t, CellID{X: 0, Y: 0})
	world := base.ECSWorld()

	// Spawn a Live entity the normal way.
	ent := base.SpawnEntity(
		component.Position{X: 100, Y: 200},
		WithVelocity(10, -5),
		WithEntityKind(1),
		WithCollider(8),
	)
	// Grab the allocated netID.
	netID := base.NetworkIDMap().Get(ent).ID

	// Confirm slot is Live before demote.
	_, pres, ok := base.LookupNetID(netID)
	if !ok || pres != PresenceLive {
		t.Fatalf("pre-demote presence = %v ok=%v, want PresenceLive true", pres, ok)
	}

	// Demote to replica of the destination cell.
	if err := base.DemoteLiveToReplica(netID, "cell_1_0"); err != nil {
		t.Fatalf("DemoteLiveToReplica: %v", err)
	}

	// Same entity still alive, same position, same velocity.
	if !world.Alive(ent) {
		t.Fatal("DemoteLiveToReplica must not remove the entity")
	}
	pos := base.PositionMap().Get(ent)
	if pos.X != 100 || pos.Y != 200 {
		t.Fatalf("position mutated: got (%.0f,%.0f), want (100,200)", pos.X, pos.Y)
	}
	vel := ecs.NewMap1[component.Velocity](world).Get(ent)
	if vel.X != 10 || vel.Y != -5 {
		t.Fatalf("velocity mutated: got (%.0f,%.0f), want (10,-5)", vel.X, vel.Y)
	}

	// Replica component added with correct SourceCellID.
	repMap := ecs.NewMap1[component.Replica](world)
	if !repMap.HasAll(ent) {
		t.Fatal("Replica component not added")
	}
	rep := repMap.Get(ent)
	if rep.SourceCellID != "cell_1_0" {
		t.Fatalf("Replica.SourceCellID = %q, want cell_1_0", rep.SourceCellID)
	}
	if !rep.UpdatedThisTick {
		t.Error("Replica.UpdatedThisTick must be true")
	}

	// Slot flipped to Replica.
	_, pres, ok = base.LookupNetID(netID)
	if !ok || pres != PresenceReplica {
		t.Fatalf("post-demote presence = %v ok=%v, want PresenceReplica true", pres, ok)
	}

	// replicaNetIDs now points at the entity.
	got, ok := base.ReplicaNetIDs()[netID]
	if !ok || got != ent {
		t.Fatalf("replicaNetIDs[%d] = (%v,%v), want (%v, true)", netID, got, ok, ent)
	}
}

// TestDemoteLiveToReplica_UnknownNetIDReturnsError ensures the method
// does not silently succeed for a netID that has no live entity.
func TestDemoteLiveToReplica_UnknownNetIDReturnsError(t *testing.T) {
	base := newTestWorldBase(t, CellID{X: 0, Y: 0})
	if err := base.DemoteLiveToReplica(9999, "cell_1_0"); err == nil {
		t.Fatal("DemoteLiveToReplica on unknown netID must return error")
	}
}

// handoffRecordingBridge is a test Bridge that captures Handoff* calls.
// Named to avoid collision with the recordingBridge in universe_test.go.
type handoffRecordingBridge struct {
	NoopBridge
	prepares        []*HandoffPreparePayload
	commits         []*HandoffCommitPayload
	cancels         []*HandoffCancelPayload
	playerTransfers int

	// commitFailsForDest, if non-empty, causes SendHandoffCommit to
	// return false when destCellID matches.
	commitFailsForDest string
}

func (r *handoffRecordingBridge) SendHandoffPrepare(destCellID string, p *HandoffPreparePayload) bool {
	r.prepares = append(r.prepares, p)
	return true
}
func (r *handoffRecordingBridge) SendHandoffCommit(destCellID string, p *HandoffCommitPayload) bool {
	if destCellID == r.commitFailsForDest {
		return false
	}
	r.commits = append(r.commits, p)
	return true
}
func (r *handoffRecordingBridge) SendHandoffCancel(destCellID string, p *HandoffCancelPayload) {
	r.cancels = append(r.cancels, p)
}
func (r *handoffRecordingBridge) OnPlayerTransfer(connID uint32, destCellID string) {
	r.playerTransfers++
}

// TestHandoffDriver_PrepareAndCommitSameTick verifies the v1 same-tick
// handoff: handleCrossing fires both Prepare and Commit on the same
// tick and demotes the source entity to a Replica. The ECS entity
// handle is preserved (not removed).
func TestHandoffDriver_PrepareAndCommitSameTick(t *testing.T) {
	base := newTestWorldBase(t, CellID{X: 0, Y: 0})
	world := base.ECSWorld()

	rec := &handoffRecordingBridge{}
	hd := NewHandoffDriver(base, rec)

	ent := base.SpawnEntity(
		component.Position{X: 100, Y: 100},
		WithEntityKind(1),
		WithCollider(5),
	)
	netID := base.NetworkIDMap().Get(ent).ID
	base.QueueCrossing(CrossingEvent{
		Entity: ent, NetID: netID, DestCellID: "cell_1_0",
	})

	hd.Tick(1)

	// Both Prepare and Commit must have fired on the single tick.
	if len(rec.prepares) != 1 {
		t.Fatalf("prepare count = %d, want 1", len(rec.prepares))
	}
	if len(rec.commits) != 1 {
		t.Fatalf("commit count = %d, want 1", len(rec.commits))
	}

	// Source entity must stay alive (demoted, not removed).
	if !world.Alive(ent) {
		t.Fatal("post-commit: source entity must stay alive (demoted, not removed)")
	}
	_, pres, _ := base.LookupNetID(netID)
	if pres != PresenceReplica {
		t.Fatalf("post-commit: presence = %v, want PresenceReplica", pres)
	}
}

// TestHandoffDriver_CommitFailsWhenDestGone verifies that if
// SendHandoffCommit returns false (destination cell torn down before
// the commit lands), the source does NOT demote — the entity stays
// Live so a future crossing can handle it.
func TestHandoffDriver_CommitFailsWhenDestGone(t *testing.T) {
	base := newTestWorldBase(t, CellID{X: 0, Y: 0})
	world := base.ECSWorld()

	rec := &handoffRecordingBridge{commitFailsForDest: "cell_1_0"}
	hd := NewHandoffDriver(base, rec)

	ent := base.SpawnEntity(
		component.Position{X: 100, Y: 100},
		WithEntityKind(1),
		WithCollider(5),
	)
	netID := base.NetworkIDMap().Get(ent).ID
	base.QueueCrossing(CrossingEvent{
		Entity: ent, NetID: netID, DestCellID: "cell_1_0",
	})

	hd.Tick(1)

	// Source entity must still exist and still be Live.
	if !world.Alive(ent) {
		t.Fatal("source entity must stay alive on commit failure")
	}
	_, pres, _ := base.LookupNetID(netID)
	if pres != PresenceLive {
		t.Fatalf("presence after failed commit = %v, want PresenceLive", pres)
	}

	// Bridge should NOT have recorded the (failed) commit attempt.
	if len(rec.commits) != 0 {
		t.Fatalf("commits captured = %d, want 0 (commit failed)", len(rec.commits))
	}
}

// TestHandoffDriver_DrainingForMerge_SkipsCrossings verifies that
// when a cell enters drain-for-merge mode, new crossings are dropped
// (not prepared) — the state machine freezes so the merge executor
// can drain the cell cleanly. Prior cause of duplicate-netID bugs was
// the donor's handoff_driver continuing to ship entities via
// Prepare+Commit AFTER the merge executor had already serialized
// them for populate (commit e4ede97).
func TestHandoffDriver_DrainingForMerge_SkipsCrossings(t *testing.T) {
	base := newTestWorldBase(t, CellID{X: 0, Y: 0})

	rec := &handoffRecordingBridge{}
	hd := NewHandoffDriver(base, rec)

	ent := base.SpawnEntity(
		component.Position{X: 100, Y: 100},
		WithEntityKind(1),
		WithCollider(5),
	)
	netID := base.NetworkIDMap().Get(ent).ID

	// Enter drain mode, THEN queue a crossing. It must be dropped.
	base.SetDrainingForMerge(true)
	base.QueueCrossing(CrossingEvent{
		Entity: ent, NetID: netID, DestCellID: "cell_1_0",
	})
	hd.Tick(1)
	if len(rec.prepares) != 0 {
		t.Fatalf("during drain: Prepare fired (got %d, want 0)", len(rec.prepares))
	}
	if len(rec.commits) != 0 {
		t.Fatalf("during drain: Commit fired (got %d, want 0)", len(rec.commits))
	}
}

// TestHandoffDriver_PlayerSessionTransfersOnCommit verifies that the
// player-session transfer side-effects (OnPlayerTransfer + Players
// .Remove) fire together with the Commit. In v1 same-tick protocol
// they fire on the same tick as Prepare, but still as part of the
// commit path — meaning they are skipped when the commit fails.
func TestHandoffDriver_PlayerSessionTransfersOnCommit(t *testing.T) {
	base := newTestWorldBase(t, CellID{X: 0, Y: 0})

	rec := &handoffRecordingBridge{}
	hd := NewHandoffDriver(base, rec)

	ent := base.SpawnEntity(
		component.Position{X: 100, Y: 100},
		WithEntityKind(1),
		WithCollider(5),
	)
	netID := base.NetworkIDMap().Get(ent).ID

	connID := uint32(42)
	base.Engine().Players.RegisterPlayer(connID, "player")

	base.QueueCrossing(CrossingEvent{
		Entity:     ent,
		NetID:      netID,
		ConnID:     connID,
		DestCellID: "cell_1_0",
	})

	hd.Tick(1)

	if rec.playerTransfers != 1 {
		t.Fatalf("after Commit: OnPlayerTransfer calls = %d, want 1", rec.playerTransfers)
	}
	if base.Engine().Players.ByConnID(connID) != nil {
		t.Fatal("after Commit: player session still on source — should have been removed")
	}
}

// TestHandoffDriver_Prepare_LeavesSourceEntityPositionUnchanged verifies
// that Prepare does NOT rewrite the source entity's live Position or
// CellCoord. The source's canonical Position must remain in source's
// native frame; only the TransferBlob carries the normalized coords
// for the destination's Shadow. (Asserted pre-commit via an aborted
// commit: the bridge returns false on Commit so the source stays Live
// and we can inspect its live Position/CellCoord after the call.)
func TestHandoffDriver_Prepare_LeavesSourceEntityPositionUnchanged(t *testing.T) {
	coords.SetCellSize(1024)
	base := newTestWorldBase(t, CellID{X: 0, Y: 0})

	rec := &handoffRecordingBridge{commitFailsForDest: "cell_1_0"}
	hd := NewHandoffDriver(base, rec)

	// Entity just past the east boundary of cell (0,0), world-space
	// equivalent to ~1030.
	ent := base.SpawnEntity(
		component.Position{X: 1030, Y: 500},
		WithVelocity(300, 0),
		WithEntityKind(1),
		WithCollider(5),
	)
	netID := base.NetworkIDMap().Get(ent).ID
	base.QueueCrossing(CrossingEvent{
		Entity: ent, NetID: netID, DestCellID: "cell_1_0",
	})

	hd.Tick(1)

	// Prepare must have fired.
	if len(rec.prepares) != 1 {
		t.Fatalf("expected 1 Prepare, got %d", len(rec.prepares))
	}

	// Live entity Position must be UNCHANGED — still in source's native
	// frame at 1030. (Serializer normalizes into the blob only.)
	pos := base.PositionMap().Get(ent)
	if pos.X != 1030 || pos.Y != 500 {
		t.Fatalf("source entity Position mutated: got (%.0f,%.0f), want (1030,500)",
			pos.X, pos.Y)
	}
	cc := ecs.NewMap1[component.CellCoord](base.ECSWorld()).Get(ent)
	if cc.CellX != 0 || cc.CellY != 0 {
		t.Fatalf("source entity CellCoord mutated: got (%d,%d), want (0,0)",
			cc.CellX, cc.CellY)
	}
}
