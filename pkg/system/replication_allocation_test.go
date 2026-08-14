package system

import (
	"testing"

	"github.com/mlange-42/ark/ecs"

	"github.com/mmokit/mmokit/pkg/net"
	"github.com/mmokit/mmokit/pkg/replication"
	"github.com/mmokit/mmokit/pkg/spatial"
)

type allocationDiscardFrameWriter struct{}

func (allocationDiscardFrameWriter) WriteFrame(*ReplicationFrame) net.SendResult {
	return net.SendResult{
		Disposition: net.SendQueued,
		Delivery:    net.DeliveryReliableOrdered,
	}
}

// allocationTrackedFrameWriter models the distributed ordered path: enqueue
// succeeds now and a reliable-ordered receipt is observed at the start of the
// next update. Fixed receipt storage keeps writer bookkeeping out of the
// allocation measurement.
type allocationTrackedFrameWriter struct {
	receipt      [1]net.ReplicationReceipt
	receiptReady bool
}

func (w *allocationTrackedFrameWriter) WriteFrame(frame *ReplicationFrame) net.SendResult {
	w.receipt[0] = net.ReplicationReceipt{
		ConnID:      frame.Viewer.ConnID,
		StreamEpoch: frame.StreamEpoch,
		Seq:         frame.Seq,
		Result: net.SendResult{
			Disposition: net.SendQueued,
			Delivery:    net.DeliveryReliableOrdered,
		},
	}
	w.receiptReady = true
	return net.SendResult{
		Disposition: net.SendQueued,
		Delivery:    net.DeliveryOrdered,
	}
}

func (w *allocationTrackedFrameWriter) DrainFrameReceipts(connID uint32) []net.ReplicationReceipt {
	if !w.receiptReady || w.receipt[0].ConnID != connID {
		return nil
	}
	w.receiptReady = false
	return w.receipt[:]
}

type replicationAllocationHarness struct {
	system *ReplicationSystem
	tick   uint32
}

func newReplicationAllocationHarness(entityCount int, dormancyThreshold uint32) *replicationAllocationHarness {
	return newReplicationAllocationHarnessWithWriter(
		entityCount,
		dormancyThreshold,
		allocationDiscardFrameWriter{},
	)
}

func newReplicationAllocationHarnessWithWriter(
	entityCount int,
	dormancyThreshold uint32,
	writer FrameWriter,
) *replicationAllocationHarness {
	world := ecs.NewWorld()
	grid := spatial.NewHashGrid(100)
	entities := newTestEntityMapper(world)

	registry := NewReplicatorRegistry()
	registry.Register(&testReplicator{entityType: 0})

	viewerEntity := entities.spawn(0, 0, uint32(entityCount+1), 0)
	for i := range entityCount {
		x := float32(10 + i%20)
		y := float32(10 + (i/20)%20)
		entity := entities.spawn(x, y, uint32(i+1), 0)
		grid.Register(spatial.Entry{Entity: entity, X: x, Y: y})
	}

	harness := &replicationAllocationHarness{}
	harness.system = NewReplicationSystem(ReplicationConfig{
		World:       world,
		SpatialGrid: grid,
		Viewers: &fixedViewerSource{viewers: []ViewerInfo{
			{ConnID: 1, Entity: viewerEntity, X: 0, Y: 0},
		}},
		Frame:             writer,
		Replicators:       registry,
		AoIRadius:         1000,
		DormancyThreshold: dormancyThreshold,
		AckMode:           replication.AckReliable,
		GetTick:           func() uint32 { return harness.tick },
	})
	return harness
}

func (h *replicationAllocationHarness) step() {
	h.tick++
	h.system.Update(0.05)
}

func TestReplicationStateTxn_CommitPreservesBaselinePointer(t *testing.T) {
	store := replication.NewBaselineStore(replication.AckReliable)
	committed := store.GetOrCreateBaseline(7, 0)
	committed.Acked = []byte{1, 2, 3}

	txn := newReplicationStateTxn(store)
	staged := txn.begin(0).baseline(7)
	staged.Acked = []byte{4, 5, 6}
	txn.commit()

	if got := store.Baseline(7); got != committed {
		t.Fatalf("commit replaced baseline pointer: got %p, want established %p", got, committed)
	}
	if got := committed.Acked; len(got) != 3 || got[0] != 4 || got[1] != 5 || got[2] != 6 {
		t.Fatalf("in-place committed snapshot = %v, want [4 5 6]", got)
	}
}

