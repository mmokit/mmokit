package mmokit

import (
	"encoding/binary"
	"testing"

	"github.com/zenion/mmoserver/pkg/net"
	"github.com/zenion/mmoserver/pkg/quantize"
)

type networkRemovalTestState struct {
	Value uint16 `net:"u16"`
}

type networkRemovalTestBundle struct {
	State *networkRemovalTestState
}

type networkRemovalCaptureTransport struct {
	frames [][]byte
}

func (t *networkRemovalCaptureTransport) SendReliable(data []byte) net.SendResult {
	return t.capture(data)
}

func (t *networkRemovalCaptureTransport) SendUnreliable(data []byte) net.SendResult {
	return t.capture(data)
}

func (t *networkRemovalCaptureTransport) capture(data []byte) net.SendResult {
	t.frames = append(t.frames, append([]byte(nil), data...))
	return net.SendResult{
		Disposition: net.SendQueued,
		Delivery:    net.DeliveryReliableOrdered,
	}
}

func (t *networkRemovalCaptureTransport) DrainInput() [][]byte   { return nil }
func (t *networkRemovalCaptureTransport) DrainOpInput() [][]byte { return nil }
func (t *networkRemovalCaptureTransport) InjectInput([]byte)     {}
func (t *networkRemovalCaptureTransport) Close()                 {}

func TestNewNetworkSystem_AuthoritativeDespawnPublishesOneRemoval(t *testing.T) {
	const kind uint8 = 241

	mmo := newTestProcess(t)
	RegisterKind[networkRemovalTestBundle](mmo, kind, "NetworkRemovalTest")
	mmo.AddSystem(NewNetworkSystem())
	mmo.Build()
	t.Cleanup(mmo.Shutdown)

	cell := firstCell(mmo)
	if cell == nil {
		t.Fatal("expected one cell")
	}
	network, ok := cell.Loop.SystemByName("Network")
	if !ok {
		t.Fatal("NewNetworkSystem was not installed")
	}

	stage := cell.Stage
	eng := cell.Engine
	transport := &networkRemovalCaptureTransport{}
	connID := eng.ConnMgr.(*net.ConnManager).AddTransport(transport)

	viewer := stage.Spawn(
		Position{X: 0, Y: 0},
		EntityKind{Type: kind},
		Collider{Radius: 1},
		networkRemovalTestState{Value: 1},
	)
	target := stage.Spawn(
		Position{X: 10, Y: 0},
		EntityKind{Type: kind},
		Collider{Radius: 1},
		networkRemovalTestState{Value: 2},
	)
	targetNetID := target.NetID()

	eng.Players.RegisterSessionTransfer(connID, "network-removal-viewer", "active", nil)
	session := eng.Players.ByConnID(connID)
	if session == nil {
		t.Fatal("active viewer session was not registered")
	}
	session.Entity = viewer.Handle()

	runNetworkTick := func() quantize.FrameHeader {
		t.Helper()
		eng.Tick++
		eng.BeginRemovalTick()
		before := len(transport.frames)
		stage.TickOne(network, 1.0/20.0)
		if len(transport.frames) != before+1 {
			t.Fatalf("network tick emitted %d frames, want 1", len(transport.frames)-before)
		}
		return decodeWorldDeltaHeader(t, transport.frames[len(transport.frames)-1])
	}

	// Establish committed visibility for the target.
	initial := runNetworkTick()
	if initial.FullCount < 1 {
		t.Fatalf("initial frame FullCount=%d, want target visibility established", initial.FullCount)
	}

	// Match the production phase order: replication samples during the tick,
	// then the authoritative mmokit.Despawn is flushed at the end of it. The
	// phase-safe engine batch carries that late tombstone into the next tick.
	eng.Tick++
	eng.BeginRemovalTick()
	Despawn(target)
	stage.TickOne(network, 1.0/20.0)
	eng.FlushRemovals()

	removedFrame := runNetworkTick()
	if removedFrame.RemovedCount != 1 {
		t.Fatalf("authoritative despawn RemovedCount=%d, want 1", removedFrame.RemovedCount)
	}
	if removedFrame.ExitedCount != 0 {
		t.Fatalf("authoritative despawn ExitedCount=%d, want 0", removedFrame.ExitedCount)
	}

	removedID := decodeOnlyRemovedID(t, transport.frames[len(transport.frames)-1])
	if removedID != targetNetID {
		t.Fatalf("removed netID=%d, want %d", removedID, targetNetID)
	}

	retiredFrame := runNetworkTick()
	if retiredFrame.RemovedCount != 0 || retiredFrame.ExitedCount != 0 {
		t.Fatalf("retired tombstone repeated: removed=%d exited=%d", retiredFrame.RemovedCount, retiredFrame.ExitedCount)
	}
}

func decodeWorldDeltaHeader(t *testing.T, frame []byte) quantize.FrameHeader {
	t.Helper()
	body := worldDeltaBody(t, frame)
	return quantize.NewFrameDecoder(body).Header()
}

func decodeOnlyRemovedID(t *testing.T, frame []byte) uint32 {
	t.Helper()
	body := worldDeltaBody(t, frame)
	decoder := quantize.NewFrameDecoder(body)
	header := decoder.Header()
	for range header.FullCount {
		decoder.NextFull()
	}
	for range header.DeltaCount {
		decoder.NextDelta()
	}
	return decoder.NextUint32()
}

func worldDeltaBody(t *testing.T, frame []byte) []byte {
	t.Helper()
	// Typed WorldDelta frame:
	// [channel][typeID:u32 LE][payloadLen:u32 LE][bodyLen:u32 LE][body].
	if len(frame) < 13 {
		t.Fatalf("WorldDelta frame length=%d, want at least 13", len(frame))
	}
	bodyLen := int(binary.LittleEndian.Uint32(frame[9:13]))
	if bodyLen > len(frame)-13 {
		t.Fatalf("WorldDelta body length=%d exceeds remaining frame bytes=%d", bodyLen, len(frame)-13)
	}
	return frame[13 : 13+bodyLen]
}
