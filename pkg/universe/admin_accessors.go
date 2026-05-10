package universe

import (
	"time"

	"github.com/zenion/mmoserver/pkg/metrics"
)

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
// Populated from c.players under the read lock; fields are best-effort and
// may be zero-valued when the underlying PlayerLocation doesn't carry richer
// state (today: WorldX, WorldY, LastLogin are always zero — universe tracks
// only HostID + CellID + Active per username, not in-world position or
// login history).
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
