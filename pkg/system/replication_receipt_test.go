package system

import (
	"testing"

	"github.com/mlange-42/ark/ecs"

	"github.com/zenion/mmokit/pkg/net"
	"github.com/zenion/mmokit/pkg/quantize"
	"github.com/zenion/mmokit/pkg/replication"
	"github.com/zenion/mmokit/pkg/spatial"
)

type receiptFrameWriter struct {
	frames   []ReplicationFrame
	results  []net.SendResult
	receipts map[uint32][]net.ReplicationReceipt
}

func (w *receiptFrameWriter) WriteFrame(frame *ReplicationFrame) net.SendResult {
	cloned := *frame
	cloned.Full = append([]FullPayload(nil), frame.Full...)
	cloned.Deltas = append([]DeltaPayload(nil), frame.Deltas...)
	cloned.Entered = append([]uint32(nil), frame.Entered...)
	cloned.Exited = append([]uint32(nil), frame.Exited...)
	cloned.Removed = append([]uint32(nil), frame.Removed...)
	w.frames = append(w.frames, cloned)
	if len(w.results) == 0 {
		return net.SendResult{Disposition: net.SendQueued, Delivery: net.DeliveryReliableOrdered}
	}
	result := w.results[0]
	w.results = w.results[1:]
	return result
}

func (w *receiptFrameWriter) DrainFrameReceipts(connID uint32) []net.ReplicationReceipt {
	receipts := w.receipts[connID]
	delete(w.receipts, connID)
	return receipts
}

func (w *receiptFrameWriter) receipt(connID, streamEpoch, seq uint32, result net.SendResult) {
	if w.receipts == nil {
		w.receipts = make(map[uint32][]net.ReplicationReceipt)
	}
	w.receipts[connID] = append(w.receipts[connID], net.ReplicationReceipt{
		ConnID:      connID,
		StreamEpoch: streamEpoch,
		Seq:         seq,
		Result:      result,
	})
}

func newReceiptReplicationHarness(t *testing.T, fw FrameWriter) (*ReplicationSystem, *testEntityMapper, *spatial.HashGrid, ecs.Entity, *uint32) {
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
		Frame:                      fw,
		Replicators:                reg,
		AoIRadius:                  1000,
		AckMode:                    replication.AckReliable,
		PendingReceiptTimeoutTicks: 3,
		GetTick:                    func() uint32 { return tick },
	})
	return sys, em, grid, entity, &tick
}

func TestReplicationSystem_OrderedReceiptCommitsOnDrain(t *testing.T) {
	fw := &receiptFrameWriter{results: []net.SendResult{
		{Disposition: net.SendQueued, Delivery: net.DeliveryOrdered},
		{Disposition: net.SendQueued, Delivery: net.DeliveryReliableOrdered},
	}}
	sys, _, _, _, tick := newReceiptReplicationHarness(t, fw)

	sys.Update(0.05)
	conn := sys.connections[1]
	if conn.pending == nil || !conn.pending.awaitReceipt {
		t.Fatal("ordered tracked frame was not retained pending its receipt")
	}
	if conn.store.Baseline(1) != nil || sys.IsVisible(1, 1) {
		t.Fatal("ordered mesh acceptance committed before final receipt")
	}

	fw.receipt(1, fw.frames[0].StreamEpoch, fw.frames[0].Seq, net.SendResult{
		Disposition: net.SendQueued,
		Delivery:    net.DeliveryReliableOrdered,
	})
	*tick = 2
	sys.Update(0.05)
	if conn.pending != nil {
		t.Fatal("successful receipt did not release pending transaction")
	}
	if baseline := conn.store.Baseline(1); baseline == nil || baseline.Acked == nil {
		t.Fatal("successful receipt did not commit the exact baseline")
	}
	if !sys.IsVisible(1, 1) {
		t.Fatal("successful receipt did not commit visibility")
	}
}

