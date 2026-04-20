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

func TestBinaryFrameWriter_StampsServerTimeMs(t *testing.T) {
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
	})
	after := uint64(time.Now().UnixMilli())

	data, ok := conn.sent[42]
	if !ok {
		t.Fatal("no frame captured for connID 42")
	}
	dec := quantize.NewFrameDecoder(data)
	hdr := dec.Header()
	if hdr.ServerTimeMs < before || hdr.ServerTimeMs > after {
		t.Fatalf("ServerTimeMs = %d, expected in [%d, %d]",
			hdr.ServerTimeMs, before, after)
	}
}
