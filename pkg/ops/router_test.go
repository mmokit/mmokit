package ops

import (
	"testing"

	"github.com/zenion/mmokit/pkg/net"
)

// smokeTransport is a minimal net.Transport implementation that captures
// SendReliable bytes for inspection and lets the test inject queued
// op-input bytes that Router.poll will drain.
type smokeTransport struct {
	opInput  [][]byte
	reliable [][]byte
}

func (s *smokeTransport) SendReliable(data []byte) net.SendResult {
	s.reliable = append(s.reliable, data)
	return net.SendResult{Disposition: net.SendQueued, Delivery: net.DeliveryReliableOrdered}
}
func (s *smokeTransport) SendUnreliable(data []byte) net.SendResult {
	return net.SendResult{Disposition: net.SendQueued, Delivery: net.DeliveryReliableOrdered}
}
func (s *smokeTransport) DrainInput() [][]byte { return nil }
func (s *smokeTransport) DrainOpInput() [][]byte {
	r := s.opInput
	s.opInput = nil
	return r
}
func (s *smokeTransport) InjectInput(_ []byte) {}
func (s *smokeTransport) Close()               {}

// TestRouter_TypedOpHandler_DispatchesEveryFrame pins the post-Plan-2 shape:
// every channel-0x01 frame the router drains is handed to the typed-op
// handler. The handler may return a response frame (sent reliably back to
// the conn) or nil (silent drop / async).
func TestRouter_TypedOpHandler_DispatchesEveryFrame(t *testing.T) {
	cm := net.NewConnManager()
	st := &smokeTransport{}
	cm.AddTransport(st, "")
	<-cm.Events() // drain connect event

	r := NewRouter(cm, NewPlayerSessions())

	var typedCalls int
	var lastPayload []byte
	r.SetTypedOpHandler(func(payload []byte, _ *OpContext) []byte {
		typedCalls++
		lastPayload = append([]byte(nil), payload...)
		return []byte{0xFF, 0xFE, 0xFD} // sentinel response
	})

	st.opInput = append(st.opInput, []byte{0xAA, 0xBB, 0xCC, 0xDD})
	st.opInput = append(st.opInput, []byte{0x11, 0x22})

	r.poll()

	if typedCalls != 2 {
		t.Fatalf("typed handler invoked %d times, want 2", typedCalls)
	}
	if len(lastPayload) != 2 || lastPayload[0] != 0x11 {
		t.Fatalf("typed handler got payload %x, want first byte 0x11", lastPayload)
	}
	if len(st.reliable) != 2 {
		t.Fatalf("expected 2 SendReliable, got %d", len(st.reliable))
	}
}

// TestRouter_NoTypedHandler_DropsFrames pins the dispatcher-absent fallback:
// without SetTypedOpHandler the poll loop drains the per-conn op queue (so
// frames don't accumulate) but invokes nothing.
func TestRouter_NoTypedHandler_DropsFrames(t *testing.T) {
	cm := net.NewConnManager()
	st := &smokeTransport{}
	cm.AddTransport(st, "")
	<-cm.Events()

	r := NewRouter(cm, NewPlayerSessions())
	// no SetTypedOpHandler call

	st.opInput = append(st.opInput, []byte{0xAA, 0xBB})
	st.opInput = append(st.opInput, []byte{0x08, 0x01})

	r.poll()

	if len(st.reliable) != 0 {
		t.Errorf("no typed handler installed; expected 0 reliable sends, got %d", len(st.reliable))
	}
}
