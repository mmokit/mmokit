package universe

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/mlange-42/ark/ecs"
	"github.com/mmokit/mmokit/pkg/component"
)

// Entity conservation across a topology commit.
//
// The five invariants in integrity.go cannot express this, and that gap is
// what §7.3 says phase 0 owes. Four of them compare coordinator maps against
// each other; the fifth scans real ECS state but asserts netID UNIQUENESS,
// which zero entities satisfies trivially. So the failure mode that motivates
// the whole 2D/3D program — a cell split that serializes nothing, with no
// error, leaving the destination empty — passes all five.
//
// Conservation is not a predicate over state at one instant, which is why it
// is not an Invariant: Invariant.Check(c *Process) sees only "now". It is a
// comparison across a commit, so it brackets ExecuteCommitPlan instead.
//
// What it asserts: no authoritative entity may VANISH from this process during
// a commit. Split and merge move entities between local cells, so the live set
// must be identical on both sides. Migrate hands a cell to another host, so
// exactly the entities of the migrating cell may leave and nothing else.
//
// What it deliberately does not assert: that no entity APPEARS. A commit can
// legitimately gain entities — a migrate destination receives them, and a
// split's children materialise border replicas — and invNoDuplicatePresencePerCell
// already catches the duplicate case that would matter.

// liveEntityCensus maps every authoritative netID this process owns to the cell
// holding it. Ghosts and replicas are excluded: both are deliberate copies of
// an entity authoritative somewhere else, so counting them would make a
// handoff look like creation.
//
// The per-cell scan runs on each cell's own loop goroutine, for the reason
// invNoDuplicatePresencePerCell documents: iterating a cell's world from the
// orchestrator while its loop writes trips Ark's world-lock guard.
func (c *Process) liveEntityCensus() (map[uint32]MeshCellID, error) {
	c.mu.RLock()
	cells := make(map[MeshCellID]*Cell, len(c.Cells))
	for k, cell := range c.Cells {
		cells[k] = cell
	}
	c.mu.RUnlock()

	census := make(map[uint32]MeshCellID)
	for cellKey, cell := range cells {
		if cell == nil || cell.Stage == nil || cell.Engine == nil {
			continue
		}
		stage := cell.Stage
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		err := cell.Engine.RunOnLoop(ctx, func() error {
			filter := ecs.NewFilter1[component.NetworkID](stage.eng.ECS).
				Without(ecs.C[component.Ghost](), ecs.C[component.Replica]())
			q := filter.Query()
			defer q.Close()
			for q.Next() {
				e := q.Entity()
				if !stage.netIDMap.HasAll(e) {
					continue
				}
				census[stage.netIDMap.Get(e).ID] = cellKey
			}
			return nil
		})
		cancel()
		if err != nil {
			return nil, fmt.Errorf("cell %q: census scan failed: %w", cellKey, err)
		}
	}
	return census, nil
}

// checkEntityConservation compares a census taken before a commit against the
// state after it, and reports any authoritative entity that vanished.
//
// departedCell names the cell whose entities may legitimately leave — the
// source of a migrate. Empty for split and merge, where every entity stays on
// this process and any loss is a bug.
func checkEntityConservation(before, after map[uint32]MeshCellID, departedCell MeshCellID) error {
	var lost []uint32
	for netID, cellKey := range before {
		if _, still := after[netID]; still {
			continue
		}
		if departedCell != "" && cellKey == departedCell {
			continue // migrated to another host with its cell
		}
		lost = append(lost, netID)
	}
	if len(lost) == 0 {
		return nil
	}
	sort.Slice(lost, func(i, j int) bool { return lost[i] < lost[j] })

	shown := lost
	if len(shown) > 8 {
		shown = shown[:8]
	}
	return fmt.Errorf("%d of %d authoritative entities vanished (netIDs %v%s); "+
		"they were on %v before the commit and are on no cell after it",
		len(lost), len(before), shown,
		map[bool]string{true: " …", false: ""}[len(lost) > len(shown)],
		cellsOf(before, lost))
}

// cellsOf reports which cells the lost netIDs came from, deduped — the first
// thing worth knowing is whether they all came from one place.
func cellsOf(before map[uint32]MeshCellID, lost []uint32) []MeshCellID {
	seen := map[MeshCellID]struct{}{}
	var out []MeshCellID
	for _, id := range lost {
		k := before[id]
		if _, dup := seen[k]; dup {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// departedCellFor returns the cell whose entities may legitimately leave this
// process during plan, or "" when none may.
func departedCellFor(plan *CommitPlan) MeshCellID {
	if plan.Kind == CommitKindMigrate && plan.Ctx != nil {
		return plan.Ctx.SrcCellKey
	}
	return ""
}

// reportConservation takes the post-commit census and reports any loss through
// the same path CheckInvariants uses — log, commit-log event, metric, and a
// panic under InvariantPanic — so a conservation failure is as loud as any
// other integrity violation and lands in the same place an operator looks.
func (c *Process) reportConservation(plan *CommitPlan, before map[uint32]MeshCellID) {
	after, err := c.liveEntityCensus()
	if err != nil {
		c.Log.Log(CatInvariant, "commit %d: entity census unavailable at exit: %v", plan.ID, err)
		return
	}
	err = checkEntityConservation(before, after, departedCellFor(plan))
	if err == nil {
		return
	}

	where := fmt.Sprintf("commit %d exit (%s)", plan.ID, plan.Kind)
	msg := fmt.Sprintf("invariant %q violated during %s: %v", "entity-conservation", where, err)
	c.Log.Log(CatInvariant, "%s", msg)
	if c.commitLog != nil {
		c.commitLog.Append(CommitEvent{
			CommitID:  plan.ID,
			Kind:      EventInvariantViolation,
			StepIndex: -1,
			Step:      "entity-conservation",
			Success:   false,
			Error:     err.Error(),
			Context:   map[string]string{"where": where},
		})
	}
	if c.invariantMode == InvariantPanic {
		panic(msg)
	}
}
