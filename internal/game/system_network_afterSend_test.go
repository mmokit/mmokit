package game

import (
	"encoding/binary"
	"sync"
	"testing"

	"github.com/zenion/mmoserver/pkg/mmokit"
	pkgnet "github.com/zenion/mmoserver/pkg/net"
)

// captureTransport records every SendUnreliable / SendReliable payload so
// tests can assert on the exact wire bytes a system writes per viewer.
type captureTransport struct {
	mu         sync.Mutex
	unreliable [][]byte
	reliable   [][]byte
}

func (t *captureTransport) SendReliable(data []byte) {
	t.mu.Lock()
	defer t.mu.Unlock()
	cp := make([]byte, len(data))
	copy(cp, data)
	t.reliable = append(t.reliable, cp)
}

func (t *captureTransport) SendUnreliable(data []byte) {
	t.mu.Lock()
	defer t.mu.Unlock()
	cp := make([]byte, len(data))
	copy(cp, data)
	t.unreliable = append(t.unreliable, cp)
}

func (t *captureTransport) DrainInput() [][]byte   { return nil }
func (t *captureTransport) DrainOpInput() [][]byte { return nil }
func (t *captureTransport) InjectInput(_ []byte)   {}
func (t *captureTransport) Close()                 {}

// LastSent returns the most recent unreliable frame, or nil if none.
func (t *captureTransport) LastSent() []byte {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.unreliable) == 0 {
		return nil
	}
	return t.unreliable[len(t.unreliable)-1]
}

// AllSent returns a copy of all captured unreliable frames.
func (t *captureTransport) AllSent() [][]byte {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([][]byte, len(t.unreliable))
	copy(out, t.unreliable)
	return out
}

// TestAfterSend_WritesBatchedTypedEventFrame verifies the new wire format:
// when broadcast events have anchors visible to a viewer, afterSend builds
// a single 0x00 [typeID:u32 LE][body_len:u32 LE][body] ... frame and writes
// it via ConnMgr.Send (unreliable).
func TestAfterSend_WritesBatchedTypedEventFrame(t *testing.T) {
	gw, cm := newTestGameWorld()

	// Wire a capture transport so we can inspect the exact bytes sent.
	tr := &captureTransport{}
	connID := cm.AddTransport(tr)
	<-cm.Events() // drain connect event

	ns := wireNetworkSystemForTest(t, gw)

	// Push two broadcast events whose anchors include the same visible NetID.
	const visibleNID = uint32(101)
	const otherNID = uint32(202) // never visible to this viewer
	body1 := []byte{0x10, 0x20}
	body2 := []byte{0x30, 0x40, 0x50}

	gw.Stage.BroadcastQueue().Push(mmokit.BroadcastEvent{
		TypeID:  0xAAAA0001,
		Body:    body1,
		Anchors: []uint32{visibleNID},
	})
	gw.Stage.BroadcastQueue().Push(mmokit.BroadcastEvent{
		TypeID:  0xBBBB0002,
		Body:    body2,
		Anchors: []uint32{otherNID, visibleNID}, // passes via second anchor
	})

	// beforeTick drains the queue into s.pendingBroadcasts.
	ns.beforeTick(0)

	viewer := &mmokit.ViewerInfo{ConnID: connID}
	visible := map[uint32]bool{visibleNID: true}
	ns.afterSend(viewer, visible)

	frame := tr.LastSent()
	if frame == nil {
		t.Fatal("expected a frame to be sent, got nil")
	}

	// Expected total: 1 (channel) + 2 entries * (4 typeID + 4 bodyLen) + len(body1) + len(body2)
	wantLen := 1 + 2*(4+4) + len(body1) + len(body2)
	if len(frame) != wantLen {
		t.Fatalf("frame length: got %d, want %d", len(frame), wantLen)
	}

	if frame[0] != pkgnet.ChannelEvent {
		t.Fatalf("channel byte: got %#x, want %#x (ChannelEvent)", frame[0], pkgnet.ChannelEvent)
	}

	// Entry 1.
	if got := binary.LittleEndian.Uint32(frame[1:5]); got != 0xAAAA0001 {
		t.Fatalf("entry 1 typeID: got %#x, want 0xAAAA0001", got)
	}
	if got := binary.LittleEndian.Uint32(frame[5:9]); got != uint32(len(body1)) {
		t.Fatalf("entry 1 body_len: got %d, want %d", got, len(body1))
	}
	gotBody1 := frame[9 : 9+len(body1)]
	for i, b := range body1 {
		if gotBody1[i] != b {
			t.Fatalf("entry 1 body[%d]: got %#x, want %#x", i, gotBody1[i], b)
		}
	}

	// Entry 2 starts after entry 1.
	off := 9 + len(body1)
	if got := binary.LittleEndian.Uint32(frame[off : off+4]); got != 0xBBBB0002 {
		t.Fatalf("entry 2 typeID: got %#x, want 0xBBBB0002", got)
	}
	if got := binary.LittleEndian.Uint32(frame[off+4 : off+8]); got != uint32(len(body2)) {
		t.Fatalf("entry 2 body_len: got %d, want %d", got, len(body2))
	}
	gotBody2 := frame[off+8 : off+8+len(body2)]
	for i, b := range body2 {
		if gotBody2[i] != b {
			t.Fatalf("entry 2 body[%d]: got %#x, want %#x", i, gotBody2[i], b)
		}
	}

	// Confirm only one frame was sent (single batched write).
	if all := tr.AllSent(); len(all) != 1 {
		t.Fatalf("expected exactly 1 frame, got %d", len(all))
	}
}

// TestAfterSend_NoVisibleAnchors_NoFrameSent verifies the empty-AoI path:
// when none of the broadcast events have anchors visible to the viewer,
// afterSend writes nothing.
func TestAfterSend_NoVisibleAnchors_NoFrameSent(t *testing.T) {
	gw, cm := newTestGameWorld()

	tr := &captureTransport{}
	connID := cm.AddTransport(tr)
	<-cm.Events()

	ns := wireNetworkSystemForTest(t, gw)

	gw.Stage.BroadcastQueue().Push(mmokit.BroadcastEvent{
		TypeID:  0xAAAA0001,
		Body:    []byte{0x10, 0x20},
		Anchors: []uint32{999},
	})
	gw.Stage.BroadcastQueue().Push(mmokit.BroadcastEvent{
		TypeID:  0xBBBB0002,
		Body:    []byte{0x30},
		Anchors: []uint32{888},
	})

	ns.beforeTick(0)

	viewer := &mmokit.ViewerInfo{ConnID: connID}
	visible := map[uint32]bool{} // empty: no anchors pass
	ns.afterSend(viewer, visible)

	if got := tr.LastSent(); got != nil {
		t.Fatalf("expected no frame to be sent (empty AoI), got %d bytes: %v", len(got), got)
	}
}
