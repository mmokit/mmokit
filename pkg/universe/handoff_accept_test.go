package universe

import (
	"bytes"
	"testing"

	"github.com/mlange-42/ark/ecs"

	"github.com/zenion/mmokit/pkg/component"
)

// spawnCrossingEntity spawns a Live entity on base and queues a crossing for
// it toward dest, returning the entity handle and its netID.
func spawnCrossingEntity(t *testing.T, base *Stage, dest MeshCellID) (ecs.Entity, uint32) {
	t.Helper()
	ent := base.Spawn(
		component.Position{X: 100, Y: 100},
		component.EntityKind{Type: 1},
		component.Collider{Radius: 5},
	).Handle()
	netID := base.NetworkIDMap().Get(ent).ID
	base.QueueCrossing(CrossingEvent{Entity: ent, NetID: netID, DestCellID: dest})
	return ent, netID
}

func presenceOf(t *testing.T, base *Stage, netID uint32) EntityPresence {
	t.Helper()
	_, pres, ok := base.LookupNetID(netID)
	if !ok {
		t.Fatalf("netID=%d not tracked by netIDIdx", netID)
	}
	return pres
}

func (hd *HandoffDriver) inflightCount() int {
	hd.mu.Lock()
	defer hd.mu.Unlock()
	return len(hd.inflight)
}

func (hd *HandoffDriver) pendingDemoteCount() int {
	hd.mu.Lock()
	defer hd.mu.Unlock()
	n := 0
	for _, list := range hd.pendingDemotes {
		n += len(list)
	}
	return n
}

// TestHandoffDriver_NoDemoteUntilAccepted is the core CE-004 regression: a
// Handoff whose destination never acknowledges must NOT demote the source.
// Before this change the driver queued the demote the instant SendHandoff
// returned true — which for a remote destination only means the send was
// enqueued — so a lost Handoff left the entity with zero authoritative
// holders, permanently.
func TestHandoffDriver_NoDemoteUntilAccepted(t *testing.T) {
	base := newTestWorldBase(t, CellID{X: 0, Y: 0})

	rec := &handoffRecordingBridge{} // no autoAccept: destination never acks
	hd := NewHandoffDriver(base, rec)

	_, netID := spawnCrossingEntity(t, base, "cell_1_0")

	hd.Tick(7)
	if len(rec.handoffs) != 1 {
		t.Fatalf("handoff count after first tick = %d, want 1", len(rec.handoffs))
	}
	if hd.inflightCount() != 1 {
		t.Fatalf("inflight = %d, want 1", hd.inflightCount())
	}
	if hd.pendingDemoteCount() != 0 {
		t.Fatalf("pendingDemotes = %d before acceptance, want 0", hd.pendingDemoteCount())
	}

	// Tick well past commitTick (7+HandoffLeadTicks) — the entity must stay
	// Live because nothing ever accepted.
	for tick := uint64(8); tick <= 7+HandoffLeadTicks+10; tick++ {
		hd.Tick(tick)
	}

	if pres := presenceOf(t, base, netID); pres != PresenceLive {
		t.Fatalf("presence = %v after unaccepted handoff, want PresenceLive", pres)
	}
	if hd.pendingDemoteCount() != 0 {
		t.Fatalf("pendingDemotes = %d, want 0 while unaccepted", hd.pendingDemoteCount())
	}
	if rec.playerTransfers != 0 {
		t.Fatalf("OnPlayerTransfer fired %d times before acceptance, want 0", rec.playerTransfers)
	}
}

