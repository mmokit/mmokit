package universe

import (
	"sync"
	"testing"
	"time"

	"github.com/zenion/mmoserver/pkg/net"
	"github.com/zenion/mmoserver/pkg/quantize"
	"github.com/zenion/mmoserver/pkg/system"
)

// captureSender wraps a ConnSender and records every outbound frame so
// the test can decode timestamps afterwards.
type captureSender struct {
	inner net.ConnSender
	mu    sync.Mutex
	sent  []capturedFrame
}

type capturedFrame struct {
	connID uint32
	data   []byte
}

func (c *captureSender) Send(connID uint32, data []byte) {
	c.mu.Lock()
	b := make([]byte, len(data))
	copy(b, data)
	c.sent = append(c.sent, capturedFrame{connID: connID, data: b})
	c.mu.Unlock()
	if c.inner != nil {
		c.inner.Send(connID, data)
	}
}

func (c *captureSender) SendReliable(connID uint32, data []byte) {
	if c.inner != nil {
		c.inner.SendReliable(connID, data)
	}
}

func (c *captureSender) InjectInput(connID uint32, data []byte) {
	if c.inner != nil {
		c.inner.InjectInput(connID, data)
	}
}

func (c *captureSender) DrainInput(connID uint32) [][]byte {
	if c.inner != nil {
		return c.inner.DrainInput(connID)
	}
	return nil
}

func (c *captureSender) DrainOpInput(connID uint32) [][]byte {
	if c.inner != nil {
		return c.inner.DrainOpInput(connID)
	}
	return nil
}

func (c *captureSender) DrainClientInput(connID uint32) [][]byte {
	if c.inner != nil {
		return c.inner.DrainClientInput(connID)
	}
	return nil
}

// TestBinaryFrameWriter_TimestampsAreMonotonic drives two
// BinaryFrameWriter.WriteFrame calls with monotonically-advancing
// per-entity ProducedAtMs stamps (as ReplicationSystem supplies upstream
// from ClusterClock.TickTime after Phase E) and asserts the writer is a
// faithful pass-through: both frames carry the stamps as-provided and the
// ordering survives the wire encode + decode round-trip.
//
// Guards against regressions where the writer silently rewrites or drops
// the stamp, or where the encoder's ProducedAtMs field is swapped with
// adjacent fields (the quantize round-trip test catches layout drift in
// isolation, but this test pins the end-to-end writer contract).
//
// A "monotonic across a real S7 split" integration — where frames
// emitted by the destination cell's goroutine after handoff are
// captured end-to-end — would require wiring a ConnSender into the
// coordinator's cell set and is follow-up work.
func TestBinaryFrameWriter_TimestampsAreMonotonic(t *testing.T) {
	makeEvent := func(_ uint32, data []byte) []byte {
		out := make([]byte, len(data))
		copy(out, data)
		return out
	}
	conn := &captureSender{}
	w := system.NewBinaryFrameWriter(conn, 99, makeEvent)

	// Stamps that ReplicationSystem would have supplied from ClusterClock.
	const stamp0 uint64 = 1_000_000
	const stamp1 uint64 = 1_000_050

	w.WriteFrame(&system.ReplicationFrame{
		Tick: 1, Seq: 1, Flags: 0,
		Viewer: &system.ViewerInfo{ConnID: 42},
		Full: []system.FullPayload{{
			NetID: 1, Epoch: 1, Type: 1, ProducedAtMs: stamp0, Snapshot: []byte{0x01},
		}},
	})
	// Simulate the next tick's advance. The sleep isn't load-bearing now
	// (stamps are explicit inputs) but mirrors the real per-tick cadence.
	time.Sleep(2 * time.Millisecond)
	w.WriteFrame(&system.ReplicationFrame{
		Tick: 2, Seq: 2, Flags: 0,
		Viewer: &system.ViewerInfo{ConnID: 42},
		Full: []system.FullPayload{{
			NetID: 1, Epoch: 1, Type: 1, ProducedAtMs: stamp1, Snapshot: []byte{0x02},
		}},
	})

	conn.mu.Lock()
	defer conn.mu.Unlock()
	if len(conn.sent) != 2 {
		t.Fatalf("expected 2 captured frames, got %d", len(conn.sent))
	}
	d0 := quantize.NewFrameDecoder(conn.sent[0].data)
	_ = d0.Header()
	f0 := d0.NextFull()
	d1 := quantize.NewFrameDecoder(conn.sent[1].data)
	_ = d1.Header()
	f1 := d1.NextFull()
	if f0.ProducedAtMs != stamp0 {
		t.Errorf("frame 0 ProducedAtMs = %d, want %d (writer must pass through)", f0.ProducedAtMs, stamp0)
	}
	if f1.ProducedAtMs != stamp1 {
		t.Errorf("frame 1 ProducedAtMs = %d, want %d (writer must pass through)", f1.ProducedAtMs, stamp1)
	}
	if f1.ProducedAtMs < f0.ProducedAtMs {
		t.Errorf("timestamps went backward: frame0=%d frame1=%d",
			f0.ProducedAtMs, f1.ProducedAtMs)
	}
}
