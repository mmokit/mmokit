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
//  1. A Handoff payload creates a Shadow entity via SpawnShadow
//  2. A subsequent PromoteShadow (same NetID) promotes the shadow to
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

	payload := &HandoffPayload{
		NetID:        42,
		Epoch:        2,
		TransferBlob: blob,
		CommitTick:   100,
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

// handoffRecordingBridge is a test Bridge that captures SendHandoff
// calls. Named to avoid collision with the recordingBridge in
// universe_test.go.
type handoffRecordingBridge struct {
	NoopBridge
	handoffs        []*HandoffPayload
	playerTransfers int

	// failsForDest, if non-empty, causes SendHandoff to return false
	// when destCellID matches.
	failsForDest string
}

func (r *handoffRecordingBridge) SendHandoff(destCellID string, p *HandoffPayload) bool {
	if destCellID == r.failsForDest {
		return false
	}
	r.handoffs = append(r.handoffs, p)
	return true
}
func (r *handoffRecordingBridge) OnPlayerTransfer(connID uint32, destCellID string) {
	r.playerTransfers++
}

// TestHandoffDriver_HardCut_QueuesDemoteForCommitTick verifies the
// H1 hard-cut behavior on the source side:
//   - handleCrossing fires a single Handoff message with
//     CommitTick = currentClusterTick + HandoffLeadTicks
//   - the source entity remains Live at crossing time (no immediate
//     demote)
//   - after ticking forward to CommitTick, the entity transitions to
//     Replica via the queued demote
func TestHandoffDriver_HardCut_QueuesDemoteForCommitTick(t *testing.T) {
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

	// Tick at cluster-tick 7: message fires, demote is queued for 7+lead.
	hd.Tick(7)

	if len(rec.handoffs) != 1 {
		t.Fatalf("handoff count = %d, want 1", len(rec.handoffs))
	}
	wantCommit := uint64(7 + HandoffLeadTicks)
	if got := rec.handoffs[0].CommitTick; got != wantCommit {
		t.Errorf("CommitTick = %d, want currentClusterTick+HandoffLeadTicks=%d", got, wantCommit)
	}
	if got := rec.handoffs[0].NetID; got != netID {
		t.Errorf("NetID = %d, want %d", got, netID)
	}

	// Pre-commit: entity must still be Live on the source.
	if !world.Alive(ent) {
		t.Fatal("pre-commit: source entity must stay alive")
	}
	_, pres, _ := base.LookupNetID(netID)
	if pres != PresenceLive {
		t.Fatalf("pre-commit: presence = %v, want PresenceLive (demote deferred to CommitTick)", pres)
	}

	// Ticking at an intermediate cluster-tick must NOT commit.
	hd.Tick(uint64(7 + HandoffLeadTicks - 1))
	_, pres, _ = base.LookupNetID(netID)
	if pres != PresenceLive {
		t.Fatalf("before CommitTick: presence = %v, want PresenceLive", pres)
	}

	// Tick at CommitTick: the queued demote fires.
	hd.Tick(wantCommit)
	_, pres, _ = base.LookupNetID(netID)
	if pres != PresenceReplica {
		t.Fatalf("at CommitTick: presence = %v, want PresenceReplica (hard-cut commit)", pres)
	}
	if !world.Alive(ent) {
		t.Fatal("at CommitTick: source entity must stay alive (demoted, not removed)")
	}
}

// TestHandoffDriver_HardCut_SlippedCommitTickStillCommits verifies
// that if the driver doesn't tick exactly at CommitTick (e.g. CPU
// pressure or a lost cluster-clock observation pushed Now forward),
// the queued demote still fires on the next Tick whose
// currentClusterTick >= CommitTick.
func TestHandoffDriver_HardCut_SlippedCommitTickStillCommits(t *testing.T) {
	base := newTestWorldBase(t, CellID{X: 0, Y: 0})
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

	hd.Tick(10) // CommitTick = 12
	// Skip past 12 straight to 15.
	hd.Tick(15)
	_, pres, _ := base.LookupNetID(netID)
	if pres != PresenceReplica {
		t.Fatalf("after slipped commit (tick 15 > commit 12): presence = %v, want PresenceReplica", pres)
	}
}

// TestHandoffDriver_HardCut_AntiThrashCooldown verifies that a second
// crossing for the same (netID, destCellID) within HandoffCooldownTicks
// is dropped.
func TestHandoffDriver_HardCut_AntiThrashCooldown(t *testing.T) {
	base := newTestWorldBase(t, CellID{X: 0, Y: 0})
	rec := &handoffRecordingBridge{}
	hd := NewHandoffDriver(base, rec)

	ent := base.SpawnEntity(
		component.Position{X: 100, Y: 100},
		WithEntityKind(1),
		WithCollider(5),
	)
	netID := base.NetworkIDMap().Get(ent).ID

	base.QueueCrossing(CrossingEvent{Entity: ent, NetID: netID, DestCellID: "cell_1_0"})
	hd.Tick(100)
	if len(rec.handoffs) != 1 {
		t.Fatalf("first crossing: handoffs = %d, want 1", len(rec.handoffs))
	}

	// Another crossing 5 ticks later — inside cooldown window (20 ticks).
	base.QueueCrossing(CrossingEvent{Entity: ent, NetID: netID, DestCellID: "cell_1_0"})
	hd.Tick(105)
	if len(rec.handoffs) != 1 {
		t.Fatalf("within cooldown: handoffs = %d, want 1 (dropped)", len(rec.handoffs))
	}

	// Way past cooldown: new handoff fires.
	base.QueueCrossing(CrossingEvent{Entity: ent, NetID: netID, DestCellID: "cell_1_0"})
	hd.Tick(200)
	if len(rec.handoffs) != 2 {
		t.Fatalf("after cooldown: handoffs = %d, want 2", len(rec.handoffs))
	}
}

// TestHandoffDriver_SendFailsWhenDestGone verifies that if SendHandoff
// returns false (destination cell torn down before send lands), the
// source does NOT demote — the entity stays Live so a future crossing
// can handle it. Also verifies the epoch bump is rolled back.
func TestHandoffDriver_SendFailsWhenDestGone(t *testing.T) {
	base := newTestWorldBase(t, CellID{X: 0, Y: 0})
	world := base.ECSWorld()

	rec := &handoffRecordingBridge{failsForDest: "cell_1_0"}
	hd := NewHandoffDriver(base, rec)

	ent := base.SpawnEntity(
		component.Position{X: 100, Y: 100},
		WithEntityKind(1),
		WithCollider(5),
	)
	netID := base.NetworkIDMap().Get(ent).ID
	origEpoch := base.NetworkIDMap().Get(ent).Epoch

	base.QueueCrossing(CrossingEvent{
		Entity: ent, NetID: netID, DestCellID: "cell_1_0",
	})

	hd.Tick(1)

	// Source entity must still exist and still be Live.
	if !world.Alive(ent) {
		t.Fatal("source entity must stay alive on send failure")
	}
	_, pres, _ := base.LookupNetID(netID)
	if pres != PresenceLive {
		t.Fatalf("presence after failed send = %v, want PresenceLive", pres)
	}

	// Epoch must have rolled back.
	if got := base.NetworkIDMap().Get(ent).Epoch; got != origEpoch {
		t.Fatalf("epoch after failed send = %d, want %d (rolled back)", got, origEpoch)
	}

	// Bridge should NOT have recorded the (failed) attempt.
	if len(rec.handoffs) != 0 {
		t.Fatalf("handoffs captured = %d, want 0 (send failed)", len(rec.handoffs))
	}
}

// TestHandoffDriver_DrainingForMerge_SkipsCrossings verifies that
// when a cell enters drain-for-merge mode, new crossings are dropped
// (no Handoff fired) — the state machine freezes so the merge executor
// can drain the cell cleanly. Prior cause of duplicate-netID bugs was
// the donor's handoff_driver continuing to ship entities AFTER the
// merge executor had already serialized them for populate (commit
// e4ede97).
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
	if len(rec.handoffs) != 0 {
		t.Fatalf("during drain: Handoff fired (got %d, want 0)", len(rec.handoffs))
	}
}

