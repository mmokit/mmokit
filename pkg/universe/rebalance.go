package universe

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/zenion/mmoserver/pkg/metrics"
)

// ═══════════════════════════════════════════════════════════════════════════
// S7 T8 — hysteresis-aware auto-rebalance loop.
//
// This loop runs in parallel with the split/merge partition monitor. It
// makes per-HOST migration decisions (rather than per-cell split/merge
// decisions) using the same metrics.LoadSnapshot feed.
//
// Default: OFF. Only runs when PartitionConfig.AutoRebalance == true.
//
// Decision sequence per tick:
//
//  1. Aggregate cell snapshots into per-host totals.
//  2. Track how long each host has stayed ≥ RebalanceLoadThreshold.
//  3. Skip if we're still inside the global RebalanceCooldown after the
//     last successful migration, or if another rebalance migration is
//     still in flight.
//  4. Pick an overloaded source host (highest load ≥ threshold, sustained
//     for RebalanceSustainTime).
//  5. Pick its heaviest cell — measured by CompositeLoad, fallback to
//     entity count, then lexicographic cell ID for determinism.
//  6. Pick a target host — the live host (excluding source) whose
//     (src_load − dst_load) is maximal AND ≥ RebalanceMinDelta. If no
//     host qualifies, skip.
//  7. Call orchestrator.BeginMigrate and fire-and-forget. A side
//     goroutine waits on req.Done to log the outcome and decrement the
//     inflight counter.
//
// Hysteresis vs sustain: two independent guards.
//   - RebalanceMinDelta prevents thrash (don't migrate if improvement
//     is smaller than the hysteresis band).
//   - RebalanceSustainTime prevents reacting to spikes (host must be
//     overloaded for this long before we fire).
// ═══════════════════════════════════════════════════════════════════════════

// rebalanceClock abstracts time.Now so tests can script time advancement
// without relying on real sleeps.
type rebalanceClock interface {
	Now() time.Time
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

// rebalanceLoadSource returns the current per-cell load snapshots and the
// cell→host ownership map. A single callback so production and test code
// can wire any source (real coordinator, scripted fake).
type rebalanceLoadSource interface {
	Snapshots() (snapshots map[string]metrics.LoadSnapshot, cellToHost map[string]string)
}

// rebalanceMigrator abstracts the orchestrator.BeginMigrate call so tests
// can record invocations without running real migrations.
type rebalanceMigrator interface {
	BeginMigrate(cellID CellID, destHost string) (*CellTransferRequest, error)
}

// hostLoad is a per-host aggregation view used by the loop.
type hostLoad struct {
	HostID        string
	Load          float64          // sum of CompositeLoad across owned cells
	CellCount     int              // number of cells assigned to this host
	Cells         []cellLoadRow    // per-cell breakdown, used for picking the donor cell
}

// cellLoadRow is one row in a host's cell breakdown.
type cellLoadRow struct {
	CellID   CellID // parsed from the cell map key
	CellKey  string // original map key (for logging)
	Load     float64
	Entities int
}

// rebalanceLoop is the main struct for the T8 auto-rebalance feature.
type rebalanceLoop struct {
	cfg      *PartitionConfig
	src      rebalanceLoadSource
	migrator rebalanceMigrator
	clock    rebalanceClock
	logf     func(format string, args ...any) // optional logger hook

	mu             sync.Mutex
	firstOverload  map[string]time.Time // hostID -> first time we saw it ≥ threshold
	lastMigration  time.Time
	inflight       int
	migrationsFired int // test instrumentation: total migrations launched
}

func newRebalanceLoop(cfg *PartitionConfig, src rebalanceLoadSource, migrator rebalanceMigrator, clock rebalanceClock, logf func(string, ...any)) *rebalanceLoop {
	if clock == nil {
		clock = realClock{}
	}
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &rebalanceLoop{
		cfg:           cfg,
		src:           src,
		migrator:      migrator,
		clock:         clock,
		logf:          logf,
		firstOverload: make(map[string]time.Time),
	}
}

// Run starts the evaluator ticker. Blocks until ctx is cancelled.
func (rl *rebalanceLoop) Run(ctx context.Context) {
	interval := rl.cfg.RebalanceEvalInterval
	if interval <= 0 {
		interval = 10 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			rl.evaluate()
		}
	}
}