// TestHandoffDriver_AcceptArmsDemoteAtCommitTick verifies that an acceptance
// arriving before commitTick arms the demote for exactly commitTick — not
// earlier, not later.
func TestHandoffDriver_AcceptArmsDemoteAtCommitTick(t *testing.T) {
	base := newTestWorldBase(t, CellID{X: 0, Y: 0})
	rec := &handoffRecordingBridge{}
	hd := NewHandoffDriver(base, rec)

	_, netID := spawnCrossingEntity(t, base, "cell_1_0")
	hd.Tick(10)
	commitTick := rec.handoffs[0].CommitTick
	if commitTick != 10+HandoffLeadTicks {
		t.Fatalf("commitTick = %d, want %d", commitTick, 10+HandoffLeadTicks)
	}

	// Accept at T+1, before commitTick.
	hd.Tick(11)
	hd.OnHandoffAccepted(netID, rec.handoffs[0].Epoch, commitTick, "cell_1_0")
	if hd.inflightCount() != 0 {
		t.Fatalf("inflight = %d after acceptance, want 0", hd.inflightCount())
	}
	if hd.pendingDemoteCount() != 1 {
		t.Fatalf("pendingDemotes = %d after acceptance, want 1", hd.pendingDemoteCount())
	}
	if pres := presenceOf(t, base, netID); pres != PresenceLive {
		t.Fatalf("presence = %v at T+1, want PresenceLive (commit not due)", pres)
	}

	hd.Tick(commitTick)
	if pres := presenceOf(t, base, netID); pres != PresenceReplica {
		t.Fatalf("presence = %v at commitTick, want PresenceReplica", pres)
	}
}

// TestHandoffDriver_LateAcceptDemotesOnNextTick covers the drain's <=
// comparison: an acceptance that lands after commitTick has already passed
// must still demote, on the very next Tick.
func TestHandoffDriver_LateAcceptDemotesOnNextTick(t *testing.T) {
	base := newTestWorldBase(t, CellID{X: 0, Y: 0})
	rec := &handoffRecordingBridge{}
	hd := NewHandoffDriver(base, rec)

	_, netID := spawnCrossingEntity(t, base, "cell_1_0")
	hd.Tick(10)
	first := rec.handoffs[0]

	// Run past commitTick with no acceptance. Retries fire; each carries the
	// identical payload, so accepting any of them is accepting the original.
	for tick := uint64(11); tick <= first.CommitTick+5; tick++ {
		hd.Tick(tick)
	}
	if pres := presenceOf(t, base, netID); pres != PresenceLive {
		t.Fatalf("presence = %v past commitTick without acceptance, want PresenceLive", pres)
	}

	hd.OnHandoffAccepted(netID, first.Epoch, first.CommitTick, "cell_1_0")
	hd.Tick(first.CommitTick + 6)
	if pres := presenceOf(t, base, netID); pres != PresenceReplica {
		t.Fatalf("presence = %v after late acceptance, want PresenceReplica", pres)
	}
}

// TestHandoffDriver_RetriesUntilAccepted asserts the retry cadence and that
// retries re-send byte-identical payloads (which is what makes the
// destination's (NetID, Epoch) dedup work), and that they stop on acceptance.
func TestHandoffDriver_RetriesUntilAccepted(t *testing.T) {
	base := newTestWorldBase(t, CellID{X: 0, Y: 0})
	rec := &handoffRecordingBridge{}
	hd := NewHandoffDriver(base, rec)

	_, netID := spawnCrossingEntity(t, base, "cell_1_0")

	hd.Tick(0)
	if len(rec.handoffs) != 1 {
		t.Fatalf("sends after tick 0 = %d, want 1", len(rec.handoffs))
	}
	// No retry before the retry deadline.
	for tick := uint64(1); tick < HandoffAcceptRetryTicks; tick++ {
		hd.Tick(tick)
	}
	if len(rec.handoffs) != 1 {
		t.Fatalf("sends before retry deadline = %d, want 1", len(rec.handoffs))
	}

	hd.Tick(HandoffAcceptRetryTicks)
	if len(rec.handoffs) != 2 {
		t.Fatalf("sends at retry deadline = %d, want 2", len(rec.handoffs))
	}
	first, retry := rec.handoffs[0], rec.handoffs[1]
	if retry.Epoch != first.Epoch || retry.CommitTick != first.CommitTick {
		t.Fatalf("retry payload differs: epoch %d/%d commitTick %d/%d",
			retry.Epoch, first.Epoch, retry.CommitTick, first.CommitTick)
	}
	if !bytes.Equal(retry.TransferBlob, first.TransferBlob) {
		t.Fatal("retry TransferBlob differs from the original send")
	}

	// Accept: retries stop immediately.
	hd.OnHandoffAccepted(netID, first.Epoch, first.CommitTick, "cell_1_0")
	for tick := uint64(HandoffAcceptRetryTicks + 1); tick <= 4*HandoffAcceptRetryTicks; tick++ {
		hd.Tick(tick)
	}
	if len(rec.handoffs) != 2 {
		t.Fatalf("sends after acceptance = %d, want 2 (retries must stop)", len(rec.handoffs))
	}
}

