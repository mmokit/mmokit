package admin

import (
	"strconv"
	"strings"
	"time"

	"github.com/zenion/mmoserver/pkg/metrics"
	"github.com/zenion/mmoserver/pkg/universe"
)

// LocalClusterView reads cluster state directly from a *universe.Process.
// Constructed once at admin server startup; methods are safe to call
// concurrently — they delegate to thread-safe Process accessors.
//
// Some fields (per-host load detail, gateway byte rates, per-cell neighbor
// lists, perf snapshots) are best-effort in v1 and return zero values when
// the underlying universe accessor doesn't yet exist. As pkg/universe
// exposes more state, these gaps close without changing the ClusterView
// interface.
type LocalClusterView struct {
	p *universe.Process
}

// NewLocalClusterView wraps a *universe.Process for the admin HTTP layer.
func NewLocalClusterView(p *universe.Process) *LocalClusterView {
	return &LocalClusterView{p: p}
}

func (v *LocalClusterView) Cluster() ClusterInfo {
	cells := v.Cells()
	hosts := v.Hosts()
	gws := v.Gateways()
	totalEntities := 0
	for _, c := range cells {
		totalEntities += c.Entities.Real
	}
	return ClusterInfo{
		Now:           time.Now(),
		HostCount:     len(hosts),
		GatewayCount:  len(gws),
		CellCount:     len(cells),
		SessionCount:  0, // populated when GatewayList exposes per-gateway sessions
		TotalEntities: totalEntities,
		RecentEvents:  v.CommitLog(CommitQuery{N: 20}),
	}
}

func (v *LocalClusterView) Cells() []CellInfo {
	snaps := v.p.MetricsSnapshots()
	out := make([]CellInfo, 0, len(snaps))
	for id, snap := range snaps {
		out = append(out, cellInfoFromSnapshot(id, snap, v.p))
	}
	return out
}

func (v *LocalClusterView) Cell(id string) (CellInfo, error) {
	snap, ok := v.p.MetricsSnapshot(id)
	if !ok {
		return CellInfo{}, ErrCellNotFound
	}
	return cellInfoFromSnapshot(id, snap, v.p), nil
}

// cellInfoFromSnapshot maps a metrics.LoadSnapshot + cell ID into the
// admin DTO. Depth/Parent are derived from the colon-segmented split-cell
// ID convention ("0_0" depth 0, "0_0:1" depth 1, ...). Neighbors is
// populated when a NeighborsOf accessor lands on universe.Process.
func cellInfoFromSnapshot(id string, snap metrics.LoadSnapshot, p *universe.Process) CellInfo {
	depth := strings.Count(id, ":")
	parent := ""
	if i := strings.LastIndex(id, ":"); i >= 0 {
		parent = id[:i]
	}
	return CellInfo{
		ID:        id,
		Depth:     depth,
		Parent:    parent,
		HostID:    p.HostForCellID(universe.MeshCellID(id)),
		Load:      snap.CompositeLoad,
		TickP99Us: snap.Tick.P99Duration.Microseconds(),
		TickP95Us: snap.Tick.P95Duration.Microseconds(),
		Entities: EntityCounts{
			Real:      snap.Entities.Real,
			Replica:   snap.Entities.Replica,
			Ghost:     snap.Entities.Ghost,
			Connected: snap.Entities.Connected,
		},
		BytesPS:   BytesPerSec{Sent: snap.Network.BytesSent, Recv: snap.Network.BytesRecv},
		Neighbors: nil, // populated when NeighborsOf accessor lands
	}
}

func (v *LocalClusterView) Hosts() []HostInfo {
	ids := v.p.LiveHostIDs()
	out := make([]HostInfo, 0, len(ids))
	for _, id := range ids {
		out = append(out, HostInfo{
			ID:    id,
			State: "live",
			// Roles, IsLocal, HeartbeatAgeMS, Cells, Load, TotalEntities:
			// populated when richer host accessors land on Process.
		})
	}
	return out
}

func (v *LocalClusterView) Gateways() []GatewayInfo {
	ids := v.p.LiveGatewayIDs()
	out := make([]GatewayInfo, 0, len(ids))
	for _, id := range ids {
		out = append(out, GatewayInfo{ID: id})
	}
	return out
}