func TestReplicationStateTxn_ExplicitAttemptDoesNotCloneSentRing(t *testing.T) {
	store := replication.NewBaselineStore(replication.AckExplicit)
	committed := store.GetOrCreateBaseline(7, 32)
	committed.PushSent(1, []byte{1, 2, 3})

	txn := newReplicationStateTxn(store)
	txn.begin(32).pushSent(7, []byte{4, 5, 6})
	state := txn.existingEntity(7)
	if state == nil || !state.promoteReady {
		t.Fatal("explicit attempt did not stage its promotable snapshot")
	}
	if state.baseline.RingHead != committed.RingHead || state.baseline.RingLen != committed.RingLen {
		t.Fatalf("staged ring metadata changed before admission: staged=(%d,%d) committed=(%d,%d)",
			state.baseline.RingHead, state.baseline.RingLen, committed.RingHead, committed.RingLen)
	}
	if &state.baseline.Ring[0] != &committed.Ring[0] {
		t.Fatal("explicit transaction cloned the committed sent ring")
	}
}

func TestReplicationSystem_DormantFastPathDoesNotStageEntities(t *testing.T) {
	harness := newReplicationAllocationHarness(128, 1)
	harness.step() // initial full snapshots
	harness.step() // unchanged counter reaches the dormancy threshold
	harness.step() // dormant fast path

	conn := harness.system.connections[1]
	if got := len(conn.txn.entities); got != 0 {
		t.Fatalf("dormant tick staged %d entity transactions, want 0", got)
	}
}

func TestReplicationSystem_SteadyStateTxnAllocationsDoNotScalePerEntity(t *testing.T) {
	const runs = 25

	small := newReplicationAllocationHarness(1, 0)
	large := newReplicationAllocationHarness(256, 0)

	// Warm snapshots, persistent baselines, transaction maps/slices, spatial
	// result buffers, and frame buffers before measuring unchanged ticks.
	small.step()
	small.step()
	large.step()
	large.step()

	smallAllocs := testing.AllocsPerRun(runs, small.step)
	largeAllocs := testing.AllocsPerRun(runs, large.step)
	t.Logf("steady-state allocations/update: 1 entity=%.1f, 256 entities=%.1f", smallAllocs, largeAllocs)

	// Map/bucket sizing may cost a handful of fixed allocations for the larger
	// visible set, but transaction staging must not add one heap object per
	// entity. The former pointer-map implementation added roughly 255 here.
	if delta := largeAllocs - smallAllocs; delta > 8 {
		t.Fatalf("steady-state allocations scale with visible entities: 1=%.1f 256=%.1f delta=%.1f", smallAllocs, largeAllocs, delta)
	}
}

func TestReplicationSystem_TrackedReceiptTxnAllocationsDoNotScalePerEntity(t *testing.T) {
	const runs = 25

	small := newReplicationAllocationHarnessWithWriter(1, 0, &allocationTrackedFrameWriter{})
	large := newReplicationAllocationHarnessWithWriter(256, 0, &allocationTrackedFrameWriter{})

	// Warm both alternating transaction buffers. Each subsequent step drains
	// one successful receipt and submits the next ordered frame.
	for range 4 {
		small.step()
		large.step()
	}

	smallAllocs := testing.AllocsPerRun(runs, small.step)
	largeAllocs := testing.AllocsPerRun(runs, large.step)
	t.Logf("tracked send+receipt allocations/update: 1 entity=%.1f, 256 entities=%.1f", smallAllocs, largeAllocs)

	if delta := largeAllocs - smallAllocs; delta > 8 {
		t.Fatalf("tracked send+receipt allocations scale with visible entities: 1=%.1f 256=%.1f delta=%.1f", smallAllocs, largeAllocs, delta)
	}

	// The pending attempt itself is stable and the two transaction buffers
	// alternate ownership instead of allocating/copying a frozen entity slice.
	conn := large.system.connections[1]
	firstPending := conn.pending
	if firstPending == nil || firstPending != &conn.pendingScratch || len(firstPending.txn.entities) == 0 {
		t.Fatal("tracked frame did not use the reusable pending scratch")
	}
	firstBuffer := &firstPending.txn.entities[0]
	large.step()
	if conn.pending != firstPending {
		t.Fatal("tracked frame replaced the reusable pending attempt")
	}
	large.step()
	if conn.pending != firstPending || &conn.pending.txn.entities[0] != firstBuffer {
		t.Fatal("tracked frame did not reuse the alternating transaction buffer")
	}
}
