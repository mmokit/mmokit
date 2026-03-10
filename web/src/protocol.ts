import { create, toBinary, fromBinary } from "@bufbuild/protobuf";
import {
  ClientMessageSchema,
  ServerMessageSchema,
  PlayerInputMsgSchema,
  RespawnRequestMsgSchema,
  LoginMsgSchema,
  ChatMsgSchema,
} from "@gen/game_pb.js";
import type { ServerMessage } from "@gen/game_pb.js";

export function encodePlayerInput(
  thrust: number,
  turn: number,
  fire: boolean,
  mine: boolean,
  sequence: number,
  targetId: number,
  jettison: number,
  sell: boolean,
): Uint8Array {
  const input = create(PlayerInputMsgSchema, {
    thrust,
    turn,
    fire,
    mine,
    sequence,
    targetId,
    jettison,
    sell,
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

export function decodeServerMessage(data: Uint8Array | ArrayBuffer): ServerMessage {
  const bytes = data instanceof Uint8Array ? data : new Uint8Array(data);
  return fromBinary(ServerMessageSchema, bytes);
}
