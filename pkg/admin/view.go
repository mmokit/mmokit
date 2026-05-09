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
	Now           time.Time
	HostCount     int
	GatewayCount  int
	CellCount     int
	SessionCount  int
	TotalEntities int
	RecentEvents  []CommitEvent // last ~20 events for the dashboard's at-a-glance view
}

// CellInfo describes a single cell at any quadtree depth.
type CellInfo struct {
	ID         string   // base or split ID, e.g. "0_0" or "0_0:1"
	Depth      int
	Parent     string   // empty when Depth==0
	HostID     string
	Load       float64  // 0..>1 (CompositeLoad)
	TickP99Us  int64
	TickP95Us  int64
	Entities   EntityCounts
	BytesPS    BytesPerSec
	Neighbors  []string
}

type EntityCounts struct {
	Real      int
	Replica   int
	Ghost     int
	Connected int
}

type BytesPerSec struct {
	Sent uint64
	Recv uint64
}

// HostInfo describes a host process in the cluster.
type HostInfo struct {
	ID             string
	Roles          []string // ["coordinator","host"], etc.
	State          string   // "live"|"draining"|"dead"
	IsLocal        bool
	HeartbeatAgeMS int64
	Cells          []string
	Load           float64 // composite over owned cells
	TotalEntities  int
}

// GatewayInfo describes a gateway process.
type GatewayInfo struct {
	ID           string
	Sessions     int
	BytesSentPS  uint64
	BytesRecvPS  uint64
	Mode         string // "local-shortcut"|"always-proxy"
}

// PlayerFilter is the query for Players().
type PlayerFilter struct {
	Status string // ""|"online"|"offline"
	Search string // username substring
	Limit  int    // default 100
	Offset int
}

// PlayerInfo describes a single player.
type PlayerInfo struct {
	Username  string
	Status    string  // "online"|"offline"
	HostID    string  // online only
	CellID    string  // online only
	WorldX    float32 // online only
	WorldY    float32 // online only
	LastLogin time.Time
}

// CommitQuery is the parameter for CommitLog().
type CommitQuery struct {
	N       int           // limit, 0 = default 200
	Since   time.Duration // 0 = no time filter
	Cell    string
	Commit  string
}

// CommitEvent mirrors universe.CommitEvent for the wire layer. The remote
// view impl produces these from the gRPC response without depending on
// the universe package's struct layout.
type CommitEvent struct {
	CommitID    string
	Scenario    string
	Step        string
	StepIndex   int
	Success     bool
	DurationMs  int64
	Affected    []string
	HostIDs     []string
	Error       string
	Timestamp   time.Time
}

// PerfSnapshot is per-cell tick profiling data.
type PerfSnapshot struct {
	CellID      string
	SystemNames []string
	Systems     []TimingStats
	Total       TimingStats
	SampleCount int
}

type TimingStats struct {
	LatestUs int64
	AvgUs    int64
	P50Us    int64
	P95Us    int64
	P99Us    int64
	MaxUs    int64
}
