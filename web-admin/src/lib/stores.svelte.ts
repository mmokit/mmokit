import type {
  CellInfo,
  HostInfo,
  GatewayInfo,
  ClusterInfo,
  CommitEvent,
  PlayerInfo,
  AuthSession,
  PanelDef,
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