// TestHandoffDriver_AbandonsWhenDestGone covers the one condition under which
// a handoff IS given up: SendHandoff returning false on a retry, meaning the
// destination cell is gone and provably cannot have promoted. The entity must
// stay Live, the epoch must NOT be rolled back (border frames carrying the
// bumped value have already shipped), and the cooldown must be cleared so the
// re-detected crossing is not blocked.
func TestHandoffDriver_AbandonsWhenDestGone(t *testing.T) {
	base := newTestWorldBase(t, CellID{X: 0, Y: 0})
	rec := &handoffRecordingBridge{}
	hd := NewHandoffDriver(base, rec)

	ent, netID := spawnCrossingEntity(t, base, "cell_1_0")
	hd.Tick(0)
	epochAfterSend := base.NetworkIDMap().Get(ent).Epoch

	// Destination disappears before the retry.
	rec.failsForDest = "cell_1_0"
	hd.Tick(HandoffAcceptRetryTicks)

	if hd.inflightCount() != 0 {
		t.Fatalf("inflight = %d after abandonment, want 0", hd.inflightCount())
	}
	if pres := presenceOf(t, base, netID); pres != PresenceLive {
		t.Fatalf("presence = %v after abandonment, want PresenceLive", pres)
	}
	if got := base.NetworkIDMap().Get(ent).Epoch; got != epochAfterSend {
		t.Fatalf("epoch = %d after abandonment, want %d (must never roll back)", got, epochAfterSend)
	}
	hd.mu.Lock()
	_, cooldownPresent := hd.lastHandoff[netID]["cell_1_0"]
	hd.mu.Unlock()
	if cooldownPresent {
		t.Fatal("cooldown entry survived abandonment; it would block the re-detected crossing")
	}

	// A re-queued crossing must issue a fresh handoff with a higher epoch.
	// The failed retry was never recorded (SendHandoff returned false before
	// appending), so this is the second recorded send.
	rec.failsForDest = ""
	base.QueueCrossing(CrossingEvent{Entity: ent, NetID: netID, DestCellID: "cell_1_0"})
	hd.Tick(HandoffAcceptRetryTicks + 1)
	if len(rec.handoffs) != 2 {
		t.Fatalf("sends after re-detected crossing = %d, want 2", len(rec.handoffs))
	}
	if got := rec.handoffs[1].Epoch; got != epochAfterSend+1 {
		t.Fatalf("re-detected crossing epoch = %d, want %d", got, epochAfterSend+1)
	}
}

// TestHandoffDriver_AcceptIsFencedAndIdempotent asserts the (netID, epoch,
// commitTick, destCellID) fence. A mismatched ack — for a superseded attempt,
// a different destination, or an unknown netID — must not arm a demote, and a
// duplicate matching ack must not queue a second one.
func TestHandoffDriver_AcceptIsFencedAndIdempotent(t *testing.T) {
	base := newTestWorldBase(t, CellID{X: 0, Y: 0})
	rec := &handoffRecordingBridge{}
	hd := NewHandoffDriver(base, rec)

	_, netID := spawnCrossingEntity(t, base, "cell_1_0")
	hd.Tick(10)
	p := rec.handoffs[0]

	cases := []struct {
		name       string
		netID      uint32
		epoch      uint32
		commitTick uint64
		from       MeshCellID
	}{
		{"unknown netID", netID + 999, p.Epoch, p.CommitTick, "cell_1_0"},
		{"wrong epoch", netID, p.Epoch + 1, p.CommitTick, "cell_1_0"},
		{"wrong commitTick", netID, p.Epoch, p.CommitTick + 1, "cell_1_0"},
		{"wrong source cell", netID, p.Epoch, p.CommitTick, "cell_0_1"},
	}
	for _, tc := range cases {
		hd.OnHandoffAccepted(tc.netID, tc.epoch, tc.commitTick, tc.from)
		if hd.pendingDemoteCount() != 0 {
			t.Fatalf("%s: pendingDemotes = %d, want 0", tc.name, hd.pendingDemoteCount())
		}
		if hd.inflightCount() != 1 {
			t.Fatalf("%s: inflight = %d, want 1 (entry must survive a fenced ack)",
				tc.name, hd.inflightCount())
		}
	}

	// Matching ack arms exactly one demote; a duplicate is a no-op.
	hd.OnHandoffAccepted(netID, p.Epoch, p.CommitTick, "cell_1_0")
	hd.OnHandoffAccepted(netID, p.Epoch, p.CommitTick, "cell_1_0")
	if got := hd.pendingDemoteCount(); got != 1 {
		t.Fatalf("pendingDemotes after duplicate ack = %d, want 1", got)
	}
}

