package system

import (
	"testing"

	"github.com/mlange-42/ark/ecs"

	"github.com/zenion/mmoserver/pkg/net"
	"github.com/zenion/mmoserver/pkg/quantize"
	"github.com/zenion/mmoserver/pkg/replication"
	"github.com/zenion/mmoserver/pkg/spatial"
)

// ---------------------------------------------------------------------------
// Deterministic datagram link
// ---------------------------------------------------------------------------
//
// pkg/universe/loopback_bridge.go looks like it should serve here and does
// not: it routes universe.CellMessage (the cell-to-cell mesh, with no path
// from a FrameWriter or client frame bytes into it), pkg/universe imports
// pkg/system so a pkg/system test cannot import it back, it applies one
// CONSTANT latency to every message so it can never reorder, and non-zero
// latency spawns an unjoinable wall-clock goroutine. This harness is
// deterministic, tick-driven, and encodes real quantize wire bytes.

// linkAction is the deterministic per-frame policy, indexed by the number of
// frames the link has accepted so far. The zero value delivers immediately.
type linkAction struct {
	drop       bool
	duplicate  bool
	delayTicks int // >0 holds the datagram, releasing it after N Pump calls
}

type pendingDatagram struct {
	releaseAt   int
	streamEpoch uint32
	bytes       []byte
}

// lossyLink implements FrameWriter over a scriptable datagram link. It
// returns exactly what UDPTransport.SendUnreliable returns, so the system
// under test takes the real datagram code path.
type lossyLink struct {
	enc      *quantize.FrameEncoder
	plan     []linkAction // consulted by write index; past the end = deliver
	writes   int
	inFlight []pendingDatagram
	now      int
	client   *refClient

	// frames records every ReplicationFrame handed to the link, deep-cloned,
	// so assertions can inspect what the system chose to emit independently
	// of what the link chose to deliver.
	frames []ReplicationFrame
}

func newLossyLink(plan []linkAction) *lossyLink {
	return &lossyLink{enc: quantize.NewFrameEncoder(8192), plan: plan}
}

func (l *lossyLink) WriteFrame(frame *ReplicationFrame) net.SendResult {
	cloned := *frame
	cloned.Full = append([]FullPayload(nil), frame.Full...)
	cloned.Deltas = append([]DeltaPayload(nil), frame.Deltas...)
	cloned.Entered = append([]uint32(nil), frame.Entered...)
	cloned.Exited = append([]uint32(nil), frame.Exited...)
	cloned.Removed = append([]uint32(nil), frame.Removed...)
	l.frames = append(l.frames, cloned)

	// Same conversion loop as BinaryFrameWriter.WriteFrame, so the test
	// exercises real wire bytes rather than a parallel encoding.
	full := make([]quantize.FullEntry, len(frame.Full))
	for i := range frame.Full {
		fp := &frame.Full[i]
		full[i] = quantize.FullEntry{
			NetID:        fp.NetID,
			Epoch:        fp.Epoch,
			EntityType:   fp.Type,
			ProducedAtMs: fp.ProducedAtMs,
			Snapshot:     fp.Snapshot,
			InitialData:  fp.InitialData,
		}
	}
	deltas := make([]quantize.DeltaEntry, len(frame.Deltas))
	for i := range frame.Deltas {
		dp := &frame.Deltas[i]
		deltas[i] = quantize.DeltaEntry{
			NetID:        dp.NetID,
			Epoch:        dp.Epoch,
			EntityType:   dp.Type,
			ProducedAtMs: dp.ProducedAtMs,
			Data:         dp.Data,
		}
	}

	var encoded []byte
	if frame.HasInputAck {
		encoded = l.enc.Encode(frame.Tick, frame.Seq, frame.Flags,
			full, deltas, frame.Removed, frame.Exited, frame.ProcessedInputSeq)
	} else {
		encoded = l.enc.Encode(frame.Tick, frame.Seq, frame.Flags,
			full, deltas, frame.Removed, frame.Exited)
	}
	raw := append([]byte(nil), encoded...)

	action := linkAction{}
	if l.writes < len(l.plan) {
		action = l.plan[l.writes]
	}
	l.writes++

	if !action.drop {
		// StreamEpoch travels out-of-band alongside the bytes, mirroring
		// makeWorldDeltaFrame's typed WorldDelta envelope.
		release := l.now + action.delayTicks
		l.inFlight = append(l.inFlight, pendingDatagram{
			releaseAt: release, streamEpoch: frame.StreamEpoch, bytes: raw,
		})
		if action.duplicate {
			l.inFlight = append(l.inFlight, pendingDatagram{
				releaseAt: release, streamEpoch: frame.StreamEpoch, bytes: append([]byte(nil), raw...),
			})
		}
	}

	// Exactly what UDPTransport.SendUnreliable reports.
	return net.SendResult{Disposition: net.SendQueued, Delivery: net.DeliveryBestEffort}
}

