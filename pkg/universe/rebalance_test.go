package universe

import (
	"sync"
	"testing"
	"time"

	"github.com/zenion/mmoserver/pkg/coords"
	"github.com/zenion/mmoserver/pkg/metrics"
)

// ═══════════════════════════════════════════════════════════════════════════
// S7 T8 — rebalance loop unit tests.
//
// These drive the decision logic directly via rebalanceLoop.evaluate() with
// a scripted loadSource, a fakeRebalanceMigrator, and a fakeClock. No
// coordinator, no goroutines, no real timers.
// ═══════════════════════════════════════════════════════════════════════════

// ─── fakes ───────────────────────────────────────────────────────────────

// fakeClock is a mockable clock driven by t.Advance(...).
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock() *fakeClock { return &fakeClock{now: time.Unix(1_700_000_000, 0)} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// scriptedSource returns whatever snapshots and cell→host map is currently
// set on it. Tests mutate the fields between evaluate() calls to script load.
type scriptedSource struct {
	mu        sync.Mutex
	snaps     map[string]metrics.LoadSnapshot
	ownership map[string]string
}

func (s *scriptedSource) Snapshots() (map[string]metrics.LoadSnapshot, map[string]string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	snapsCopy := make(map[string]metrics.LoadSnapshot, len(s.snaps))
	for k, v := range s.snaps {
		snapsCopy[k] = v
	}
	ownCopy := make(map[string]string, len(s.ownership))
	for k, v := range s.ownership {
		ownCopy[k] = v
	}
	return snapsCopy, ownCopy
}

func (s *scriptedSource) setLoad(cellKey string, load float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snaps[cellKey] = metrics.LoadSnapshot{CompositeLoad: load}
}

// fakeRebalanceMigrator records every BeginMigrate call. The returned
// request is synthetic: Done is pre-closed so the watchRequest goroutine
// decrements inflight immediately.
type fakeRebalanceMigrator struct {
	mu    sync.Mutex
	calls []fakeMigrateCall
	// when nonzero, Done is NOT pre-closed — tests that want to assert
	// on inflight behavior can drive it manually by closing
	// .lastDone explicitly.
	holdInflight bool
	lastDone     chan struct{}
}

type fakeMigrateCall struct {
	Cell CellID
	Dest string
}

func (m *fakeRebalanceMigrator) BeginMigrate(cellID CellID, destHost string) (*CellTransferRequest, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, fakeMigrateCall{Cell: cellID, Dest: destHost})
	done := make(chan struct{})
	if !m.holdInflight {
		close(done)
	}
	m.lastDone = done
	return &CellTransferRequest{
		ID:   uint64(len(m.calls)),
		Kind: CellTransferMigrate,
		Done: done,
	}, nil
}

func (m *fakeRebalanceMigrator) callSnapshot() []fakeMigrateCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]fakeMigrateCall, len(m.calls))
	copy(out, m.calls)
	return out
}

// ─── helpers ─────────────────────────────────────────────────────────────

// newTestRebalance builds a loop wired to a 2-host / 2-cell scripted
// source. host-a owns cell_0_0, host-b owns cell_1_0.
func newTestRebalance(t *testing.T, cfg *PartitionConfig) (*rebalanceLoop, *scriptedSource, *fakeRebalanceMigrator, *fakeClock) {
	t.Helper()
	clock := newFakeClock()
	src := &scriptedSource{
		snaps: map[string]metrics.LoadSnapshot{
			"cell_0_0": {CompositeLoad: 0.1},
			"cell_1_0": {CompositeLoad: 0.1},
		},
		ownership: map[string]string{
			"cell_0_0": "host-a",
			"cell_1_0": "host-b",
		},
	}
	migrator := &fakeRebalanceMigrator{}
	loop := newRebalanceLoop(cfg, src, migrator, clock, nil)
	return loop, src, migrator, clock
}

