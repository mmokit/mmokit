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
func dumpProtocolSchema() {
	proto := mmokit.NewProtocol("space")

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

	// --- Engine server → client events ---
	mmokit.ServerEvent(proto, enginepb.ServerEventCode_SE_PLAYER_SPAWNED, "playerSpawned", "gamepb.PlayerSpawnedMsg")
	mmokit.ServerEvent(proto, enginepb.ServerEventCode_SE_WORLD_UPDATE, "worldUpdate", "gamepb.WorldUpdateMsg")
	mmokit.ServerEvent(proto, enginepb.ServerEventCode_SE_PONG, "pong", "enginepb.PongMsg")
	mmokit.ServerEvent(proto, enginepb.ServerEventCode_SE_PLAYER_DIED, "playerDied", "enginepb.PlayerDiedMsg")
	mmokit.ServerEvent(proto, enginepb.ServerEventCode_SE_LOGIN_REJECTED, "loginRejected", "enginepb.LoginRejectedMsg")
	mmokit.ServerEvent(proto, enginepb.ServerEventCode_SE_PLAYER_OWN_STATE, "playerOwnState", "gamepb.PlayerOwnStateMsg")
	mmokit.ServerEvent(proto, enginepb.ServerEventCode_SE_CELL_CHANGE, "cellChange", "enginepb.CellChangeMsg")
	mmokit.ServerEvent(proto, enginepb.ServerEventCode_SE_DELTA_WORLD_UPDATE, "deltaWorldUpdate", "")
	mmokit.ServerEvent(proto, enginepb.ServerEventCode_SE_CELL_TOPOLOGY, "cellTopology", "enginepb.CellTopologyMsg")

	// --- Game server → client events ---
	mmokit.ServerEvent(proto, gamepb.GameServerEventCode_GSE_BANK_CONTENTS, "bankContents", "gamepb.BankContentsMsg")
	mmokit.ServerEvent(proto, gamepb.GameServerEventCode_GSE_TRANSFER_RESULT, "transferResult", "gamepb.TransferResultMsg")
	mmokit.ServerEvent(proto, gamepb.GameServerEventCode_GSE_EQUIP_RESULT, "equipResult", "gamepb.EquipResultMsg")
	mmokit.ServerEvent(proto, gamepb.GameServerEventCode_GSE_DOCKING_STATE, "dockingState", "gamepb.DockingStateMsg")
	mmokit.ServerEvent(proto, gamepb.GameServerEventCode_GSE_DOCKED, "docked", "gamepb.DockedMsg")
	mmokit.ServerEvent(proto, gamepb.GameServerEventCode_GSE_MAP_DATA, "mapData", "gamepb.MapDataMsg")
	mmokit.ServerEvent(proto, gamepb.GameServerEventCode_GSE_CURRENCY_UPDATE, "currencyUpdate", "gamepb.CurrencyUpdateMsg")

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
