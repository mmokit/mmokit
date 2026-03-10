export class WSTransport {
  private ws: WebSocket;
  private _onMessage: ((data: Uint8Array) => void) | null = null;
  private _onOpen: (() => void) | null = null;
  private _onClose: (() => void) | null = null;

  constructor(url: string) {
    this.ws = new WebSocket(url);
    this.ws.binaryType = "arraybuffer";

    this.ws.addEventListener("message", (event) => {
      if (this._onMessage) {
        this._onMessage(new Uint8Array(event.data as ArrayBuffer));
      }
    });

    this.ws.addEventListener("open", () => {
      if (this._onOpen) this._onOpen();
    });

    this.ws.addEventListener("close", () => {
      if (this._onClose) this._onClose();
    });
  }

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

  onMessage(callback: (data: Uint8Array) => void): void {
    this._onMessage = callback;
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
