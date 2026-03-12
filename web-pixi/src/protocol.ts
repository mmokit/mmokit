import { create, toBinary, fromBinary } from "@bufbuild/protobuf";
import {
  ClientMessageSchema,
  ServerMessageSchema,
  PlayerInputMsgSchema,
  RespawnRequestMsgSchema,
  LoginMsgSchema,
  ChatMsgSchema,
  InventoryTransferMsgSchema,
  BankRequestMsgSchema,
  SellBankItemMsgSchema,
  EquipRequestMsgSchema,
  ShopBuyMsgSchema,
  DockRequestMsgSchema,
  UndockRequestMsgSchema,
  LootItemMsgSchema,
  LootAllMsgSchema,
  PingMsgSchema,
} from "@gen/game_pb.js";
import type { ServerMessage } from "@gen/game_pb.js";

export function encodePlayerInput(opts: {
  sequence: number;
  jettison: number;
  moveX: number;
  moveY: number;
  moveActive: boolean;
  abilityCast: number;
  lockTargetId: number;
}): Uint8Array {
  const input = create(PlayerInputMsgSchema, {
    sequence: opts.sequence,
    jettison: opts.jettison,
    moveX: opts.moveX,
    moveY: opts.moveY,
    moveActive: opts.moveActive,
    abilityCast: opts.abilityCast,
    lockTargetId: opts.lockTargetId,
  });
  const msg = create(ClientMessageSchema, {
    msg: { case: "input", value: input },
  });
  return toBinary(ClientMessageSchema, msg);
}

export function encodeRespawnRequest(): Uint8Array {
  const respawn = create(RespawnRequestMsgSchema, {});
  const msg = create(ClientMessageSchema, {
    msg: { case: "respawn", value: respawn },
  });
  return toBinary(ClientMessageSchema, msg);
}

export function encodeLogin(username: string): Uint8Array {
  const login = create(LoginMsgSchema, { username });
  const msg = create(ClientMessageSchema, {
    msg: { case: "login", value: login },
  });
  return toBinary(ClientMessageSchema, msg);
}

export function encodeChatMessage(text: string): Uint8Array {
  const chat = create(ChatMsgSchema, { text });
  const msg = create(ClientMessageSchema, {
    msg: { case: "chat", value: chat },
  });
  return toBinary(ClientMessageSchema, msg);
}

export function encodeTransferRequest(itemId: number, quantity: number, deposit: boolean): Uint8Array {
  const transfer = create(InventoryTransferMsgSchema, {
    itemId,
    quantity,
    deposit,
  });
  const msg = create(ClientMessageSchema, {
    msg: { case: "transfer", value: transfer },
  });
  return toBinary(ClientMessageSchema, msg);
}

export function encodeBankRequest(): Uint8Array {
  const bankReq = create(BankRequestMsgSchema, {});
  const msg = create(ClientMessageSchema, {
    msg: { case: "bankRequest", value: bankReq },
  });
  return toBinary(ClientMessageSchema, msg);
}

export function encodeSellBankItem(itemId: number, quantity: number): Uint8Array {
  const sell = create(SellBankItemMsgSchema, { itemId, quantity });
  const msg = create(ClientMessageSchema, {
    msg: { case: "sellBankItem", value: sell },
  });
  return toBinary(ClientMessageSchema, msg);
}

export function encodeEquipRequest(itemId: number, slot: number): Uint8Array {
  const req = create(EquipRequestMsgSchema, { itemId, slot });
  const msg = create(ClientMessageSchema, {
    msg: { case: "equipRequest", value: req },
  });
  return toBinary(ClientMessageSchema, msg);
}

export function encodeShopBuy(itemId: number, quantity: number): Uint8Array {
  const buy = create(ShopBuyMsgSchema, { itemId, quantity });
  const msg = create(ClientMessageSchema, {
    msg: { case: "shopBuy", value: buy },
  });
  return toBinary(ClientMessageSchema, msg);
}

export function encodeDockRequest(): Uint8Array {
  const req = create(DockRequestMsgSchema, {});
  const msg = create(ClientMessageSchema, {
    msg: { case: "dockRequest", value: req },
  });
  return toBinary(ClientMessageSchema, msg);
}

export function encodeUndockRequest(): Uint8Array {
  const req = create(UndockRequestMsgSchema, {});
  const msg = create(ClientMessageSchema, {
    msg: { case: "undockRequest", value: req },
  });
  return toBinary(ClientMessageSchema, msg);
}

export function encodeLootItem(crateNetId: number, itemId: number): Uint8Array {
  const loot = create(LootItemMsgSchema, { crateNetId, itemId });
  const msg = create(ClientMessageSchema, {
    msg: { case: "lootItem", value: loot },
  });
  return toBinary(ClientMessageSchema, msg);
}

export function encodeLootAll(crateNetId: number): Uint8Array {
  const loot = create(LootAllMsgSchema, { crateNetId });
  const msg = create(ClientMessageSchema, {
    msg: { case: "lootAll", value: loot },
  });
  return toBinary(ClientMessageSchema, msg);
}

export function encodePing(): Uint8Array {
  const ping = create(PingMsgSchema, { clientTime: BigInt(Date.now()) });
  const msg = create(ClientMessageSchema, {
    msg: { case: "ping", value: ping },
  });
  return toBinary(ClientMessageSchema, msg);
}

export function decodeServerMessage(data: Uint8Array | ArrayBuffer): ServerMessage {
  const bytes = data instanceof Uint8Array ? data : new Uint8Array(data);
  return fromBinary(ServerMessageSchema, bytes);
}