// defaultT8Cfg returns a PartitionConfig with AutoRebalance=true and
// production-default rebalance knobs. Tests tweak as needed.
func defaultT8Cfg() *PartitionConfig {
	cfg := DefaultPartitionConfig()
	cfg.AutoRebalance = true
	return cfg
}

// waitForInflight polls rl.inflight until it reaches want or a deadline
// elapses. Used to synchronize the test with the watchRequest goroutine
// spawned by evaluate() so assertions on subsequent evaluate() calls are
// not racing the decrement.
func waitForInflight(t *testing.T, rl *rebalanceLoop, want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		rl.mu.Lock()
		got := rl.inflight
		rl.mu.Unlock()
		if got == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	rl.mu.Lock()
	got := rl.inflight
	rl.mu.Unlock()
	t.Fatalf("timeout waiting for inflight=%d, got %d", want, got)
}

// ─── tests ───────────────────────────────────────────────────────────────

// TestRebalanceDefaultOff verifies that with AutoRebalance=false the loop
// makes zero migration decisions even when a host is catastrophically
// overloaded. This is the "primitive ships silent" guarantee.
func TestRebalanceDefaultOff(t *testing.T) {
	cfg := DefaultPartitionConfig()
	if cfg.AutoRebalance {
		t.Fatalf("DefaultPartitionConfig.AutoRebalance should be false, got true")
	}

	loop, src, migrator, clock := newTestRebalance(t, cfg)
	src.setLoad("cell_0_0", 10.0) // obscene overload

	// Repeatedly evaluate across several sustain windows.
	for i := 0; i < 20; i++ {
		loop.evaluate()
		clock.Advance(cfg.RebalanceSustainTime)
	}

	if got := migrator.callSnapshot(); len(got) != 0 {
		t.Errorf("AutoRebalance=false should never migrate; got %d calls", len(got))
	}
}

// TestRebalanceHysteresisPreventsThrash verifies that a load which bounces
// around the threshold but never stays above it never triggers a migration,
// AND that if delta between hosts is too small to exceed MinDelta, no
// migration fires even at sustained overload.
func TestRebalanceHysteresisPreventsThrash(t *testing.T) {
	cfg := defaultT8Cfg()
	cfg.RebalanceLoadThreshold = 0.85
	cfg.RebalanceSustainTime = 60 * time.Second
	cfg.RebalanceMinDelta = 0.20

	// Case 1: load bounces around the threshold.
	t.Run("fluctuating_load_never_sustains", func(t *testing.T) {
		loop, src, migrator, clock := newTestRebalance(t, cfg)

		for i := 0; i < 30; i++ {
			if i%2 == 0 {
				src.setLoad("cell_0_0", 0.9) // above
			} else {
				src.setLoad("cell_0_0", 0.5) // below — resets firstOverload
			}
			loop.evaluate()
			clock.Advance(10 * time.Second)
		}

		if got := migrator.callSnapshot(); len(got) != 0 {
			t.Errorf("fluctuating load should not migrate; got %d calls", len(got))
		}
	})

	// Case 2: sustained overload BUT the other host is almost as busy,
	// so (src − dst) < MinDelta and no target qualifies.
	t.Run("delta_too_small", func(t *testing.T) {
		loop, src, migrator, clock := newTestRebalance(t, cfg)
		src.setLoad("cell_0_0", 0.90) // sustained overload on host-a
		src.setLoad("cell_1_0", 0.80) // host-b only 0.10 lower — below MinDelta=0.20

		// Let the sustain window elapse.
		loop.evaluate()
		clock.Advance(cfg.RebalanceSustainTime + time.Second)
		loop.evaluate()

		if got := migrator.callSnapshot(); len(got) != 0 {
			t.Errorf("delta 0.10 < MinDelta 0.20 should not migrate; got %d calls", len(got))
		}
	})
}

