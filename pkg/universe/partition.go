package universe

import (
	"fmt"
	"sync"
	"time"

	"github.com/mmokit/mmokit/pkg/coords"
	"github.com/mmokit/mmokit/pkg/metrics"
)

// PartitionConfig configures dynamic cell partitioning behavior.
// Use DefaultPartitionConfig() for sensible defaults.
type PartitionConfig struct {
	// MinCellSize is the smallest allowed cell size. Cells at this size cannot
	// split further. Zero means BaseCellSize / 4 (resolved during Build).
	MinCellSize float32

	// SplitThreshold is the load fraction (0.0–1.0+) above which a cell
	// should split. Default: 0.75.
	SplitThreshold float64

	// MergeThreshold is the load fraction below which cells should merge.
	// All 4 siblings must be below this threshold. Default: 0.20.
	MergeThreshold float64

	// SplitSustain is how long the split threshold must be continuously
	// exceeded before a split is triggered. Default: 30s.
	SplitSustain time.Duration

	// MergeSustain is how long all 4 siblings must be below the merge
	// threshold before a merge is triggered. Default: 60s.
	MergeSustain time.Duration

	// Cooldown is the minimum time between automatic split/merge operations
	// on the same cells. Console commands bypass this. Default: 60s.
	Cooldown time.Duration

	// EvalInterval is how often the partition monitor checks load metrics.
	// Default: 5s.
	EvalInterval time.Duration

	// AutoSplitEnabled controls whether the partition monitor automatically
	// splits cells when load exceeds SplitThreshold. Default: true.
	// Manual splits via console are always allowed regardless of this setting.
	AutoSplitEnabled bool

	// AutoMergeEnabled controls whether the partition monitor automatically
	// merges cells when load drops below MergeThreshold. Default: true.
	// Manual merges via console are always allowed regardless of this setting.
	AutoMergeEnabled bool

	// MetricFunc computes a load score (0.0 = idle, 1.0 = full budget) from
	// a node's load snapshot. Nil uses the default (tick budget usage).
	MetricFunc func(snap metrics.LoadSnapshot) float64

	// OnTopologyChanged is called after each split or merge completes.
	OnTopologyChanged func()

	// ─────────────────────────────────────────────────────────────────────
	// S7 T8 — auto-rebalance loop (per-host migration).
	//
	// AutoRebalance defaults to FALSE. The split/merge monitor and the
	// rebalance monitor are independent: split/merge reacts to per-cell
	// load, rebalance reacts to per-host load and migrates cells off
	// overloaded hosts via CellTransferOrchestrator.BeginMigrate.
	// ─────────────────────────────────────────────────────────────────────

	// AutoRebalance enables the per-host rebalance loop. Default: false.
	// The BeginMigrate primitive still works manually (cell migrate
	// console command) regardless of this setting — it only gates the
	// background loop.
	AutoRebalance bool

	// RebalanceEvalInterval is how often the rebalance loop samples
	// host loads. Default: 10s.
	RebalanceEvalInterval time.Duration

	// RebalanceLoadThreshold is the per-host CompositeLoad value above
	// which the host is considered overloaded. Expressed in the same
	// units as metrics.LoadSnapshot.CompositeLoad (0.0 = idle, 1.0 = at
	// budget, >1.0 = over budget). Default: 0.85.
	//
	// Note: earlier drafts called this "CPUThreshold"; the field is
	// named after the metric it actually reads (CompositeLoad).
	RebalanceLoadThreshold float64

	// RebalanceSustainTime is how long a host must stay at or above
	// RebalanceLoadThreshold before the loop attempts a migration. Guards
	// against reacting to load spikes. Default: 60s.
	RebalanceSustainTime time.Duration

	// RebalanceMinDelta is the minimum (src_load − dst_load) difference
	// required to pick a destination host. Hysteresis against thrash:
	// without this guard, a loop could keep ping-ponging a cell back and
	// forth between hosts whose loads differ by a rounding error. Default: 0.20.
	RebalanceMinDelta float64

	// RebalanceCooldown is the minimum time between two successive
	// migrations fired by the rebalance loop, globally. Prevents a
	// still-overloaded host from triggering a second migration before
	// the first has had time to take effect. Default: 30s.
	RebalanceCooldown time.Duration

	// RebalanceMaxConcurrent caps how many rebalance migrations are allowed
	// to be in flight at once. Default: 1. The T8 loop always fires
	// at most one migration per tick, so this is mostly a safety rail;
	// T9+ may expand it.
	RebalanceMaxConcurrent int

	// ─────────────────────────────────────────────────────────────────────
	// Transparent cell transfers — source-side border-replica context.
	//
	// On SPLIT/MERGE/MIGRATE the source cell ships its cross-cell visible
	// set (sibling locals + outer-neighbor replicas) to the destination so
	// the destination's first replication frame is complete and clients see
	// no transition artifact. See
	// docs/superpowers/specs/2026-05-28-transparent-cell-transfers-design.md.
	// ─────────────────────────────────────────────────────────────────────

	// IncludeBorderContext enables source-side context collection at
	// transfer commit time. Default: true. Disable for tests or
	// minimal-bandwidth scenarios (legacy behavior — destination
	// reconstructs cross-cell visibility via async border replication).
	IncludeBorderContext bool

	// BorderContextRadius is the AoI margin used when collecting context
	// entities. 0 means "use the cell's replication AoI radius" (the
	// default). Tunable for games whose context-seed reach differs from
	// the replication AoI.
	BorderContextRadius float32

	// BorderContextMaxCount caps the number of context entities serialized
	// per destination cell, bounding transfer payload size. 0 = unbounded
	// (default). On overflow, the first N entries ship and a structured
	// warning logs; the remainder fall back to async border replication.
	BorderContextMaxCount int
}

