package system

import (
	"testing"

	"github.com/mlange-42/ark/ecs"

	"github.com/mmokit/mmokit/pkg/component"
	"github.com/mmokit/mmokit/pkg/net"
	"github.com/mmokit/mmokit/pkg/quantize"
	"github.com/mmokit/mmokit/pkg/spatial"
)

type captureConn struct {
	sent   map[uint32][]byte
	result net.SendResult
}

func (c *captureConn) Send(connID uint32, data []byte) net.SendResult {
	c.sent[connID] = append([]byte(nil), data...)
	return c.result
}
func (c *captureConn) SendReliable(connID uint32, data []byte) net.SendResult {
	c.sent[connID] = append([]byte(nil), data...)
	return c.result
}
func (c *captureConn) InjectInput(connID uint32, data []byte) {}
func (c *captureConn) DrainInput(connID uint32) [][]byte      { return nil }
func (c *captureConn) DrainOpInput(connID uint32) [][]byte    { return nil }

var _ net.ConnSender = (*captureConn)(nil)

type trackedCaptureConn struct {
	captureConn
	trackedScopes []uint64
	trackedEpochs []uint32
	trackedSeqs   []uint32
	receipts      map[uint64][]net.ReplicationReceipt
}

func (c *trackedCaptureConn) SendReplication(connID uint32, scope uint64, streamEpoch, seq uint32, data []byte) net.SendResult {
	c.sent[connID] = append([]byte(nil), data...)
	c.trackedScopes = append(c.trackedScopes, scope)
	c.trackedEpochs = append(c.trackedEpochs, streamEpoch)
	c.trackedSeqs = append(c.trackedSeqs, seq)
	return c.result
}

func (c *trackedCaptureConn) DrainReplicationReceipts(_ uint32, scope uint64) []net.ReplicationReceipt {
	out := c.receipts[scope]
	delete(c.receipts, scope)
	return out
}

var _ net.TrackedReplicationSender = (*trackedCaptureConn)(nil)
var _ net.ReplicationReceiptSource = (*trackedCaptureConn)(nil)

// TestBinaryFrameWriter_PassesThroughPerEntityProducedAtMs verifies the writer
// is a pure pass-through for per-entity stamps — the actual stamping happens
// upstream in ReplicationSystem (local = ClusterClock.TickTime, replica =
// cached Replica.ProducedAtMs from the border-frame codec).
func TestBinaryFrameWriter_PassesThroughPerEntityProducedAtMs(t *testing.T) {
	makeEvent := func(data []byte) []byte {
		out := make([]byte, len(data))
		copy(out, data)
		return out
	}
	conn := &captureConn{
		sent: make(map[uint32][]byte),
		result: net.SendResult{
			Disposition: net.SendQueued,
			Delivery:    net.DeliveryReliableOrdered,
		},
	}
	w := NewBinaryFrameWriter(conn, makeEvent)

	const localStamp uint64 = 42_000_000
	const replicaStamp uint64 = 7_777_777

	result := w.WriteFrame(&ReplicationFrame{
		Tick:   1,
		Seq:    1,
		Flags:  0,
		Viewer: &ViewerInfo{ConnID: 42},
		Full: []FullPayload{{
			NetID:        101,
			Epoch:        1,
			Type:         1,
			ProducedAtMs: localStamp,
			Snapshot:     []byte{0x01, 0x02},
		}},
		Deltas: []DeltaPayload{{
			NetID:        102,
			Epoch:        2,
			Type:         2,
			ProducedAtMs: replicaStamp,
			Data:         []byte{0xAA},
		}},
	})
	if !result.Supports(net.DeliveryReliableOrdered) {
		t.Fatalf("WriteFrame result = %+v, want reliable ordered enqueue", result)
	}

	data, ok := conn.sent[42]
	if !ok {
		t.Fatal("no frame captured for connID 42")
	}
	dec := quantize.NewFrameDecoder(data)
	hdr := dec.Header()
	if hdr.FullCount != 1 || hdr.DeltaCount != 1 {
		t.Fatalf("header counts: full=%d delta=%d", hdr.FullCount, hdr.DeltaCount)
	}
	full := dec.NextFull()
	if full.ProducedAtMs != localStamp {
		t.Fatalf("full.ProducedAtMs = %d, want %d (writer must pass through, not restamp)",
			full.ProducedAtMs, localStamp)
	}
	delta := dec.NextDelta()
	if delta.ProducedAtMs != replicaStamp {
		t.Fatalf("delta.ProducedAtMs = %d, want %d (writer must pass through, not restamp)",
			delta.ProducedAtMs, replicaStamp)
	}
}

