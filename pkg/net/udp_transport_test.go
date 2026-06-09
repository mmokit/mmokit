package net

import (
	"slices"
	"testing"
)

// A channel-0x01 frame must land in the op queue (channel byte stripped),
// a channel-0x00 frame in the event queue, and the queues must be
// independent. This is the path auth + every typed op rides.
func TestUDPTransport_RoutePayloadDemuxesChannels(t *testing.T) {
	tr := &UDPTransport{}

	tr.routePayload([]byte{ChannelOperation, 0xAA, 0xBB}) // op
	tr.routePayload([]byte{ChannelEvent, 0x11, 0x22})     // event

	ops := tr.DrainOpInput()
	if len(ops) != 1 {
		t.Fatalf("DrainOpInput: got %d msgs, want 1", len(ops))
	}
	if !slices.Equal(ops[0], []byte{0xAA, 0xBB}) {
		t.Fatalf("op payload = %v, want [170 187]", ops[0])
	}

	evs := tr.DrainInput()
	if len(evs) != 1 {
		t.Fatalf("DrainInput: got %d msgs, want 1", len(evs))
	}
	if !slices.Equal(evs[0], []byte{0x11, 0x22}) {
		t.Fatalf("event payload = %v, want [17 34]", evs[0])
	}

	// Draining clears the queue.
	if got := tr.DrainOpInput(); got != nil {
		t.Fatalf("DrainOpInput after drain = %v, want nil", got)
	}
}

// A frame whose leading byte is neither 0x00 nor 0x01 is a legacy
// pre-channel-prefix event — it must go to the event queue with bytes
// intact and never into the op queue.
func TestUDPTransport_RoutePayloadLegacyNoPrefix(t *testing.T) {
	tr := &UDPTransport{}
	tr.routePayload([]byte{0x42, 0x99})

	evs := tr.DrainInput()
	if len(evs) != 1 || !slices.Equal(evs[0], []byte{0x42, 0x99}) {
		t.Fatalf("legacy event routing = %v, want [[66 153]]", evs)
	}
	if got := tr.DrainOpInput(); got != nil {
		t.Fatalf("legacy frame leaked into op queue: %v", got)
	}
}

// The reliable inbound path (handleReliable) is what auth ops use. Verify
// an op frame delivered reliably surfaces via DrainOpInput.
func TestUDPTransport_ReliableOpFrameReachesOpQueue(t *testing.T) {
	tr := &UDPTransport{}
	tr.handleReliable(1, []byte{ChannelOperation, 0x01, 0x02, 0x03})

	ops := tr.DrainOpInput()
	if len(ops) != 1 || !slices.Equal(ops[0], []byte{0x01, 0x02, 0x03}) {
		t.Fatalf("reliable op frame routing = %v, want [[1 2 3]]", ops)
	}
}
