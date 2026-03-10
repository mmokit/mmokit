/**
 * WSTransport wraps a WebSocket connection with the same reliable/unreliable
 * API shape as a hypothetical UDP transport. Since WebSocket runs over TCP,
 * both channels are identical. This lets game code use a single interface
 * without transport-specific branches.
 *
 * Usage:
 *   import { WSTransport } from './transport.js';
 *   const transport = new WSTransport('ws://localhost:8080/ws');
 *   transport.onOpen(() => {
 *     transport.sendReliable(loginBytes);
 *   });
 *   transport.onMessage((data) => { ... });
 */
export class WSTransport {
  /**
   * @param {string} url - WebSocket URL (e.g. 'ws://localhost:8080/ws')
   */
  constructor(url) {
    this.ws = new WebSocket(url);
    this.ws.binaryType = 'arraybuffer';
    this._onMessage = null;
    this._onOpen = null;
    this._onClose = null;

    this.ws.addEventListener('message', (event) => {
      if (this._onMessage) {
        this._onMessage(new Uint8Array(event.data));
      }
    });

    this.ws.addEventListener('open', () => {
      if (this._onOpen) this._onOpen();
    });

    this.ws.addEventListener('close', () => {
      if (this._onClose) this._onClose();
    });
  }

  /** Send a message reliably. Over TCP this is the same as unreliable. */
  sendReliable(data) {
    if (this.ws.readyState === WebSocket.OPEN) {
      this.ws.send(data);
    }
  }

  /** Send a message unreliably. Over TCP this is the same as reliable. */
  sendUnreliable(data) {
    if (this.ws.readyState === WebSocket.OPEN) {
      this.ws.send(data);
    }
  }

  /** @param {function(Uint8Array)} callback */
  onMessage(callback) {
    this._onMessage = callback;
  }

  /** @param {function()} callback */
  onOpen(callback) {
    this._onOpen = callback;
    // If already open, fire immediately
    if (this.ws.readyState === WebSocket.OPEN) {
      callback();
    }
  }

  /** @param {function()} callback */
  onClose(callback) {
    this._onClose = callback;
  }

  close() {
    this.ws.close();
  }
}