func (v *LocalClusterView) Players(filter PlayerFilter) []PlayerInfo {
	limit := filter.Limit
	if limit <= 0 {
		limit = 100
	}
	online := v.p.ActivePlayerSnapshots()
	out := make([]PlayerInfo, 0, len(online))
	for _, u := range online {
		if filter.Search != "" && !strings.Contains(u.Username, filter.Search) {
			continue
		}
		if filter.Status == "offline" {
			continue
		}
		out = append(out, PlayerInfo{
			Username:  u.Username,
			Status:    "online",
			HostID:    u.HostID,
			CellID:    u.CellID,
			WorldX:    u.WorldX,
			WorldY:    u.WorldY,
			LastLogin: u.LastLogin,
		})
	}
	// Offline lookups go through the game's PlayerDataLocator when one is
	// installed (universe.Process.SetPlayerDataLocator). The v1 dashboard
	// fetches offline players via the player.list cmdsys verb when the
	// operator opts in (?status=all); the offline branch here is a no-op
	// until a search/list API is exposed on PlayerDataLocator.
	if filter.Offset >= len(out) {
		return nil
	}
	end := filter.Offset + limit
	if end > len(out) {
		end = len(out)
	}
	return out[filter.Offset:end]
}

func (v *LocalClusterView) Player(username string) (PlayerInfo, error) {
	username = strings.ToLower(username)
	for _, u := range v.p.ActivePlayerSnapshots() {
		if u.Username == username {
			return PlayerInfo{
				Username:  u.Username,
				Status:    "online",
				HostID:    u.HostID,
				CellID:    u.CellID,
				WorldX:    u.WorldX,
				WorldY:    u.WorldY,
				LastLogin: u.LastLogin,
			}, nil
		}
	}
	// Offline branch deferred — see Players() comment.
	return PlayerInfo{}, ErrPlayerNotFound
}

func (v *LocalClusterView) CommitLog(q CommitQuery) []CommitEvent {
	cl := v.p.CommitLog()
	if cl == nil {
		return nil
	}
	var raws []universe.CommitEvent
	switch {
	case q.Commit != "":
		id, err := strconv.ParseUint(q.Commit, 10, 64)
		if err != nil {
			return nil
		}
		raws = cl.ByCommitID(id)
	case q.Cell != "":
		raws = cl.ByCell(q.Cell)
	case q.Since > 0:
		raws = cl.Since(time.Now().Add(-q.Since))
	default:
		n := q.N
		if n <= 0 {
			n = 200
		}
		raws = cl.Recent(n)
	}
	out := make([]CommitEvent, 0, len(raws))
	for _, r := range raws {
		out = append(out, mapCommitEvent(r))
	}
	return out
}

// mapCommitEvent maps a universe.CommitEvent to the admin DTO.
//
//   - CommitID is encoded as a decimal string for JS integer-precision safety
//     (uint64 exceeds Number.MAX_SAFE_INTEGER).
//   - Scenario uses universe.CommitKind.String() ("split"|"merge"|"migrate").
//   - Kind uses universe.EventKind.String() ("commit-step"|"invariant"|"host"|...).
func mapCommitEvent(r universe.CommitEvent) CommitEvent {
	return CommitEvent{
		CommitID:   strconv.FormatUint(r.CommitID, 10),
		Scenario:   r.Scenario.String(),
		Step:       r.Step,
		StepIndex:  r.StepIndex,
		Success:    r.Success,
		DurationMs: r.DurationMs,
		Affected:   append([]string(nil), r.Affected...),
		HostIDs:    append([]string(nil), r.HostIDs...),
		Error:      r.Error,
		SeqNo:      r.SeqNo,
		Kind:       r.Kind.String(),
		Timestamp:  r.Timestamp,
	}
}

// Perf returns per-cell tick profiling data. v1: per-cell tick stats
// accessor doesn't yet exist on Process. Returns ErrUnavailable so the
// dashboard's perf panel can render an "unavailable" state without
// crashing. Wire up when TickStatsForCell lands in pkg/universe.
func (v *LocalClusterView) Perf(cellID string) (PerfSnapshot, error) {
	_ = cellID
	return PerfSnapshot{}, ErrUnavailable
}