// DefaultPartitionConfig returns a PartitionConfig with sensible defaults.
// MinCellSize defaults to 0, which Build() resolves to BaseCellSize / 4.
func DefaultPartitionConfig() *PartitionConfig {
	return &PartitionConfig{
		AutoSplitEnabled:       true,
		AutoMergeEnabled:       true,
		SplitThreshold:         0.75,
		MergeThreshold:         0.20,
		SplitSustain:           30 * time.Second,
		MergeSustain:           60 * time.Second,
		Cooldown:               60 * time.Second,
		EvalInterval:           5 * time.Second,
		AutoRebalance:          false, // opt-in; primitive ships silent
		RebalanceEvalInterval:  10 * time.Second,
		RebalanceLoadThreshold: 0.85,
		RebalanceSustainTime:   60 * time.Second,
		RebalanceMinDelta:      0.20,
		RebalanceCooldown:      30 * time.Second,
		RebalanceMaxConcurrent: 1,
		IncludeBorderContext:   true, // transparent transfers on by default
		// BorderContextRadius: 0 → use the cell's replication AoI radius.
		// BorderContextMaxCount: 0 → unbounded.
	}
}

// partitionState tracks per-cell cooldowns for the partition system.
type partitionState struct {
	mu        sync.Mutex
	cooldowns map[CellID]time.Time // cell -> earliest next auto split/merge
}

func newPartitionState() *partitionState {
	return &partitionState{
		cooldowns: make(map[CellID]time.Time),
	}
}

func (ps *partitionState) onCooldown(cell CellID) bool {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	return time.Now().Before(ps.cooldowns[cell])
}

func (ps *partitionState) setCooldown(cell CellID, d time.Duration) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	ps.cooldowns[cell] = time.Now().Add(d)
}

func (ps *partitionState) clearCooldown(cell CellID) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	delete(ps.cooldowns, cell)
}

