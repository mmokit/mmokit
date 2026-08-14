package universe

import (
	"context"
	"errors"
	"fmt"
	"time"

	meshpb "github.com/zenion/mmokit/gen/go/meshpb"
	"github.com/zenion/mmokit/pkg/cmdsys"
	"github.com/zenion/mmokit/pkg/metrics"
)

// remoteMetricsEntry caches a per-cell LoadSnapshot reported via Heartbeat,
// tagged with the source host so stale entries can be evicted when a host
// disappears.
type remoteMetricsEntry struct {
	hostID string
	snap   metrics.LoadSnapshot
}

// applyRemoteCellMetrics records per-cell metrics reported by a remote host
// in its Heartbeat. The coordinator caches these so the admin dashboard can
// surface real entity counts / load / tick stats from cells that live on
// remote hosts.
//
// Each heartbeat is treated as the authoritative snapshot for the reporting
// host: any cells previously cached under hostID that are absent from the
// new report list are evicted. Without this, post-merge donor cells (or
// post-migrate cells the host no longer owns) linger in the cache and the
// dashboard keeps showing them.
func (c *Process) applyRemoteCellMetrics(hostID string, reports []*meshpb.CellMetricsReport) {
	if hostID == "" {
		return
	}
	c.remoteCellMetricsMu.Lock()
	defer c.remoteCellMetricsMu.Unlock()
	if c.remoteCellMetrics == nil {
		c.remoteCellMetrics = make(map[MeshCellID]remoteMetricsEntry, len(reports))
	}
	// Build the set of cells the host now claims to own, then evict any
	// stale entries previously attributed to this host.
	current := make(map[MeshCellID]struct{}, len(reports))
	for _, r := range reports {
		if r == nil || r.CellId == "" {
			continue
		}
		current[MeshCellID(r.CellId)] = struct{}{}
	}
	for id, e := range c.remoteCellMetrics {
		if e.hostID != hostID {
			continue
		}
		if _, kept := current[id]; !kept {
			delete(c.remoteCellMetrics, id)
		}
	}
	for _, r := range reports {
		if r == nil || r.CellId == "" {
			continue
		}
		c.remoteCellMetrics[MeshCellID(r.CellId)] = remoteMetricsEntry{
			hostID: hostID,
			snap: metrics.LoadSnapshot{
				CellID:        r.CellId,
				CompositeLoad: r.CompositeLoad,
				Tick: metrics.TickHealthSnapshot{
					P99Duration: time.Duration(r.TickP99Us) * time.Microsecond,
					P95Duration: time.Duration(r.TickP95Us) * time.Microsecond,
				},
				Entities: metrics.EntitySnapshot{
					Real:      int(r.EntitiesReal),
					Replica:   int(r.EntitiesReplica),
					Ghost:     int(r.EntitiesGhost),
					Connected: int(r.EntitiesConnected),
				},
				Network: metrics.NetworkSnapshot{
					BytesSent: r.BytesSent,
					BytesRecv: r.BytesRecv,
				},
				Timestamp: time.Now(),
			},
		}
	}
}

// dropRemoteCellMetricsForHost evicts cached metrics for cells last reported
// by hostID. Called when the host's heartbeat times out so the dashboard
// doesn't show stale data after a host crash.
func (c *Process) dropRemoteCellMetricsForHost(hostID string) {
	if hostID == "" {
		return
	}
	c.remoteCellMetricsMu.Lock()
	defer c.remoteCellMetricsMu.Unlock()
	for id, e := range c.remoteCellMetrics {
		if e.hostID == hostID {
			delete(c.remoteCellMetrics, id)
		}
	}
}

// remoteCellMetricsSnapshot returns a point-in-time copy of the cached
// remote-cell metrics map. Used by allCellLoads to merge remote metrics
// into the dashboard view.
func (c *Process) remoteCellMetricsSnapshot() map[string]metrics.LoadSnapshot {
	c.remoteCellMetricsMu.RLock()
	defer c.remoteCellMetricsMu.RUnlock()
	if len(c.remoteCellMetrics) == 0 {
		return nil
	}
	out := make(map[string]metrics.LoadSnapshot, len(c.remoteCellMetrics))
	for id, e := range c.remoteCellMetrics {
		out[string(id)] = e.snap
	}
	return out
}

// MetricsSnapshots returns a point-in-time copy of the per-cell load snapshot
// map. Used by pkg/admin's LocalClusterView for the dashboard. Read-only —
// callers should not mutate the LoadSnapshot values.
//
// Internally delegates to allCellLoads, which acquires c.mu.RLock for the
// duration of the snapshot copy.
func (c *Process) MetricsSnapshots() map[string]metrics.LoadSnapshot {
	return c.allCellLoads()
}

// MetricsSnapshot returns the load snapshot for a single cell by ID.
// Returns ok=false when the cell is unknown.
func (c *Process) MetricsSnapshot(cellID string) (metrics.LoadSnapshot, bool) {
	return c.cellLoad(MeshCellID(cellID))
}

