package facadetest

import (
	"encoding/binary"
	"reflect"
	"testing"

	"github.com/mmokit/mmokit"
	"github.com/mmokit/mmokit/pkg/engine"
	"github.com/mmokit/mmokit/pkg/logger"
	pkgnet "github.com/mmokit/mmokit/pkg/net"
	pkguniverse "github.com/mmokit/mmokit/pkg/universe"
)

// sendEventCaptureConn captures SendReliable bytes per connID so the
// test can decode and inspect the frame layout end-to-end.
type sendEventCaptureConn struct {
	sent map[uint32][]byte
}

func (c *sendEventCaptureConn) Send(connID uint32, data []byte) pkgnet.SendResult {
	c.sent[connID] = append([]byte(nil), data...)
	return pkgnet.SendResult{Disposition: pkgnet.SendQueued, Delivery: pkgnet.DeliveryReliableOrdered}
}
func (c *sendEventCaptureConn) SendReliable(connID uint32, data []byte) pkgnet.SendResult {
	c.sent[connID] = append([]byte(nil), data...)
	return pkgnet.SendResult{Disposition: pkgnet.SendQueued, Delivery: pkgnet.DeliveryReliableOrdered}
}
func (c *sendEventCaptureConn) InjectInput(connID uint32, data []byte) {}
func (c *sendEventCaptureConn) DrainInput(connID uint32) [][]byte      { return nil }
func (c *sendEventCaptureConn) DrainOpInput(connID uint32) [][]byte    { return nil }

var _ pkgnet.ConnSender = (*sendEventCaptureConn)(nil)

// mmokitSendEventMsg has two int32 fields → 8-byte LE body via ReflectMarshal.
type mmokitSendEventMsg struct {
	A int32
	B int32
}

// TestSendEvent_ThroughFacade verifies that mmokit.SendEvent is a thin
// pass-through to pkguniverse.SendEventTyped. We register a typed event via
// mmokit.RegisterEvent[T], call mmokit.SendEvent (NOT the universe-layer
// helper), and assert the captured frame matches the typed wire layout:
//
//	[0x00][typeID:u32 BE][bodyLen:u32 BE][body]
func TestSendEvent_ThroughFacade(t *testing.T) {
	conn := &sendEventCaptureConn{sent: make(map[uint32][]byte)}
	log := logger.New()
	eng := engine.New(engine.DefaultConfig(), conn, log)

	cellID, err := pkguniverse.ParseCellID("0_0")
	if err != nil {
		t.Fatalf("ParseCellID: %v", err)
	}
	// The stage carries the registry, so it is both what the verb registers
	// against and what the send path resolves against.
	stage := pkguniverse.NewStage(eng, cellID, 300, nil, pkguniverse.NewWireRegistry())
	mmokit.RegisterEvent[mmokitSendEventMsg](stage)

	const connID uint32 = 42
	msg := &mmokitSendEventMsg{A: 7, B: 9}

	// Call through the facade — this is what game code would write.
	mmokit.SendEvent(stage, connID, msg)

	frame, ok := conn.sent[connID]
	if !ok {
		t.Fatalf("no frame captured for connID=%d (sent=%v)", connID, conn.sent)
	}

	// Frame layout: [0x00][typeID:u32 BE][bodyLen:u32 BE][body]
	const headerLen = 1 + 4 + 4
	const bodyLen = 8 // two int32s
	wantTotal := headerLen + bodyLen
	if len(frame) != wantTotal {
		t.Fatalf("frame length = %d, want %d (frame=%x)", len(frame), wantTotal, frame)
	}
	if frame[0] != pkgnet.ChannelEvent {
		t.Fatalf("frame[0] = %#x, want %#x (ChannelEvent)", frame[0], pkgnet.ChannelEvent)
	}

	gotTypeID := binary.LittleEndian.Uint32(frame[1:5])
	wantTypeID := mmokit.TypeIDOf(reflect.TypeFor[mmokitSendEventMsg]())
	if gotTypeID != wantTypeID {
		t.Fatalf("typeID = %#x, want %#x", gotTypeID, wantTypeID)
	}

	gotBodyLen := binary.LittleEndian.Uint32(frame[5:9])
	if gotBodyLen != bodyLen {
		t.Fatalf("bodyLen field = %d, want %d", gotBodyLen, bodyLen)
	}

	// Reflection codec: int32 LE in field-declaration order.
	wantBody := []byte{
		7, 0, 0, 0,
		9, 0, 0, 0,
	}
	gotBody := frame[headerLen:]
	if !reflect.DeepEqual(gotBody, wantBody) {
		t.Fatalf("body = %x, want %x", gotBody, wantBody)
	}
}
