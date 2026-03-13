import { create, toBinary } from "@bufbuild/protobuf";
import {
  ClientEventSchema,
  OperationRequestSchema,
} from "@gen/game_pb.js";

const CHANNEL_EVENT = 0x00;
const CHANNEL_OPERATION = 0x01;

export class WSTransport {
  private ws: WebSocket;
  private _onEvent: ((data: Uint8Array) => void) | null = null;
  private _onOperation: ((data: Uint8Array) => void) | null = null;
  private _onOpen: (() => void) | null = null;
  private _onClose: (() => void) | null = null;

  constructor(url: string) {
    this.ws = new WebSocket(url);
    this.ws.binaryType = "arraybuffer";

    this.ws.addEventListener("message", (event) => {
      const raw = new Uint8Array(event.data as ArrayBuffer);
      if (raw.length < 2) return;
      const channel = raw[0];
      const payload = raw.slice(1);
      if (channel === CHANNEL_OPERATION) {
        if (this._onOperation) this._onOperation(payload);
      } else {
        if (this._onEvent) this._onEvent(payload);
      }
    });

    this.ws.addEventListener("open", () => {
      if (this._onOpen) this._onOpen();
    });

    this.ws.addEventListener("close", () => {
      if (this._onClose) this._onClose();
    });
  }

  /** Send a game event (channel 0x00). */
  sendEvent(code: number, innerData: Uint8Array): void {
    if (this.ws.readyState !== WebSocket.OPEN) return;
    const evt = create(ClientEventSchema, { code, data: innerData });
    const evtBytes = toBinary(ClientEventSchema, evt);
    const frame = new Uint8Array(1 + evtBytes.length);
    frame[0] = CHANNEL_EVENT;
    frame.set(evtBytes, 1);
    this.ws.send(frame);
  }

  /** Send an operation request (channel 0x01). */
  sendOperation(code: number, requestId: number, innerData: Uint8Array): void {
    if (this.ws.readyState !== WebSocket.OPEN) return;
    const req = create(OperationRequestSchema, { code, requestId, data: innerData });
    const reqBytes = toBinary(OperationRequestSchema, req);
    const frame = new Uint8Array(1 + reqBytes.length);
    frame[0] = CHANNEL_OPERATION;
    frame.set(reqBytes, 1);
    this.ws.send(frame);
  }

  /** Legacy: send raw reliable data (used during login before channel byte). */
  sendReliable(data: Uint8Array): void {
    if (this.ws.readyState === WebSocket.OPEN) {
      this.ws.send(data);
    }
  }

  sendUnreliable(data: Uint8Array): void {
    if (this.ws.readyState === WebSocket.OPEN) {
      this.ws.send(data);
    }
  }

  onEvent(callback: (data: Uint8Array) => void): void {
    this._onEvent = callback;
  }

  onOperation(callback: (data: Uint8Array) => void): void {
    this._onOperation = callback;
  }

  /** @deprecated Use onEvent/onOperation instead */
  onMessage(callback: (data: Uint8Array) => void): void {
    this._onEvent = callback;
  }

  onOpen(callback: () => void): void {
    this._onOpen = callback;
    if (this.ws.readyState === WebSocket.OPEN) {
      callback();
    }
  }

  onClose(callback: () => void): void {
    this._onClose = callback;
  }

  close(): void {
    this.ws.close();
  }
}
