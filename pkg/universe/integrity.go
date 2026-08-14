package universe

import (
	"context"
	"fmt"
	"time"

	"github.com/mlange-42/ark/ecs"
	"github.com/mmokit/mmokit/pkg/component"
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
			if c.commitLog != nil {
				c.commitLog.Append(CommitEvent{
					Kind:      EventInvariantViolation,
					StepIndex: -1, // not a plan step
					Step:      inv.Name,
					Success:   false,
					Error:     err.Error(),
					Context:   map[string]string{"where": contextMsg},
				})
			}
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
			gotKey, ok := c.CellOwner[cell.CellID()]
			if !ok {
				return fmt.Errorf("c.Cells[%q] references CellID %v but c.CellOwner[%v] is missing",
					key, cell.CellID(), cell.CellID())
			}
			if gotKey != key {
				return fmt.Errorf("c.Cells[%q].Cell=%v but c.CellOwner[%v]=%q (mismatch)",
					key, cell.CellID(), cell.CellID(), gotKey)
			}
		}
		for cellID, key := range c.CellOwner {
			cell, ok := c.Cells[key]
			if !ok {
				return fmt.Errorf("c.CellOwner[%v]=%q but c.Cells[%q] is missing",
					cellID, key, key)
			}
			if cell.CellID() != cellID {
				return fmt.Errorf("c.CellOwner[%v]=%q but c.Cells[%q].Cell=%v (mismatch)",
					cellID, key, key, cell.CellID())
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
			if _, ok := host.Cells[cell.CellID()]; !ok {
				return fmt.Errorf("cellToHostMap[%q]=%q but host %q has no Cells entry for %v",
					cellKey, hostID, hostID, cell.CellID())
			}
		}
		return nil
	},
}

// invTopologyNeighborsOwned asserts that every cell appearing as a
// neighbor in the Topology.Neighbors map has a valid cluster-wide
// ownership entry. Catches the class of bugs where topology rewiring
// runs before ownership is registered — the merge blink we saw this
// session.
//
// Uses Control.OwnerOf, the unified cluster-wide ownership lookup
// (hostRegistry first, cellToHostMap fallback), rather than
// c.CellOwner (local-only, populated by createNode under RoleHost) so
// the check produces the same verdict on pure-coord processes (where
// ownership lives in hostRegistry), standalone hosts (where it lives
// in cellToHostMap via applyPeerList), and classic `all` processes
// (both populated). We snapshot neighbor pairs under Control.mu.RLock,
// then release it before calling OwnerOf so the RWMutex doesn't
// deadlock on recursive-read (OwnerOf acquires the same lock itself).
var invTopologyNeighborsOwned = Invariant{
	Name: "topology-neighbors-owned",
	Check: func(c *Process) error {
		type pair struct {
			cell, neighbor CellID
		}
		var cells []CellID
		var pairs []pair
		c.Control.mu.RLock()
		for cell, neighbors := range c.Control.Topology.Neighbors {
			cells = append(cells, cell)
			for _, n := range neighbors {
				pairs = append(pairs, pair{cell: cell, neighbor: n})
			}
		}
		c.Control.mu.RUnlock()
		for _, cell := range cells {
			key := cell.MeshID()
			if _, ok := c.Control.OwnerOf(key); !ok {
				return fmt.Errorf("Topology.Neighbors contains cell %v but Control.OwnerOf(%q) is missing",
					cell, key)
			}
		}
		for _, p := range pairs {
			nkey := p.neighbor.MeshID()
			if _, ok := c.Control.OwnerOf(nkey); !ok {
				return fmt.Errorf("Topology.Neighbors[%v] contains neighbor %v but Control.OwnerOf(%q) is missing",
					p.cell, p.neighbor, nkey)
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

// defaultInvariants is the set of invariants run at commit entry and
// commit exit — NOT mid-step. Plan steps routinely transition state
// through intermediate forms that legitimately violate invariants (e.g.
// a just-deleted parent cell before the child is installed). Checking
// mid-step would surface spurious violations. Phase B's ExecuteCommitPlan
// runs the full plan atomically under the coord lock; by the time
// CheckInvariants runs on exit, every step has landed.
//
// The set is intentionally small — each invariant is O(n) on a coord-
// level map and runs at topology-event frequency (dozens of times per
// minute at the high end), not per-tick.
var defaultInvariants = []Invariant{
	invCoordMapsConsistent,
	invHostOwnershipMatchesCoord,
	invTopologyNeighborsOwned,
	invSessionRouteHostLive,
	invNoDuplicatePresencePerCell,
}

// invNoDuplicatePresencePerCell asserts that within each cell, no netID
// has more than one non-replica/non-ghost ECS entry. Replicas and ghosts
// are by design duplicates of a live entity living on another cell — they
// share the live netID intentionally for AoI rendering — so they must be
// excluded from this check. The invariant catches the real bug: two
// authoritative (live) entities with the same netID on the same cell,
// which would indicate a spawn path bypassed the netIDIndex.
//
// The per-cell ECS scan runs on each cell's own game-loop goroutine via
// RunOnLoop. Iterating a cell's world from the orchestrator goroutine while
// the cell's loop is concurrently writing (e.g. DemoteLiveToReplica adding
// a Replica component during a handoff commit) trips Ark's world-lock
// guard: the orchestrator's open Query holds a lock bit, and the loop's
// next Add panics with "cannot modify a locked world". RunOnLoop serializes
// the scan against the loop's own writes — same goroutine, no race.
var invNoDuplicatePresencePerCell = Invariant{
	Name: "no-duplicate-presence-per-cell",
	Check: func(c *Process) error {
		for cellKey, cell := range c.Cells {
			if cell.Stage == nil || cell.Stage.netIDIdx == nil {
				continue
			}
			netIDMap := cell.Stage.netIDMap
			seen := make(map[uint32]int)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			runErr := cell.Engine.RunOnLoop(ctx, func() error {
				filter := ecs.NewFilter1[component.NetworkID](cell.Stage.eng.ECS).
					Without(ecs.C[component.Ghost](), ecs.C[component.Replica]())
				q := filter.Query()
				defer q.Close()
				for q.Next() {
					e := q.Entity()
					if !netIDMap.HasAll(e) {
						continue
					}
					id := netIDMap.Get(e).ID
					seen[id]++
				}
				return nil
			})
			cancel()
			if runErr != nil {
				return fmt.Errorf("cell %q: scan failed: %w", cellKey, runErr)
			}
			for id, count := range seen {
				if count > 1 {
					return fmt.Errorf("cell %q: netID %d has %d ECS entries",
						cellKey, id, count)
				}
			}
		}
		return nil
	},
}
