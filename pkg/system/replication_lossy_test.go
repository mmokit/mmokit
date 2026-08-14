package system

import (
	"bytes"
	"testing"

	"github.com/mlange-42/ark/ecs"

	"github.com/mmokit/mmokit/pkg/net"
	"github.com/mmokit/mmokit/pkg/quantize"
	"github.com/mmokit/mmokit/pkg/replication"
	"github.com/mmokit/mmokit/pkg/spatial"
)

// assertClientMatchesAuthority is the central property of this file: for the
// entity the refClient believes it holds, its decoded baseline must byte-equal
// the snapshot the replicator would produce for that entity's CURRENT
// position. If it ever diverges, the client is rendering a world the server
// does not have.
func assertClientMatchesAuthority(t *testing.T, h *lossyHarness, when string) {
	t.Helper()
	got, ok := h.client.baselines[h.netID]
	if !ok {
		t.Fatalf("%s: client has no baseline for netID=%d", when, h.netID)
	}
	want := h.authoritativeSnapshot()
	if !bytes.Equal(got, want) {
		t.Fatalf("%s: client state diverged from authority: got %v, want %v", when, got, want)
	}
}

// TestReplication_DatagramCleanLinkUsesDeltas is the CE-003 regression. Before
// the per-connection ACK-mode latch, a datagram connection failed both the
// AckReliable commit test and the receipt-tracking test, fell through to the
// backpressure branch, and set forceFresh — so EVERY frame was a complete
// FreshSnapshot, forever. Delta compression was entirely off for UDP.
func TestReplication_DatagramCleanLinkUsesDeltas(t *testing.T) {
	h := newLossyHarness(t, nil)

	h.sys.Update(0.05) // first frame: full snapshot for a brand-new stream
	if len(h.link.frames) != 1 {
		t.Fatalf("frames after first update = %d, want 1", len(h.link.frames))
	}
	if len(h.link.frames[0].Full) != 1 {
		t.Fatalf("first frame Full = %d, want 1", len(h.link.frames[0].Full))
	}

	x := float32(100)
	for i := 0; i < 5; i++ {
		x += 10
		newX := x
		h.step(func() { h.moveTo(newX, 0) })

		frame := h.link.frames[len(h.link.frames)-1]
		if len(frame.Deltas) != 1 || len(frame.Full) != 0 {
			t.Fatalf("frame %d: Full=%d Deltas=%d, want Full=0 Deltas=1 — a clean datagram link must use deltas",
				i+2, len(frame.Full), len(frame.Deltas))
		}
		if frame.Flags&quantize.FrameFlagFreshSnapshot != 0 {
			t.Fatalf("frame %d carried FreshSnapshot on a clean link", i+2)
		}
		h.flush()
		assertClientMatchesAuthority(t, h, "clean link")
	}
}

// TestReplication_DroppedFrameStallsThenRecoversWithFreshSnapshot asserts the
// bounded-stall recovery: while an attempt is pending nothing is emitted, and
// the retry after PendingAckTimeoutTicks is a self-contained FreshSnapshot
// carrying a NEW seq — never the abandoned one.
func TestReplication_DroppedFrameStallsThenRecoversWithFreshSnapshot(t *testing.T) {
	// Frame index 1 (the second write) is dropped.
	h := newLossyHarness(t, []linkAction{{}, {drop: true}})

	h.sys.Update(0.05)
	h.step(func() { h.moveTo(150, 0) }) // this frame is dropped
	droppedSeq := h.link.frames[1].Seq
	emittedAfterDrop := len(h.link.frames)

	// No emission while the attempt is pending.
	for i := 0; i < 9; i++ {
		h.step(func() { h.moveTo(160+float32(i)*10, 0) })
	}
	if len(h.link.frames) != emittedAfterDrop {
		t.Fatalf("frames during the pending window = %d, want %d (no emission while pending)",
			len(h.link.frames), emittedAfterDrop)
	}

	// The timeout fires and a fresh attempt goes out.
	h.step(func() { h.moveTo(300, 0) })
	if len(h.link.frames) != emittedAfterDrop+1 {
		t.Fatalf("frames after the timeout = %d, want %d", len(h.link.frames), emittedAfterDrop+1)
	}
	retry := h.link.frames[len(h.link.frames)-1]
	if retry.Flags&quantize.FrameFlagFreshSnapshot == 0 {
		t.Fatal("retry after a lost frame must be a self-contained FreshSnapshot")
	}
	if retry.Seq == droppedSeq {
		t.Fatalf("retry reused the abandoned seq %d", droppedSeq)
	}
	if len(retry.Full) != 1 || len(retry.Deltas) != 0 {
		t.Fatalf("retry Full=%d Deltas=%d, want Full=1 Deltas=0", len(retry.Full), len(retry.Deltas))
	}

	// The client converges once the retry lands.
	h.flush()
	assertClientMatchesAuthority(t, h, "after fresh-snapshot recovery")
}

