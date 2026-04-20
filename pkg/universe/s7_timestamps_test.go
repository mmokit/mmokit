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

// TestBinaryFrameWriter_TimestampsAreMonotonic drives two real
// BinaryFrameWriter.WriteFrame calls in sequence and asserts both
// frames carry a non-zero server_time_ms that doesn't go backwards.
// Guards against two regression modes:
//   - time.Now().UnixMilli() in frame_writer.go being replaced with 0
//     (pkg/quantize's round-trip test doesn't catch this — it probes
//     the encoder in isolation).
//   - the stamp being moved to a build-time site where multiple frames
//     in one tick share a frozen stamp (breaks interp on the client).
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

	w.WriteFrame(&system.ReplicationFrame{
		Tick: 1, Seq: 1, Flags: 0,
		Viewer: &system.ViewerInfo{ConnID: 42},
	})
	// Force a wall-clock advance so the second frame's ms stamp differs
	// from the first — any monotonic failure must come from the stamping
	// logic itself, not from two frames rounding to the same ms.
	time.Sleep(2 * time.Millisecond)
	w.WriteFrame(&system.ReplicationFrame{
		Tick: 2, Seq: 2, Flags: 0,
		Viewer: &system.ViewerInfo{ConnID: 42},
	})

	conn.mu.Lock()
	defer conn.mu.Unlock()
	if len(conn.sent) != 2 {
		t.Fatalf("expected 2 captured frames, got %d", len(conn.sent))
	}
	h0 := quantize.NewFrameDecoder(conn.sent[0].data).Header()
	h1 := quantize.NewFrameDecoder(conn.sent[1].data).Header()
	if h0.ServerTimeMs == 0 {
		t.Errorf("frame 0 ServerTimeMs == 0, expected non-zero")
	}
	if h1.ServerTimeMs == 0 {
		t.Errorf("frame 1 ServerTimeMs == 0, expected non-zero")
	}
	if h1.ServerTimeMs < h0.ServerTimeMs {
		t.Errorf("timestamps went backward: frame0=%d frame1=%d",
			h0.ServerTimeMs, h1.ServerTimeMs)
	}
}