// TestRebalanceFiresWhenSustained verifies that after sustained overload
// AND a suitable target host, exactly one migration is dispatched to the
// right (cell, host) pair.
func TestRebalanceFiresWhenSustained(t *testing.T) {
	cfg := defaultT8Cfg()
	cfg.RebalanceSustainTime = 30 * time.Second
	cfg.RebalanceLoadThreshold = 0.85
	cfg.RebalanceMinDelta = 0.20
	cfg.RebalanceCooldown = 5 * time.Minute

	loop, src, migrator, clock := newTestRebalance(t, cfg)
	src.setLoad("cell_0_0", 0.95) // host-a = 0.95
	src.setLoad("cell_1_0", 0.10) // host-b = 0.10 (delta 0.85 ≥ 0.20)

	// First tick marks host-a as overloaded at t=0. Sustain window hasn't
	// elapsed yet, so no migration.
	loop.evaluate()
	if got := migrator.callSnapshot(); len(got) != 0 {
		t.Fatalf("migration fired before sustain window elapsed: %d calls", len(got))
	}

	// Advance past the sustain window. Second tick should fire.
	clock.Advance(cfg.RebalanceSustainTime + time.Second)
	loop.evaluate()

	got := migrator.callSnapshot()
	if len(got) != 1 {
		t.Fatalf("expected exactly 1 migration, got %d", len(got))
	}
	if got[0].Dest != "host-b" {
		t.Errorf("target host = %s, want host-b", got[0].Dest)
	}
	expectCell := CellID{X: 0, Y: 0, Depth: 0}
	if got[0].Cell != expectCell {
		t.Errorf("migrated cell = %v, want %v", got[0].Cell, expectCell)
	}
}

// TestRebalanceCooldownPreventsRapid verifies that after firing one
// migration, the loop doesn't fire another until RebalanceCooldown elapses
// — even if the overload is still present.
func TestRebalanceCooldownPreventsRapid(t *testing.T) {
	cfg := defaultT8Cfg()
	cfg.RebalanceSustainTime = 10 * time.Second
	cfg.RebalanceCooldown = 2 * time.Minute
	cfg.RebalanceLoadThreshold = 0.85
	cfg.RebalanceMinDelta = 0.20

	loop, src, migrator, clock := newTestRebalance(t, cfg)
	src.setLoad("cell_0_0", 0.95)
	src.setLoad("cell_1_0", 0.10)

	// Seed overload, elapse sustain, fire one migration.
	loop.evaluate()
	clock.Advance(cfg.RebalanceSustainTime + time.Second)
	loop.evaluate()
	// Let the fire-and-forget goroutine decrement inflight before the
	// next evaluate runs — pre-closed Done drains almost instantly but
	// the decrement is still on a goroutine.
	waitForInflight(t, loop, 0)
	if got := migrator.callSnapshot(); len(got) != 1 {
		t.Fatalf("want 1 initial migration, got %d", len(got))
	}

	// Keep load above threshold. Evaluate several times inside the
	// cooldown window — zero new migrations should fire.
	for i := 0; i < 10; i++ {
		clock.Advance(cfg.RebalanceCooldown / 20) // 10 ticks, total = cooldown/2
		loop.evaluate()
	}
	if got := migrator.callSnapshot(); len(got) != 1 {
		t.Fatalf("cooldown should block migrations; got %d calls", len(got))
	}

	// Elapse the cooldown and the sustain window from NOW (firstOverload
	// is still set from the earlier ticks). One more migration should fire.
	clock.Advance(cfg.RebalanceCooldown + cfg.RebalanceSustainTime + time.Second)
	loop.evaluate()
	waitForInflight(t, loop, 0)
	if got := migrator.callSnapshot(); len(got) != 2 {
		t.Fatalf("after cooldown expiry want 2 migrations total, got %d", len(got))
	}
}