// TestReplication_DroppedAckNeverAdvancesBaseline asserts an unacknowledged
// snapshot is never promoted to Acked, so no later delta can be encoded
// against state the client may not hold.
func TestReplication_DroppedAckNeverAdvancesBaseline(t *testing.T) {
	h := newLossyHarness(t, nil)

	h.sys.Update(0.05)
	h.step(nil) // deliver + ack the first frame
	baseline := h.conn().store.Baseline(h.netID)
	if baseline == nil || baseline.Acked == nil {
		t.Fatal("first frame was never acknowledged")
	}
	ackedBefore := append([]byte(nil), baseline.Acked...)

	// The frame is delivered but the client withholds its ack.
	h.client.acksEnabled = false
	h.step(func() { h.moveTo(150, 0) })
	h.step(nil) // pump: delivery happens, no ack sent

	if !bytes.Equal(baseline.Acked, ackedBefore) {
		t.Fatalf("Acked advanced without an ACK: got %v, want %v", baseline.Acked, ackedBefore)
	}
	if h.conn().pending == nil {
		t.Fatal("attempt was resolved despite no ACK")
	}

	// No further frame is emitted, so nothing can be a delta against the
	// unacknowledged snapshot.
	before := len(h.link.frames)
	h.step(func() { h.moveTo(200, 0) })
	if len(h.link.frames) != before {
		t.Fatalf("emitted %d extra frame(s) while an ACK was outstanding", len(h.link.frames)-before)
	}
}

// TestReplication_DuplicateDatagramIsIdempotent asserts a duplicated datagram
// is rejected by the client's seq gate and that its extra AckFrame is a no-op.
func TestReplication_DuplicateDatagramIsIdempotent(t *testing.T) {
	h := newLossyHarness(t, []linkAction{{duplicate: true}})

	h.sys.Update(0.05)
	h.flush() // both copies land together

	if h.client.accepted != 1 {
		t.Fatalf("client accepted %d datagrams, want 1", h.client.accepted)
	}
	if h.client.rejected != 1 {
		t.Fatalf("client rejected %d datagrams, want 1 (the duplicate)", h.client.rejected)
	}
	if h.conn().pending != nil {
		t.Fatal("attempt still pending after the first copy was acked")
	}
	assertClientMatchesAuthority(t, h, "after duplicate delivery")

	// An unsolicited ack for the already-committed frame changes nothing.
	before := h.conn().store.Baseline(h.netID).Acked
	h.sys.AckFrame(1, h.client.epoch, h.client.lastSeq)
	if !bytes.Equal(h.conn().store.Baseline(h.netID).Acked, before) {
		t.Fatal("a repeated ACK mutated committed state")
	}
}

