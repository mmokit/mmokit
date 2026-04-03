import { create, toBinary, fromBinary } from "@bufbuild/protobuf";
import {
    ClientEventSchema,
    ServerEventSchema, type ServerEvent,
    ServerEventCode,
} from "@gen/enginepb/engine_pb.js";
import {
    SlitherInputMsgSchema, SkinSelectMsgSchema, SlitherClientEventCode,
    SlitherServerEventCode,
    SlitherSpawnedMsgSchema, type SlitherSpawnedMsg,
    SlitherLeaderboardMsgSchema, type SlitherLeaderboardMsg,
    SlitherKillFeedMsgSchema, type SlitherKillFeedMsg,
    type SlitherLeaderEntry, type SlitherKillFeedEntry,
} from "@gen/slitherpb/slither_pb.js";
import { DeltaWorldDecoder } from "./delta-decoder";
import type { SegmentPos } from "./state";

const CHANNEL = 0x00;

// SE_DELTA_WORLD_UPDATE = 13 (from engine.proto)
const SE_DELTA_WORLD_UPDATE = 13;


export interface WorldUpdateData {
  tick: number;
  snakes: {
    id: number;
    headX: number;
    headY: number;
    angle: number;
    speed: number;
    mass: number;
    skinID: number;
    length: number;
    boosting: boolean;
    name: string;
    segments: SegmentPos[];
  }[];
  foods: { id: number; x: number; y: number; value: number; color: number }[];
  removed: number[];
}

export interface LeaderboardData {
  entries: { name: string; mass: number; skinID: number }[];
}

export interface KillFeedData {
  entries: { victim: string; killer: string; mass: number }[];
}

export interface SpawnedData {
  entityID: number;
  cellX: number;
  cellY: number;
}

export interface NetworkCallbacks {
  onWorldUpdate: (data: WorldUpdateData) => void;
  onLeaderboard: (data: LeaderboardData) => void;
  onKillFeed: (data: KillFeedData) => void;
  onSpawned: (data: SpawnedData) => void;
  onOpen: () => void;
  onClose: () => void;
}

export class Network {
  private ws: WebSocket | null = null;
  private callbacks: NetworkCallbacks;
  private inputSequence: number = 0;
  private deltaDecoder = new DeltaWorldDecoder();

  constructor(callbacks: NetworkCallbacks) {
    this.callbacks = callbacks;
  }

  connect() {
    const protocol = location.protocol === "https:" ? "wss:" : "ws:";
    this.ws = new WebSocket(`${protocol}//${location.host}/ws`);
    this.ws.binaryType = "arraybuffer";

    this.ws.onopen = () => {
      this.callbacks.onOpen();
    };

    this.ws.onclose = () => {
      this.callbacks.onClose();
    };

    this.ws.onmessage = (event: MessageEvent) => {
      if (!(event.data instanceof ArrayBuffer)) return;
      const bytes = new Uint8Array(event.data);
      if (bytes.length < 2) return;

      const channel = bytes[0];
      if (channel !== CHANNEL) return;

      // Decode ServerEvent envelope
      const evt = fromBinary(ServerEventSchema, bytes.subarray(1)) as ServerEvent;

      switch (evt.code) {
        case SE_DELTA_WORLD_UPDATE: {
          // Binary delta-compressed world update.
          const data = this.deltaDecoder.decode(new Uint8Array(evt.data));
          this.callbacks.onWorldUpdate(data);
          break;
        }
        case ServerEventCode.SE_PLAYER_SPAWNED: {
          const msg = fromBinary(SlitherSpawnedMsgSchema, evt.data) as SlitherSpawnedMsg;
          const data: SpawnedData = {
            entityID: msg.entityNetId,
            cellX: msg.cellX,
            cellY: msg.cellY,
          };
          this.callbacks.onSpawned(data);
          break;
        }
        case SlitherServerEventCode.SSE_LEADERBOARD: {
          const msg = fromBinary(SlitherLeaderboardMsgSchema, evt.data) as SlitherLeaderboardMsg;
          const data: LeaderboardData = {
            entries: msg.entries.map((e: SlitherLeaderEntry) => ({
              name: e.name,
              mass: e.mass,
              skinID: e.skinId,
            })),
          };
          this.callbacks.onLeaderboard(data);
          break;
        }
        case SlitherServerEventCode.SSE_KILL_FEED: {
          const msg = fromBinary(SlitherKillFeedMsgSchema, evt.data) as SlitherKillFeedMsg;
          const data: KillFeedData = {
            entries: msg.entries.map((e: SlitherKillFeedEntry) => ({
              victim: e.victimName,
              killer: e.killerName,
              mass: e.victimMass,
            })),
          };
          this.callbacks.onKillFeed(data);
          break;
        }
      }
    };
  }

  sendInput(targetAngle: number, boost: boolean) {
    if (!this.ws || this.ws.readyState !== WebSocket.OPEN) return;

    const inner = create(SlitherInputMsgSchema, {
      targetAngle,
      boost,
      sequence: this.inputSequence++,
    });
    const evt = create(ClientEventSchema, {
      code: SlitherClientEventCode.SCE_PLAYER_INPUT,
      data: toBinary(SlitherInputMsgSchema, inner),
    });
    const payload = toBinary(ClientEventSchema, evt);

    // Prepend channel byte
    const buf = new Uint8Array(1 + payload.length);
    buf[0] = CHANNEL;
    buf.set(payload, 1);
    this.ws.send(buf);
  }

  sendSkinSelect(skinID: number, name: string) {
    if (!this.ws || this.ws.readyState !== WebSocket.OPEN) return;

    const inner = create(SkinSelectMsgSchema, { skinId: skinID, name });
    const evt = create(ClientEventSchema, {
      code: SlitherClientEventCode.SCE_SKIN_SELECT,
      data: toBinary(SkinSelectMsgSchema, inner),
    });
    const payload = toBinary(ClientEventSchema, evt);

    const buf = new Uint8Array(1 + payload.length);
    buf[0] = CHANNEL;
    buf.set(payload, 1);
    this.ws.send(buf);
  }

  sendRespawn() {
    if (!this.ws || this.ws.readyState !== WebSocket.OPEN) return;

    const evt = create(ClientEventSchema, {
      code: SlitherClientEventCode.SCE_RESPAWN,
    });
    const payload = toBinary(ClientEventSchema, evt);

    const buf = new Uint8Array(1 + payload.length);
    buf[0] = CHANNEL;
    buf.set(payload, 1);
    this.ws.send(buf);
  }

  disconnect() {
    if (this.ws) {
      this.ws.close();
      this.ws = null;
    }
  }
}