// evaluate runs one decision pass. Exported for testability — tests call
// this directly instead of Run() so they don't have to spin a real timer.
func (rl *rebalanceLoop) evaluate() {
	if !rl.cfg.AutoRebalance {
		return
	}

	rl.mu.Lock()
	// In-flight gate — don't exceed RebalanceMaxConcurrent.
	maxConcurrent := rl.cfg.RebalanceMaxConcurrent
	if maxConcurrent <= 0 {
		maxConcurrent = 1
	}
	if rl.inflight >= maxConcurrent {
		rl.mu.Unlock()
		return
	}
	// Global cooldown gate.
	if !rl.lastMigration.IsZero() && rl.clock.Now().Sub(rl.lastMigration) < rl.cfg.RebalanceCooldown {
		rl.mu.Unlock()
		return
	}
	rl.mu.Unlock()

	snapshots, cellToHost := rl.src.Snapshots()
	if len(snapshots) == 0 || len(cellToHost) == 0 {
		return
	}

	hosts := aggregateByHost(snapshots, cellToHost)
	if len(hosts) < 2 {
		// Need at least two hosts to move anything between.
		return
	}

	threshold := rl.cfg.RebalanceLoadThreshold
	sustain := rl.cfg.RebalanceSustainTime
	now := rl.clock.Now()

	// Update per-host overload tracking.
	rl.mu.Lock()
	// Drop tracking for hosts that have gone away entirely.
	for id := range rl.firstOverload {
		if _, ok := hosts[id]; !ok {
			delete(rl.firstOverload, id)
		}
	}
	for id, hl := range hosts {
		if hl.Load >= threshold {
			if _, ok := rl.firstOverload[id]; !ok {
				rl.firstOverload[id] = now
			}
		} else {
			delete(rl.firstOverload, id)
		}
	}
	// Snapshot the sustained-overload set so we can release the lock.
	type sustainedHost struct {
		id   string
		load float64
	}
	var sustained []sustainedHost
	for id := range rl.firstOverload {
		if now.Sub(rl.firstOverload[id]) >= sustain {
			sustained = append(sustained, sustainedHost{id: id, load: hosts[id].Load})
		}
	}
	rl.mu.Unlock()

	if len(sustained) == 0 {
		return
	}

	// Pick the most-overloaded sustained host (tiebreak lexicographic).
	sort.Slice(sustained, func(i, j int) bool {
		if sustained[i].load != sustained[j].load {
			return sustained[i].load > sustained[j].load
		}
		return sustained[i].id < sustained[j].id
	})
	src := hosts[sustained[0].id]

	// Pick the heaviest cell on the source host.
	donor := pickHeaviestCell(src.Cells)
	if donor == nil {
		return
	}

	// Pick the target host: the one with the biggest positive delta ≥ MinDelta.
	target, delta := pickTargetHost(hosts, src.HostID, rl.cfg.RebalanceMinDelta)
	if target == "" {
		rl.logf("rebalance: host %s overloaded (load=%.2f) but no target has ≥%.2f headroom — skipping",
			src.HostID, src.Load, rl.cfg.RebalanceMinDelta)
		return
	}

	// Fire the migration. We hold the inflight reservation before the
	// BeginMigrate call so a tight test loop can't race past the gate.
	rl.mu.Lock()
	rl.inflight++
	rl.mu.Unlock()

	req, err := rl.migrator.BeginMigrate(donor.CellID, target)
	if err != nil {
		rl.mu.Lock()
		rl.inflight--
		rl.mu.Unlock()
		rl.logf("rebalance: BeginMigrate cell=%s %s→%s failed: %v",
			donor.CellKey, src.HostID, target, err)
		return
	}

	rl.mu.Lock()
	rl.lastMigration = now
	rl.migrationsFired++
	rl.mu.Unlock()
	rl.logf("rebalance: migrating cell=%s %s→%s (src_load=%.2f dst_delta=%.2f)",
		donor.CellKey, src.HostID, target, src.Load, delta)

	// Fire-and-forget: wait on req.Done in a side goroutine so we never
	// block the ticker on a 5-30s commit. The cooldown prevents a second
	// migration from piling up. When the request completes, decrement
	// the inflight counter. req may be nil in tests that don't return
	// a real request — guard for that.
	go rl.watchRequest(req, donor.CellKey, src.HostID, target)
}

// watchRequest waits on req.Done, logs the outcome, and decrements inflight.
// In test code paths that return a nil or synthetic request, this is a no-op
// beyond the inflight decrement.
func (rl *rebalanceLoop) watchRequest(req *CellTransferRequest, cellKey, srcHost, dstHost string) {
	defer func() {
		rl.mu.Lock()
		if rl.inflight > 0 {
			rl.inflight--
		}
		rl.mu.Unlock()
	}()
	if req == nil || req.Done == nil {
		return
	}
	<-req.Done
	if req.Result != nil {
		rl.logf("rebalance: migrate cell=%s %s→%s failed: %v", cellKey, srcHost, dstHost, req.Result)
		return
	}
	rl.logf("rebalance: migrate cell=%s %s→%s committed", cellKey, srcHost, dstHost)
}