// TestReplication_ReorderedDatagramIsRejected asserts a late datagram neither
// rolls the client's baselines back nor commits a superseded transaction via
// its ack — the regression AckFrame's (streamEpoch, seq) pair exists to
// prevent.
func TestReplication_ReorderedDatagramIsRejected(t *testing.T) {
	// Frame index 1 is held for 3 pumps; the timeout retry (index 2) will
	// arrive first, so the held copy lands stale.
	h := newLossyHarness(t, []linkAction{{}, {delayTicks: 3}})

	h.sys.Update(0.05)
	h.step(nil) // first frame delivered + acked

	h.step(func() { h.moveTo(150, 0) }) // held in flight
	heldSeq := h.link.frames[1].Seq

	// Run out the pending-ack timeout so a fresh attempt is emitted, then let
	// the held datagram land afterwards.
	for i := 0; i < 12; i++ {
		h.step(func() { h.moveTo(400, 0) })
	}
	if len(h.link.frames) < 3 {
		t.Fatalf("frames = %d, want at least 3 (initial, held, retry)", len(h.link.frames))
	}
	retrySeq := h.link.frames[len(h.link.frames)-1].Seq
	if retrySeq == heldSeq {
		t.Fatalf("retry reused the held seq %d", heldSeq)
	}

	acceptedBefore := h.client.accepted
	baselineBefore := append([]byte(nil), h.client.baselines[h.netID]...)

	// Deliver the stale datagram now.
	h.client.Deliver(h.client.epoch, encodeStaleCopy(t, h, heldSeq))

	if h.client.accepted != acceptedBefore {
		t.Fatal("client accepted a stale, reordered datagram")
	}
	if !bytes.Equal(h.client.baselines[h.netID], baselineBefore) {
		t.Fatal("a stale datagram rolled the client's baseline back")
	}

	// Its late ack must not commit whatever is currently in flight.
	pendingBefore := h.conn().pending
	h.sys.AckFrame(1, h.client.epoch, heldSeq)
	if h.conn().pending != pendingBefore {
		t.Fatal("a stale ACK committed a superseded transaction")
	}
}

// encodeStaleCopy re-encodes an already-emitted frame by seq so the test can
// deliver it out of order without depending on link scheduling.
func encodeStaleCopy(t *testing.T, h *lossyHarness, seq uint32) []byte {
	t.Helper()
	for i := range h.link.frames {
		f := &h.link.frames[i]
		if f.Seq != seq {
			continue
		}
		enc := quantize.NewFrameEncoder(1024)
		full := make([]quantize.FullEntry, len(f.Full))
		for j := range f.Full {
			full[j] = quantize.FullEntry{
				NetID: f.Full[j].NetID, Epoch: f.Full[j].Epoch,
				EntityType: f.Full[j].Type, Snapshot: f.Full[j].Snapshot,
			}
		}
		deltas := make([]quantize.DeltaEntry, len(f.Deltas))
		for j := range f.Deltas {
			deltas[j] = quantize.DeltaEntry{
				NetID: f.Deltas[j].NetID, Epoch: f.Deltas[j].Epoch,
				EntityType: f.Deltas[j].Type, Data: f.Deltas[j].Data,
			}
		}
		return append([]byte(nil), enc.Encode(f.Tick, f.Seq, f.Flags, full, deltas, f.Removed, f.Exited)...)
	}
	t.Fatalf("no emitted frame with seq=%d", seq)
	return nil
}

// TestReplication_RemovalTombstoneSurvivesLoss asserts a dropped removal frame
// retries as Removed rather than degrading into an AoI Exited — the client must
// learn the entity was destroyed, not that it left view.
func TestReplication_RemovalTombstoneSurvivesLoss(t *testing.T) {
	h := newLossyHarness(t, []linkAction{{}, {drop: true}})

	h.sys.Update(0.05)
	h.flush() // initial full delivered + acked

	// Destroy the entity and feed the tick-scoped removal.
	h.grid.Deregister(h.entity)
	h.removed = []uint32{h.netID}
	h.step(nil) // this frame carries Removed and is dropped

	dropped := h.link.frames[len(h.link.frames)-1]
	if len(dropped.Removed) != 1 || dropped.Removed[0] != h.netID {
		t.Fatalf("dropped frame Removed = %v, want [%d]", dropped.Removed, h.netID)
	}

	// Run out the pending window and capture the FIRST frame emitted after
	// the timeout — that is the retry. Later ticks emit their own frames.
	before := len(h.link.frames)
	for i := 0; i < 12 && len(h.link.frames) == before; i++ {
		h.step(nil)
	}
	if len(h.link.frames) <= before {
		t.Fatal("no retry emitted after the removal frame was lost")
	}
	retry := h.link.frames[before]
	if len(retry.Removed) != 1 || retry.Removed[0] != h.netID {
		t.Fatalf("retry Removed = %v, want [%d] (a lost tombstone must not degrade to Exited)",
			retry.Removed, h.netID)
	}
	if len(retry.Exited) != 0 {
		t.Fatalf("retry Exited = %v, want empty", retry.Exited)
	}

	h.flush() // deliver the retry
	if _, ok := h.client.baselines[h.netID]; ok {
		t.Fatal("client kept a baseline for a removed entity")
	}
}