func TestBinaryFrameWriter_EncodesProcessedInputSequence(t *testing.T) {
	conn := &captureConn{
		sent:   make(map[uint32][]byte),
		result: net.SendResult{Disposition: net.SendQueued, Delivery: net.DeliveryReliableOrdered},
	}
	w := NewBinaryFrameWriter(conn, func(data []byte) []byte { return data })
	const want = uint32(77)
	got := w.WriteFrame(&ReplicationFrame{
		Viewer:            &ViewerInfo{ConnID: 9},
		HasInputAck:       true,
		ProcessedInputSeq: want,
	})
	if !got.Queued() {
		t.Fatalf("WriteFrame result = %+v, want queued", got)
	}

	dec := quantize.NewFrameDecoder(conn.sent[9])
	hdr := dec.Header()
	ack, ok := dec.NextInputAck(hdr.Flags)
	if !ok || ack != want {
		t.Fatalf("input ack = (%d, %v), want (%d, true)", ack, ok, want)
	}
}

func TestBinaryFrameWriter_PassesFrameMetadataToEnvelope(t *testing.T) {
	conn := &captureConn{
		sent:   make(map[uint32][]byte),
		result: net.SendResult{Disposition: net.SendQueued, Delivery: net.DeliveryReliableOrdered},
	}
	var capturedEpoch uint32
	w := NewBinaryFrameWriterWithMetadata(conn, func(frame *ReplicationFrame, data []byte) []byte {
		capturedEpoch = frame.StreamEpoch
		return data
	})

	const wantEpoch = uint32(29)
	got := w.WriteFrame(&ReplicationFrame{
		Seq:         3,
		StreamEpoch: wantEpoch,
		Viewer:      &ViewerInfo{ConnID: 9},
	})
	if !got.Queued() {
		t.Fatalf("WriteFrame result = %+v, want queued", got)
	}
	if capturedEpoch != wantEpoch {
		t.Fatalf("captured stream epoch = %d, want %d", capturedEpoch, wantEpoch)
	}
}

func TestBinaryFrameWriter_PropagatesSendRejection(t *testing.T) {
	want := net.SendResult{Disposition: net.SendBackpressure}
	conn := &captureConn{sent: make(map[uint32][]byte), result: want}
	w := NewBinaryFrameWriter(conn, func(data []byte) []byte { return data })

	got := w.WriteFrame(&ReplicationFrame{Viewer: &ViewerInfo{ConnID: 7}})
	if got.Disposition != want.Disposition {
		t.Fatalf("WriteFrame disposition = %v, want %v", got.Disposition, want.Disposition)
	}
}

func TestBinaryFrameWriter_NilEnvelopeIsFailure(t *testing.T) {
	conn := &captureConn{
		sent:   make(map[uint32][]byte),
		result: net.SendResult{Disposition: net.SendQueued, Delivery: net.DeliveryReliableOrdered},
	}
	w := NewBinaryFrameWriter(conn, func([]byte) []byte { return nil })

	got := w.WriteFrame(&ReplicationFrame{Viewer: &ViewerInfo{ConnID: 7}})
	if got.Disposition != net.SendFailed {
		t.Fatalf("WriteFrame disposition = %v, want failed", got.Disposition)
	}
	if len(conn.sent) != 0 {
		t.Fatal("nil envelope should not reach ConnSender")
	}
}

func TestBinaryFrameWriterScopesTrackedSendsAndReceiptDrains(t *testing.T) {
	conn := &trackedCaptureConn{
		captureConn: captureConn{
			sent:   make(map[uint32][]byte),
			result: net.SendResult{Disposition: net.SendQueued, Delivery: net.DeliveryOrdered},
		},
		receipts: make(map[uint64][]net.ReplicationReceipt),
	}
	wrap := func(data []byte) []byte { return data }
	first := NewBinaryFrameWriter(conn, wrap)
	second := NewBinaryFrameWriter(conn, wrap)

	if got := first.WriteFrame(&ReplicationFrame{StreamEpoch: 4, Seq: 10, Viewer: &ViewerInfo{ConnID: 7}}); !got.Supports(net.DeliveryOrdered) {
		t.Fatalf("first tracked send = %+v", got)
	}
	if got := second.WriteFrame(&ReplicationFrame{StreamEpoch: 5, Seq: 10, Viewer: &ViewerInfo{ConnID: 7}}); !got.Supports(net.DeliveryOrdered) {
		t.Fatalf("second tracked send = %+v", got)
	}
	if len(conn.trackedScopes) != 2 || conn.trackedScopes[0] == 0 || conn.trackedScopes[0] == conn.trackedScopes[1] {
		t.Fatalf("tracked scopes = %v, want two distinct non-zero scopes", conn.trackedScopes)
	}
	if conn.trackedSeqs[0] != 10 || conn.trackedSeqs[1] != 10 {
		t.Fatalf("tracked sequences = %v", conn.trackedSeqs)
	}
	if conn.trackedEpochs[0] != 4 || conn.trackedEpochs[1] != 5 {
		t.Fatalf("tracked stream epochs = %v", conn.trackedEpochs)
	}

	firstScope, secondScope := conn.trackedScopes[0], conn.trackedScopes[1]
	conn.receipts[firstScope] = []net.ReplicationReceipt{{ConnID: 7, Scope: firstScope, StreamEpoch: 4, Seq: 10}}
	conn.receipts[secondScope] = []net.ReplicationReceipt{{ConnID: 7, Scope: secondScope, StreamEpoch: 5, Seq: 10}}
	if got := first.DrainFrameReceipts(7); len(got) != 1 || got[0].Scope != firstScope {
		t.Fatalf("first drain = %+v", got)
	}
	if got := second.DrainFrameReceipts(7); len(got) != 1 || got[0].Scope != secondScope {
		t.Fatalf("second drain = %+v", got)
	}
}