// ───────────────────────────────────────────────────────────────────────────
// helpers
// ───────────────────────────────────────────────────────────────────────────

// aggregateByHost folds per-cell snapshots into per-host totals using the
// cell→host ownership map. Cells without a mapped host are ignored — they're
// either transient (being migrated) or belong to a host this process doesn't
// know about. Cells with a mapped host but no snapshot contribute zero.
func aggregateByHost(snapshots map[string]metrics.LoadSnapshot, cellToHost map[string]string) map[string]*hostLoad {
	out := make(map[string]*hostLoad)
	for cellKey, hostID := range cellToHost {
		hl, ok := out[hostID]
		if !ok {
			hl = &hostLoad{HostID: hostID}
			out[hostID] = hl
		}
		hl.CellCount++

		row := cellLoadRow{CellKey: cellKey}
		if cell, err := ParseCellID(cellKey); err == nil {
			row.CellID = cell
		}
		if snap, ok := snapshots[cellKey]; ok {
			row.Load = snap.CompositeLoad
			row.Entities = snap.Entities.Real + snap.Entities.Replica + snap.Entities.Ghost
			hl.Load += snap.CompositeLoad
		}
		hl.Cells = append(hl.Cells, row)
	}
	return out
}

// pickHeaviestCell returns the most-loaded cell in the slice, ties broken
// by entity count and then lexicographic cell key for determinism. Returns
// nil if the slice is empty.
func pickHeaviestCell(cells []cellLoadRow) *cellLoadRow {
	if len(cells) == 0 {
		return nil
	}
	best := &cells[0]
	for i := 1; i < len(cells); i++ {
		c := &cells[i]
		if c.Load > best.Load {
			best = c
			continue
		}
		if c.Load < best.Load {
			continue
		}
		// Equal load — fall through to entity-count tiebreak.
		if c.Entities > best.Entities {
			best = c
			continue
		}
		if c.Entities < best.Entities {
			continue
		}
		// Still tied — lexicographic cell key for determinism.
		if c.CellKey < best.CellKey {
			best = c
		}
	}
	return best
}

// pickTargetHost walks every host except the source and returns the one
// whose (src_load − dst_load) is largest, provided that delta is ≥ minDelta.
// Ties broken lexicographically by hostID. Returns ("", 0) if no host
// qualifies.
func pickTargetHost(hosts map[string]*hostLoad, srcID string, minDelta float64) (string, float64) {
	src, ok := hosts[srcID]
	if !ok {
		return "", 0
	}

	bestID := ""
	bestDelta := -1.0
	for id, hl := range hosts {
		if id == srcID {
			continue
		}
		delta := src.Load - hl.Load
		if delta < minDelta {
			continue
		}
		if delta > bestDelta {
			bestID = id
			bestDelta = delta
			continue
		}
		if delta == bestDelta && id < bestID {
			bestID = id
		}
	}
	if bestID == "" {
		return "", 0
	}
	return bestID, bestDelta
}

// ───────────────────────────────────────────────────────────────────────────
// Coordinator adapters
// ───────────────────────────────────────────────────────────────────────────

// coordRebalanceSource adapts a *Coordinator to rebalanceLoadSource.
// Reads per-cell snapshots from c.allCellLoads() (which returns a map
// keyed on cell string ID) and the cell→host map from c.cellToHostMap.
type coordRebalanceSource struct {
	coord *Coordinator
}

func (s *coordRebalanceSource) Snapshots() (map[string]metrics.LoadSnapshot, map[string]string) {
	snaps := s.coord.allCellLoads() // keyed on cell string ID
	s.coord.mu.RLock()
	cellToHost := make(map[string]string, len(s.coord.cellToHostMap))
	for k, v := range s.coord.cellToHostMap {
		cellToHost[k] = v
	}
	// In a single-host `all` preset (no TestHosts), cellToHostMap is
	// empty. Synthesize it from CellOwner using the node ID as the host ID
	// so the loop still has something meaningful to chew on — though in
	// that case aggregation produces exactly one host and the loop will
	// early-return from the "need ≥2 hosts" check.
	if len(cellToHost) == 0 {
		for cell, nodeID := range s.coord.CellOwner {
			cellToHost[MeshCellID(cell)] = nodeID
		}
	}
	s.coord.mu.RUnlock()
	return snaps, cellToHost
}

// coordRebalanceMigrator adapts the coordinator's orchestrator to
// rebalanceMigrator. Thin delegation — BeginMigrate already returns the
// request + error contract we want.
type coordRebalanceMigrator struct {
	coord *Coordinator
}

func (m *coordRebalanceMigrator) BeginMigrate(cellID CellID, destHost string) (*CellTransferRequest, error) {
	return m.coord.orchestrator.BeginMigrate(cellID, destHost)
}