// TestReplication_StreamGenerationBumpResetsClient asserts an authority change
// mid-flight restarts the sequence domain, clears the client's baselines, and
// invalidates any pre-bump ack.
func TestReplication_StreamGenerationBumpResetsClient(t *testing.T) {
	world := ecs.NewWorld()
	grid := spatial.NewHashGrid(100)
	em := newTestEntityMapper(world)
	reg := NewReplicatorRegistry()
	reg.Register(&testReplicator{entityType: 0})

	viewerEntity := em.spawn(0, 0, 100, 0)
	entity := em.spawn(100, 0, 1, 0)
	grid.Register(spatial.Entry{Entity: entity, X: 100, Y: 0})

	link := newLossyLink(nil)
	tick := uint32(1)
	generation := uint32(7)
	sys := NewReplicationSystem(ReplicationConfig{
		World:       world,
		SpatialGrid: grid,
		Viewers: &fixedViewerSource{viewers: []ViewerInfo{
			{ConnID: 1, Entity: viewerEntity, X: 0, Y: 0},
		}},
		Frame:                  link,
		Replicators:            reg,
		AoIRadius:              1000,
		AckModeFor:             func(uint32) replication.AckMode { return replication.AckExplicit },
		SentHistoryDepth:       4,
		PendingAckTimeoutTicks: 10,
		GetTick:                func() uint32 { return tick },
		StreamGeneration: func(*ViewerInfo) (uint32, bool) {
			return generation, true
		},
	})
	client := newRefClient(sys, 1, 4, 4)
	link.client = client

	sys.Update(0.05)
	link.Pump()
	if client.epoch != 7 {
		t.Fatalf("client epoch = %d, want 7", client.epoch)
	}
	preBumpSeq := link.frames[0].Seq
	if _, ok := client.baselines[1]; !ok {
		t.Fatal("client has no baseline after the first frame")
	}

	// Authority moves: generation advances, so the stream restarts.
	generation = 8
	tick = 2
	pos, _, _ := em.mapper.Get(entity)
	pos.X = 150
	grid.Update(spatial.Entry{Entity: entity, X: 150, Y: 0})
	sys.Update(0.05)

	postBump := link.frames[len(link.frames)-1]
	if postBump.StreamEpoch != 8 {
		t.Fatalf("post-bump StreamEpoch = %d, want 8", postBump.StreamEpoch)
	}
	if postBump.Seq >= preBumpSeq && preBumpSeq != 0 {
		// Sequences restart at one for the new stream.
		if postBump.Seq != 1 {
			t.Fatalf("post-bump Seq = %d, want the stream to restart at 1", postBump.Seq)
		}
	}
	if postBump.Flags&quantize.FrameFlagFreshSnapshot == 0 {
		t.Fatal("the first frame of a new stream generation must be a FreshSnapshot")
	}

	// A pre-bump ack must not commit the new stream's attempt.
	pendingBefore := sys.connections[1].pending
	sys.AckFrame(1, 7, preBumpSeq)
	if sys.connections[1].pending != pendingBefore {
		t.Fatal("a pre-bump ACK committed the post-bump attempt")
	}

	link.Pump()
	if client.epoch != 8 {
		t.Fatalf("client epoch = %d after the bump, want 8", client.epoch)
	}
	w := quantize.NewSnapshotWriter(make([]byte, 8))
	w.Float32(150)
	w.Float32(0)
	if !bytes.Equal(client.baselines[1], w.Bytes()) {
		t.Fatalf("client baseline after the bump = %v, want %v", client.baselines[1], w.Bytes())
	}
}

