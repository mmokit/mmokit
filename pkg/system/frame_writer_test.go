package system

import (
	"testing"
	"time"

	"github.com/zenion/mmoserver/pkg/net"
	"github.com/zenion/mmoserver/pkg/quantize"
)

type captureConn struct {
	sent map[uint32][]byte
}

func (c *captureConn) Send(connID uint32, data []byte) {
	c.sent[connID] = append([]byte(nil), data...)
}
func (c *captureConn) SendReliable(connID uint32, data []byte) {
	c.sent[connID] = append([]byte(nil), data...)
}
func (c *captureConn) InjectInput(connID uint32, data []byte)  {}
func (c *captureConn) DrainInput(connID uint32) [][]byte       { return nil }
func (c *captureConn) DrainOpInput(connID uint32) [][]byte     { return nil }

var _ net.ConnSender = (*captureConn)(nil)

func TestBinaryFrameWriter_StampsProducedAtMsPerEntity(t *testing.T) {
	// makeEvent stub — returns the raw binary unchanged, no envelope.
	makeEvent := func(code uint32, data []byte) []byte {
		out := make([]byte, len(data))
		copy(out, data)
		return out
	}
	conn := &captureConn{sent: make(map[uint32][]byte)}
	w := NewBinaryFrameWriter(conn, 99 /* eventCode */, makeEvent)

	before := uint64(time.Now().UnixMilli())
	w.WriteFrame(&ReplicationFrame{
		Tick:   1,
		Seq:    1,
		Flags:  0,
		Viewer: &ViewerInfo{ConnID: 42},
		Full: []FullPayload{{
			NetID:    101,
			Epoch:    1,
			Type:     1,
			Snapshot: []byte{0x01, 0x02},
		}},
		Deltas: []DeltaPayload{{
			NetID: 102,
			Epoch: 2,
			Type:  2,
			Data:  []byte{0xAA},
		}},
	})
	after := uint64(time.Now().UnixMilli())

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
	if full.ProducedAtMs < before || full.ProducedAtMs > after {
		t.Fatalf("full.ProducedAtMs = %d, expected in [%d, %d]",
			full.ProducedAtMs, before, after)
	}
	delta := dec.NextDelta()
	if delta.ProducedAtMs < before || delta.ProducedAtMs > after {
		t.Fatalf("delta.ProducedAtMs = %d, expected in [%d, %d]",
			delta.ProducedAtMs, before, after)
	}
}
