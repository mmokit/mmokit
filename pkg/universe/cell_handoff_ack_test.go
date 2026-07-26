package universe

import (
	"testing"
)

// ackCaptureBridge records SendHandoffAccepted calls made by a destination
// cell and can be made to fail, standing in for a source cell whose inbox is
// full or whose stream is down.
type ackCaptureBridge struct {
	NoopBridge
	acks   []*HandoffAckPayload
	failed bool
}

func (b *ackCaptureBridge) SendHandoffAccepted(_ MeshCellID, ack *HandoffAckPayload) bool {
	if b.failed {
		return false
	}
	clone := *ack
	b.acks = append(b.acks, &clone)
	return true
}

func handoffMsg(netID, epoch uint32, commitTick uint64) CellMessage {
	return CellMessage{
		Type:       MsgHandoff,
		FromCellID: "cell_0_0",
		Handoff: &HandoffPayload{
			NetID:      netID,
			Epoch:      epoch,
			CommitTick: commitTick,
		},
	}
}

func pendingPromoteCount(c *Cell) int {
	n := 0
	for _, list := range c.pendingPromotes {
		n += len(list)
	}
	return n
}

// TestCellHandoff_DuplicateIsDedupedAndReAcked covers the destination-side
// (NetID, Epoch) dedup. The source re-sends the identical Handoff every
// HandoffAcceptRetryTicks until it sees an ack, so duplicates are the normal
// case under ack loss: each must be re-acked (the ack is idempotent) while
// exactly one promote is queued.
func TestCellHandoff_DuplicateIsDedupedAndReAcked(t *testing.T) {
	c := newMinimalCell(t, CellID{X: 1, Y: 0})
	bridge := &ackCaptureBridge{}
	c.Bridge = bridge

	c.processMessage(handoffMsg(42, 3, 12))
	c.processMessage(handoffMsg(42, 3, 12))
	c.processMessage(handoffMsg(42, 3, 12))

	if got := pendingPromoteCount(c); got != 1 {
		t.Fatalf("pendingPromotes = %d after 3 identical handoffs, want 1", got)
	}
	if got := len(bridge.acks); got != 3 {
		t.Fatalf("acks = %d, want 3 (every duplicate must be re-acked)", got)
	}
	for i, ack := range bridge.acks {
		if ack.NetID != 42 || ack.Epoch != 3 || ack.CommitTick != 12 {
			t.Fatalf("ack[%d] = %+v, want {42 3 12}", i, *ack)
		}
	}
}

// TestCellHandoff_HigherEpochIsAccepted asserts the dedup is per-epoch, not
// per-netID: an entity that leaves and comes back gets a bumped epoch and
// must be accepted again.
func TestCellHandoff_HigherEpochIsAccepted(t *testing.T) {
	c := newMinimalCell(t, CellID{X: 1, Y: 0})
	bridge := &ackCaptureBridge{}
	c.Bridge = bridge

	c.processMessage(handoffMsg(42, 3, 12))
	c.processMessage(handoffMsg(42, 4, 20))

	if got := pendingPromoteCount(c); got != 2 {
		t.Fatalf("pendingPromotes = %d, want 2 (distinct epochs)", got)
	}
	if got := len(bridge.acks); got != 2 {
		t.Fatalf("acks = %d, want 2", got)
	}
	if bridge.acks[1].Epoch != 4 || bridge.acks[1].CommitTick != 20 {
		t.Fatalf("second ack = %+v, want epoch 4 commitTick 20", *bridge.acks[1])
	}
}

// TestCellHandoff_LowerEpochIsRejected asserts a stale, reordered Handoff for
// an epoch this cell has already superseded does not queue another promote.
func TestCellHandoff_LowerEpochIsRejected(t *testing.T) {
	c := newMinimalCell(t, CellID{X: 1, Y: 0})
	bridge := &ackCaptureBridge{}
	c.Bridge = bridge

	c.processMessage(handoffMsg(42, 7, 20))
	c.processMessage(handoffMsg(42, 6, 12))

	if got := pendingPromoteCount(c); got != 1 {
		t.Fatalf("pendingPromotes = %d, want 1 (stale epoch must not queue)", got)
	}
}

// TestCellHandoff_UndeliverableAckSkipsPromote is the invariant that prevents
// a destination from becoming authoritative with no way for the source to
// learn it should demote. If the ack cannot be enqueued, nothing is marked
// and nothing is queued — the source retries the identical Handoff.
func TestCellHandoff_UndeliverableAckSkipsPromote(t *testing.T) {
	c := newMinimalCell(t, CellID{X: 1, Y: 0})
	bridge := &ackCaptureBridge{failed: true}
	c.Bridge = bridge

	c.processMessage(handoffMsg(42, 3, 12))

	if got := pendingPromoteCount(c); got != 0 {
		t.Fatalf("pendingPromotes = %d after undeliverable ack, want 0", got)
	}
	if c.Stage.handoffAlreadyAccepted(42, 3) {
		t.Fatal("handoffAccepted was marked despite an undeliverable ack")
	}

	// Once the ack path recovers, the retried Handoff is accepted normally.
	bridge.failed = false
	c.processMessage(handoffMsg(42, 3, 12))
	if got := pendingPromoteCount(c); got != 1 {
		t.Fatalf("pendingPromotes = %d after ack path recovered, want 1", got)
	}
}

// TestCellHandoff_MarkForgottenOnDemote asserts DemoteLiveToReplica clears the
// acceptance mark, so an entity that later hands back into this cell is not
// deduped away.
func TestCellHandoff_MarkForgottenOnDemote(t *testing.T) {
	c := newMinimalCell(t, CellID{X: 1, Y: 0})
	bridge := &ackCaptureBridge{}
	c.Bridge = bridge

	// Spawn a Live entity on this cell and accept a handoff for its netID,
	// which is the state a real promoted entity ends up in.
	_, netID := spawnCrossingEntity(t, c.Stage, "cell_0_0")
	c.Stage.DrainCrossingQueue()
	c.processMessage(handoffMsg(netID, 3, 12))
	if !c.Stage.handoffAlreadyAccepted(netID, 3) {
		t.Fatal("handoffAccepted not marked after a successful ack")
	}

	if err := c.Stage.DemoteLiveToReplica(netID, "cell_0_0"); err != nil {
		t.Fatalf("DemoteLiveToReplica: %v", err)
	}
	if c.Stage.handoffAlreadyAccepted(netID, 3) {
		t.Fatal("handoffAccepted survived DemoteLiveToReplica; a later handoff back in would be deduped away")
	}
}

// TestCellHandoffAccepted_NilDriverIsDropped asserts an acceptance arriving at
// a cell whose bridge hosts no HandoffDriver (NoopBridge, tests) is dropped
// rather than panicking.
func TestCellHandoffAccepted_NilDriverIsDropped(t *testing.T) {
	c := newMinimalCell(t, CellID{X: 0, Y: 0})
	c.Bridge = &ackCaptureBridge{}

	c.processMessage(CellMessage{
		Type:       MsgHandoffAccepted,
		FromCellID: "cell_1_0",
		HandoffAck: &HandoffAckPayload{NetID: 42, Epoch: 3, CommitTick: 12},
	})
	c.processMessage(CellMessage{Type: MsgHandoffAccepted, FromCellID: "cell_1_0"})
}
