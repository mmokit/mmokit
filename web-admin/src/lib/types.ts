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

export type AuditEntry = {
  traceId?: string;
  username: string;
  ip: string;
  verb: string;
  args?: string;
  ok: boolean;
  error?: string;
  startedAt: string;
  finishedAt: string;
};

// CommandEntry mirrors the anonymous `entry` struct in
// pkg/admin/api_commands.go::handleCommandsList — GET /admin/api/commands.
// The SPA uses the verb set as a capability probe: panels whose verbs are
// registered only by a particular game hide themselves when that game is not
// the one running.
export type CommandEntry = {
  verb: string;
  capability: string;
  description: string;
  route: string;
  hidden?: boolean;
  aliases?: string[];
};

export type LogCategory = { name: string; enabled: boolean };

export type LogGroup = { name: string; categories: LogCategory[] };

export type LogCategoriesResp = { groups: LogGroup[] };

// FieldSchema mirrors pkg/cmdsys/schema.go::FieldSchema. Returned as part
// of the GET /admin/api/commands/<verb> response's argsSchema field.
export type FieldSchema = {
  name: string;
  kind: string;          // "string", "int32", "int64", "float32", "float64", "bool", "[]<elem>", "{...}"
  required: boolean;
  named_only: boolean;
  default: string;
  enum: string[] | null;
  help?: string;
  rest?: boolean;
  complete?: string;
  // secret marks the field as a sensitive value (password, token). The
  // ArgsModal renders these as <input type="password">; the server-side
  // console TTY-prompts for them. See pkg/cmdsys/schema.go::FieldSchema.
  secret?: boolean;
};

// Schema mirrors pkg/cmdsys/schema.go::Schema.
export type Schema = {
  struct: string;
  fields: FieldSchema[];
};

// LogEntry mirrors pkg/admin/log_ring.go::LogEntry. Streamed via the
// "logs" SSE topic and hydrated from GET /admin/api/logs/recent?n=N.
export type LogEntry = {
  host: string;   // "local" for coord, host_id for remote
  cat: string;
  msg: string;
  t: string;      // ISO timestamp
};

// Entity inspect/list types mirror the Go result structs in
// pkg/universe/builtins_entity.go (entityRow / entityInspectRow /
// entityInspectResult). Those structs have no json tags, so the wire keys are
// the exported Go field names verbatim.
export type EntityListRow = {
  NetID: number;
  Kind: string;
  WorldX: number;
  WorldY: number;
  CellID: string;
  HostID: string;
};

export type EntityInspectRow = {
  Component: string;
  Field: string;
  Type: string;
  Value: string;
  Editable: boolean;
};

export type EntityInspectResult = {
  NetID: number;
  Kind: string;
  Components: EntityInspectRow[];
};