// SplitCell validates a split request, checks cooldowns, and delegates to
// the S7 CellTransferOrchestrator. Blocks until the orchestrator commits
// (success) or rolls back (failure). The entity serialization, cell
// creation, and topology mutation all happen inside the orchestrator →
// executor → commit flow — this wrapper is just the cooldown/validation
// gate that the partition monitor and admin console already depend on.
//
// If bypassCooldown is true, cooldown checks are skipped (for console commands).
func (c *Process) SplitCell(cell CellID, bypassCooldown bool) error {
	// pc may be nil when dynamic partitioning is opted out — the
	// auto-monitor stays silent, but programmatic split/merge still
	// works (console command, admin API, tests). Fall back to a default
	// MinCellSize (BaseCellSize/4) when pc is absent, matching the
	// resolved value Build() installs on the default-partition path.
	pc := c.cfg.DynamicPartitioning
	minCellSize := coords.CellSize / 4
	if pc != nil && pc.MinCellSize > 0 {
		minCellSize = pc.MinCellSize
	}

	// Ownership lives in HostRegistry for remote-host cells and in
	// cellToHostMap for local cells; HostForCellID unifies both.
	// c.CellOwner alone is insufficient on a pure-coordinator process.
	if c.HostForCellID(cell.MeshID()) == "" {
		return fmt.Errorf("cell %s does not exist", cell)
	}

	cellSize := cell.Size(coords.CellSize)
	if cellSize/2 < minCellSize {
		return fmt.Errorf("cell %s (size %.0f) cannot split: would be below min size %.0f", cell, cellSize, minCellSize)
	}

	if !bypassCooldown && c.partState != nil && c.partState.onCooldown(cell) {
		return fmt.Errorf("cell %s is on cooldown", cell)
	}

	req, err := c.orchestrator.BeginSplit(cell)
	if err != nil {
		return err
	}
	<-req.Done
	return req.Result
}

// MergeCell validates a merge request, checks sibling cooldowns, and
// delegates to the S7 CellTransferOrchestrator. Blocks until the
// orchestrator commits (success) or rolls back (failure). The executor
// handles entity draining from donors; commit() renames the survivor
// sibling to the parent cell ID and tears down donors.
//
// If bypassCooldown is true, cooldown checks are skipped (for console commands).
func (c *Process) MergeCell(cell CellID, bypassCooldown bool) error {
	// c.cfg.DynamicPartitioning may be nil when dynamic partitioning is
	// opted out — programmatic merges (console, admin, tests) still
	// work. Cooldown state is only consulted when partState exists, so
	// nil pc is a clean no-op for it.
	if cell.Depth == 0 {
		return fmt.Errorf("cannot merge depth-0 cells")
	}

	siblings := cell.Siblings()
	// HostForCellID consults hostRegistry + cellToHostMap, unifying local
	// and remote ownership for pure-coordinator processes.
	for _, s := range siblings {
		if c.HostForCellID(s.MeshID()) == "" {
			return fmt.Errorf("sibling cell %s does not exist — cannot merge partial split", s)
		}
	}

	if !bypassCooldown && c.partState != nil {
		for _, s := range siblings {
			if c.partState.onCooldown(s) {
				return fmt.Errorf("cell %s is on cooldown", s)
			}
		}
	}

	req, err := c.orchestrator.BeginMerge(cell.Parent())
	if err != nil {
		return err
	}
	<-req.Done
	return req.Result
}

// rewireDirective describes a single cell's new neighbor set, computed under
// c.mu and then applied at a structurally safe point on the cell's game loop.
type rewireDirective struct {
	cell      *Cell
	neighbors map[MeshCellID]*Cell
}

