import type {
  CellInfo,
  HostInfo,
  GatewayInfo,
  ClusterInfo,
  CommitEvent,
  PlayerInfo,
  AuthSession,
  PanelDef,
  MetricsSample,
} from "./types";

// A reactive holder backed by Svelte 5 $state. Use directly: cellsStore.value.
//
// Pattern: each topic owns one store. The Cluster page subscribes via
// stream.subscribe(topic, (data) => cellsStore.set(data)) at mount, and
// unsubscribes via the returned destructor in onDestroy.
class Store<T> {
  #value = $state<T | null>(null);
  get value(): T | null {
    return this.#value;
  }
  set(v: T | null): void {
    this.#value = v;
  }
  clear(): void {
    this.#value = null;
  }
}

export const sessionStore = new Store<AuthSession>();
export const cellsStore = new Store<CellInfo[]>();
export const hostsStore = new Store<HostInfo[]>();
export const gatewaysStore = new Store<GatewayInfo[]>();
export const playersStore = new Store<PlayerInfo[]>();
export const eventsStore = new Store<CommitEvent[]>();
export const alertsStore = new Store<CommitEvent[]>();
export const clusterStore = new Store<ClusterInfo>();
export const panelsStore = new Store<PanelDef[]>();

// alerts: push-only ring of recent invariant violations / important commits.
export function pushAlert(e: CommitEvent): void {
  const cur = alertsStore.value ?? [];
  const next = [e, ...cur].slice(0, 50);
  alertsStore.set(next);
}

// METRICS_HISTORY_LEN is the per-cell sample budget. At ~4Hz from the cells
// SSE topic, 60 samples covers ~15 seconds — enough for a useful sparkline
// without holding huge arrays. A page reload starts fresh.
export const METRICS_HISTORY_LEN = 60;

// metricsHistory is a per-cell ring buffer of MetricsSample. Pushes wrap
// at METRICS_HISTORY_LEN. Reactive: callers can read .value and re-render
// when it changes.
class MetricsHistory {
  #map = $state<Record<string, MetricsSample[]>>({});
  get value(): Record<string, MetricsSample[]> {
    return this.#map;
  }
  push(cellId: string, sample: MetricsSample): void {
    const cur = this.#map[cellId] ?? [];
    const next = cur.length < METRICS_HISTORY_LEN
      ? [...cur, sample]
      : [...cur.slice(1), sample];
    this.#map = { ...this.#map, [cellId]: next };
  }
  clear(): void {
    this.#map = {};
  }
}

export const metricsHistoryStore = new MetricsHistory();

class PaletteOpen {
  #open = $state(false);
  get value(): boolean {
    return this.#open;
  }
  set(v: boolean): void {
    this.#open = v;
  }
  toggle(): void {
    this.#open = !this.#open;
  }
}

export const paletteOpen = new PaletteOpen();
