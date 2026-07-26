package system

import (
	"testing"

	"github.com/mlange-42/ark/ecs"

	"github.com/zenion/mmoserver/pkg/net"
	"github.com/zenion/mmoserver/pkg/quantize"
	"github.com/zenion/mmoserver/pkg/replication"
	"github.com/zenion/mmoserver/pkg/spatial"
)

func newPendingAckHarness(t *testing.T, fw FrameWriter, ackTimeout uint32) (*ReplicationSystem, *spatial.HashGrid, ecs.Entity, *uint32) {
	t.Helper()
	world := ecs.NewWorld()
	grid := spatial.NewHashGrid(100)
	em := newTestEntityMapper(world)
	reg := NewReplicatorRegistry()
	reg.Register(&testReplicator{entityType: 0})
	viewerEntity := em.spawn(0, 0, 100, 0)
	entity := em.spawn(100, 0, 1, 0)
	grid.Register(spatial.Entry{Entity: entity, X: 100, Y: 0})
	tick := uint32(1)
	sys := NewReplicationSystem(ReplicationConfig{
		World:       world,
		SpatialGrid: grid,
		Viewers: &fixedViewerSource{viewers: []ViewerInfo{
			{ConnID: 1, Entity: viewerEntity, X: 0, Y: 0},
		}},
		Frame:                  fw,
		Replicators:            reg,
		AoIRadius:              1000,
		AckMode:                replication.AckExplicit,
		SentHistoryDepth:       4,
		PendingAckTimeoutTicks: ackTimeout,
		GetTick:                func() uint32 { return tick },
	})
	return sys, grid, entity, &tick
}

func TestReplicationSystem_ExplicitFirstFrameWaitsForExactAck(t *testing.T) {
	fw := &scriptedFrameWriter{results: []net.SendResult{{
		Disposition: net.SendQueued,
		Delivery:    net.DeliveryBestEffort,
	}}}
	sys, _, _, _ := newPendingAckHarness(t, fw, 10)

	sys.Update(0.05)
	conn := sys.connections[1]
	if conn.pending == nil || conn.pending.seq != fw.frames[0].Seq {
		t.Fatal("queued explicit frame was not retained as the causal attempt")
	}
	baseline := conn.store.Baseline(1)
	if baseline == nil || baseline.RingLen != 1 {
		t.Fatalf("queued explicit ring = %+v, want one attempted snapshot", baseline)
	}
	if baseline.Acked != nil || conn.store.HasLastHash(1) || conn.store.ExistingPriority(1) != nil {
		t.Fatal("queued explicit frame advanced acknowledged entity state")
	}
	if sys.IsVisible(1, 1) {
		t.Fatal("queued explicit frame advanced acknowledged visibility")
	}

	// Only the exact in-flight sequence can promote the frame transaction.
	sys.AckSequence(1, fw.frames[0].Seq+1)
	if baseline.Acked != nil || conn.pending == nil {
		t.Fatal("non-matching ACK promoted explicit state")
	}
	sys.AckSequence(1, fw.frames[0].Seq)
	if conn.pending != nil || baseline.Acked == nil || baseline.RingLen != 0 {
		t.Fatal("matching ACK did not promote and prune explicit state")
	}
	if !conn.store.HasLastHash(1) || conn.store.ExistingPriority(1) == nil || !sys.IsVisible(1, 1) {
		t.Fatal("matching ACK did not commit hash, priority, and visibility atomically")
	}
}

func TestReplicationSystem_ExplicitAckMustMatchStreamGeneration(t *testing.T) {
	fw := &scriptedFrameWriter{results: []net.SendResult{
		{Disposition: net.SendQueued, Delivery: net.DeliveryBestEffort},
		{Disposition: net.SendQueued, Delivery: net.DeliveryBestEffort},
	}}
	sys, _, _, tick := newPendingAckHarness(t, fw, 10)
	generation := uint32(11)
	sys.cfg.StreamGeneration = func(*ViewerInfo) (uint32, bool) {
		return generation, true
	}

	sys.Update(0.05)
	oldFrame := fw.frames[0]
	generation = 12
	*tick = 2
	sys.Update(0.05)
	newFrame := fw.frames[1]
	conn := sys.connections[1]
	if oldFrame.Seq != newFrame.Seq || conn.pending == nil {
		t.Fatalf("fixture did not create colliding seq values: old=%d new=%d", oldFrame.Seq, newFrame.Seq)
	}

	// The legacy API deliberately refuses explicitly generated streams.
	sys.AckSequence(1, newFrame.Seq)
	sys.AckFrame(1, oldFrame.StreamEpoch, oldFrame.Seq)
	if conn.pending == nil || conn.store.Baseline(1).Acked != nil {
		t.Fatal("unscoped or old-generation ACK committed successor state")
	}

	sys.AckFrame(1, newFrame.StreamEpoch, newFrame.Seq)
	if conn.pending != nil || conn.store.Baseline(1).Acked == nil {
		t.Fatal("matching generation-aware ACK did not commit successor state")
	}
}