// TestRebalanceNoTargetHost verifies the degenerate case: only one host
// exists in the topology, so there's no target to migrate to. The loop
// should early-return without firing anything regardless of how overloaded
// the lone host is.
func TestRebalanceNoTargetHost(t *testing.T) {
	cfg := defaultT8Cfg()
	cfg.RebalanceSustainTime = 10 * time.Second

	clock := newFakeClock()
	src := &scriptedSource{
		snaps: map[string]metrics.LoadSnapshot{
			"cell_0_0": {CompositeLoad: 5.0}, // catastrophic overload
			"cell_1_0": {CompositeLoad: 5.0},
		},
		// Only one host — same host owns both cells.
		ownership: map[string]string{
			"cell_0_0": "host-solo",
			"cell_1_0": "host-solo",
		},
	}
	migrator := &fakeRebalanceMigrator{}
	loop := newRebalanceLoop(cfg, src, migrator, clock, nil)

	for i := 0; i < 20; i++ {
		loop.evaluate()
		clock.Advance(cfg.RebalanceSustainTime)
	}
	if got := migrator.callSnapshot(); len(got) != 0 {
		t.Errorf("single-host topology should never migrate; got %d calls", len(got))
	}
}

// TestRebalanceAggregateByHost verifies the per-host aggregation folds
// multi-cell hosts correctly — summing composite load and capturing all
// cells in the row breakdown.
func TestRebalanceAggregateByHost(t *testing.T) {
	snaps := map[string]metrics.LoadSnapshot{
		"cell_0_0": {CompositeLoad: 0.3},
		"cell_1_0": {CompositeLoad: 0.4},
		"cell_0_1": {CompositeLoad: 0.1},
	}
	ownership := map[string]string{
		"cell_0_0": "host-a",
		"cell_1_0": "host-a",
		"cell_0_1": "host-b",
	}

	hosts := aggregateByHost(snaps, ownership)
	if len(hosts) != 2 {
		t.Fatalf("aggregateByHost hosts=%d, want 2", len(hosts))
	}
	if got := hosts["host-a"].Load; got < 0.699 || got > 0.701 {
		t.Errorf("host-a load = %.3f, want 0.7", got)
	}
	if got := hosts["host-b"].Load; got < 0.099 || got > 0.101 {
		t.Errorf("host-b load = %.3f, want 0.1", got)
	}
	if got := hosts["host-a"].CellCount; got != 2 {
		t.Errorf("host-a cell count = %d, want 2", got)
	}
}

// TestRebalancePicksHeaviestCell verifies that on an overloaded multi-cell
// host, the rebalance loop picks the cell that contributes the most load.
func TestRebalancePicksHeaviestCell(t *testing.T) {
	cfg := defaultT8Cfg()
	cfg.RebalanceSustainTime = 5 * time.Second
	cfg.RebalanceCooldown = 5 * time.Minute
	cfg.RebalanceLoadThreshold = 0.85
	cfg.RebalanceMinDelta = 0.10

	clock := newFakeClock()
	src := &scriptedSource{
		snaps: map[string]metrics.LoadSnapshot{
			"cell_0_0": {CompositeLoad: 0.2}, // light on host-a
			"cell_1_0": {CompositeLoad: 0.8}, // heavy on host-a
			"cell_0_1": {CompositeLoad: 0.1}, // host-b
		},
		ownership: map[string]string{
			"cell_0_0": "host-a",
			"cell_1_0": "host-a",
			"cell_0_1": "host-b",
		},
	}
	// host-a total = 1.0 (overloaded); host-b total = 0.1
	migrator := &fakeRebalanceMigrator{}
	loop := newRebalanceLoop(cfg, src, migrator, clock, nil)

	loop.evaluate() // seed
	clock.Advance(cfg.RebalanceSustainTime + time.Second)
	loop.evaluate()

	got := migrator.callSnapshot()
	if len(got) != 1 {
		t.Fatalf("want 1 migration, got %d", len(got))
	}
	// cell_1_0 is the heaviest → expect it to be the migrated cell.
	want := CellID{X: 1, Y: 0, Depth: 0}
	if got[0].Cell != want {
		t.Errorf("migrated cell = %v, want %v (heaviest on host-a)", got[0].Cell, want)
	}
	if got[0].Dest != "host-b" {
		t.Errorf("target = %s, want host-b", got[0].Dest)
	}
}