// PlayerSnapshot describes one online player's location at a moment in time.
// Populated from c.players under the read lock.
//
// Fields populated in v1: Username, HostID, CellID. WorldX/WorldY/LastLogin
// are zero unless the underlying PlayerLocation tracks them — they're left
// in the struct so the wire shape is stable as those fields land. Per-player
// detail (last login, exact world position) lands via a separate
// `player.info`-backed lookup when the dashboard adds detail routes.
type PlayerSnapshot struct {
	Username  string
	HostID    string
	CellID    string
	WorldX    float32
	WorldY    float32
	LastLogin time.Time
}

// ActivePlayerSnapshots returns a snapshot of every active player's
// location. Used by pkg/admin's LocalClusterView.Players — the universe's
// own data plane uses ActiveUsers / ActiveUserHost / activeUserLocked,
// which are leaner.
//
// Inactive (disconnected, in grace period) entries are skipped; only
// loc.Active==true is returned.
func (c *Process) ActivePlayerSnapshots() []PlayerSnapshot {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]PlayerSnapshot, 0, len(c.players))
	for username, loc := range c.players {
		if loc == nil || !loc.Active {
			continue
		}
		out = append(out, PlayerSnapshot{
			Username: username,
			HostID:   loc.HostID,
			CellID:   string(loc.CellID),
			// WorldX, WorldY, LastLogin: PlayerLocation does not carry
			// these today (see coordinator.go:391). Populate when richer
			// fields land on the struct.
		})
	}
	return out
}

// HostListEntry is a richer-than-LiveHostIDs view of a single host. Includes
// what the dashboard's Hosts roster needs: roles inferred from the registry,
// cells the host owns, heartbeat age, total entity count summed from those
// cells, and per-host load (max of the cells' CompositeLoad).
type HostListEntry struct {
	ID            string
	Roles         []string
	State         string // "Live" | "Registered" | "Dead" | "Leaving" | "Unknown"
	IsLocal       bool
	HeartbeatAge  time.Duration
	OwnedCells    []string
	Load          float64 // max of CompositeLoad across owned cells
	TotalEntities int     // sum of Entities.Real across owned cells
}

// HostListEntries returns one entry per host known to this Process. Combines
// the registry view (live/heartbeat/state for remote hosts) with the local
// Hosts map (the all-in-one preset's single in-process host) and the metrics
// map (load + entity count).
//
// Used by pkg/admin's LocalClusterView.Hosts(). Read-locked snapshot — safe
// to call concurrently with cluster work.
func (c *Process) HostListEntries() []HostListEntry {
	c.mu.RLock()
	defer c.mu.RUnlock()

	// 1. Build a cellsPerHost map from the control plane (union of
	//    hostRegistry ownership + cellToHostMap entries).
	cellsPerHost := make(map[string][]string)
	if c.Control != nil {
		c.Control.AllOwnedCells(func(cellID, hostID string) bool {
			cellsPerHost[hostID] = append(cellsPerHost[hostID], cellID)
			return true
		})
	}
	// Fallback for processes where Control hasn't been populated with
	// any ownership yet (or has none): assign every local Cell to the
	// single local host. Only applied when there is exactly one local
	// host so there's no ambiguity.
	if len(cellsPerHost) == 0 && len(c.Hosts) == 1 {
		var localHostID string
		for id := range c.Hosts {
			localHostID = id
		}
		ids := make([]string, 0, len(c.Cells))
		for id := range c.Cells {
			ids = append(ids, string(id))
		}
		cellsPerHost[localHostID] = ids
	}

	metricsByCell := make(map[string]metrics.LoadSnapshot, len(c.Cells))
	for id, node := range c.Cells {
		if node.Metrics != nil {
			metricsByCell[string(id)] = node.Metrics.Snapshot()
		}
	}

	// 2. Aggregate from the registry (rich state, heartbeats) when
	//    available. RegisterLocal places local hosts in the registry too,
	//    so the all-in-one preset is covered by this branch.
	out := []HostListEntry{}
	seen := map[string]bool{}
	now := time.Now()
	if c.hostRegistry != nil {
		for _, h := range c.hostRegistry.LiveHosts() {
			cells := cellsPerHost[h.ID]
			ent := HostListEntry{
				ID:         h.ID,
				State:      h.State.String(),
				IsLocal:    h.Local,
				OwnedCells: cells,
			}
			if !h.Local {
				ent.HeartbeatAge = now.Sub(h.LastHeartbeat)
			}
			if h.ServiceOnly {
				ent.Roles = []string{"service"}
			} else {
				ent.Roles = []string{"host"}
			}
			ent.Load, ent.TotalEntities = aggregateCellMetrics(cells, metricsByCell)
			out = append(out, ent)
			seen[h.ID] = true
		}
	}

	// 3. Backfill any local-only host not present in the registry — e.g.
	//    a remote-host process that never bound a hostRegistry, or an
	//    early-Build phase before RegisterLocal fires.
	for id := range c.Hosts {
		if seen[id] {
			continue
		}
		cells := cellsPerHost[id]
		ent := HostListEntry{
			ID:         id,
			State:      "Live",
			IsLocal:    true,
			OwnedCells: cells,
			Roles:      []string{"host"},
		}
		ent.Load, ent.TotalEntities = aggregateCellMetrics(cells, metricsByCell)
		out = append(out, ent)
	}
	return out
}