// fakeClock is a ClusterClock for tests. Now() returns a fixed value so
// assertions can compare producer stamps exactly. TickTime returns the
// same fixed value — tests supplying a tick-aligned `t` get exact
// equality, tests supplying a non-aligned value should not call TickTime.
type fakeClock struct{ t uint64 }

func (f *fakeClock) Now() uint64                           { return f.t }
func (f *fakeClock) TickTime(tickIntervalMs uint64) uint64 { return f.t }

// TestReplicationSystem_StampsLocalEntitiesFromClusterClock verifies the
// ReplicationSystem stamps locally-authoritative entities with the
// configured ClusterClock.TickTime (tick-aligned) at emit time.
func TestReplicationSystem_StampsLocalEntitiesFromClusterClock(t *testing.T) {
	world := ecs.NewWorld()
	grid := spatial.NewHashGrid(100)
	em := newTestEntityMapper(world)
	fw := &stubFrameWriter{}

	reg := NewReplicatorRegistry()
	reg.Register(&testReplicator{entityType: 0})

	viewerEntity := em.spawn(0, 0, 100, 0)
	local := em.spawn(50, 0, 1, 0)
	grid.Register(spatial.Entry{Entity: local, X: 50, Y: 0})

	clock := &fakeClock{t: 42_000_000}
	tick := uint32(1)
	sys := NewReplicationSystem(ReplicationConfig{
		World:        world,
		SpatialGrid:  grid,
		Viewers:      &fixedViewerSource{viewers: []ViewerInfo{{ConnID: 1, Entity: viewerEntity, X: 0, Y: 0}}},
		Frame:        fw,
		Replicators:  reg,
		AoIRadius:    1000,
		GetTick:      func() uint32 { return tick },
		ClusterClock: clock,
	})

	sys.Update(0.05)

	if len(fw.frames) != 1 {
		t.Fatalf("expected 1 frame, got %d", len(fw.frames))
	}
	if len(fw.frames[0].Full) != 1 {
		t.Fatalf("expected 1 full entry, got %d", len(fw.frames[0].Full))
	}
	got := fw.frames[0].Full[0].ProducedAtMs
	if got != clock.t {
		t.Fatalf("local entity ProducedAtMs = %d, want %d (must be stamped from ClusterClock.TickTime)",
			got, clock.t)
	}
}

// TestReplicationSystem_PassesThroughReplicaProducedAtMs verifies replicas
// re-use the cached Replica.ProducedAtMs stamp (populated by the
// border-frame codec at the receiving cell) — NOT the local clock, so
// clients see the source cell's producer timeline, not this cell's emit
// time.
func TestReplicationSystem_PassesThroughReplicaProducedAtMs(t *testing.T) {
	world := ecs.NewWorld()
	grid := spatial.NewHashGrid(100)
	em := newTestEntityMapper(world)
	fw := &stubFrameWriter{}

	reg := NewReplicatorRegistry()
	reg.Register(&testReplicator{entityType: 0})

	viewerEntity := em.spawn(0, 0, 100, 0)
	replica := em.spawn(50, 0, 1, 0)
	replicaMap := ecs.NewMap1[component.Replica](world)
	const producerStamp uint64 = 7_777_777
	replicaMap.Add(replica, &component.Replica{
		SourceCellID: "neighbor",
		TTL:          30,
		ProducedAtMs: producerStamp,
	})
	grid.Register(spatial.Entry{Entity: replica, X: 50, Y: 0})

	clock := &fakeClock{t: 42_000_000} // would be wrong to use for replica
	tick := uint32(1)
	sys := NewReplicationSystem(ReplicationConfig{
		World:        world,
		SpatialGrid:  grid,
		Viewers:      &fixedViewerSource{viewers: []ViewerInfo{{ConnID: 1, Entity: viewerEntity, X: 0, Y: 0}}},
		Frame:        fw,
		Replicators:  reg,
		AoIRadius:    1000,
		GetTick:      func() uint32 { return tick },
		ClusterClock: clock,
	})

	sys.Update(0.05)

	if len(fw.frames) != 1 {
		t.Fatalf("expected 1 frame, got %d", len(fw.frames))
	}
	if len(fw.frames[0].Full) != 1 {
		t.Fatalf("expected 1 full entry, got %d", len(fw.frames[0].Full))
	}
	got := fw.frames[0].Full[0].ProducedAtMs
	if got != producerStamp {
		t.Fatalf("replica ProducedAtMs = %d, want %d (must pass through cached stamp, NOT clock.Now()=%d)",
			got, producerStamp, clock.t)
	}
}