// computeRewireDirectivesLocked builds per-cell neighbor maps for the given
// affected cells plus every cell that currently lists one of them in its
// Topology.Neighbors entry. Returns the list of directives without touching
// node.Neighbors — the caller is expected to apply them on each cell's game
// loop after releasing c.mu.
//
// Caller must hold c.mu.
func (c *Process) computeRewireDirectivesLocked(affected []CellID) []rewireDirective {
	if len(affected) == 0 {
		return nil
	}
	affectedSet := make(map[CellID]struct{}, len(affected))
	for _, a := range affected {
		affectedSet[a] = struct{}{}
	}
	// Expand the frontier to any cell whose Topology.Neighbors entry still
	// lists an affected cell — those cells' node.Neighbors pointers may be
	// stale (pointing at a deleted parent, missing a new child, etc).
	touched := make(map[CellID]struct{}, len(affectedSet))
	for a := range affectedSet {
		touched[a] = struct{}{}
	}
	if c.Control.Topology.Neighbors != nil {
		for cid, neighborList := range c.Control.Topology.Neighbors {
			if _, hit := affectedSet[cid]; hit {
				continue
			}
			for _, nc := range neighborList {
				if _, hit := affectedSet[nc]; hit {
					touched[cid] = struct{}{}
					break
				}
			}
		}
	}

	out := make([]rewireDirective, 0, len(touched))
	for cid := range touched {
		nodeKey := c.CellOwner[cid]
		node := c.Cells[nodeKey]
		if node == nil {
			continue
		}
		newNeighbors := make(map[MeshCellID]*Cell)
		if c.Control.Topology.Neighbors != nil {
			for _, nc := range c.Control.Topology.Neighbors[cid] {
				neighborKey := c.CellOwner[nc]
				if neighbor, ok := c.Cells[neighborKey]; ok {
					newNeighbors[neighborKey] = neighbor
				}
			}
		}
		out = append(out, rewireDirective{cell: node, neighbors: newNeighbors})
	}
	return out
}

// applyRewireDirectives writes every directive onto its target cell's game
// loop via SubmitLoopJob so topology changes land at a tick boundary. The
// closure also takes c.mu, which uniformly guards Cell.Neighbors against
// off-loop mesh-control reconciliation. Callers must NOT hold c.mu while
// invoking this helper — the loop closure acquires it while draining.
//
// Each directive also invalidates the cell's cached BorderDispatcher so
// the next tick rebuilds its CellViewer set from the new neighbor map.
//
// If a cell's game loop is not actively running (e.g. unit-test fixtures
// that build a Process without calling cell.Run), the bounded loop-job queue
// can still accept the closure, but it will not execute until a loop drains
// it. Tests that drive the flow synchronously run at least one tick before
// asserting.
func (c *Process) applyRewireDirectives(dirs []rewireDirective) {
	for _, d := range dirs {
		if d.cell == nil || d.cell.Engine == nil {
			continue
		}
		neighbors := d.neighbors
		target := d.cell
		// Fire-and-forget: rewire directives are idempotent and the next
		// full rewireNeighbors pass will converge if this one is dropped.
		if !target.Engine.SubmitLoopJob(func() error {
			c.mu.Lock()
			defer c.mu.Unlock()
			for k := range target.Neighbors {
				delete(target.Neighbors, k)
			}
			for k, v := range neighbors {
				target.Neighbors[k] = v
			}
			if nb := unwrapCellBridge(target.Bridge); nb != nil {
				nb.invalidateBorderDispatcher()
			}
			return nil
		}) {
			c.Log.Log(CatMeshCell, "coordinator: rewire directive for %s dropped (admin queue full)", target.MeshID())
		}
	}
}

// (rewireNeighbors was removed as part of S7-T9. The full O(N) rebuild path
// is no longer needed — cell-transfer commits use computeRewireDirectivesLocked
// + applyRewireDirectives to rewire only the affected frontier on each cell's
// own game loop, and Build() wires initial neighbor state before loops start.
// If a future crash-recovery path needs a full rebuild, rebuild the
// Topology.Neighbors map and then call applyRewireDirectives over every cell;
// every runtime Cell.Neighbors read/write must remain under Process.mu.)