func TestReplicationSystem_ReceiptMustMatchStreamGeneration(t *testing.T) {
	fw := &receiptFrameWriter{results: []net.SendResult{
		{Disposition: net.SendQueued, Delivery: net.DeliveryOrdered},
		{Disposition: net.SendQueued, Delivery: net.DeliveryOrdered},
	}}
	sys, _, _, _, tick := newReceiptReplicationHarness(t, fw)
	generation := uint32(7)
	sys.cfg.StreamGeneration = func(*ViewerInfo) (uint32, bool) {
		return generation, true
	}

	sys.Update(0.05)
	oldFrame := fw.frames[0]
	if oldFrame.StreamEpoch != 7 || oldFrame.Seq != 1 {
		t.Fatalf("old frame = epoch %d seq %d, want 7/1", oldFrame.StreamEpoch, oldFrame.Seq)
	}

	generation = 8
	*tick = 2
	sys.Update(0.05)
	newFrame := fw.frames[1]
	conn := sys.connections[1]
	if newFrame.StreamEpoch != 8 || newFrame.Seq != 1 || conn.pending == nil {
		t.Fatalf("new frame = epoch %d seq %d pending=%v, want 8/1 pending", newFrame.StreamEpoch, newFrame.Seq, conn.pending != nil)
	}

	// The old and new stream deliberately share seq=1. Only the generation
	// keeps this delayed receipt from committing the new transaction.
	fw.receipt(1, oldFrame.StreamEpoch, oldFrame.Seq, net.SendResult{
		Disposition: net.SendQueued,
		Delivery:    net.DeliveryReliableOrdered,
	})
	*tick = 3
	sys.Update(0.05)
	if conn.pending == nil || conn.store.Baseline(1) != nil {
		t.Fatal("old-generation receipt committed the successor transaction")
	}

	fw.receipt(1, newFrame.StreamEpoch, newFrame.Seq, net.SendResult{
		Disposition: net.SendQueued,
		Delivery:    net.DeliveryReliableOrdered,
	})
	*tick = 4
	sys.Update(0.05)
	if conn.pending != nil || conn.store.Baseline(1) == nil {
		t.Fatal("matching successor receipt did not commit its transaction")
	}
}

func TestReplicationSystem_ReceiptFailureRetriesRemovalAsFresh(t *testing.T) {
	fw := &receiptFrameWriter{results: []net.SendResult{
		{Disposition: net.SendQueued, Delivery: net.DeliveryReliableOrdered},
		{Disposition: net.SendQueued, Delivery: net.DeliveryOrdered},
		{Disposition: net.SendQueued, Delivery: net.DeliveryOrdered},
		{Disposition: net.SendQueued, Delivery: net.DeliveryReliableOrdered},
	}}
	sys, _, grid, entity, tick := newReceiptReplicationHarness(t, fw)
	var removedIDs []uint32
	sys.cfg.RemovedIDs = func() []uint32 {
		ids := removedIDs
		removedIDs = nil
		return ids
	}

	sys.Update(0.05)
	conn := sys.connections[1]
	grid.Deregister(entity)
	sys.cfg.World.RemoveEntity(entity)
	removedIDs = []uint32{1}
	*tick = 2
	sys.Update(0.05)
	if conn.pending == nil || len(fw.frames[1].Removed) != 1 {
		t.Fatal("tracked removal was not retained pending receipt")
	}
	if !sys.IsVisible(1, 1) || conn.store.Baseline(1) == nil || !conn.pendingRemoved[1] {
		t.Fatal("tracked removal advanced committed state before receipt")
	}

	fw.receipt(1, fw.frames[1].StreamEpoch, fw.frames[1].Seq, net.SendResult{Disposition: net.SendBackpressure})
	*tick = 3
	sys.Update(0.05)
	if len(fw.frames) != 3 || len(fw.frames[2].Removed) != 1 || fw.frames[2].Removed[0] != 1 {
		t.Fatalf("failed receipt retry = %+v, want retained removal", fw.frames)
	}
	if fw.frames[2].Flags&quantize.FrameFlagFreshSnapshot == 0 {
		t.Fatal("retry after failed receipt must force a fresh reset")
	}
	if !sys.IsVisible(1, 1) || conn.store.Baseline(1) == nil {
		t.Fatal("removal retry committed before its successful receipt")
	}

	fw.receipt(1, fw.frames[2].StreamEpoch, fw.frames[2].Seq, net.SendResult{
		Disposition: net.SendQueued,
		Delivery:    net.DeliveryReliableOrdered,
	})
	*tick = 4
	sys.Update(0.05)
	if sys.IsVisible(1, 1) || conn.store.Baseline(1) != nil || conn.pendingRemoved[1] {
		t.Fatal("successful retry receipt did not commit removal atomically")
	}
}

