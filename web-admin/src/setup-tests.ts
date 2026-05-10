// Vitest jsdom doesn't ship EventSource. Provide a minimal mock so SSE tests
// can exercise the demuxer without spinning up a real server.
class MockEventSource extends EventTarget {
  static readonly CONNECTING = 0;
  static readonly OPEN = 1;
  static readonly CLOSED = 2;

  readyState: number = MockEventSource.CONNECTING;
  url: string;
  onopen: ((ev: Event) => void) | null = null;
  onerror: ((ev: Event) => void) | null = null;
  onmessage: ((ev: MessageEvent) => void) | null = null;

  constructor(url: string) {
    super();
    this.url = url;
    queueMicrotask(() => {
      this.readyState = MockEventSource.OPEN;
      this.onopen?.(new Event("open"));
    });
  }

  // Test-only: fire a server-sent event of the given type with JSON payload.
  emit(type: string, data: unknown) {
    const evt = new MessageEvent(type, { data: JSON.stringify(data) });
    this.dispatchEvent(evt);
  }

  close() {
    this.readyState = MockEventSource.CLOSED;
  }
}

// Recording wrapper so tests can grab the most recent constructed instance.
class RecordingEventSource extends MockEventSource {
  constructor(url: string) {
    super(url);
    (globalThis as unknown as { __lastES?: MockEventSource }).__lastES = this;
  }
}

(globalThis as unknown as { EventSource: typeof RecordingEventSource }).EventSource =
  RecordingEventSource;

// Test helper to fetch the most recent constructed mock.
export function lastEventSource(): MockEventSource | undefined {
  return (globalThis as unknown as { __lastES?: MockEventSource }).__lastES;
}