// TestRebalanceInflightGate verifies that when a migration is in flight
// (watchRequest hasn't completed yet), subsequent evaluate() calls don't
// launch a second migration even with the cooldown disabled.
func TestRebalanceInflightGate(t *testing.T) {
	cfg := defaultT8Cfg()
	cfg.RebalanceSustainTime = 5 * time.Second
	cfg.RebalanceCooldown = 0 // disable the cooldown to isolate the inflight gate
	cfg.RebalanceLoadThreshold = 0.85
	cfg.RebalanceMinDelta = 0.20
	cfg.RebalanceMaxConcurrent = 1

	loop, src, migrator, clock := newTestRebalance(t, cfg)
	migrator.holdInflight = true // block watchRequest from decrementing
	src.setLoad("cell_0_0", 0.95)
	src.setLoad("cell_1_0", 0.10)

	loop.evaluate() // seed
	clock.Advance(cfg.RebalanceSustainTime + time.Second)
	loop.evaluate() // fire #1

	if got := migrator.callSnapshot(); len(got) != 1 {
		t.Fatalf("want 1 fire, got %d", len(got))
	}

	// Evaluate many times with the request still "in flight".
	for i := 0; i < 10; i++ {
		clock.Advance(cfg.RebalanceSustainTime + time.Second)
		loop.evaluate()
	}
	if got := migrator.callSnapshot(); len(got) != 1 {
		t.Errorf("inflight gate should block; got %d calls", len(got))
	}

	// Release the held request and verify the loop recovers on the
	// next tick.
	close(migrator.lastDone)
	// Give the watchRequest goroutine a moment to decrement.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		loop.mu.Lock()
		inflight := loop.inflight
		loop.mu.Unlock()
		if inflight == 0 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	clock.Advance(cfg.RebalanceSustainTime + time.Second)
	loop.evaluate()
	if got := migrator.callSnapshot(); len(got) != 2 {
		t.Errorf("after release want 2 calls, got %d", len(got))
	}
}

// ═══════════════════════════════════════════════════════════════════════════
// S7 T10 — rebalance → real-orchestrator integration
//
// TestS7AutoRebalanceEndToEnd wires the rebalance loop to a live
// 2-host Coordinator through coordRebalanceMigrator (the adapter used in
// production from Coordinator.Start). It scripts an overloaded host via
// a fake rebalanceLoadSource, runs evaluate() twice (once to arm the
// sustain window, once past it), and verifies that the real orchestrator's
// BeginMigrate path fired, committed, and flipped cellToHostMap.
//
// Why this test matters: the individual T8 unit tests cover decision
// logic with fake migrators. This test proves the decision → dispatch →
// commit wiring is intact: rebalance loop → coordRebalanceMigrator →
// orchestrator.BeginMigrate → real dispatcher → executor → commit →
// cellToHostMap update. A regression anywhere in that chain would flip
// the flakiness or the assertion below.
//
// The real LoadSource wiring (Coordinator.allCellLoads) is still tested
// separately; here we inject loads directly via a scriptedSource because
// driving real CompositeLoad from a tick-hz metrics collector in under a
// second of test time is not practical.
// ═══════════════════════════════════════════════════════════════════════════

