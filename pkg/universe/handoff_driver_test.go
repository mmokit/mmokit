package universe

import (
	"testing"

	"github.com/mlange-42/ark/ecs"

	"github.com/zenion/mmoserver/pkg/component"
	"github.com/zenion/mmoserver/pkg/coords"
)

// TestDemoteLiveToReplica_PreservesEntityAndTransitionsSlot verifies
// the source-side mirror of PromoteReplicaToLive. The same ECS entity must
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
	failsForDest MeshCellID
}

func (r *handoffRecordingBridge) SendHandoff(destCellID MeshCellID, p *HandoffPayload) bool {
	if destCellID == r.failsForDest {
		return false
	}
	r.handoffs = append(r.handoffs, p)
	return true
}
func (r *handoffRecordingBridge) OnPlayerTransfer(connID uint32, destCellID MeshCellID) {
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

	// Tick to the commit so the demote drains and the entity becomes
	// Replica on this cell. PromoteReplicaToLive then puts it back to
	// Live — modeling the realistic case where the player crossed away,
	// came back, and is now re-crossing to the same dest within
	// HandoffCooldownTicks.
	hd.Tick(102)
	if err := base.PromoteReplicaToLive(netID, 99); err != nil {
		t.Fatalf("PromoteReplicaToLive: %v", err)
	}

	// Another crossing 3 ticks after the re-promotion — inside cooldown
	// window (20 ticks since lastHandoff at commitTick=102).
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
// for the destination spawn. (Asserted pre-demote via a bridge that
// returns false so the source stays Live and we can inspect its live
// Position/CellCoord after the call.)
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

// TestHandoffDriver_PositionReplicatorInRegistry_DoesNotCorruptDestSpawn
// is a regression test for the cross-cell position corruption bug:
//
// If component.Position is registered in the shared ReplicationRegistry
// (e.g. because a game component bundle includes a Position field), the
// HandoffDriver serializes the RAW (unnormalized) source position into
// frame.Components. SpawnFromTransferCore then creates the entity with the
// normalized frame.PosX, but the registered Add closure immediately
// overwrites it with the raw value — placing the entity on the wrong side
// of the cell.
//
// The fix: SpawnFromTransferCore must not allow frame.Components to
// overwrite the core position that was already written from frame.PosX/PosY.
func TestHandoffDriver_PositionReplicatorInRegistry_DoesNotCorruptDestSpawn(t *testing.T) {
	coords.SetCellSize(1024)

	// Source cell: register component.Position in the ReplicationRegistry using
	// the default reflection-based codec — simulates Field[mmokit.Position]() in
	// a game's RegisterKind call (the root cause of the cross-cell position bug).
	src := newTestWorldBase(t, CellID{X: 0, Y: 0})
	srcPosMap := ecs.NewMap1[component.Position](src.ECSWorld())
	RegisterComponent(src.ReplicationRegistry(), srcPosMap)

	rec := &handoffRecordingBridge{}
	hd := NewHandoffDriver(src, rec)

	// Spawn entity just past the east boundary of cell(0,0).
	// Raw X=1030, normalized X=1030-1024=6.
	const rawX = float32(1030)
	const normX = rawX - 1024
	ent := src.SpawnEntity(
		component.Position{X: rawX, Y: 500},
		WithEntityKind(1),
		WithCollider(5),
	)
	netID := src.NetworkIDMap().Get(ent).ID
	src.QueueCrossing(CrossingEvent{
		Entity: ent, NetID: netID, DestCellID: "cell_1_0",
	})
	hd.Tick(1)

	if len(rec.handoffs) != 1 {
		t.Fatalf("expected 1 handoff, got %d", len(rec.handoffs))
	}
	blob := rec.handoffs[0].TransferBlob

	// Destination cell with its own ReplicationRegistry containing Position.
	dst := newTestWorldBase(t, CellID{X: 1, Y: 0})
	dstPosMap := ecs.NewMap1[component.Position](dst.ECSWorld())
	RegisterComponent(dst.ReplicationRegistry(), dstPosMap)

	newEnt, _, err := dst.SpawnFromTransferCore(blob, PresenceLive)
	if err != nil {
		t.Fatalf("SpawnFromTransferCore: %v", err)
	}

	pos := dstPosMap.Get(newEnt)
	if pos.X != normX {
		t.Errorf("destination X = %.0f, want %.0f (normalized); Position replicator in registry overwrote with raw source X=%.0f",
			pos.X, normX, rawX)
	}
	if pos.Y != 500 {
		t.Errorf("destination Y = %.0f, want 500", pos.Y)
	}
}

// TestHandoffDriver_DropsRedundantCrossingWhilePending verifies that a
// second crossing for the same netID is dropped when a previous handoff
// is still in-flight (between sent and committed) — even when the second
// crossing targets a DIFFERENT destination cell.
//
// Without this guard, a player rapidly crossing diagonally (e.g. east
// then south within HandoffLeadTicks) triggers two simultaneous handoffs
// for the same netID. The per-(netID, destCellID) cooldown only blocks
// repeats to the SAME destination; a different destination slips
// through. Both handoffs commit independently — the source-side demote
// of the second fails (already demoted by the first), but the
// destination-side spawn still proceeds, leaving DUPLICATE LIVE entities
// on two cells. The player session is then routed to whichever
// destination commits last, while the original Live copy is orphaned and
// keeps moving via inertia, triggering further phantom handoffs.
//
// Symptom in production: client sees jumpy movement, then stops
// receiving frames entirely (player session orphaned on a cell whose
// entity got duplicate-spawn-blocked).
func TestHandoffDriver_DropsRedundantCrossingWhilePending(t *testing.T) {
	base := newTestWorldBase(t, CellID{X: 0, Y: 0})
	rec := &handoffRecordingBridge{}
	hd := NewHandoffDriver(base, rec)

	ent := base.SpawnEntity(
		component.Position{X: 100, Y: 100},
		WithEntityKind(1),
		WithCollider(5),
	)
	netID := base.NetworkIDMap().Get(ent).ID

	// First crossing: handoff sent, demote queued for tick 100+HandoffLeadTicks.
	base.QueueCrossing(CrossingEvent{Entity: ent, NetID: netID, DestCellID: "cell_1_0"})
	hd.Tick(100)
	if len(rec.handoffs) != 1 {
		t.Fatalf("first crossing: handoffs = %d, want 1", len(rec.handoffs))
	}

	// Second crossing to a DIFFERENT destination, BEFORE the first commits.
	// The per-(netID, destCellID) cooldown does NOT catch this (different
	// destCellID). The pending-demote check must.
	base.QueueCrossing(CrossingEvent{Entity: ent, NetID: netID, DestCellID: "cell_0_1"})
	hd.Tick(101) // still before commitTick (100 + HandoffLeadTicks = 102)
	if len(rec.handoffs) != 1 {
		dests := make([]string, len(rec.handoffs))
		t.Fatalf("redundant crossing to different dest while handoff pending: "+
			"handoffs = %d, want 1; sent to dests=%v",
			len(rec.handoffs), append(dests, ""))
	}

	// After the first commits, the entity is Replica on this cell. A
	// future crossing wouldn't reach BoundarySystem (Replica is excluded
	// from its query), but explicitly verify the driver gates correctly:
	// once the pending demote has fired, hasPendingDemote returns false.
	hd.Tick(102) // commit fires
	_, pres, _ := base.LookupNetID(netID)
	if pres != PresenceReplica {
		t.Fatalf("post-commit presence = %v, want PresenceReplica", pres)
	}
}

// TestHandoffDriver_AcceptsNonNeighborDestination verifies that the
// handoff driver does NOT require the destination to be a Moore-neighbor
// of the source. Long-distance teleports (Stage.MoveEntityTo) feed the
// same crossing-event queue with arbitrary destCellIDs and the driver
// must dispatch them without filtering.
func TestHandoffDriver_AcceptsNonNeighborDestination(t *testing.T) {
	base := newTestWorldBase(t, CellID{X: 0, Y: 0})
	rec := &handoffRecordingBridge{}
	hd := NewHandoffDriver(base, rec)

	ent := base.SpawnEntity(
		component.Position{X: 100, Y: 100},
		WithEntityKind(1),
		WithCollider(5),
	)
	netID := base.NetworkIDMap().Get(ent).ID

	// cell_5_5 is far from cell_0_0 — not a Moore-neighbor.
	base.QueueCrossing(CrossingEvent{
		Entity: ent, NetID: netID, DestCellID: "cell_5_5",
	})
	hd.Tick(7)

	if len(rec.handoffs) != 1 {
		t.Fatalf("non-neighbor handoff dropped: handoffs = %d, want 1", len(rec.handoffs))
	}
	if rec.handoffs[0].NetID != netID {
		t.Errorf("NetID = %d, want %d", rec.handoffs[0].NetID, netID)
	}
}

// TestHandoffDriver_BypassCooldown verifies that an explicit teleport
// (BypassCooldown=true) skips the HandoffCooldownTicks anti-thrash
// window. Three rapid handoffs to the same (netID, dest) all succeed.
//
// Without BypassCooldown the second crossing inside the window would be
// dropped — TestHandoffDriver_HardCut_AntiThrashCooldown covers that.
func TestHandoffDriver_BypassCooldown(t *testing.T) {
	base := newTestWorldBase(t, CellID{X: 0, Y: 0})
	rec := &handoffRecordingBridge{}
	hd := NewHandoffDriver(base, rec)

	ent := base.SpawnEntity(
		component.Position{X: 100, Y: 100},
		WithEntityKind(1),
		WithCollider(5),
	)
	netID := base.NetworkIDMap().Get(ent).ID

	// Each iteration simulates one teleport:
	//   1. Queue the crossing (BypassCooldown=true).
	//   2. Tick to fire the handoff (entity still Live, no pending demote).
	//   3. Tick past the commitTick so the pending demote fires (entity→Replica).
	//   4. PromoteReplicaToLive — models the dest-side spawn returning
	//      authority to this cell (test uses the same Stage as both sides).
	//
	// Steps 3+4 reset the state for the next iteration well within
	// HandoffCooldownTicks of the previous commitTick, which is exactly
	// the condition BypassCooldown is meant to skip.
	for i := 0; i < 3; i++ {
		sendTick := uint64(i*10 + 100)
		commitTick := sendTick + HandoffLeadTicks

		base.QueueCrossing(CrossingEvent{
			Entity:         ent,
			NetID:          netID,
			DestCellID:     "cell_1_0",
			BypassCooldown: true,
		})

		// Tick at sendTick: handoff fires, demote queued for commitTick.
		hd.Tick(sendTick)

		// Tick at commitTick: pending demote fires → entity becomes Replica.
		hd.Tick(commitTick)

		// PromoteReplicaToLive resets entity to Live for the next crossing.
		if err := base.PromoteReplicaToLive(netID, uint32(i+2)); err != nil {
			t.Fatalf("iteration %d: promote between crossings: %v", i, err)
		}
	}

	if len(rec.handoffs) != 3 {
		t.Fatalf("bypass-cooldown handoff count = %d, want 3 (cooldown was bypassed)", len(rec.handoffs))
	}
}

// TestHandoffDriver_DropsCrossingForReplicaInSameTickAsCommit verifies the
// stale-state guard for the in-tick race between a queued crossing and
// the previous handoff's commit-tick demote.
//
// Scenario reproduced from production logs (cell_1_0 at 20:21:40):
//
//  1. Tick T: BoundarySystem queues a crossing for netID=2 (entity Live).
//  2. Tick T also happens to be the commit-tick of a PREVIOUS handoff
//     for netID=2.
//  3. HandoffDriver.Tick(T) runs:
//     a. Drains pending demotes due now (the previous handoff commits;
//        entity becomes Replica on this cell). The entry is removed
//        from pendingDemotes — so hasPendingDemote(netID) returns false
//        when the next phase asks.
//     b. Processes new crossings. The queued crossing for netID=2 is
//        still in the queue. handleCrossing fires for an entity that is
//        now Replica on this cell.
//
// Without the presence check, step 3b ships a Handoff for a Replica.
// The destination spawns a new Live, leaving the previous destination's
// Live (from the previous handoff) ALSO Live — duplicate Live entities,
// orphaned player session, frame freeze.
func TestHandoffDriver_DropsCrossingForReplicaInSameTickAsCommit(t *testing.T) {
	base := newTestWorldBase(t, CellID{X: 0, Y: 0})
	rec := &handoffRecordingBridge{}
	hd := NewHandoffDriver(base, rec)

	ent := base.SpawnEntity(
		component.Position{X: 100, Y: 100},
		WithEntityKind(1),
		WithCollider(5),
	)
	netID := base.NetworkIDMap().Get(ent).ID

	// First crossing: handoff sent at tick 100, demote queued for 102.
	base.QueueCrossing(CrossingEvent{Entity: ent, NetID: netID, DestCellID: "cell_1_0"})
	hd.Tick(100)
	if len(rec.handoffs) != 1 {
		t.Fatalf("first crossing: handoffs = %d, want 1", len(rec.handoffs))
	}

	// At tick 102 (the previous handoff's commit tick), simulate
	// BoundarySystem having queued a crossing earlier this tick. The
	// driver MUST drain the previous demote first (entity becomes
	// Replica), then refuse to process the new crossing because the
	// entity is no longer Live on this cell.
	base.QueueCrossing(CrossingEvent{Entity: ent, NetID: netID, DestCellID: "cell_0_1"})
	hd.Tick(102)

	// The demote drain happened.
	_, pres, _ := base.LookupNetID(netID)
	if pres != PresenceReplica {
		t.Fatalf("post-drain presence = %v, want PresenceReplica", pres)
	}

	// The queued crossing must NOT have produced a second handoff:
	// the entity is now Replica on this cell, and shipping it again
	// would create duplicate Live copies.
	if len(rec.handoffs) != 1 {
		t.Fatalf("crossing for replica in commit-tick: handoffs = %d, want 1 (dropped)",
			len(rec.handoffs))
	}
}
