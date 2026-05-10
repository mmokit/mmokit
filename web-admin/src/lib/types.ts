// Type definitions mirror pkg/admin/view.go DTOs (camelCase wire format).
// Hand-maintained for v1; if drift becomes a problem, codegen via cmd/sdkgen.

export type CellInfo = {
  id: string;
  depth: number;
  parent?: string;
  hostId: string;
  load: number;
  tickP99Us: number;
  tickP95Us: number;
  entities: EntityCounts;
  bytes: BytesTotal;
  neighbors: string[] | null;
};

export type EntityCounts = {
  real: number;
  replica: number;
  ghost: number;
  connected: number;
};

export type BytesTotal = {
  sent: number;
  recv: number;
};

export type HostInfo = {
  id: string;
  roles: string[] | null;
  state: string; // "live"|"registered"|"dead"|"leaving"|"unknown"
  isLocal: boolean;
  heartbeatAgeMs: number;
  cells: string[] | null;
  load: number;
  totalEntities: number;
};

export type GatewayInfo = {
  id: string;
  sessions: number;
  bytesSent: number;
  bytesRecv: number;
  mode: string;
  isLocal: boolean;
};

export type PlayerInfo = {
  username: string;
  status: "online" | "offline";
  hostId?: string;
  cellId?: string;
  worldX?: number;
  worldY?: number;
  lastLogin?: string;
};

export type CommitEvent = {
  commitId: string;
  scenario: string;
  step: string;
  stepIndex: number;
  success: boolean;
  durationMs: number;
  affected: string[] | null;
  hostIds: string[] | null;
  error?: string;
  seqNo: number;
  kind: string;
  timestamp: string;
};

export type ClusterInfo = {
  now: string;
  hostCount: number;
  gatewayCount: number;
  cellCount: number;
  sessionCount: number;
  totalEntities: number;
  recentEvents: CommitEvent[] | null;
};

export type PerfSnapshot = {
  cellId: string;
  systemNames: string[] | null;
  systems: TimingStats[] | null;
  total: TimingStats;
  sampleCount: number;
};

export type TimingStats = {
  latestUs: number;
  avgUs: number;
  p50Us: number;
  p95Us: number;
  p99Us: number;
  maxUs: number;
};

export type PanelDef = {
  id: string;
  label: string;
  icon: string;
  group: string;
  topics: string[] | null;
  commands: string[] | null;
  initialFetch?: string;
  component?: string;
  visualization?: string;
};

export type AuthSession = {
  user: string;
  grants: string[];
  expiresAt: string;
};

// MetricsSample is one tick's worth of dashboard-relevant numbers for one
// cell, captured from the `cells` SSE topic. The performance route keeps a
// rolling ring of these per cell to drive sparklines.
export type MetricsSample = {
  t: number;          // ms epoch when the sample was captured client-side
  load: number;
  tickP99Us: number;
  tickP95Us: number;
  entitiesReal: number;
  bytesSent: number;
  bytesRecv: number;
};
