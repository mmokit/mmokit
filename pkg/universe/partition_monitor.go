package universe

import (
	"context"
	"time"

	"github.com/zenion/mmoserver/pkg/coords"
	"github.com/zenion/mmoserver/pkg/metrics"
)

// partitionMonitor checks cell loads periodically and triggers automatic
// splits and merges when thresholds are sustained.
type partitionMonitor struct {
	coord *Coordinator
	cfg   *PartitionConfig

	// Per-cell tracking
	smoothedLoad   map[CellID]float64
	sustainedAbove map[CellID]time.Duration // accumulated time above split threshold
	sustainedBelow map[CellID]time.Duration // accumulated time below merge threshold
}

func newPartitionMonitor(coord *Coordinator, cfg *PartitionConfig) *partitionMonitor {
	return &partitionMonitor{
		coord:          coord,
		cfg:            cfg,
		smoothedLoad:   make(map[CellID]float64),
		sustainedAbove: make(map[CellID]time.Duration),
		sustainedBelow: make(map[CellID]time.Duration),
	}
}

// run is the main monitoring loop. Blocks until ctx is cancelled.
func (pm *partitionMonitor) run(ctx context.Context) {
	ticker := time.NewTicker(pm.cfg.EvalInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			pm.evaluate()
		}
	}
}

// evaluate checks all cells and triggers splits/merges as needed.
func (pm *partitionMonitor) evaluate() {
	interval := pm.cfg.EvalInterval

	// Snapshot current cells (CellOwner may change during evaluation)
	cells := make(map[CellID]string, len(pm.coord.CellOwner))
	for cell, nodeID := range pm.coord.CellOwner {
		cells[cell] = nodeID
	}

	for cell, nodeID := range cells {
		rawLoad := pm.getRawLoad(nodeID)
		smoothed := pm.ewmaSmooth(cell, rawLoad)

		// Skip cells on cooldown — don't accumulate sustain time either
		if pm.coord.partState != nil && pm.coord.partState.onCooldown(cell) {
			pm.sustainedAbove[cell] = 0
			pm.sustainedBelow[cell] = 0
			continue
		}

		// --- Split check ---
		cellSize := cell.Size(coords.CellSize)
		canSplit := cellSize/2 >= pm.cfg.MinCellSize

		if pm.cfg.AutoSplitEnabled && smoothed > pm.cfg.SplitThreshold && canSplit {
			pm.sustainedAbove[cell] += interval
			if pm.sustainedAbove[cell] >= pm.cfg.SplitSustain {
				if err := pm.coord.SplitCell(cell, false); err != nil {
					pm.coord.Log.Log(CatMeshNode, "partition monitor: auto-split %s failed: %v", cell, err)
				} else {
					pm.coord.Log.Log(CatMeshNode, "partition monitor: auto-split triggered for %s", cell)
					// Reset tracking for this cell (it no longer exists)
					delete(pm.sustainedAbove, cell)
					delete(pm.sustainedBelow, cell)
					delete(pm.smoothedLoad, cell)
					continue
				}
			}
		} else {
			pm.sustainedAbove[cell] = 0
		}

		// --- Merge check ---
		if pm.cfg.AutoMergeEnabled && cell.Depth > 0 && smoothed < pm.cfg.MergeThreshold {
			pm.sustainedBelow[cell] += interval

			// Check if ALL 4 siblings are below threshold for long enough
			siblings := cell.Siblings()
			allBelow := true
			for _, s := range siblings {
				// Skip if any sibling is on cooldown
				if pm.coord.partState != nil && pm.coord.partState.onCooldown(s) {
					allBelow = false
					break
				}
				if pm.sustainedBelow[s] < pm.cfg.MergeSustain {
					allBelow = false
					break
				}
			}
			if allBelow {
				if err := pm.coord.MergeCell(cell, false); err != nil {
					pm.coord.Log.Log(CatMeshNode, "partition monitor: auto-merge %s failed: %v", cell, err)
				} else {
					pm.coord.Log.Log(CatMeshNode, "partition monitor: auto-merge triggered for %s", cell)
					// Clean up tracking for merged cells
					for _, s := range siblings {
						delete(pm.sustainedAbove, s)
						delete(pm.sustainedBelow, s)
						delete(pm.smoothedLoad, s)
					}
					continue
				}
			}
		} else {
			pm.sustainedBelow[cell] = 0
		}
	}

	// Clean up tracking for cells that no longer exist
	for cell := range pm.smoothedLoad {
		if _, ok := pm.coord.CellOwner[cell]; !ok {
			delete(pm.smoothedLoad, cell)
			delete(pm.sustainedAbove, cell)
			delete(pm.sustainedBelow, cell)
		}
	}
}

// getRawLoad gets the current load metric for a node.
func (pm *partitionMonitor) getRawLoad(nodeID string) float64 {
	snap, ok := pm.coord.nodeLoad(nodeID)
	if !ok {
		return 0
	}
	if pm.cfg.MetricFunc != nil {
		return pm.cfg.MetricFunc(snap)
	}
	return defaultMetricFunc(snap)
}

// ewmaSmooth applies exponential weighted moving average to the load signal.
func (pm *partitionMonitor) ewmaSmooth(cell CellID, rawLoad float64) float64 {
	// EWMA with alpha ~= interval/30s (30s effective window)
	alpha := float64(pm.cfg.EvalInterval) / float64(30*time.Second)
	if alpha > 1 {
		alpha = 1
	}

	prev, ok := pm.smoothedLoad[cell]
	if !ok {
		pm.smoothedLoad[cell] = rawLoad
		return rawLoad
	}
	smoothed := prev*(1-alpha) + rawLoad*alpha
	pm.smoothedLoad[cell] = smoothed
	return smoothed
}

// defaultMetricFunc returns the tick budget usage from the load snapshot.
func defaultMetricFunc(snap metrics.LoadSnapshot) float64 {
	return snap.CompositeLoad
}