func TestReplicationSystem_LostExplicitFirstFrameRetriesFreshFull(t *testing.T) {
	fw := &scriptedFrameWriter{results: []net.SendResult{
		{Disposition: net.SendQueued, Delivery: net.DeliveryBestEffort},
		{Disposition: net.SendQueued, Delivery: net.DeliveryBestEffort},
	}}
	sys, _, _, tick := newPendingAckHarness(t, fw, 2)

	sys.Update(0.05)
	firstSeq := fw.frames[0].Seq
	*tick = 2
	sys.Update(0.05)
	if len(fw.frames) != 1 {
		t.Fatalf("sent %d frames while causal attempt pending, want 1", len(fw.frames))
	}

	*tick = 3
	sys.Update(0.05)
	if len(fw.frames) != 2 || len(fw.frames[1].Full) != 1 {
		t.Fatalf("timeout retry = %+v, want one full entity", fw.frames)
	}
	if fw.frames[1].Flags&quantize.FrameFlagFreshSnapshot == 0 {
		t.Fatal("timeout retry did not force FreshSnapshot")
	}
	retrySeq := fw.frames[1].Seq
	if retrySeq == firstSeq {
		t.Fatal("timeout retry reused the abandoned sequence")
	}

	sys.AckSequence(1, firstSeq)
	conn := sys.connections[1]
	if conn.pending == nil || conn.pending.seq != retrySeq || conn.store.Baseline(1).Acked != nil {
		t.Fatal("late ACK for abandoned attempt disturbed the fresh retry")
	}
	sys.AckSequence(1, retrySeq)
	if conn.pending != nil || conn.store.Baseline(1).Acked == nil || !sys.IsVisible(1, 1) {
		t.Fatal("fresh retry ACK did not establish acknowledged state")
	}
}

func TestReplicationSystem_LostExplicitRemovalRetriesUntilExactAck(t *testing.T) {
	fw := &scriptedFrameWriter{}
	sys, grid, entity, tick := newPendingAckHarness(t, fw, 2)
	var removedIDs []uint32
	sys.cfg.RemovedIDs = func() []uint32 {
		ids := removedIDs
		removedIDs = nil
		return ids
	}

	sys.Update(0.05)
	sys.AckSequence(1, fw.frames[0].Seq)
	conn := sys.connections[1]
	grid.Deregister(entity)
	sys.cfg.World.RemoveEntity(entity)
	removedIDs = []uint32{1}
	*tick = 2
	sys.Update(0.05)
	firstRemovalSeq := fw.frames[1].Seq
	if len(fw.frames[1].Removed) != 1 || !sys.IsVisible(1, 1) || conn.store.Baseline(1) == nil {
		t.Fatal("unacknowledged removal changed committed visibility/baseline")
	}
	if !conn.pendingRemoved[1] {
		t.Fatal("unacknowledged tick-scoped removal was not retained")
	}

	*tick = 3
	sys.Update(0.05)
	*tick = 4
	sys.Update(0.05)
	if len(fw.frames) != 3 || len(fw.frames[2].Removed) != 1 || len(fw.frames[2].Exited) != 0 {
		t.Fatalf("removal retry = %+v, want a tombstone (not AoI exit)", fw.frames)
	}
	if fw.frames[2].Flags&quantize.FrameFlagFreshSnapshot == 0 {
		t.Fatal("removal retry after timeout did not force FreshSnapshot")
	}
	retrySeq := fw.frames[2].Seq
	sys.AckSequence(1, firstRemovalSeq)
	if conn.pending == nil || conn.pending.seq != retrySeq || !sys.IsVisible(1, 1) {
		t.Fatal("late removal ACK disturbed the current retry")
	}
	sys.AckSequence(1, retrySeq)
	if conn.pending != nil || sys.IsVisible(1, 1) || conn.store.Baseline(1) != nil || conn.pendingRemoved[1] {
		t.Fatal("exact removal ACK did not commit tombstone state")
	}
}

func TestReplicationSystem_ForcedFreshDoesNotFabricateBlink(t *testing.T) {
	fw := &stubFrameWriter{}
	world := ecs.NewWorld()
	grid := spatial.NewHashGrid(100)
	em := newTestEntityMapper(world)
	reg := NewReplicatorRegistry()
	reg.Register(&testReplicator{entityType: 0})
	viewerEntity := em.spawn(0, 0, 100, 0)
	entity := em.spawn(100, 0, 1, 0)
	grid.Register(spatial.Entry{Entity: entity, X: 100, Y: 0})
	tick := uint32(1)
	blinks := 0
	sys := NewReplicationSystem(ReplicationConfig{
		World:       world,
		SpatialGrid: grid,
		Viewers: &fixedViewerSource{viewers: []ViewerInfo{
			{ConnID: 1, Entity: viewerEntity, X: 0, Y: 0},
		}},
		Frame:              fw,
		Replicators:        reg,
		AoIRadius:          1000,
		AckMode:            replication.AckReliable,
		GetTick:            func() uint32 { return tick },
		BlinkDetectorTicks: 10,
		OnBlinkDetected:    func(_, _ uint32, _ uint64) { blinks++ },
	})

	sys.Update(0.05)
	conn := sys.connections[1]
	conn.recentRemovals[1] = 1
	conn.forceFresh = true
	tick = 2
	sys.Update(0.05)
	if len(fw.frames[1].Full) != 1 || fw.frames[1].Flags&quantize.FrameFlagFreshSnapshot == 0 {
		t.Fatalf("forced reset frame = %+v, want fresh full payload", fw.frames[1])
	}
	if len(fw.frames[1].Entered) != 0 {
		t.Fatalf("forced reset fabricated semantic Entered=%v", fw.frames[1].Entered)
	}
	if blinks != 0 {
		t.Fatalf("forced reset fired %d blink callbacks, want 0", blinks)
	}
}
