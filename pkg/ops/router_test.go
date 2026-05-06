package ops

import (
	"testing"

	gamepb "github.com/zenion/mmoserver/gen/go/gamepb"
	"github.com/zenion/mmoserver/pkg/net"
)

func TestRouterTypedRegisterAndSchema(t *testing.T) {
	r := NewRouter(net.NewConnManager(), NewPlayerSessions(), 1, dummyParser, dummyFrameBuilder)
	Register[gamepb.MarketBrowseRequest, gamepb.MarketOrderBookResponse](
		r, uint32(gamepb.OperationCode_OP_MARKET_BROWSE), "marketBrowse",
		func(ctx *OpContext, req *gamepb.MarketBrowseRequest) (*gamepb.MarketOrderBookResponse, error) {
			return &gamepb.MarketOrderBookResponse{ItemId: req.ItemId}, nil
		})

	schema := r.Schema()
	if len(schema) != 1 {
		t.Fatalf("Schema() returned %d entries, want 1", len(schema))
	}
	s := schema[0]
	if s.Code != uint32(gamepb.OperationCode_OP_MARKET_BROWSE) {
		t.Errorf("Code = %d, want %d", s.Code, gamepb.OperationCode_OP_MARKET_BROWSE)
	}
	if s.Name != "marketBrowse" {
		t.Errorf("Name = %q, want %q", s.Name, "marketBrowse")
	}
	if s.RequestProto != "gamepb.MarketBrowseRequest" {
		t.Errorf("RequestProto = %q", s.RequestProto)
	}
	if s.ResponseProto != "gamepb.MarketOrderBookResponse" {
		t.Errorf("ResponseProto = %q", s.ResponseProto)
	}

	// Verify untyped Register still works but doesn't appear in Schema().
	r.Register(uint32(gamepb.OperationCode_OP_MARKET_CANCEL_ORDER), func(ctx *OpContext, payload []byte) ([]byte, error) {
		return nil, nil
	})
	if got := len(r.Schema()); got != 1 {
		t.Errorf("untyped Register should not appear in Schema(); got %d entries", got)
	}
}

func dummyParser(raw []byte) (ParsedRequest, error)                             { return ParsedRequest{}, nil }
func dummyFrameBuilder(_, _ uint32, _ int32, _ string, _ []byte) []byte         { return nil }

// smokeTransport is a minimal net.Transport implementation that captures
// SendReliable bytes for inspection and lets the test inject queued
// op-input bytes that Router.poll will drain.
type smokeTransport struct {
	opInput  [][]byte
	reliable [][]byte
}

func (s *smokeTransport) SendReliable(data []byte)   { s.reliable = append(s.reliable, data) }
func (s *smokeTransport) SendUnreliable(data []byte) {}
func (s *smokeTransport) DrainInput() [][]byte       { return nil }
func (s *smokeTransport) DrainOpInput() [][]byte {
	r := s.opInput
	s.opInput = nil
	return r
}
func (s *smokeTransport) InjectInput(_ []byte) {}
func (s *smokeTransport) Close()               {}

// TestRouter_Disambiguation_LegacyVsTyped pins the Plan 2 first-byte
// peek: a 0x08 prefix routes to the legacy proto-op path; any other
// prefix routes to the typed-op handler. Verifies the typed-op handler
// is consulted exactly once per non-0x08 frame, and not at all for
// 0x08 frames.
func TestRouter_Disambiguation_LegacyVsTyped(t *testing.T) {
	cm := net.NewConnManager()
	st := &smokeTransport{}
	connID := cm.AddTransport(st)
	// Drain the connect event so it doesn't pile up.
	<-cm.Events()

	r := NewRouter(cm, NewPlayerSessions(), 1, dummyParser, dummyFrameBuilder)

	var typedCalls int
	var typedRaw []byte
	r.SetTypedOpHandler(func(payload []byte, _ *OpContext) []byte {
		typedCalls++
		typedRaw = append([]byte(nil), payload...)
		return []byte{0xFF, 0xFE, 0xFD} // sentinel response
	})

	// Frame 1: typed-op payload (first byte != 0x08 by construction).
	st.opInput = append(st.opInput, []byte{0xAA, 0xBB, 0xCC, 0xDD})
	// Frame 2: legacy proto frame (first byte == 0x08).
	st.opInput = append(st.opInput, []byte{0x08, 0x01, 0x10, 0x02})

	r.poll()

	if typedCalls != 1 {
		t.Fatalf("typed handler invoked %d times, want 1", typedCalls)
	}
	if len(typedRaw) != 4 || typedRaw[0] != 0xAA {
		t.Fatalf("typed handler got payload %x, want first byte 0xAA", typedRaw)
	}
	if len(st.reliable) != 1 {
		t.Fatalf("expected 1 SendReliable from typed handler, got %d", len(st.reliable))
	}
	_ = connID
}

// TestRouter_TypedHandler_NilFalls_BackToLegacy pins that frames with
// first byte != 0x08 still hit the legacy parser when no typed-op
// handler is installed (default state pre-Plan-2-Phase-1 wiring).
func TestRouter_TypedHandler_NilFalls_BackToLegacy(t *testing.T) {
	cm := net.NewConnManager()
	st := &smokeTransport{}
	cm.AddTransport(st)
	<-cm.Events()

	parserCalls := 0
	parser := func(raw []byte) (ParsedRequest, error) {
		parserCalls++
		return ParsedRequest{Code: 1, RequestID: 1}, nil
	}
	r := NewRouter(cm, NewPlayerSessions(), 1, parser, dummyFrameBuilder)
	// no SetTypedOpHandler call → all frames go to legacy parser

	st.opInput = append(st.opInput, []byte{0xAA, 0xBB})
	st.opInput = append(st.opInput, []byte{0x08, 0x01})

	r.poll()
	if parserCalls != 2 {
		t.Errorf("legacy parser invoked %d times, want 2 (no typed handler installed)", parserCalls)
	}
}
