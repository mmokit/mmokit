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
