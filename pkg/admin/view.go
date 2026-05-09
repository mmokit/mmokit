// Package admin provides the engine-shipped admin/observability dashboard
// HTTP API and Svelte SPA. The dashboard mounts on the coordinator's
// AdminListen mux when Config.Admin.Enabled is true.
package admin

import (
	"errors"
	"time"
)

// View errors. Implementations map their underlying errors to these so the
// HTTP layer can render consistent responses regardless of whether reads
// come from in-process state or a future remote MeshControl client.
var (
	ErrCellNotFound   = errors.New("admin: cell not found")
	ErrPlayerNotFound = errors.New("admin: player not found")
	ErrUnavailable    = errors.New("admin: cluster view unavailable")
)

// ClusterView is the read surface the admin HTTP layer consumes. v1 has one
// implementation (LocalClusterView) reading from *universe.Process. A future
// RemoteClusterView calls a MeshControl AdminQuery RPC. Handlers, panel
// registry, and tests never branch on the concrete type.
type ClusterView interface {
	Cluster() ClusterInfo
	Cells() []CellInfo
	Cell(id string) (CellInfo, error)
	Hosts() []HostInfo
	Gateways() []GatewayInfo
	Players(filter PlayerFilter) []PlayerInfo
	Player(username string) (PlayerInfo, error)
	CommitLog(query CommitQuery) []CommitEvent
	Perf(cellID string) (PerfSnapshot, error)
}

// ClusterInfo is the cluster-wide one-shot snapshot returned by GET /api/cluster.
type ClusterInfo struct {
	Now           time.Time     `json:"now"`
	HostCount     int           `json:"hostCount"`
	GatewayCount  int           `json:"gatewayCount"`
	CellCount     int           `json:"cellCount"`
	SessionCount  int           `json:"sessionCount"`
	TotalEntities int           `json:"totalEntities"`
	RecentEvents  []CommitEvent `json:"recentEvents"` // last ~20 events for the dashboard's at-a-glance view
}

// CellInfo describes a single cell at any quadtree depth.
type CellInfo struct {
	ID        string       `json:"id"` // base or split ID, e.g. "0_0" or "0_0:1"
	Depth     int          `json:"depth"`
	Parent    string       `json:"parent"` // empty when Depth==0
	HostID    string       `json:"hostId"`
	Load      float64      `json:"load"` // 0..>1 (CompositeLoad)
	TickP99Us int64        `json:"tickP99Us"`
	TickP95Us int64        `json:"tickP95Us"`
	Entities  EntityCounts `json:"entities"`
	// Bytes is cumulative — frontend computes rate via diff over time.
	Bytes     BytesTotal `json:"bytes"`
	Neighbors []string   `json:"neighbors"`
}

type EntityCounts struct {
	Real      int `json:"real"`
	Replica   int `json:"replica"`
	Ghost     int `json:"ghost"`
	Connected int `json:"connected"` // count of connected sessions (player WebSockets) currently in the cell
}

// BytesTotal carries cumulative bytes counters (counter, not rate). Frontend derives per-second rates from successive snapshots.
type BytesTotal struct {
	Sent uint64 `json:"sent"`
	Recv uint64 `json:"recv"`
}

// HostInfo describes a host process in the cluster.
type HostInfo struct {
	ID             string   `json:"id"`
	Roles          []string `json:"roles"` // ["coordinator","host"], etc.
	State          string   `json:"state"` // "live"|"draining"|"dead"
	IsLocal        bool     `json:"isLocal"`
	HeartbeatAgeMS int64    `json:"heartbeatAgeMs"`
	Cells          []string `json:"cells"`
	Load           float64  `json:"load"` // composite over owned cells
	TotalEntities  int      `json:"totalEntities"`
}

// GatewayInfo describes a gateway process.
// BytesSentPS / BytesRecvPS are cumulative counters today (rate computation deferred).
type GatewayInfo struct {
	ID          string `json:"id"`
	Sessions    int    `json:"sessions"`
	BytesSentPS uint64 `json:"bytesSent"`
	BytesRecvPS uint64 `json:"bytesRecv"`
	Mode        string `json:"mode"` // "local-shortcut"|"always-proxy"
}

// PlayerFilter is the query for Players().
type PlayerFilter struct {
	// Status filter: ""|"online"|"offline"|"all". v1 only resolves online
	// players via in-memory state; "offline" and "all" return online entries
	// only until persist.PlayerRepository exposes a search API.
	Status string `json:"status"`
	Search string `json:"search"` // username substring
	Limit  int    `json:"limit"`  // default 100
	Offset int    `json:"offset"`
}

// PlayerInfo describes a single player.
type PlayerInfo struct {
	Username  string    `json:"username"`
	Status    string    `json:"status"` // "online"|"offline"
	HostID    string    `json:"hostId"` // online only
	CellID    string    `json:"cellId"` // online only
	WorldX    float32   `json:"worldX"` // online only
	WorldY    float32   `json:"worldY"` // online only
	LastLogin time.Time `json:"lastLogin"`
}

// CommitQuery is the parameter for CommitLog().
type CommitQuery struct {
	N      int           `json:"n"`     // limit, 0 = default 200
	Since  time.Duration `json:"since"` // 0 = no time filter
	Cell   string        `json:"cell"`
	Commit string        `json:"commit"`
}

// CommitEvent mirrors universe.CommitEvent for the wire layer. The remote
// view impl produces these from the gRPC response without depending on
// the universe package's struct layout.
type CommitEvent struct {
	CommitID   string    `json:"commitId"` // string-encoded universe.CommitEvent.CommitID (uint64) — string for JS integer-precision safety
	Scenario   string    `json:"scenario"`
	Step       string    `json:"step"`
	StepIndex  int       `json:"stepIndex"`
	Success    bool      `json:"success"`
	DurationMs int64     `json:"durationMs"`
	Affected   []string  `json:"affected"`
	HostIDs    []string  `json:"hostIds"`
	Error      string    `json:"error"`
	SeqNo      uint64    `json:"seqNo"` // monotonic per-process commit-log seq for stable ordering
	Kind       string    `json:"kind"`  // "commit" | "invariant" | "host" | "session"
	Timestamp  time.Time `json:"timestamp"`
}

// PerfSnapshot is per-cell tick profiling data.
type PerfSnapshot struct {
	CellID      string        `json:"cellId"`
	SystemNames []string      `json:"systemNames"`
	Systems     []TimingStats `json:"systems"`
	Total       TimingStats   `json:"total"`
	SampleCount int           `json:"sampleCount"`
}

type TimingStats struct {
	LatestUs int64 `json:"latestUs"`
	AvgUs    int64 `json:"avgUs"`
	P50Us    int64 `json:"p50Us"`
	P95Us    int64 `json:"p95Us"`
	P99Us    int64 `json:"p99Us"`
	MaxUs    int64 `json:"maxUs"`
}