func TestS7AutoRebalanceEndToEnd(t *testing.T) {
	coords.SetCellSize(1024)

	fx := newDistributedFixture(t, FixtureConfig{
		CellsX:   2,
		CellsY:   2,
		CellSize: 1024,
		HostIDs:  []string{"host-a", "host-b"},
	})
	coord := fx.Coord()

	// Snapshot pre-rebalance ownership via the coord-role's authoritative
	// view (hostRegistry + cellToHostMap). snapshotCellOwnership unifies
	// both; in distributed mode hostRegistry is the primary source.
	preOwnership := coord.snapshotCellOwnership()

	hasHostACells := false
	for _, v := range preOwnership {
		if v == "host-a" {
			hasHostACells = true
			break
		}
	}
	if !hasHostACells {
		t.Fatalf("pre-rebalance: no cells on host-a (preOwnership=%v)", preOwnership)
	}

	// Build the scripted load source. Every cell on host-a gets an
	// obscene CompositeLoad; every cell on host-b is idle. host-b's
	// (src_load − dst_load) delta then trivially exceeds MinDelta.
	snaps := make(map[string]metrics.LoadSnapshot, len(preOwnership))
	ownership := make(map[string]string, len(preOwnership))
	for k, v := range preOwnership {
		ownership[k] = v
		if v == "host-a" {
			snaps[k] = metrics.LoadSnapshot{CompositeLoad: 5.0}
		} else {
			snaps[k] = metrics.LoadSnapshot{CompositeLoad: 0.05}
		}
	}
	src := &scriptedSource{snaps: snaps, ownership: ownership}

	// Short-interval rebalance config. Sustain=50ms so the second evaluate()
	// exits the hysteresis window immediately.
	pc := DefaultPartitionConfig()
	pc.AutoRebalance = true
	pc.RebalanceEvalInterval = 20 * time.Millisecond
	pc.RebalanceSustainTime = 50 * time.Millisecond
	pc.RebalanceCooldown = 100 * time.Millisecond
	pc.RebalanceLoadThreshold = 0.85
	pc.RebalanceMinDelta = 0.20

	clock := newFakeClock()
	loop := newRebalanceLoop(pc, src, &coordRebalanceMigrator{coord: coord}, clock, func(format string, args ...any) {
		t.Logf("rebalance: "+format, args...)
	})

	// First tick arms host-a in firstOverload with time=clock.now.
	loop.evaluate()

	// Advance past the sustain window and evaluate again. This call should
	// route through coordRebalanceMigrator → orchestrator.BeginMigrate →
	// real executor → commit, and block the eval() goroutine on Dispatch
	// until the first Ready lands and the orchestrator commits.
	clock.Advance(pc.RebalanceSustainTime + 10*time.Millisecond)
	loop.evaluate()

	// The watchRequest goroutine decrements inflight when req.Done closes;
	// commit happens synchronously inside BeginMigrate → Execute → Receive →
	// OnReady, so by the time evaluate() returns the commit is already in.
	// Poll briefly on inflight to be robust against the watcher scheduling.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		loop.mu.Lock()
		inflight := loop.inflight
		fired := loop.migrationsFired
		loop.mu.Unlock()
		if fired >= 1 && inflight == 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	loop.mu.Lock()
	migrationsFired := loop.migrationsFired
	loop.mu.Unlock()
	if migrationsFired == 0 {
		t.Fatal("post-evaluate: no migrations fired — rebalance → BeginMigrate wiring broken")
	}

	// Verify the commit landed in the real cellToHostMap. Since the
	// scripted source injected load on an arbitrary host-a cell, the
	// migration winds up picking the lexicographically-first cell by the
	// pickHeaviestCell tiebreak — which may or may not be overloadedCell.
	// So instead we assert that AT LEAST ONE cell previously on host-a
	// is now on host-b.
	postOwnership := coord.snapshotCellOwnership()

	moved := 0
	for k, v := range preOwnership {
		if v == "host-a" && postOwnership[k] == "host-b" {
			moved++
		}
	}
	if moved == 0 {
		t.Errorf("post-rebalance: no cells migrated from host-a to host-b (pre=%v post=%v)",
			preOwnership, postOwnership)
	}
	t.Logf("post-rebalance: %d cell(s) moved host-a → host-b", moved)
}