// TestCancelPendingDemotesTo_CancelsInflight covers the merge interaction:
// after BeginMerge cancels a donor-bound handoff, a late ack from that donor
// must not re-arm a demote toward a cell that is being torn down.
func TestCancelPendingDemotesTo_CancelsInflight(t *testing.T) {
	base := newTestWorldBase(t, CellID{X: 0, Y: 0})
	rec := &handoffRecordingBridge{}
	hd := NewHandoffDriver(base, rec)

	_, netID := spawnCrossingEntity(t, base, "cell_1_0")
	hd.Tick(10)
	p := rec.handoffs[0]

	if n := hd.CancelPendingDemotesTo(map[MeshCellID]struct{}{"cell_1_0": {}}); n != 1 {
		t.Fatalf("CancelPendingDemotesTo returned %d, want 1 (the inflight entry)", n)
	}
	if hd.inflightCount() != 0 {
		t.Fatalf("inflight = %d after cancel, want 0", hd.inflightCount())
	}

	hd.OnHandoffAccepted(netID, p.Epoch, p.CommitTick, "cell_1_0")
	if hd.pendingDemoteCount() != 0 {
		t.Fatalf("pendingDemotes = %d after cancelled ack, want 0", hd.pendingDemoteCount())
	}
	hd.Tick(p.CommitTick)
	if pres := presenceOf(t, base, netID); pres != PresenceLive {
		t.Fatalf("presence = %v after cancelled handoff, want PresenceLive", pres)
	}
}

// TestHandoffDriver_InflightGCOnEntityDeath asserts the inflight map is
// bounded: an entry whose entity is no longer Live on this cell is dropped on
// the next retry pass and generates no further sends.
func TestHandoffDriver_InflightGCOnEntityDeath(t *testing.T) {
	base := newTestWorldBase(t, CellID{X: 0, Y: 0})
	rec := &handoffRecordingBridge{}
	hd := NewHandoffDriver(base, rec)

	_, netID := spawnCrossingEntity(t, base, "cell_1_0")
	hd.Tick(0)
	if hd.inflightCount() != 1 {
		t.Fatalf("inflight = %d, want 1", hd.inflightCount())
	}

	// Entity leaves this cell's Live set by another route.
	if err := base.DemoteLiveToReplica(netID, "cell_1_0"); err != nil {
		t.Fatalf("DemoteLiveToReplica: %v", err)
	}

	hd.Tick(HandoffAcceptRetryTicks)
	if hd.inflightCount() != 0 {
		t.Fatalf("inflight = %d after GC pass, want 0", hd.inflightCount())
	}
	if len(rec.handoffs) != 1 {
		t.Fatalf("sends = %d after GC, want 1 (no retry for a dead entity)", len(rec.handoffs))
	}

	for tick := uint64(HandoffAcceptRetryTicks + 1); tick <= 5*HandoffAcceptRetryTicks; tick++ {
		hd.Tick(tick)
	}
	if len(rec.handoffs) != 1 {
		t.Fatalf("sends after further ticks = %d, want 1", len(rec.handoffs))
	}
}