func TestReplicationSystem_RemovedDuringPendingThenVisibleForcesFullLifecycle(t *testing.T) {
	fw := &receiptFrameWriter{results: []net.SendResult{
		{Disposition: net.SendQueued, Delivery: net.DeliveryReliableOrdered},
		{Disposition: net.SendQueued, Delivery: net.DeliveryOrdered},
		{Disposition: net.SendQueued, Delivery: net.DeliveryReliableOrdered},
	}}
	sys, em, grid, entity, tick := newReceiptReplicationHarness(t, fw)
	var removedIDs []uint32
	sys.cfg.RemovedIDs = func() []uint32 {
		ids := removedIDs
		removedIDs = nil
		return ids
	}

	// Establish the committed lifecycle, then submit a changed frame that
	// waits for a gateway receipt.
	sys.Update(0.05)
	position, _, _ := em.mapper.Get(entity)
	position.X = 150
	grid.Update(spatial.Entry{Entity: entity, X: 150, Y: 0})
	*tick = 2
	sys.Update(0.05)
	conn := sys.connections[1]
	if conn.pending == nil || len(fw.frames[1].Deltas) != 1 {
		t.Fatalf("changed tracked frame = %+v, want one pending delta", fw.frames[1])
	}

	// The entity is destroyed and recreated between observable replication
	// frames. Supplying the tombstone while the prior frame is frozen models
	// that lifecycle race; it is visible again by the time the receipt lands.
	removedIDs = []uint32{1}
	*tick = 3
	sys.Update(0.05)
	if !conn.pendingRemoved[1] {
		t.Fatal("removal observed during pending frame was not retained")
	}

	fw.receipt(1, fw.frames[1].StreamEpoch, fw.frames[1].Seq, net.SendResult{
		Disposition: net.SendQueued,
		Delivery:    net.DeliveryReliableOrdered,
	})
	*tick = 4
	sys.Update(0.05)
	latest := fw.frames[len(fw.frames)-1]
	if len(latest.Full) != 1 || len(latest.Deltas) != 0 {
		t.Fatalf("reappeared retained-tombstone frame full=%d deltas=%d, want full lifecycle", len(latest.Full), len(latest.Deltas))
	}
	if len(latest.Entered) != 0 {
		t.Fatalf("full lifecycle refresh fabricated semantic entry: %v", latest.Entered)
	}
	if conn.pendingRemoved[1] {
		t.Fatal("accepted full lifecycle refresh did not supersede retained tombstone")
	}
}

func TestReplicationSystem_RemovalDuringPendingExitRetiresAfterExitReceipt(t *testing.T) {
	fw := &receiptFrameWriter{results: []net.SendResult{
		{Disposition: net.SendQueued, Delivery: net.DeliveryReliableOrdered},
		{Disposition: net.SendQueued, Delivery: net.DeliveryOrdered},
		{Disposition: net.SendQueued, Delivery: net.DeliveryReliableOrdered},
	}}
	sys, _, grid, entity, tick := newReceiptReplicationHarness(t, fw)
	var removedIDs []uint32
	sys.cfg.RemovedIDs = func() []uint32 {
		ids := removedIDs
		removedIDs = nil
		return ids
	}

	sys.Update(0.05) // establish committed visibility
	grid.Deregister(entity)
	*tick = 2
	sys.Update(0.05)
	conn := sys.connections[1]
	if conn.pending == nil || len(fw.frames[1].Exited) != 1 || fw.frames[1].Exited[0] != 1 {
		t.Fatalf("pending exit frame = %+v, want Exited=[1]", fw.frames[1])
	}

	// The entity is authoritatively destroyed after the exit was submitted but
	// before its final gateway receipt arrives.
	sys.cfg.World.RemoveEntity(entity)
	removedIDs = []uint32{1}
	*tick = 3
	sys.Update(0.05)
	if !conn.pendingRemoved[1] {
		t.Fatal("late removal was not retained while exit receipt was pending")
	}

	fw.receipt(1, fw.frames[1].StreamEpoch, fw.frames[1].Seq, net.SendResult{
		Disposition: net.SendQueued,
		Delivery:    net.DeliveryReliableOrdered,
	})
	*tick = 4
	sys.Update(0.05)
	if conn.pendingRemoved[1] {
		t.Fatal("committed exit left an unreachable pending tombstone")
	}
	if sys.IsVisible(1, 1) || conn.store.Baseline(1) != nil {
		t.Fatal("committed exit did not retire visibility and baseline")
	}
	latest := fw.frames[len(fw.frames)-1]
	if len(latest.Removed) != 0 {
		t.Fatalf("post-exit frame emitted redundant tombstone: %v", latest.Removed)
	}
}

