package universe_test

import (
	"encoding/binary"
	"reflect"
	"testing"

	"github.com/zenion/mmoserver/pkg/engine"
	"github.com/zenion/mmoserver/pkg/logger"
	"github.com/zenion/mmoserver/pkg/mmokit"
	pkgnet "github.com/zenion/mmoserver/pkg/net"
	pkguniverse "github.com/zenion/mmoserver/pkg/universe"
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

// sendEventTestMsg has two int32 fields → 8-byte LE body via ReflectMarshal.
type sendEventTestMsg struct {
	A int32
	B int32
}

// TestStageSendEventTyped pins the typed-event send path:
// register a type via mmokit.RegisterEvent[T], call SendEventTyped, and
// assert the captured frame is exactly
//
//	[0x00][typeID:u32 BE][bodyLen:u32 BE][body]
//
// where body is the reflection-codec encoding of the struct.
func TestStageSendEventTyped(t *testing.T) {
	mmokit.ResetServerEventRegistryForTest()
	t.Cleanup(mmokit.ResetServerEventRegistryForTest)

	mmokit.RegisterEvent[sendEventTestMsg]()

	conn := &sendEventCaptureConn{sent: make(map[uint32][]byte)}
	log := logger.New()
	eng := engine.New(engine.DefaultConfig(), conn, log)

	cellID, err := pkguniverse.ParseCellID("0_0")
	if err != nil {
		t.Fatalf("ParseCellID: %v", err)
	}
	stage := pkguniverse.NewStage(eng, cellID, 300, nil)

	const connID uint32 = 42
	msg := &sendEventTestMsg{A: 7, B: 9}
	pkguniverse.SendEventTyped(stage, connID, msg)

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
	wantTypeID := mmokit.TypeIDOf(reflect.TypeFor[sendEventTestMsg]())
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

// TestStageSendEventTyped_PanicsWhenUnregistered guards the safety check:
// sending a type that was never registered via mmokit.RegisterEvent[T] is
// a programmer error and must panic with an actionable message.
func TestStageSendEventTyped_PanicsWhenUnregistered(t *testing.T) {
	mmokit.ResetServerEventRegistryForTest()
	t.Cleanup(mmokit.ResetServerEventRegistryForTest)

	conn := &sendEventCaptureConn{sent: make(map[uint32][]byte)}
	log := logger.New()
	eng := engine.New(engine.DefaultConfig(), conn, log)

	cellID, err := pkguniverse.ParseCellID("0_0")
	if err != nil {
		t.Fatalf("ParseCellID: %v", err)
	}
	stage := pkguniverse.NewStage(eng, cellID, 300, nil)

	defer func() {
		r := recover()
		if r == nil {
			t.Fatalf("expected panic, got none")
		}
		msg, ok := r.(string)
		if !ok {
			t.Fatalf("panic value not string: %T %v", r, r)
		}
		// Must mention the type and the registration verb.
		if !containsAll(msg, "sendEventTestMsg", "RegisterEvent") {
			t.Fatalf("panic message %q missing type name or registration hint", msg)
		}
	}()

	pkguniverse.SendEventTyped(stage, 1, &sendEventTestMsg{A: 1, B: 2})
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !contains(s, sub) {
			return false
		}
	}
	return true
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
