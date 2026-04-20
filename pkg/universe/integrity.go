package universe

import (
	"fmt"
)

// InvariantMode controls how invariant violations are handled.
type InvariantMode uint8

const (
	// InvariantOff disables all invariant checking. Not recommended
	// outside microbenchmarks.
	InvariantOff InvariantMode = iota
	// InvariantLog records a violation via the commit log and the
	// InvariantViolations metric, then continues execution. Production
	// default — one latent inconsistency should not take down a shard.
	InvariantLog
	// InvariantPanic records the violation and then panics. Default for
	// tests and dev — fail loud at the point of violation rather than
	// chasing symptoms hours later.
	InvariantPanic
)

// Invariant is a named predicate over Process state. Check returns nil
// when the invariant holds and a descriptive error when it's been
// violated. The error's Error() value appears in the commit log and in
// any panic message, so it should identify enough of the offending state
// to be debuggable without extra logging.
type Invariant struct {
	Name  string
	Check func(c *Process) error
}

// CatInvariant is the logger category used for invariant-related output.
const CatInvariant = "integrity"

// CheckInvariants runs each invariant in order. On a violation it logs,
// records a commit-log event, bumps the metric, and — when mode is
// InvariantPanic — panics. Callers typically pass the default invariant
// set and a short context string identifying where the check fired
// (e.g. "commit 17 after apply-cell-to-host-map").
func (c *Process) CheckInvariants(invs []Invariant, contextMsg string) {
	if c.invariantMode == InvariantOff {
		return
	}
	for _, inv := range invs {
		if err := inv.Check(c); err != nil {
			msg := fmt.Sprintf("invariant %q violated during %s: %v",
				inv.Name, contextMsg, err)
			c.Log.Log(CatInvariant, "%s", msg)
			// commit log + metric hooks are wired in Phase C; leave
			// stubs here so this file is self-contained for now.
			if c.invariantMode == InvariantPanic {
				panic(msg)
			}
		}
	}
}

// invCoordMapsConsistent asserts that c.Cells and c.CellOwner are
// consistent two-way mappings: every cell present in one map must be
// resolvable in the other.
var invCoordMapsConsistent = Invariant{
	Name: "coord-maps-consistent",
	Check: func(c *Process) error {
		for key, cell := range c.Cells {
			if cell == nil {
				return fmt.Errorf("c.Cells[%q] is nil", key)
			}
			gotKey, ok := c.CellOwner[cell.Cell]
			if !ok {
				return fmt.Errorf("c.Cells[%q] references CellID %v but c.CellOwner[%v] is missing",
					key, cell.Cell, cell.Cell)
			}
			if gotKey != key {
				return fmt.Errorf("c.Cells[%q].Cell=%v but c.CellOwner[%v]=%q (mismatch)",
					key, cell.Cell, cell.Cell, gotKey)
			}
		}
		for cellID, key := range c.CellOwner {
			cell, ok := c.Cells[key]
			if !ok {
				return fmt.Errorf("c.CellOwner[%v]=%q but c.Cells[%q] is missing",
					cellID, key, key)
			}
			if cell.Cell != cellID {
				return fmt.Errorf("c.CellOwner[%v]=%q but c.Cells[%q].Cell=%v (mismatch)",
					cellID, key, key, cell.Cell)
			}
		}
		return nil
	},
}

// invHostOwnershipMatchesCoord asserts that whenever c.CellOwner says
// host H owns cell K, the corresponding local Host struct (if H is a
// process-local host) has K in its Cells map. Remote hosts are skipped
// because the coordinator doesn't hold their internal state.
var invHostOwnershipMatchesCoord = Invariant{
	Name: "host-ownership-matches-coord",
	Check: func(c *Process) error {
		c.Control.mu.RLock()
		defer c.Control.mu.RUnlock()
		for cellKey, hostID := range c.Control.cellToHostMap {
			host, isLocal := c.Hosts[hostID]
			if !isLocal {
				continue
			}
			// Cell lookup: reverse-map cellKey -> CellID via c.Cells.
			cell, ok := c.Cells[cellKey]
			if !ok {
				return fmt.Errorf("cellToHostMap[%q]=%q but c.Cells[%q] is missing",
					cellKey, hostID, cellKey)
			}
			if _, ok := host.Cells[cell.Cell]; !ok {
				return fmt.Errorf("cellToHostMap[%q]=%q but host %q has no Cells entry for %v",
					cellKey, hostID, hostID, cell.Cell)
			}
		}
		return nil
	},
}

// invTopologyNeighborsOwned asserts that every cell appearing as a
// neighbor in the Topology.Neighbors map has a valid c.CellOwner entry.
// Catches the class of bugs where topology rewiring runs before coord
// maps are updated — the merge blink we saw this session.
var invTopologyNeighborsOwned = Invariant{
	Name: "topology-neighbors-owned",
	Check: func(c *Process) error {
		c.Control.mu.RLock()
		defer c.Control.mu.RUnlock()
		for cell, neighbors := range c.Control.Topology.Neighbors {
			if _, ok := c.CellOwner[cell]; !ok {
				return fmt.Errorf("Topology.Neighbors contains cell %v but c.CellOwner[%v] is missing",
					cell, cell)
			}
			for _, n := range neighbors {
				if _, ok := c.CellOwner[n]; !ok {
					return fmt.Errorf("Topology.Neighbors[%v] contains neighbor %v but c.CellOwner[%v] is missing",
						cell, n, n)
				}
			}
		}
		return nil
	},
}

// invSessionRouteHostLive asserts every session route points at either
// an empty HostID or a host that's currently registered. Catches stale
// routes after crashed-host cleanup.
var invSessionRouteHostLive = Invariant{
	Name: "session-route-host-live",
	Check: func(c *Process) error {
		if c.sessionRoutes == nil || c.hostRegistry == nil {
			return nil
		}
		var violation error
		c.sessionRoutes.ForEach(func(r *SessionRoute) bool {
			if r.HostID == "" {
				return true
			}
			if c.hostRegistry.Get(r.HostID) == nil {
				violation = fmt.Errorf("sessionRoutes[%v].HostID=%q but host is not registered",
					r.Key, r.HostID)
				return false
			}
			return true
		})
		return violation
	},
}