// Pump advances the link clock and delivers every datagram now due, in
// release order. Called before the system builds the next frame, mirroring
// engine.clientInputTick running before the system loop.
func (l *lossyLink) Pump() {
	l.now++
	l.deliverDue()
}

// deliverDue delivers datagrams whose release time has arrived WITHOUT
// advancing the link clock. Tests use it to settle a frame emitted by the
// update that just ran, so an assertion about client state is not
// systematically one frame behind authority. Anything still held by
// delayTicks stays held.
func (l *lossyLink) deliverDue() {
	if len(l.inFlight) == 0 {
		return
	}
	var held []pendingDatagram
	for _, d := range l.inFlight {
		if d.releaseAt > l.now {
			held = append(held, d)
			continue
		}
		l.client.Deliver(d.streamEpoch, d.bytes)
	}
	l.inFlight = held
}

// ---------------------------------------------------------------------------
// Reference client decoder
// ---------------------------------------------------------------------------

// refClient mirrors the generated TS/C# decoders: one baseline per netID,
// RFC 1982 serial gates on streamEpoch and seq, FreshSnapshot clears
// baselines. It acks every ACCEPTED datagram.
//
// Acking on acceptance rather than after a higher-level apply is sound ONLY
// because at most one causal attempt is in flight per connection, so a
// reordered or duplicated datagram can never ack a frame this decoder
// rejected. The generated C# client relies on the same invariant.
type refClient struct {
	sys         *ReplicationSystem
	connID      uint32
	dec         *quantize.DeltaEncoder
	baselines   map[uint32][]byte
	epoch       uint32
	haveEpoch   bool
	lastSeq     uint32
	haveSeq     bool
	acksEnabled bool

	// accepted/rejected count decoder outcomes so tests can assert on the
	// gate without reaching into it.
	accepted int
	rejected int
}

func newRefClient(sys *ReplicationSystem, connID uint32, layout ...int) *refClient {
	return &refClient{
		sys:         sys,
		connID:      connID,
		dec:         quantize.NewDeltaEncoder(layout...),
		baselines:   make(map[uint32][]byte),
		acksEnabled: true,
	}
}

// isNewerSerial is the same rule as streamGenerationAfter: false for equal
// values and for the undefined half-range distance.
func isNewerSerial(candidate, current uint32) bool {
	return candidate != current && candidate-current < 1<<31
}

func (c *refClient) Deliver(streamEpoch uint32, raw []byte) {
	// Stream gate: a new epoch resets the sequence domain entirely.
	if c.haveEpoch {
		if streamEpoch != c.epoch {
			if !isNewerSerial(streamEpoch, c.epoch) {
				c.rejected++
				return
			}
			c.epoch = streamEpoch
			c.haveSeq = false
			c.baselines = make(map[uint32][]byte)
		}
	} else {
		c.epoch = streamEpoch
		c.haveEpoch = true
	}

	d := quantize.NewFrameDecoder(raw)
	hdr := d.Header()

	if c.haveSeq && !isNewerSerial(hdr.Seq, c.lastSeq) {
		c.rejected++
		return
	}
	c.lastSeq = hdr.Seq
	c.haveSeq = true
	c.accepted++

	if hdr.Flags&quantize.FrameFlagFreshSnapshot != 0 {
		c.baselines = make(map[uint32][]byte)
	}

	for i := 0; i < int(hdr.FullCount); i++ {
		e := d.NextFull()
		c.baselines[e.NetID] = append([]byte(nil), e.Snapshot...)
	}
	for i := 0; i < int(hdr.DeltaCount); i++ {
		e := d.NextDelta()
		base, ok := c.baselines[e.NetID]
		if !ok {
			// No baseline to apply against — the server should never emit
			// this, and the real clients drop it too.
			continue
		}
		c.baselines[e.NetID] = c.dec.Decode(base, e.Data)
	}
	for i := 0; i < int(hdr.RemovedCount); i++ {
		delete(c.baselines, d.NextUint32())
	}
	for i := 0; i < int(hdr.ExitedCount); i++ {
		delete(c.baselines, d.NextUint32())
	}

	if c.acksEnabled {
		c.sys.AckFrame(c.connID, streamEpoch, hdr.Seq)
	}
}