// GatewayListEntry is the dashboard-facing view of one gateway.
type GatewayListEntry struct {
	ID        string
	Sessions  int    // count of active sessions routed through this gateway
	BytesSent uint64 // cumulative (zero until per-gateway byte counters are wired)
	BytesRecv uint64 // cumulative (zero until per-gateway byte counters are wired)
	Mode      string // "local-shortcut" | "always-proxy" | "" (unknown / standalone)
	IsLocal   bool   // true for the embedded gateway in the all-in-one preset
}

// GatewayListEntries returns one entry per gateway known to this Process.
// Combines the gatewayRegistry (remote standalone gateways + the embedded
// gateway after RegisterLocal) with the local embedded gateway when present
// — deduped by gateway ID.
//
// Used by pkg/admin's LocalClusterView.Gateways(). Read-locked snapshot —
// safe to call concurrently with cluster work.
func (c *Process) GatewayListEntries() []GatewayListEntry {
	c.mu.RLock()
	defer c.mu.RUnlock()

	// Sessions per gateway: walk sessionRoutes (keyed by GatewayID+ConnID).
	// sessionRoutes has its own mutex, separate from c.mu.
	sessionsByGateway := make(map[string]int)
	if c.sessionRoutes != nil {
		c.sessionRoutes.ForEach(func(r *SessionRoute) bool {
			sessionsByGateway[r.Key.GatewayID]++
			return true
		})
	}

	out := []GatewayListEntry{}
	seen := map[string]bool{}

	// Embedded gateway's mode reflects this process's GatewayMode config —
	// applies to in-process / coord+gateway combos. Standalone gateways
	// don't expose their mode via the registry (no field for it), so we
	// leave Mode empty for non-local entries.
	embeddedMode := c.cfg.GatewayMode

	if c.gatewayRegistry != nil {
		for _, g := range c.gatewayRegistry.LiveGateways() {
			ent := GatewayListEntry{
				ID:       g.ID,
				Sessions: sessionsByGateway[g.ID],
				IsLocal:  g.Local,
			}
			if g.Local {
				ent.Mode = embeddedMode
			}
			out = append(out, ent)
			seen[g.ID] = true
		}
	}

	// Backfill the embedded gateway when it isn't in the registry (e.g.
	// early Build phase before RegisterLocal fires, or a coord+gateway
	// preset that hasn't published yet). The embedded gateway uses
	// InprocGatewayID by convention.
	if c.gateway != nil && !seen[c.gateway.id] {
		out = append(out, GatewayListEntry{
			ID:       c.gateway.id,
			Sessions: sessionsByGateway[c.gateway.id],
			IsLocal:  true,
			Mode:     embeddedMode,
		})
	}

	return out
}

// PerfSnapshotForCell returns live tick profiling data for one cell. Routes
// through the existing perf.snapshot cmdsys verb (RouteAllHosts), which
// fans out to every host and returns the matching cell's snapshot —
// transparent over distributed mode.
//
// Returns (PerfCellSnapshot{}, false, nil) when the cell is not currently
// owned by any reachable host. Errors from the dispatcher (e.g. context
// cancel, no dispatcher) surface as the error return.
//
// Used by pkg/admin's LocalClusterView.Perf(). Caller must supply a context
// with a deadline; ~2s is a sensible default since the call fans out over
// MeshControl in distributed deployments.
func (c *Process) PerfSnapshotForCell(ctx context.Context, cellID string) (PerfCellSnapshot, bool, error) {
	if c.dispatcher == nil {
		return PerfCellSnapshot{}, false, errors.New("perf snapshot: no dispatcher")
	}
	caller := cmdsys.Caller{
		ID:     "admin",
		Source: cmdsys.SourceAdminHTTP,
		Grants: []cmdsys.Grant{{Pattern: "perf", Allow: true}},
	}
	res, err := c.dispatcher.Invoke(ctx, caller, "perf.snapshot", perfSnapshotArgs{CellID: cellID})
	if err != nil {
		return PerfCellSnapshot{}, false, fmt.Errorf("perf snapshot: %w", err)
	}
	for _, tr := range res.PerTarget {
		if !tr.OK {
			continue
		}
		r, ok := tr.Result.(perfSnapshotResult)
		if !ok {
			continue
		}
		for _, row := range r.Rows {
			if row.CellID == cellID {
				if row.HostID == "" {
					row.HostID = tr.TargetID
				}
				return row, true, nil
			}
		}
	}
	return PerfCellSnapshot{}, false, nil
}

func aggregateCellMetrics(cellIDs []string, metricsByCell map[string]metrics.LoadSnapshot) (load float64, totalEntities int) {
	for _, id := range cellIDs {
		s, ok := metricsByCell[id]
		if !ok {
			continue
		}
		if s.CompositeLoad > load {
			load = s.CompositeLoad
		}
		totalEntities += s.Entities.Real
	}
	return load, totalEntities
}
