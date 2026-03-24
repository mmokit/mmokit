package marketplace

import (
	"fmt"

	"google.golang.org/protobuf/proto"

	gamepb "github.com/zenion/mmoserver/gen/go/gamepb"
	"github.com/zenion/mmoserver/pkg/ops"
	"github.com/zenion/mmoserver/pkg/orderbook"
)

// RegisterHandlers registers all marketplace operation handlers with the router.
func RegisterHandlers(router *ops.Router, svc *Settlement, stationID uint32) {
	router.Register(uint32(gamepb.OperationCode_OP_MARKET_BROWSE), func(ctx *ops.OpContext, payload []byte) ([]byte, error) {
		var req gamepb.MarketBrowseRequest
		if err := proto.Unmarshal(payload, &req); err != nil {
			return nil, fmt.Errorf("invalid browse request: %w", err)
		}
		view := svc.Browse(stationID, req.ItemId)
		resp := &gamepb.MarketOrderBookResponse{
			ItemId: view.ItemID,
		}
		for _, l := range view.SellLevels {
			resp.SellLevels = append(resp.SellLevels, &gamepb.MarketPriceLevel{
				Price:      l.Price,
				Quantity:   l.Quantity,
				OrderCount: uint32(l.Count),
			})
		}
		for _, l := range view.BuyLevels {
			resp.BuyLevels = append(resp.BuyLevels, &gamepb.MarketPriceLevel{
				Price:      l.Price,
				Quantity:   l.Quantity,
				OrderCount: uint32(l.Count),
			})
		}
		return proto.Marshal(resp)
	})

	router.Register(uint32(gamepb.OperationCode_OP_MARKET_CREATE_ORDER), func(ctx *ops.OpContext, payload []byte) ([]byte, error) {
		var req gamepb.MarketCreateOrderRequest
		if err := proto.Unmarshal(payload, &req); err != nil {
			return nil, fmt.Errorf("invalid create order request: %w", err)
		}

		var result *orderbook.PlaceResult
		var err error
		if req.IsBuy {
			result, err = svc.PlaceBuyOrder(ctx.Username, stationID, req.ItemId, req.PricePerUnit, req.Quantity)
		} else {
			result, err = svc.PlaceSellOrder(ctx.Username, stationID, req.ItemId, req.PricePerUnit, req.Quantity)
		}
		if err != nil {
			return nil, err
		}

		return proto.Marshal(&gamepb.MarketOrderResultResponse{
			OrderId:   result.OrderID,
			FilledQty: result.FilledQty,
			AvgPrice:  result.AvgPrice,
			TotalCost: result.TotalCost,
		})
	})

	router.Register(uint32(gamepb.OperationCode_OP_MARKET_CANCEL_ORDER), func(ctx *ops.OpContext, payload []byte) ([]byte, error) {
		var req gamepb.MarketCancelOrderRequest
		if err := proto.Unmarshal(payload, &req); err != nil {
			return nil, fmt.Errorf("invalid cancel order request: %w", err)
		}
		if err := svc.CancelOrder(ctx.Username, req.OrderId); err != nil {
			return nil, err
		}
		return proto.Marshal(&gamepb.MarketOrderResultResponse{OrderId: req.OrderId})
	})

	router.Register(uint32(gamepb.OperationCode_OP_MARKET_MY_ORDERS), func(ctx *ops.OpContext, payload []byte) ([]byte, error) {
		orders := svc.PlayerOrders(ctx.Username)
		resp := &gamepb.MarketMyOrdersResponse{}
		for _, o := range orders {
			resp.Orders = append(resp.Orders, &gamepb.MarketOrderEntry{
				OrderId:      o.ID,
				ItemId:       o.ItemID,
				IsBuy:        o.Side == orderbook.SideBuy,
				PricePerUnit: o.Price,
				Quantity:     o.Quantity,
				OrigQuantity: o.OrigQty,
				CreatedAt:    o.CreatedAt,
				ExpiresAt:    o.ExpiresAt,
			})
		}
		return proto.Marshal(resp)
	})

	router.Register(uint32(gamepb.OperationCode_OP_MARKET_INSTANT_TRADE), func(ctx *ops.OpContext, payload []byte) ([]byte, error) {
		var req gamepb.MarketInstantTradeRequest
		if err := proto.Unmarshal(payload, &req); err != nil {
			return nil, fmt.Errorf("invalid instant trade request: %w", err)
		}

		var result *orderbook.PlaceResult
		var err error
		if req.IsBuy {
			result, err = svc.InstantBuy(ctx.Username, stationID, req.ItemId, req.Quantity)
		} else {
			result, err = svc.InstantSell(ctx.Username, stationID, req.ItemId, req.Quantity)
		}
		if err != nil {
			return nil, err
		}

		return proto.Marshal(&gamepb.MarketOrderResultResponse{
			FilledQty: result.FilledQty,
			AvgPrice:  result.AvgPrice,
			TotalCost: result.TotalCost,
		})
	})
}