// TestReplicationSystem_MixedTransportsPerConnectionAckMode is the direct test
// of the per-connection latch: one ReplicationSystem, two viewers, different
// transports, different modes. A single scalar AckMode cannot express this,
// and one ConnManager really does hold a mixed map of WebSocket and UDP
// transports.
func TestReplicationSystem_MixedTransportsPerConnectionAckMode(t *testing.T) {
	world := ecs.NewWorld()
	grid := spatial.NewHashGrid(100)
	em := newTestEntityMapper(world)
	reg := NewReplicatorRegistry()
	reg.Register(&testReplicator{entityType: 0})

	viewerA := em.spawn(0, 0, 100, 0)
	viewerB := em.spawn(0, 0, 101, 0)
	entity := em.spawn(50, 0, 1, 0)
	grid.Register(spatial.Entry{Entity: entity, X: 50, Y: 0})

	fw := &perConnResultWriter{results: map[uint32]net.SendResult{
		1: {Disposition: net.SendQueued, Delivery: net.DeliveryReliableOrdered},
		2: {Disposition: net.SendQueued, Delivery: net.DeliveryBestEffort},
	}}
	tick := uint32(1)
	sys := NewReplicationSystem(ReplicationConfig{
		World:       world,
		SpatialGrid: grid,
		Viewers: &fixedViewerSource{viewers: []ViewerInfo{
			{ConnID: 1, Entity: viewerA, X: 0, Y: 0},
			{ConnID: 2, Entity: viewerB, X: 0, Y: 0},
		}},
		Frame:       fw,
		Replicators: reg,
		AoIRadius:   1000,
		AckModeFor: func(connID uint32) replication.AckMode {
			if connID == 2 {
				return replication.AckExplicit
			}
			return replication.AckReliable
		},
		SentHistoryDepth:       4,
		PendingAckTimeoutTicks: 10,
		GetTick:                func() uint32 { return tick },
	})

	sys.Update(0.05)

	connA, connB := sys.connections[1], sys.connections[2]
	if connA == nil || connB == nil {
		t.Fatal("both connections must have state after the first update")
	}
	if connA.mode != replication.AckReliable {
		t.Fatalf("conn 1 mode = %v, want AckReliable", connA.mode)
	}
	if connB.mode != replication.AckExplicit {
		t.Fatalf("conn 2 mode = %v, want AckExplicit", connB.mode)
	}
	if connA.pending != nil {
		t.Fatal("reliable-ordered connection must commit on send, not stay pending")
	}
	if connB.pending == nil {
		t.Fatal("best-effort connection must stay pending until an explicit ACK")
	}
	if bl := connA.store.Baseline(1); bl == nil || bl.Acked == nil {
		t.Fatal("reliable connection did not commit its baseline")
	}
	if bl := connB.store.Baseline(1); bl == nil || bl.Acked != nil {
		t.Fatal("explicit connection committed a baseline before its ACK")
	}

	// The explicit connection commits only on the matching ACK.
	sys.AckFrame(2, connB.streamEpoch, connB.pending.seq)
	if connB.pending != nil {
		t.Fatal("explicit connection still pending after a matching ACK")
	}
	if bl := connB.store.Baseline(1); bl == nil || bl.Acked == nil {
		t.Fatal("explicit connection did not commit on its ACK")
	}
}

// perConnResultWriter returns a per-connection SendResult, standing in for one
// ConnManager holding transports of different classes.
type perConnResultWriter struct {
	results map[uint32]net.SendResult
	frames  []ReplicationFrame
}

func (w *perConnResultWriter) WriteFrame(frame *ReplicationFrame) net.SendResult {
	cloned := *frame
	cloned.Full = append([]FullPayload(nil), frame.Full...)
	cloned.Deltas = append([]DeltaPayload(nil), frame.Deltas...)
	w.frames = append(w.frames, cloned)
	if r, ok := w.results[frame.Viewer.ConnID]; ok {
		return r
	}
	return net.SendResult{Disposition: net.SendQueued, Delivery: net.DeliveryReliableOrdered}
}
