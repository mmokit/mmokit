package main

import (
	"os"

	"github.com/mlange-42/ark/ecs"

	enginepb "github.com/zenion/mmoserver/gen/go/enginepb"
	gamepb "github.com/zenion/mmoserver/gen/go/gamepb"
	"github.com/zenion/mmoserver/internal/game"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// dumpProtocolSchema writes the space game protocol schema as JSON to stdout.
// Called by `./cmd/server --dump-schema` to drive sdkgen.
func dumpProtocolSchema(cfg mmokit.Config) {
	// Use the Protocol declared on cfg (which carries the ServerEvents registry)
	// so the schema dump reflects the same declaration as the running server.
	proto, _ := cfg.Protocol.(*mmokit.Protocol)
	if proto == nil {
		proto = mmokit.NewProtocol("space")
	}
	proto.SetClientRenderMode(cfg.ClientRenderMode)

	// --- Engine client → server events ---
	mmokit.ClientEvent(proto, enginepb.ClientEventCode_CE_LOGIN, "enginepb.LoginMsg")
	mmokit.ClientEvent(proto, enginepb.ClientEventCode_CE_PING, "enginepb.PingMsg")
	mmokit.ClientEvent(proto, enginepb.ClientEventCode_CE_RESPAWN, "enginepb.RespawnRequestMsg")
	mmokit.ClientEvent(proto, enginepb.ClientEventCode_CE_CHAT, "enginepb.ChatMsg")
	mmokit.ClientEvent(proto, enginepb.ClientEventCode_CE_PLAYER_INPUT, "gamepb.PlayerInputMsg")

	// --- Game client → server events ---
	mmokit.ClientEvent(proto, gamepb.GameClientEventCode_GCE_INVENTORY_TRANSFER, "gamepb.InventoryTransferMsg")
	mmokit.ClientEvent(proto, gamepb.GameClientEventCode_GCE_BANK_REQUEST, "gamepb.BankRequestMsg")
	mmokit.ClientEvent(proto, gamepb.GameClientEventCode_GCE_SELL_BANK_ITEM, "gamepb.SellBankItemMsg")
	mmokit.ClientEvent(proto, gamepb.GameClientEventCode_GCE_EQUIP, "gamepb.EquipRequestMsg")
	mmokit.ClientEvent(proto, gamepb.GameClientEventCode_GCE_SHOP_BUY, "gamepb.ShopBuyMsg")
	mmokit.ClientEvent(proto, gamepb.GameClientEventCode_GCE_DOCK, "gamepb.DockRequestMsg")
	mmokit.ClientEvent(proto, gamepb.GameClientEventCode_GCE_UNDOCK, "gamepb.UndockRequestMsg")
	mmokit.ClientEvent(proto, gamepb.GameClientEventCode_GCE_LOOT_ITEM, "gamepb.LootItemMsg")
	mmokit.ClientEvent(proto, gamepb.GameClientEventCode_GCE_LOOT_ALL, "gamepb.LootAllMsg")

	// Binary-encoded server event (no proto type; not in ServerEvents registry).
	mmokit.ServerEvent(proto, enginepb.ServerEventCode_SE_DELTA_WORLD_UPDATE, "deltaWorldUpdate", "")

	// --- Marketplace operations (channel 0x01, request/response) ---
	mmokit.Operation(proto, gamepb.OperationCode_OP_MARKET_BROWSE, "marketBrowse",
		"gamepb.MarketBrowseRequest", "gamepb.MarketOrderBookResponse")
	mmokit.Operation(proto, gamepb.OperationCode_OP_MARKET_CREATE_ORDER, "marketCreateOrder",
		"gamepb.MarketCreateOrderRequest", "gamepb.MarketOrderResultResponse")
	mmokit.Operation(proto, gamepb.OperationCode_OP_MARKET_CANCEL_ORDER, "marketCancelOrder",
		"gamepb.MarketCancelOrderRequest", "gamepb.MarketOrderResultResponse")
	mmokit.Operation(proto, gamepb.OperationCode_OP_MARKET_MY_ORDERS, "marketMyOrders",
		"gamepb.MarketMyOrdersRequest", "gamepb.MarketMyOrdersResponse")
	mmokit.Operation(proto, gamepb.OperationCode_OP_MARKET_INSTANT_TRADE, "marketInstantTrade",
		"gamepb.MarketInstantTradeRequest", "gamepb.MarketOrderResultResponse")

	// --- Entity replication schema — use a throwaway world ---
	w := ecs.NewWorld()
	c := game.NewComponents(w)
	defs := game.BuildEntityKindDefs(c)
	for _, def := range defs {
		proto.EntityName(def.Kind, def.Name)
	}
	proto.SetReplicators(mmokit.BuildReplicators(w, nil, defs...))

	_ = proto.WriteSchema(os.Stdout)
}
