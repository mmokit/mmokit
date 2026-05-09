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