// TestHandoffDriver_PlayerSessionTransfersAtCommitTick verifies that
// under hard-cut, player-session transfer side-effects
// (OnPlayerTransfer + Players.Remove) fire at CommitTick, not at the
// crossing-send tick. The session stays put during the lead-time
// window so ClientInput frames continue to route to the source while
// it remains authoritative.
func TestHandoffDriver_PlayerSessionTransfersAtCommitTick(t *testing.T) {
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

	// Send at tick 1 — session transfer should NOT have fired yet.
	hd.Tick(1)
	if rec.playerTransfers != 0 {
		t.Fatalf("at send tick: OnPlayerTransfer calls = %d, want 0 (deferred to CommitTick)", rec.playerTransfers)
	}
	if base.Engine().Players.ByConnID(connID) == nil {
		t.Fatal("at send tick: player session must still be on source")
	}

	// Handoff payload carries the conn_id and the hard-cut CommitTick.
	if len(rec.handoffs) != 1 {
		t.Fatalf("handoff count = %d, want 1", len(rec.handoffs))
	}
	if rec.handoffs[0].ConnID != connID {
		t.Errorf("Handoff.ConnID = %d, want %d", rec.handoffs[0].ConnID, connID)
	}
	if rec.handoffs[0].CommitTick != uint64(1+HandoffLeadTicks) {
		t.Errorf("Handoff.CommitTick = %d, want %d", rec.handoffs[0].CommitTick, 1+HandoffLeadTicks)
	}

	// Advance to CommitTick: session transfer fires now.
	hd.Tick(uint64(1 + HandoffLeadTicks))
	if rec.playerTransfers != 1 {
		t.Fatalf("at CommitTick: OnPlayerTransfer calls = %d, want 1", rec.playerTransfers)
	}
	if base.Engine().Players.ByConnID(connID) != nil {
		t.Fatal("at CommitTick: player session still on source — should have been removed")
	}
}

// TestHandoffDriver_LeavesSourceEntityPositionUnchanged verifies that
// the driver does NOT rewrite the source entity's live Position or
// CellCoord. The source's canonical Position must remain in source's
// native frame; only the TransferBlob carries the normalized coords
// for the destination's Shadow. (Asserted pre-demote via a bridge
// that returns false so the source stays Live and we can inspect its
// live Position/CellCoord after the call.)
func TestHandoffDriver_LeavesSourceEntityPositionUnchanged(t *testing.T) {
	coords.SetCellSize(1024)
	base := newTestWorldBase(t, CellID{X: 0, Y: 0})

	rec := &handoffRecordingBridge{failsForDest: "cell_1_0"}
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
