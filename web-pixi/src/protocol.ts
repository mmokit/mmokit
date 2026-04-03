import { create, toBinary, fromBinary } from "@bufbuild/protobuf";
import {
  ClientEventSchema,
  ServerEventSchema,
  OperationResponseSchema,
  RespawnRequestMsgSchema,
  LoginMsgSchema,
  ChatMsgSchema,
  PingMsgSchema,
  ClientEventCode,
} from "@gen/engine_pb.js";
import type { ServerEvent, OperationResponse } from "@gen/engine_pb.js";
import {
  PlayerInputMsgSchema,
  InventoryTransferMsgSchema,
  BankRequestMsgSchema,
  SellBankItemMsgSchema,
  EquipRequestMsgSchema,
  ShopBuyMsgSchema,
  DockRequestMsgSchema,
  UndockRequestMsgSchema,
  LootItemMsgSchema,
  LootAllMsgSchema,
  MarketBrowseRequestSchema,
  MarketCreateOrderRequestSchema,
  MarketCancelOrderRequestSchema,
  MarketMyOrdersRequestSchema,
  MarketInstantTradeRequestSchema,
  GameClientEventCode,
  OperationCode,
} from "@gen/game_pb.js";

// === Client Event Encoders (channel 0x00) ===

function makeEventPayload(code: number, innerData: Uint8Array): { code: number; data: Uint8Array } {
  return { code, data: innerData };
}

export function encodePlayerInput(opts: {
  sequence: number;
  jettison: number;
  moveX: number;
  moveY: number;
  moveActive: boolean;
  abilityCast: number;
  lockTargetId: number;
  dirX: number;
  dirY: number;
  dirActive: boolean;
}): { code: number; data: Uint8Array } {
  const input = create(PlayerInputMsgSchema, {
    sequence: opts.sequence,
    jettison: opts.jettison,
    moveX: opts.moveX,
    moveY: opts.moveY,
    moveActive: opts.moveActive,
    abilityCast: opts.abilityCast,
    lockTargetId: opts.lockTargetId,
    dirX: opts.dirX,
    dirY: opts.dirY,
    dirActive: opts.dirActive,
  });
  return makeEventPayload(ClientEventCode.CE_PLAYER_INPUT, toBinary(PlayerInputMsgSchema, input));
}

export function encodeRespawnRequest(): { code: number; data: Uint8Array } {
  const respawn = create(RespawnRequestMsgSchema, {});
  return makeEventPayload(ClientEventCode.CE_RESPAWN, toBinary(RespawnRequestMsgSchema, respawn));
}

export function encodeLogin(username: string): { code: number; data: Uint8Array } {
  const login = create(LoginMsgSchema, { username });
  return makeEventPayload(ClientEventCode.CE_LOGIN, toBinary(LoginMsgSchema, login));
}

export function encodeChatMessage(text: string): { code: number; data: Uint8Array } {
  const chat = create(ChatMsgSchema, { text });
  return makeEventPayload(ClientEventCode.CE_CHAT, toBinary(ChatMsgSchema, chat));
}

export function encodeTransferRequest(itemId: number, quantity: number, deposit: boolean): { code: number; data: Uint8Array } {
  const transfer = create(InventoryTransferMsgSchema, { itemId, quantity, deposit });
  return makeEventPayload(GameClientEventCode.GCE_INVENTORY_TRANSFER, toBinary(InventoryTransferMsgSchema, transfer));
}

export function encodeBankRequest(): { code: number; data: Uint8Array } {
  const bankReq = create(BankRequestMsgSchema, {});
  return makeEventPayload(GameClientEventCode.GCE_BANK_REQUEST, toBinary(BankRequestMsgSchema, bankReq));
}

export function encodeSellBankItem(itemId: number, quantity: number): { code: number; data: Uint8Array } {
  const sell = create(SellBankItemMsgSchema, { itemId, quantity });
  return makeEventPayload(GameClientEventCode.GCE_SELL_BANK_ITEM, toBinary(SellBankItemMsgSchema, sell));
}