// TestReplicationSystem_DatagramReceiptLatchesExplicitAck covers the
// distributed half of CE-003. On a host, eng.ConnMgr is a VirtualConnManager
// that cannot see the client's transport, so the connection starts on
// AckReliable. The gateway's mesh receipt carries the delivery class actually
// achieved; when that is below reliable-ordered, the client is on a datagram
// transport and the connection must switch to explicit client ACKs.
func TestReplicationSystem_DatagramReceiptLatchesExplicitAck(t *testing.T) {
	fw := &receiptFrameWriter{results: []net.SendResult{
		{Disposition: net.SendQueued, Delivery: net.DeliveryOrdered},
	}}
	sys, _, _, _, tick := newReceiptReplicationHarness(t, fw)

	sys.Update(0.05)
	first := sys.connections[1]
	if first.mode != replication.AckReliable {
		t.Fatalf("initial mode = %v, want AckReliable (a host cannot see the client transport)", first.mode)
	}
	if first.pending == nil || !first.pending.awaitReceipt {
		t.Fatal("ordered tracked frame was not retained pending its receipt")
	}

	// The gateway reports a successful enqueue on a best-effort transport.
	fw.receipt(1, fw.frames[0].StreamEpoch, fw.frames[0].Seq, net.SendResult{
		Disposition: net.SendQueued,
		Delivery:    net.DeliveryBestEffort,
	})
	*tick = 2
	sys.Update(0.05)

	latched := sys.connections[1]
	if latched == first {
		t.Fatal("connection state was mutated in place; the BaselineStore is built from the mode and must be replaced with it")
	}
	if latched.mode != replication.AckExplicit {
		t.Fatalf("latched mode = %v, want AckExplicit", latched.mode)
	}
	if latched.streamEpoch != first.streamEpoch || latched.hasStreamEpoch != first.hasStreamEpoch {
		t.Fatal("stream identity was not carried across the latch")
	}
	if !sys.IsVisible(1, 1) && len(fw.frames) < 2 {
		t.Fatal("no frame emitted after the latch")
	}

	// The frame after the latch is a self-contained reset and waits for an
	// explicit client ACK rather than another receipt.
	if len(fw.frames) < 2 {
		t.Fatalf("frames = %d, want at least 2", len(fw.frames))
	}
	post := fw.frames[len(fw.frames)-1]
	if post.Flags&quantize.FrameFlagFreshSnapshot == 0 {
		t.Fatal("the first frame after the latch must be a FreshSnapshot")
	}
	if latched.pending == nil {
		t.Fatal("post-latch frame did not stay pending")
	}
	if latched.pending.awaitReceipt {
		t.Fatal("post-latch frame is waiting for a receipt; it must wait for an explicit client ACK")
	}
	if bl := latched.store.Baseline(1); bl != nil && bl.Acked != nil {
		t.Fatal("post-latch frame committed a baseline before its ACK")
	}

	sys.AckFrame(1, latched.pending.streamEpoch, latched.pending.seq)
	if latched.pending != nil {
		t.Fatal("explicit ACK did not release the pending attempt")
	}
	if bl := latched.store.Baseline(1); bl == nil || bl.Acked == nil {
		t.Fatal("explicit ACK did not commit the baseline")
	}
}

// TestReplicationSystem_DatagramReceiptLatchesOnce asserts the latch is
// one-shot: a second sub-reliable receipt on an already-explicit connection
// must not tear the stream down again.
func TestReplicationSystem_DatagramReceiptLatchesOnce(t *testing.T) {
	fw := &receiptFrameWriter{results: []net.SendResult{
		{Disposition: net.SendQueued, Delivery: net.DeliveryOrdered},
	}}
	sys, _, _, _, tick := newReceiptReplicationHarness(t, fw)

	sys.Update(0.05)
	fw.receipt(1, fw.frames[0].StreamEpoch, fw.frames[0].Seq, net.SendResult{
		Disposition: net.SendQueued, Delivery: net.DeliveryBestEffort,
	})
	*tick = 2
	sys.Update(0.05)
	latched := sys.connections[1]

	fw.receipt(1, latched.pending.streamEpoch, latched.pending.seq, net.SendResult{
		Disposition: net.SendQueued, Delivery: net.DeliveryBestEffort,
	})
	*tick = 3
	sys.Update(0.05)
	if sys.connections[1] != latched {
		t.Fatal("a second sub-reliable receipt re-latched an already-explicit connection")
	}
}
