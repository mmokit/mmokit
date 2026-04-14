package universe

import (
	"fmt"
	"sync"
	"time"

	"github.com/zenion/mmoserver/pkg/coords"
	"github.com/zenion/mmoserver/pkg/metrics"
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
func (c *Coordinator) SplitCell(cell CellID, bypassCooldown bool) error {
	pc := c.cfg.DynamicPartitioning
	if pc == nil {
		return fmt.Errorf("dynamic partitioning is not enabled")
	}

	c.mu.RLock()
	_, ok := c.CellOwner[cell]
	c.mu.RUnlock()
	if !ok {
		return fmt.Errorf("cell %s does not exist", cell)
	}

	cellSize := cell.Size(coords.CellSize)
	if cellSize/2 < pc.MinCellSize {
		return fmt.Errorf("cell %s (size %.0f) cannot split: would be below min size %.0f", cell, cellSize, pc.MinCellSize)
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
func (c *Coordinator) MergeCell(cell CellID, bypassCooldown bool) error {
	pc := c.cfg.DynamicPartitioning
	if pc == nil {
		return fmt.Errorf("dynamic partitioning is not enabled")
	}
	if cell.Depth == 0 {
		return fmt.Errorf("cannot merge depth-0 cells")
	}

	siblings := cell.Siblings()
	c.mu.RLock()
	for _, s := range siblings {
		if _, ok := c.CellOwner[s]; !ok {
			c.mu.RUnlock()
			return fmt.Errorf("sibling cell %s does not exist — cannot merge partial split", s)
		}
	}
	c.mu.RUnlock()

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

// rewireNeighbors rebuilds all Node.Neighbors maps from the current topology.
// Caller must hold c.mu write lock.
//
// Every node's cached BorderDispatcher is invalidated so the next PostSystems
// tick rebuilds its CellViewer set from the new neighbor map. Skipping this
// would leave stale viewer → neighbor pointers after a split or merge and
// silently break border replication until the next full restart.
func (c *Coordinator) rewireNeighbors() {
	// Clear all neighbor maps
	for _, node := range c.Cells {
		for k := range node.Neighbors {
			delete(node.Neighbors, k)
		}
	}

	// Rebuild from topology
	for cell, neighborCells := range c.Topology.Neighbors {
		nodeID := c.CellOwner[cell]
		node := c.Cells[nodeID]
		if node == nil {
			continue
		}
		for _, nc := range neighborCells {
			neighborID := c.CellOwner[nc]
			if neighbor, ok := c.Cells[neighborID]; ok {
				node.Neighbors[neighborID] = neighbor
			}
		}
	}

	// Invalidate every node's BorderDispatcher so it rebuilds its viewer
	// set from the new topology on the next PostSystems tick.
	for _, node := range c.Cells {
		if nb, ok := node.Bridge.(*cellBridge); ok {
			nb.invalidateBorderDispatcher()
		}
	}
}