export function encodeEquipRequest(itemId: number, slot: number): { code: number; data: Uint8Array } {
  const req = create(EquipRequestMsgSchema, { itemId, slot });
  return makeEventPayload(GameClientEventCode.GCE_EQUIP, toBinary(EquipRequestMsgSchema, req));
}

export function encodeShopBuy(itemId: number, quantity: number): { code: number; data: Uint8Array } {
  const buy = create(ShopBuyMsgSchema, { itemId, quantity });
  return makeEventPayload(GameClientEventCode.GCE_SHOP_BUY, toBinary(ShopBuyMsgSchema, buy));
}

export function encodeDockRequest(): { code: number; data: Uint8Array } {
  const req = create(DockRequestMsgSchema, {});
  return makeEventPayload(GameClientEventCode.GCE_DOCK, toBinary(DockRequestMsgSchema, req));
}

export function encodeUndockRequest(): { code: number; data: Uint8Array } {
  const req = create(UndockRequestMsgSchema, {});
  return makeEventPayload(GameClientEventCode.GCE_UNDOCK, toBinary(UndockRequestMsgSchema, req));
}

export function encodeLootItem(crateNetId: number, itemId: number): { code: number; data: Uint8Array } {
  const loot = create(LootItemMsgSchema, { crateNetId, itemId });
  return makeEventPayload(GameClientEventCode.GCE_LOOT_ITEM, toBinary(LootItemMsgSchema, loot));
}

export function encodeLootAll(crateNetId: number): { code: number; data: Uint8Array } {
  const loot = create(LootAllMsgSchema, { crateNetId });
  return makeEventPayload(GameClientEventCode.GCE_LOOT_ALL, toBinary(LootAllMsgSchema, loot));
}

export function encodePing(): { code: number; data: Uint8Array } {
  const ping = create(PingMsgSchema, { clientTime: BigInt(Date.now()) });
  return makeEventPayload(ClientEventCode.CE_PING, toBinary(PingMsgSchema, ping));
}

// === Operation Request Encoders (channel 0x01) ===

export function encodeMarketBrowse(itemId: number): { code: number; data: Uint8Array } {
  const req = create(MarketBrowseRequestSchema, { itemId });
  return { code: OperationCode.OP_MARKET_BROWSE, data: toBinary(MarketBrowseRequestSchema, req) };
}

export function encodeMarketCreateOrder(itemId: number, isBuy: boolean, pricePerUnit: number, quantity: number): { code: number; data: Uint8Array } {
  const req = create(MarketCreateOrderRequestSchema, { itemId, isBuy, pricePerUnit: BigInt(pricePerUnit), quantity });
  return { code: OperationCode.OP_MARKET_CREATE_ORDER, data: toBinary(MarketCreateOrderRequestSchema, req) };
}

export function encodeMarketCancelOrder(orderId: bigint): { code: number; data: Uint8Array } {
  const req = create(MarketCancelOrderRequestSchema, { orderId });
  return { code: OperationCode.OP_MARKET_CANCEL_ORDER, data: toBinary(MarketCancelOrderRequestSchema, req) };
}

export function encodeMarketMyOrders(): { code: number; data: Uint8Array } {
  const req = create(MarketMyOrdersRequestSchema, {});
  return { code: OperationCode.OP_MARKET_MY_ORDERS, data: toBinary(MarketMyOrdersRequestSchema, req) };
}

export function encodeMarketInstantTrade(itemId: number, isBuy: boolean, quantity: number): { code: number; data: Uint8Array } {
  const req = create(MarketInstantTradeRequestSchema, { itemId, isBuy, quantity });
  return { code: OperationCode.OP_MARKET_INSTANT_TRADE, data: toBinary(MarketInstantTradeRequestSchema, req) };
}

// === Server Event Decoder ===

export function decodeServerEvent(data: Uint8Array): ServerEvent {
  return fromBinary(ServerEventSchema, data);
}

// === Operation Response Decoder ===

export function decodeOperationResponse(data: Uint8Array): OperationResponse {
  return fromBinary(OperationResponseSchema, data);
}