// ---------------------------------------------------------------------------
// Harness
// ---------------------------------------------------------------------------

type lossyHarness struct {
	sys    *ReplicationSystem
	link   *lossyLink
	client *refClient
	grid   *spatial.HashGrid
	em     *testEntityMapper
	tick   uint32

	entity ecs.Entity
	netID  uint32

	// removed is the tick-scoped removal feed handed to RemovedIDs, drained
	// once per Update exactly as the production feed is.
	removed []uint32
}

// newLossyHarness builds a single-viewer, single-entity world whose
// connection is latched to AckExplicit, wired to a scriptable datagram link
// and a reference client decoder.
func newLossyHarness(t *testing.T, plan []linkAction) *lossyHarness {
	t.Helper()
	world := ecs.NewWorld()
	grid := spatial.NewHashGrid(100)
	em := newTestEntityMapper(world)
	reg := NewReplicatorRegistry()
	reg.Register(&testReplicator{entityType: 0})

	viewerEntity := em.spawn(0, 0, 100, 0)
	entity := em.spawn(100, 0, 1, 0)
	grid.Register(spatial.Entry{Entity: entity, X: 100, Y: 0})

	h := &lossyHarness{
		link:   newLossyLink(plan),
		grid:   grid,
		em:     em,
		tick:   1,
		entity: entity,
		netID:  1,
	}
	h.sys = NewReplicationSystem(ReplicationConfig{
		World:       world,
		SpatialGrid: grid,
		Viewers: &fixedViewerSource{viewers: []ViewerInfo{
			{ConnID: 1, Entity: viewerEntity, X: 0, Y: 0},
		}},
		Frame:                  h.link,
		Replicators:            reg,
		AoIRadius:              1000,
		AckModeFor:             func(uint32) replication.AckMode { return replication.AckExplicit },
		SentHistoryDepth:       4,
		PendingAckTimeoutTicks: 10,
		GetTick:                func() uint32 { return h.tick },
		RemovedIDs:             func() []uint32 { return h.takeRemoved() },
	})
	// testReplicator's snapshot layout is X/Y as two float32.
	h.client = newRefClient(h.sys, 1, 4, 4)
	h.link.client = h.client
	return h
}

// removedQueue lets a test schedule a removal for the next tick only, the
// same tick-scoped contract RemovedIDs has in production.
func (h *lossyHarness) takeRemoved() []uint32 {
	out := h.removed
	h.removed = nil
	return out
}

// step advances one tick: move the world, deliver + ack anything the link has
// due (before the frame is built, matching clientInputTick running before the
// system loop), then run the system.
func (h *lossyHarness) step(move func()) {
	h.tick++
	if move != nil {
		move()
	}
	h.link.Pump()
	h.sys.Update(0.05)
}

// flush delivers whatever the update that just ran put on the link, without
// advancing the link clock — so an assertion about client state compares
// against the authority state that frame was built from.
func (h *lossyHarness) flush() { h.link.deliverDue() }

// moveTo repositions the tracked entity and refreshes its grid entry.
func (h *lossyHarness) moveTo(x, y float32) {
	pos, _, _ := h.em.mapper.Get(h.entity)
	pos.X, pos.Y = x, y
	h.grid.Update(spatial.Entry{Entity: h.entity, X: x, Y: y})
}

// authoritativeSnapshot returns the bytes testReplicator would produce for
// the entity's CURRENT grid position — i.e. what the client's baseline must
// equal if its state has not diverged from authority.
func (h *lossyHarness) authoritativeSnapshot() []byte {
	pos, _, _ := h.em.mapper.Get(h.entity)
	w := quantize.NewSnapshotWriter(make([]byte, 8))
	w.Float32(pos.X)
	w.Float32(pos.Y)
	return append([]byte(nil), w.Bytes()...)
}

func (h *lossyHarness) conn() *connState { return h.sys.connections[1] }
