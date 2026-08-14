# Dual-Topology Test Harness Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a `pkg/universe` test harness that runs the 8 target test files' scenarios under both colocated and distributed topologies, so the "works in all-preset, fails in split-process" bug class gets caught at CI time rather than production time. No production code changes.

**Architecture:** New unexported `clusterFixture` interface + `FixtureConfig` + `forEachTopology(t, cfg, body)` helper. Two concrete implementations — `colocatedFixture` wraps today's `newMigrateTestCoord` pattern; `distributedFixture` spins up one RoleCoordinator `*Coordinator` on `127.0.0.1:0` plus N RoleHost `*Coordinator`s dialing it over real gRPC MeshControl (the exact pattern already in `s4_control_plane_test.go`). Deterministic layout is seeded in distributed mode via `assignmentEngine.dispatchCellAssign` per declared placement, with the 5s settle window bypassed by flipping `assignmentEngine.settled = true` before hosts register. The fixture API exposes just enough to answer topology-agnostic questions (`CellOwner`, `HostOwnsCell`, `CellOn`, `WaitForCellOwner`), and 20 existing tests are migrated in-place to use it.

**Tech Stack:** Go 1.22+, existing `pkg/universe` (Coordinator, HostRegistry, meshControlServer, assignmentEngine, `waitFor` helper from `s4_control_plane_test.go:155`, `spawnTestEntity` + `execOnLoop` from `cell_transfer_executor_test.go:86-111`), google.golang.org/grpc (already a dependency). No new modules.

**Reference files the implementor should read before starting:**
- `docs/superpowers/specs/2026-04-18-dual-topology-test-harness-design.md` — the spec for this plan
- `pkg/universe/s4_control_plane_test.go` — already demonstrates coord+remote-host gRPC loopback; the distributed fixture is a generalization of this
- `pkg/universe/s7_migrate_test.go:36-65` — `newMigrateTestCoord` is what the colocated fixture replaces
- `pkg/universe/coord_assignment.go:181-225` — assignment engine internals we touch for fast seeding

---

## File structure

**New files (all `_test.go` — test-only, no production code):**
- `pkg/universe/cluster_fixture_test.go` — `clusterFixture` interface, `FixtureConfig`, `forEachTopology`, `colocatedFixture` (colocated is small enough to live alongside the shared types)
- `pkg/universe/cluster_fixture_distributed_test.go` — `distributedFixture` (gRPC bring-up + seeding)
- `pkg/universe/cluster_fixture_smoke_test.go` — self-tests that exercise the fixture itself under both modes

**Modified files (migration targets — Tasks 7-14 rewrite assertions to go through the fixture):**
- `pkg/universe/s7_migrate_test.go`
- `pkg/universe/s7_migrate_epoch_test.go`
- `pkg/universe/s7_split_test.go`
- `pkg/universe/s7_merge_test.go`
- `pkg/universe/s7_graceful_shutdown_test.go`
- `pkg/universe/s7_concurrent_test.go`
- `pkg/universe/s6_gateway_test.go`
- `pkg/universe/partition_test.go`

---

## Task 1: Scaffold shared types + forEachTopology helper

**Files:**
- Create: `pkg/universe/cluster_fixture_test.go`

**Goal:** Define the `clusterFixture` interface, `FixtureConfig`, and `forEachTopology` helper. Colocated + distributed implementations come in later tasks. After this task the package builds but the fixture has no topology implementations yet.

- [ ] **Step 1: Write the file skeleton**

Create `pkg/universe/cluster_fixture_test.go`:

```go
package universe

import (
	"context"
	"fmt"
	"sort"
	"testing"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// Dual-topology test harness — see
// docs/superpowers/specs/2026-04-18-dual-topology-test-harness-design.md
//
// Tests that want to run under both colocated and distributed topologies
// use forEachTopology. Each subtest receives a clusterFixture that answers
// ownership/layout questions identically regardless of wire path, so the
// test body doesn't know or care which mode it's running under.
// ─────────────────────────────────────────────────────────────────────────────

// clusterFixture is the topology-abstract handle test bodies receive.
// Implementations: colocatedFixture, distributedFixture.
type clusterFixture interface {
	// Coord returns the coord-role Coordinator. Test code drives
	// orchestrator.BeginSplit/Merge/Migrate through Coord().orchestrator.
	Coord() *Coordinator

	// HostIDs returns every host participating in the cluster, in the
	// deterministic order FixtureConfig.HostIDs declared.
	HostIDs() []string

	// CellOwner returns the host ID currently owning cellKey, or "" if
	// no host owns it. Always goes through HostForCellID so the answer
	// matches production code.
	CellOwner(cellKey string) string

	// HostOwnsCell is true iff the named host has an in-process *Cell
	// for this key on its own side.
	HostOwnsCell(hostID, cellKey string) bool

	// CellOn returns the in-process *Cell on the given host, or nil if
	// that host doesn't own it. Used by tests for execOnLoop and direct
	// ECS access.
	CellOn(hostID, cellKey string) *Cell

	// WaitForCellOwner blocks until cellKey is owned by hostID, or ctx
	// expires. Colocated returns immediately; distributed polls the
	// coord's hostRegistry until the CellReady roundtrip lands.
	WaitForCellOwner(ctx context.Context, cellKey, hostID string) error
}

// FixtureConfig declares the cluster shape both topologies build against.
// Defaults (applied in normalize()): 2×2 grid, ["host-a","host-b"] hosts,
// CellSize 1024. Layout defaults to column-first round-robin, matching
// the colocated TestHosts placement: (0,0)→host-a, (1,0)→host-b,
// (0,1)→host-a, (1,1)→host-b.
type FixtureConfig struct {
	CellsX   uint32
	CellsY   uint32
	CellSize float32
	HostIDs  []string

	// Layout maps each cell key (MeshCellID form) to the host that
	// should own it at fixture creation. Leave nil to use the default
	// column-first round-robin over HostIDs.
	Layout map[string]string
}

func (cfg *FixtureConfig) normalize() {
	if cfg.CellsX == 0 {
		cfg.CellsX = 2
	}
	if cfg.CellsY == 0 {
		cfg.CellsY = 2
	}
	if cfg.CellSize == 0 {
		cfg.CellSize = 1024
	}
	if len(cfg.HostIDs) == 0 {
		cfg.HostIDs = []string{"host-a", "host-b"}
	}
	if cfg.Layout == nil {
		cfg.Layout = defaultRoundRobinLayout(cfg.CellsX, cfg.CellsY, cfg.HostIDs)
	}
}

// defaultRoundRobinLayout reproduces the column-first placement that
// Coordinator.Build() applies to TestHosts. Scanning order: for each row
// y, for each column x, assign cell (x,y) to hosts[i%N] where i is the
// visit index. This matches newMigrateTestCoord's behaviour so tests
// that hardcoded "host-a" keep passing.
func defaultRoundRobinLayout(cellsX, cellsY uint32, hostIDs []string) map[string]string {
	out := make(map[string]string, cellsX*cellsY)
	i := 0
	for y := uint32(0); y < cellsY; y++ {
		for x := uint32(0); x < cellsX; x++ {
			key := MeshCellID(CellID{X: int32(x), Y: int32(y)})
			out[key] = hostIDs[i%len(hostIDs)]
			i++
		}
	}
	return out
}

// topoBuilder constructs one fixture. Registered in the topologies table
// below so adding a new topology means one line here plus an impl.
type topoBuilder struct {
	name  string
	build func(t *testing.T, cfg FixtureConfig) clusterFixture
}

var topologies = []topoBuilder{
	{name: "colocated", build: newColocatedFixture},
	{name: "distributed", build: newDistributedFixture},
}

// forEachTopology runs body once per registered topology as a subtest,
// passing a fresh clusterFixture each time. Cleanup happens via t.Cleanup
// registered inside the builders.
func forEachTopology(t *testing.T, cfg FixtureConfig, body func(t *testing.T, fx clusterFixture)) {
	t.Helper()
	for _, topo := range topologies {
		t.Run(topo.name, func(t *testing.T) {
			// Copy the config so one subtest can't mutate a map the next
			// subtest reads.
			copied := cfg
			if cfg.HostIDs != nil {
				copied.HostIDs = append([]string(nil), cfg.HostIDs...)
			}
			if cfg.Layout != nil {
				copied.Layout = make(map[string]string, len(cfg.Layout))
				for k, v := range cfg.Layout {
					copied.Layout[k] = v
				}
			}
			copied.normalize()
			fx := topo.build(t, copied)
			body(t, fx)
		})
	}
}

// sortedKeys returns map keys in sorted order — used by fixture methods
// that need deterministic iteration without leaking map-order nondeterminism
// into tests.
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// waitForCellOwnerViaRegistry polls coord.HostForCellID every 25ms until
// it returns hostID or ctx expires. Reused by both fixtures; the only
// difference between them is whether they need to poll at all.
func waitForCellOwnerViaRegistry(ctx context.Context, coord *Coordinator, cellKey, hostID string) error {
	tick := time.NewTicker(25 * time.Millisecond)
	defer tick.Stop()
	for {
		if coord.HostForCellID(cellKey) == hostID {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("waitForCellOwner: %s not owned by %s before deadline (current owner=%q)",
				cellKey, hostID, coord.HostForCellID(cellKey))
		case <-tick.C:
		}
	}
}

// Stub builders — implementations land in Tasks 2 and 3/4. Keeps the
// package compiling until then.
func newColocatedFixture(t *testing.T, cfg FixtureConfig) clusterFixture {
	t.Helper()
	t.Fatal("newColocatedFixture not yet implemented — Task 2")
	return nil
}

func newDistributedFixture(t *testing.T, cfg FixtureConfig) clusterFixture {
	t.Helper()
	t.Fatal("newDistributedFixture not yet implemented — Task 3/4")
	return nil
}
```

- [ ] **Step 2: Verify the package compiles**

Run: `go vet ./pkg/universe/...`
Expected: no output (clean).

Run: `go test ./pkg/universe/ -run TestOrchestratorBeginMigrateSingleDispatch -count=1`
Expected: PASS — sanity check that the new file didn't break the existing build.

- [ ] **Step 3: Commit**

```bash
git add pkg/universe/cluster_fixture_test.go
git commit -m "$(cat <<'EOF'
test(universe): scaffold clusterFixture interface + forEachTopology helper

Scaffolds the dual-topology test harness: the clusterFixture interface,
FixtureConfig (with normalize() defaults), the forEachTopology helper
that runs test bodies under each registered topology, and stub builders.
Implementations land in follow-up tasks.

No production code changes; pure test-only additions behind package-private
identifiers.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 2: Implement colocated fixture

**Files:**
- Modify: `pkg/universe/cluster_fixture_test.go` (replace the stubbed `newColocatedFixture`)

**Goal:** Wrap today's `newMigrateTestCoord` pattern into a `clusterFixture` implementation. Since colocated uses the existing `TestHosts` + `Build()` path, layout is already deterministic via round-robin; no extra seeding needed.

- [ ] **Step 1: Replace the stub with the real implementation**

In `pkg/universe/cluster_fixture_test.go`, replace the `newColocatedFixture` stub with:

```go
// colocatedFixture wraps a single Coordinator running Roles={coordinator,
// host, gateway} with TestHosts populated. Matches today's
// newMigrateTestCoord behaviour.
type colocatedFixture struct {
	t     *testing.T
	coord *Coordinator
	hosts []string
}

func newColocatedFixture(t *testing.T, cfg FixtureConfig) clusterFixture {
	t.Helper()
	coords.SetCellSize(cfg.CellSize)

	coord := NewCoordinator(Config{
		CellsX:       cfg.CellsX,
		CellsY:       cfg.CellsY,
		CellSize:     cfg.CellSize,
		TestHosts:    cfg.HostIDs,
		Headless:     true,
		ConnManager:  net.NewConnManager(),
		Logger:       logger.New(),
		LoginHandler: func(connID uint32, msgs [][]byte) (string, any, error) { return "", nil, ErrLoginPending },
	})
	coord.SetWorld(func(base *WorldBase) GameWorld { return base })
	coord.Build()

	ctx, cancel := context.WithCancel(context.Background())
	for _, cell := range coord.Cells {
		go cell.Run(ctx)
	}
	// Let every cell drain its first admin-cmd pass before anything else
	// runs. Matches newMigrateTestCoord's 20ms sleep.
	time.Sleep(20 * time.Millisecond)

	t.Cleanup(func() {
		cancel()
		coord.Shutdown()
	})

	return &colocatedFixture{t: t, coord: coord, hosts: append([]string(nil), cfg.HostIDs...)}
}

func (f *colocatedFixture) Coord() *Coordinator { return f.coord }
func (f *colocatedFixture) HostIDs() []string   { return f.hosts }

func (f *colocatedFixture) CellOwner(cellKey string) string {
	return f.coord.HostForCellID(cellKey)
}

func (f *colocatedFixture) HostOwnsCell(hostID, cellKey string) bool {
	f.coord.mu.RLock()
	h, ok := f.coord.Hosts[hostID]
	f.coord.mu.RUnlock()
	if !ok || h == nil {
		return false
	}
	return h.CellByID(cellKey) != nil
}

func (f *colocatedFixture) CellOn(hostID, cellKey string) *Cell {
	f.coord.mu.RLock()
	h, ok := f.coord.Hosts[hostID]
	f.coord.mu.RUnlock()
	if !ok || h == nil {
		return nil
	}
	return h.CellByID(cellKey)
}

func (f *colocatedFixture) WaitForCellOwner(ctx context.Context, cellKey, hostID string) error {
	// Colocated placement is synchronous; no wait needed. Still poll in
	// case the caller passed a cellKey that isn't owned (so the error
	// message comes from the shared helper, not from a silent mismatch).
	return waitForCellOwnerViaRegistry(ctx, f.coord, cellKey, hostID)
}
```

- [ ] **Step 2: Add the import for `net` and `logger`**

At the top of `cluster_fixture_test.go`, update the import block:

```go
import (
	"context"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/mmokit/mmokit/pkg/coords"
	"github.com/mmokit/mmokit/pkg/logger"
	"github.com/mmokit/mmokit/pkg/net"
)
```

- [ ] **Step 3: Verify compile**

Run: `go vet ./pkg/universe/...`
Expected: no output.

- [ ] **Step 4: Quick smoke via inline test**

Temporarily add this test to `cluster_fixture_test.go` and run it:

```go
func TestColocatedFixtureSmoke_Ephemeral(t *testing.T) {
	fx := newColocatedFixture(t, FixtureConfig{CellsX: 2, CellsY: 2, HostIDs: []string{"host-a", "host-b"}})
	(&FixtureConfig{CellsX: 2, CellsY: 2, HostIDs: []string{"host-a", "host-b"}}).normalize()
	key := MeshCellID(CellID{X: 0, Y: 0})
	if owner := fx.CellOwner(key); owner != "host-a" {
		t.Fatalf("CellOwner(%s) = %q, want host-a", key, owner)
	}
	if !fx.HostOwnsCell("host-a", key) {
		t.Fatal("host-a should own cell_0_0")
	}
	if fx.HostOwnsCell("host-b", key) {
		t.Fatal("host-b should not own cell_0_0")
	}
	if fx.CellOn("host-a", key) == nil {
		t.Fatal("CellOn returned nil for owner")
	}
}
```

Run: `go test ./pkg/universe/ -run TestColocatedFixtureSmoke_Ephemeral -v -count=1`
Expected: PASS.

- [ ] **Step 5: Remove the ephemeral test**

Delete `TestColocatedFixtureSmoke_Ephemeral` — the real smoke tests land in Task 5.

- [ ] **Step 6: Commit**

```bash
git add pkg/universe/cluster_fixture_test.go
git commit -m "$(cat <<'EOF'
test(universe): implement colocatedFixture

Wraps the existing newMigrateTestCoord pattern (one Coordinator with
TestHosts) behind the clusterFixture interface. All methods resolve
against the single Coordinator via HostForCellID + coord.Hosts[h].
WaitForCellOwner uses the shared polling helper but returns fast since
colocated placement is synchronous.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 3: Distributed fixture skeleton — coord + host bring-up

**Files:**
- Create: `pkg/universe/cluster_fixture_distributed_test.go`
- Modify: `pkg/universe/cluster_fixture_test.go` (delete the `newDistributedFixture` stub — real one lives in the new file)

**Goal:** Stand up one RoleCoordinator process on `127.0.0.1:0` and N RoleHost processes that dial it, matching the pattern from `s4_control_plane_test.go:29-104`. Wait for all hosts to register in `hostRegistry`. No cell assignment yet — that's Task 4.

- [ ] **Step 1: Delete the stub from `cluster_fixture_test.go`**

Remove the `newDistributedFixture` stub function from `cluster_fixture_test.go`. The real one lives in the new file.

- [ ] **Step 2: Create `cluster_fixture_distributed_test.go`**

```go
package universe

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/mmokit/mmokit/pkg/coords"
	"github.com/mmokit/mmokit/pkg/logger"
	"github.com/mmokit/mmokit/pkg/net"
)

// distributedFixture spins up one coord-role *Coordinator (Mode=coordinator,
// ControlListen=127.0.0.1:0) plus N host-role *Coordinators (Mode=host,
// CoordinatorAddr=<coord addr>), connected via real gRPC MeshControl —
// the same wire path production uses. Layout seeding lands in Task 4.
type distributedFixture struct {
	t     *testing.T
	coord *Coordinator
	hosts map[string]*Coordinator // hostID -> host-role *Coordinator
	order []string                // declared host order (for HostIDs())
}

func newDistributedFixture(t *testing.T, cfg FixtureConfig) clusterFixture {
	t.Helper()
	coords.SetCellSize(cfg.CellSize)

	// 1. Coord-role process on an ephemeral port.
	coord := NewCoordinator(Config{
		CellsX:        cfg.CellsX,
		CellsY:        cfg.CellsY,
		CellSize:      cfg.CellSize,
		Mode:          "coordinator",
		ControlListen: "127.0.0.1:0",
		Headless:      true,
		ConnManager:   net.NewConnManager(),
		Logger:        logger.New(),
		LoginHandler:  func(connID uint32, msgs [][]byte) (string, any, error) { return "", nil, ErrLoginPending },
	})
	coord.SetWorld(func(base *WorldBase) GameWorld { return base })
	coord.Build()
	t.Cleanup(coord.Shutdown)

	coordAddr := coord.controlListener.Addr().String()

	// 2. One host-role process per host ID, each dialing the coord.
	hosts := make(map[string]*Coordinator, len(cfg.HostIDs))
	for _, hid := range cfg.HostIDs {
		host := NewCoordinator(Config{
			CellsX:          cfg.CellsX,
			CellsY:          cfg.CellsY,
			CellSize:        cfg.CellSize,
			Mode:            "host",
			CoordinatorAddr: coordAddr,
			HostID:          hid,
			Headless:        true,
			ConnManager:     net.NewConnManager(),
			Logger:          logger.New(),
			LoginHandler:    func(connID uint32, msgs [][]byte) (string, any, error) { return "", nil, ErrLoginPending },
		})
		host.SetWorld(func(base *WorldBase) GameWorld { return base })
		host.Build()
		t.Cleanup(host.Shutdown)
		hosts[hid] = host
	}

	// 3. Wait until every host has reached the coord's hostRegistry. The
	// settle loop may still be running; that's fine — Task 4 bypasses it
	// before seeding layout.
	regDeadline := time.Now().Add(3 * time.Second)
	for _, hid := range cfg.HostIDs {
		for {
			if rh := coord.hostRegistry.Get(hid); rh != nil {
				break
			}
			if time.Now().After(regDeadline) {
				t.Fatalf("distributedFixture: host %q failed to register within 3s", hid)
			}
			time.Sleep(25 * time.Millisecond)
		}
	}

	return &distributedFixture{
		t:     t,
		coord: coord,
		hosts: hosts,
		order: append([]string(nil), cfg.HostIDs...),
	}
}

func (f *distributedFixture) Coord() *Coordinator { return f.coord }
func (f *distributedFixture) HostIDs() []string   { return f.order }

func (f *distributedFixture) CellOwner(cellKey string) string {
	return f.coord.HostForCellID(cellKey)
}

// HostOwnsCell reaches into the host-role Coordinator's local Hosts map.
// Each host-role *Coordinator has exactly one local Host (itself). The
// localHost() method returns that entry.
func (f *distributedFixture) HostOwnsCell(hostID, cellKey string) bool {
	host, ok := f.hosts[hostID]
	if !ok {
		return false
	}
	lh := host.localHost()
	if lh == nil {
		return false
	}
	return lh.CellByID(cellKey) != nil
}

func (f *distributedFixture) CellOn(hostID, cellKey string) *Cell {
	host, ok := f.hosts[hostID]
	if !ok {
		return nil
	}
	lh := host.localHost()
	if lh == nil {
		return nil
	}
	return lh.CellByID(cellKey)
}

func (f *distributedFixture) WaitForCellOwner(ctx context.Context, cellKey, hostID string) error {
	if err := waitForCellOwnerViaRegistry(ctx, f.coord, cellKey, hostID); err != nil {
		return fmt.Errorf("distributedFixture: %w", err)
	}
	return nil
}
```

- [ ] **Step 3: Verify compile**

Run: `go vet ./pkg/universe/...`
Expected: no output.

- [ ] **Step 4: Manual smoke — bring-up only**

Temporarily add this ephemeral test to `cluster_fixture_distributed_test.go`:

```go
func TestDistributedFixtureBringUp_Ephemeral(t *testing.T) {
	fx := newDistributedFixture(t, FixtureConfig{CellsX: 2, CellsY: 2, HostIDs: []string{"host-a", "host-b"}})
	df := fx.(*distributedFixture)
	if df.coord == nil {
		t.Fatal("coord nil")
	}
	if len(df.hosts) != 2 {
		t.Fatalf("hosts=%d want 2", len(df.hosts))
	}
	if df.coord.hostRegistry.Get("host-a") == nil {
		t.Fatal("host-a not in registry")
	}
	if df.coord.hostRegistry.Get("host-b") == nil {
		t.Fatal("host-b not in registry")
	}
}
```

Run: `go test ./pkg/universe/ -run TestDistributedFixtureBringUp_Ephemeral -v -count=1 -timeout 30s`
Expected: PASS. No cells assigned yet — that's Task 4.

- [ ] **Step 5: Delete the ephemeral test**

Remove `TestDistributedFixtureBringUp_Ephemeral` — smoke tests land in Task 5.

- [ ] **Step 6: Commit**

```bash
git add pkg/universe/cluster_fixture_distributed_test.go pkg/universe/cluster_fixture_test.go
git commit -m "$(cat <<'EOF'
test(universe): distributedFixture skeleton — coord + host bring-up

Stands up one RoleCoordinator Coordinator on 127.0.0.1:0 and N RoleHost
Coordinators dialing it over real gRPC MeshControl, following the pattern
from s4_control_plane_test.go. Waits for all hosts to register in the
coord's hostRegistry. No cell assignment yet — Task 4 adds deterministic
layout seeding.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 4: Distributed layout seeding (bypass settle window)

**Files:**
- Modify: `pkg/universe/cluster_fixture_distributed_test.go`

**Goal:** After host registration, place each cell on its declared host (per `cfg.Layout`) by driving `assignmentEngine.dispatchCellAssign` directly. Bypass the 5s settle window by flipping `assignmentEngine.settled = true` before dispatching, so the natural rendezvous rebalance doesn't override our seeding.

- [ ] **Step 1: Add layout seeding to `newDistributedFixture`**

In `cluster_fixture_distributed_test.go`, insert the seeding block between step 3 (registration wait) and the final `return` in `newDistributedFixture`:

```go
	// 4. Bypass the 5s settle window — our tests seed layout explicitly.
	// Flip settled before dispatching so the settle loop's first rebalance
	// is a no-op rather than a rendezvous placement that would stomp our
	// seeding. firstRegistered is already true (set by onHostRegistered
	// during step 3), so the settle loop has already picked up the clock.
	ae := coord.assignmentEngine
	ae.mu.Lock()
	ae.settled = true
	ae.mu.Unlock()

	// 5. Drive CellAssign for every (cell, host) in the declared layout.
	// dispatchCellAssign sends NetIDRangeGrant + CellAssign over MeshControl;
	// the host executor creates the cell and responds with CellReady,
	// which updates hostRegistry.OwnedCells on the coord side.
	for _, cellKey := range sortedKeys(cfg.Layout) {
		hostID := cfg.Layout[cellKey]
		ae.dispatchCellAssign(hostID, cellKey)
	}

	// 6. Wait for every (cell, host) pair to land in the registry. Deadline
	// is generous — per-cell createNode + CellReady roundtrip is well under
	// 500ms in practice.
	waitCtx, waitCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer waitCancel()
	for _, cellKey := range sortedKeys(cfg.Layout) {
		hostID := cfg.Layout[cellKey]
		if err := waitForCellOwnerViaRegistry(waitCtx, coord, cellKey, hostID); err != nil {
			t.Fatalf("distributedFixture: seed %s -> %s: %v", cellKey, hostID, err)
		}
	}
```

- [ ] **Step 2: Verify compile**

Run: `go vet ./pkg/universe/...`
Expected: no output.

- [ ] **Step 3: Full-path smoke**

Add this ephemeral test to `cluster_fixture_distributed_test.go`:

```go
func TestDistributedFixtureSeeded_Ephemeral(t *testing.T) {
	fx := newDistributedFixture(t, FixtureConfig{CellsX: 2, CellsY: 2, HostIDs: []string{"host-a", "host-b"}})
	key := MeshCellID(CellID{X: 0, Y: 0})
	if owner := fx.CellOwner(key); owner != "host-a" {
		t.Fatalf("CellOwner(%s) = %q, want host-a", key, owner)
	}
	if !fx.HostOwnsCell("host-a", key) {
		t.Fatal("host-a should own cell_0_0 in distributed mode")
	}
	if fx.HostOwnsCell("host-b", key) {
		t.Fatal("host-b should not own cell_0_0")
	}
	if fx.CellOn("host-a", key) == nil {
		t.Fatal("CellOn returned nil for owner")
	}
}
```

Run: `go test ./pkg/universe/ -run TestDistributedFixtureSeeded_Ephemeral -v -count=1 -timeout 30s`
Expected: PASS within ~2s.

- [ ] **Step 4: Remove the ephemeral test**

Delete `TestDistributedFixtureSeeded_Ephemeral` — proper smoke tests land next.

- [ ] **Step 5: Commit**

```bash
git add pkg/universe/cluster_fixture_distributed_test.go
git commit -m "$(cat <<'EOF'
test(universe): distributedFixture layout seeding

After all hosts register, flip assignmentEngine.settled=true to suppress
the natural rendezvous rebalance, then drive dispatchCellAssign directly
for each (cell, host) in the declared layout. Wait via the registry-based
polling helper until every (cell, host) pair lands in hostRegistry.

This makes distributed placement deterministic AND fast — no 5s settle
window wait per test.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 5: Fixture smoke tests

**Files:**
- Create: `pkg/universe/cluster_fixture_smoke_test.go`

**Goal:** Tests that exercise the fixture itself — setup, API surface, teardown — under both topologies. These are the fixture's own regression tests; if they break, the migration tasks land on a broken foundation.

- [ ] **Step 1: Create the smoke test file**

```go
package universe

import (
	"context"
	"testing"
	"time"
)

// TestFixtureSmoke_Ownership exercises CellOwner + HostOwnsCell + CellOn
// across the default 2×2 / 2-host layout.
func TestFixtureSmoke_Ownership(t *testing.T) {
	forEachTopology(t, FixtureConfig{}, func(t *testing.T, fx clusterFixture) {
		type want struct {
			hostA bool
			hostB bool
			owner string
		}
		cases := map[string]want{
			MeshCellID(CellID{X: 0, Y: 0}): {hostA: true, owner: "host-a"},
			MeshCellID(CellID{X: 1, Y: 0}): {hostB: true, owner: "host-b"},
			MeshCellID(CellID{X: 0, Y: 1}): {hostA: true, owner: "host-a"},
			MeshCellID(CellID{X: 1, Y: 1}): {hostB: true, owner: "host-b"},
		}
		for key, w := range cases {
			if got := fx.CellOwner(key); got != w.owner {
				t.Errorf("CellOwner(%s) = %q, want %q", key, got, w.owner)
			}
			if got := fx.HostOwnsCell("host-a", key); got != w.hostA {
				t.Errorf("HostOwnsCell(host-a, %s) = %v, want %v", key, got, w.hostA)
			}
			if got := fx.HostOwnsCell("host-b", key); got != w.hostB {
				t.Errorf("HostOwnsCell(host-b, %s) = %v, want %v", key, got, w.hostB)
			}
			if w.hostA && fx.CellOn("host-a", key) == nil {
				t.Errorf("CellOn(host-a, %s) returned nil for owner", key)
			}
			if w.hostB && fx.CellOn("host-b", key) == nil {
				t.Errorf("CellOn(host-b, %s) returned nil for owner", key)
			}
		}
	})
}

// TestFixtureSmoke_HostIDs checks HostIDs returns the declared list in order.
func TestFixtureSmoke_HostIDs(t *testing.T) {
	forEachTopology(t, FixtureConfig{HostIDs: []string{"host-a", "host-b"}}, func(t *testing.T, fx clusterFixture) {
		ids := fx.HostIDs()
		if len(ids) != 2 || ids[0] != "host-a" || ids[1] != "host-b" {
			t.Errorf("HostIDs = %v, want [host-a host-b]", ids)
		}
	})
}

// TestFixtureSmoke_CoordIsCoordRole asserts that fx.Coord() returns a
// Coordinator with the coord role (and the orchestrator wired).
func TestFixtureSmoke_CoordIsCoordRole(t *testing.T) {
	forEachTopology(t, FixtureConfig{}, func(t *testing.T, fx clusterFixture) {
		c := fx.Coord()
		if c == nil {
			t.Fatal("Coord() is nil")
		}
		if c.orchestrator == nil {
			t.Fatal("Coord().orchestrator is nil — not a coord-role Coordinator")
		}
		if !c.Roles().Has(RoleCoordinator) {
			t.Errorf("Coord().Roles()=%s, missing RoleCoordinator", c.Roles())
		}
	})
}

// TestFixtureSmoke_WaitForCellOwner is a no-op on freshly-seeded layouts
// (the cell is already owned) but ensures the API doesn't hang or error.
func TestFixtureSmoke_WaitForCellOwner(t *testing.T) {
	forEachTopology(t, FixtureConfig{}, func(t *testing.T, fx clusterFixture) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		key := MeshCellID(CellID{X: 0, Y: 0})
		if err := fx.WaitForCellOwner(ctx, key, "host-a"); err != nil {
			t.Errorf("WaitForCellOwner: %v", err)
		}
	})
}

// TestFixtureSmoke_MissingHost ensures HostOwnsCell returns false for
// an unknown host ID rather than panicking.
func TestFixtureSmoke_MissingHost(t *testing.T) {
	forEachTopology(t, FixtureConfig{}, func(t *testing.T, fx clusterFixture) {
		key := MeshCellID(CellID{X: 0, Y: 0})
		if fx.HostOwnsCell("host-ghost", key) {
			t.Error("HostOwnsCell returned true for unknown host")
		}
		if fx.CellOn("host-ghost", key) != nil {
			t.Error("CellOn returned non-nil for unknown host")
		}
	})
}
```

- [ ] **Step 2: Run the smoke tests**

Run: `go test ./pkg/universe/ -run TestFixtureSmoke -v -count=1 -timeout 60s`
Expected: all five tests PASS under both `/colocated` and `/distributed`.

- [ ] **Step 3: Commit**

```bash
git add pkg/universe/cluster_fixture_smoke_test.go
git commit -m "$(cat <<'EOF'
test(universe): smoke tests for clusterFixture under both topologies

Exercises CellOwner, HostOwnsCell, CellOn, HostIDs, Coord, and
WaitForCellOwner across the default 2×2 / 2-host layout. Both colocated
and distributed subtests pass — the harness foundation is verified.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 6: Migrate s7_migrate_test.go

**Files:**
- Modify: `pkg/universe/s7_migrate_test.go`

**Goal:** Rewrite `TestS7MigrateAcrossHosts` to use `forEachTopology` + `clusterFixture`. Existing test body stays structurally identical — only the peek-into-coord-internals assertions change.

- [ ] **Step 1: Read the current test**

Run: `cat pkg/universe/s7_migrate_test.go` (or open in editor). Note every place the test touches `coord.cellToHostMap`, `coord.Cells`, `coord.Hosts`, `coord.orchestrator`, or the `srcCell` pointer.

- [ ] **Step 2: Rewrite `TestS7MigrateAcrossHosts`**

Apply these transformations everywhere in the test:

| Old | New |
|---|---|
| `coord, cancel := newMigrateTestCoord(t)` + `t.Cleanup(...)` | wrap body in `forEachTopology(t, FixtureConfig{CellsX:2,CellsY:2,CellSize:1024,HostIDs:[]string{"host-a","host-b"}}, func(t *testing.T, fx clusterFixture) { ... })` |
| `coord.cellToHostMap[key]` | `fx.CellOwner(key)` |
| `coord.Cells[key]` | `fx.CellOn(hostID, key)` — pass the expected owner |
| `coord.Hosts[h].CellByID(key)` | `fx.HostOwnsCell(h, key)` for bool tests; `fx.CellOn(h, key)` for pointer |
| `coord.orchestrator.BeginMigrate(...)` | `fx.Coord().orchestrator.BeginMigrate(...)` |
| bare `coord.Shutdown()` / `cancel()` | remove — fixture handles cleanup via t.Cleanup |

Concrete rewrite of the full test (`pkg/universe/s7_migrate_test.go`, replacing `TestS7MigrateAcrossHosts`):

```go
func TestS7MigrateAcrossHosts(t *testing.T) {
	forEachTopology(t, FixtureConfig{
		CellsX: 2, CellsY: 2, CellSize: 1024,
		HostIDs: []string{"host-a", "host-b"},
	}, func(t *testing.T, fx clusterFixture) {
		srcCellID := CellID{X: 0, Y: 0}
		srcKey := MeshCellID(srcCellID)

		if owner := fx.CellOwner(srcKey); owner != "host-a" {
			t.Fatalf("pre-migrate: expected cell %s on host-a, got %q", srcKey, owner)
		}
		srcCell := fx.CellOn("host-a", srcKey)
		if srcCell == nil {
			t.Fatalf("pre-migrate: cell %s missing from host-a", srcKey)
		}

		// Spawn two real entities on the source cell's game loop.
		execOnLoop(t, srcCell, func() {
			spawnTestEntity(srcCell, 4242, 10, 20)
			spawnTestEntity(srcCell, 4343, 30, 40)
		})

		req, err := fx.Coord().orchestrator.BeginMigrate(srcCellID, "host-b")
		if err != nil {
			t.Fatalf("BeginMigrate: %v", err)
		}
		select {
		case <-req.Done:
		case <-time.After(5 * time.Second):
			t.Fatalf("BeginMigrate req=%d did not complete in 5s", req.ID)
		}
		if req.Result != nil {
			t.Fatalf("BeginMigrate req=%d failed: %v", req.ID, req.Result)
		}

		// Invariant 1: ownership flipped.
		if owner := fx.CellOwner(srcKey); owner != "host-b" {
			t.Errorf("post-commit: CellOwner(%s) = %q, want host-b", srcKey, owner)
		}

		// Invariant 2: host-b has the cell locally.
		destCell := fx.CellOn("host-b", srcKey)
		if destCell == nil {
			t.Fatalf("post-commit: host-b has no cell %s", srcKey)
		}

		// Invariant 3: host-a has released the cell.
		if fx.HostOwnsCell("host-a", srcKey) {
			t.Errorf("post-commit: source host still owns cell %s", srcKey)
		}

		// Invariant 4: entities moved (netID preserved, positions match).
		foundNetIDs := map[uint32]bool{}
		execOnLoop(t, destCell, func() {
			netIDMap := ecs.NewMap1[component.NetworkID](destCell.Engine.ECS)
			posMap := ecs.NewMap1[component.Position](destCell.Engine.ECS)
			for e := range ecs.FilterN(destCell.Engine.ECS, netIDMap).Iter() {
				nid := netIDMap.Get(e).ID
				foundNetIDs[nid] = true
				p := posMap.Get(e)
				switch nid {
				case 4242:
					if p.X != 10 || p.Y != 20 {
						t.Errorf("netID=4242 pos=(%v,%v) want (10,20)", p.X, p.Y)
					}
				case 4343:
					if p.X != 30 || p.Y != 40 {
						t.Errorf("netID=4343 pos=(%v,%v) want (30,40)", p.X, p.Y)
					}
				}
			}
		})
		if !foundNetIDs[4242] || !foundNetIDs[4343] {
			t.Errorf("missing migrated entities: got %v", foundNetIDs)
		}
	})
}
```

Imports at the top of the file — keep the existing block; adjust if some become unused. After the rewrite, the test no longer references `newMigrateTestCoord`, so if that helper becomes unused after all migrations, leave it for now; removal comes in a later cleanup.

- [ ] **Step 3: Run the migrated test under both topologies**

Run: `go test ./pkg/universe/ -run TestS7MigrateAcrossHosts -v -count=1 -timeout 60s`
Expected: both `TestS7MigrateAcrossHosts/colocated` and `TestS7MigrateAcrossHosts/distributed` PASS.

- [ ] **Step 4: Commit**

```bash
git add pkg/universe/s7_migrate_test.go
git commit -m "$(cat <<'EOF'
test(universe): migrate TestS7MigrateAcrossHosts to dual-topology harness

Rewrites peek assertions to go through clusterFixture. Test body and
invariants unchanged — just accessor substitution. Passes in both
/colocated and /distributed subtests.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 7: Migrate s7_migrate_epoch_test.go

**Files:**
- Modify: `pkg/universe/s7_migrate_epoch_test.go`

- [ ] **Step 1: Rewrite `TestMigrateEpochSourceCellReleased`**

Apply the same substitution table as Task 6. The test has one extra concern — it seeds a `SessionRoute` directly via `coord.sessionRoutes.Set(...)`. That API stays reachable through `fx.Coord().sessionRoutes.Set(...)` — no fixture method needed; `Coord()` is the escape hatch for things not worth abstracting.

Replace the body of `TestMigrateEpochSourceCellReleased`:

```go
func TestMigrateEpochSourceCellReleased(t *testing.T) {
	forEachTopology(t, FixtureConfig{
		CellsX: 2, CellsY: 2, CellSize: 1024,
		HostIDs: []string{"host-a", "host-b"},
	}, func(t *testing.T, fx clusterFixture) {
		srcCellID := CellID{X: 0, Y: 0}
		srcKey := MeshCellID(srcCellID)

		srcHost := fx.CellOwner(srcKey)
		if srcHost == "" {
			t.Fatalf("pre-migrate: cell %s has no owner", srcKey)
		}
		destHost := "host-b"
		if srcHost == "host-b" {
			destHost = "host-a"
		}

		srcCell := fx.CellOn(srcHost, srcKey)
		if srcCell == nil {
			t.Fatalf("pre-migrate: cell %s missing from host %s", srcKey, srcHost)
		}
		execOnLoop(t, srcCell, func() {
			spawnTestEntity(srcCell, 7001, 100, 200)
			spawnTestEntity(srcCell, 7002, 300, 400)
		})

		const testConnID = uint32(9900)
		const initialEpoch = uint64(3)
		fx.Coord().sessionRoutes.Set(&SessionRoute{
			Key:      SessionKey{GatewayID: InprocGatewayID, ConnID: testConnID},
			Username: "epoch-test-player",
			HostID:   srcHost,
			CellID:   srcKey,
			Epoch:    initialEpoch,
		})

		req, err := fx.Coord().orchestrator.BeginMigrate(srcCellID, destHost)
		if err != nil {
			t.Fatalf("BeginMigrate: %v", err)
		}
		select {
		case <-req.Done:
		case <-time.After(5 * time.Second):
			t.Fatalf("BeginMigrate req=%d did not complete in 5s", req.ID)
		}
		if req.Result != nil {
			t.Fatalf("BeginMigrate req=%d failed: %v", req.ID, req.Result)
		}

		if got := fx.CellOwner(srcKey); got != destHost {
			t.Errorf("post-migrate CellOwner(%s) = %q, want %q", srcKey, got, destHost)
		}
		if fx.HostOwnsCell(srcHost, srcKey) {
			t.Errorf("post-migrate: source host %q still owns cell %s — CellRelease did not run", srcHost, srcKey)
		}
		if !fx.HostOwnsCell(destHost, srcKey) {
			t.Errorf("post-migrate: dest host %q has no cell %s", destHost, srcKey)
		}

		route, ok := fx.Coord().sessionRoutes.Get(SessionKey{GatewayID: InprocGatewayID, ConnID: testConnID})
		if !ok {
			t.Fatal("post-migrate: session route missing")
		}
		if route.Epoch <= initialEpoch {
			t.Errorf("post-migrate: session epoch=%d, want > initial %d", route.Epoch, initialEpoch)
		}
	})
}
```

- [ ] **Step 2: Run**

Run: `go test ./pkg/universe/ -run TestMigrateEpochSourceCellReleased -v -count=1 -timeout 60s`
Expected: both subtests PASS.

- [ ] **Step 3: Commit**

```bash
git add pkg/universe/s7_migrate_epoch_test.go
git commit -m "$(cat <<'EOF'
test(universe): migrate TestMigrateEpochSourceCellReleased to harness

Same mechanical rewrite as TestS7MigrateAcrossHosts — assertions go
through clusterFixture, sessionRoute seeding stays via fx.Coord()
escape hatch. Both /colocated and /distributed pass.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 8: Migrate s7_split_test.go

**Files:**
- Modify: `pkg/universe/s7_split_test.go`

**Goal:** Rewrite every test in the file to use `forEachTopology`. Split tests assert post-split: parent gone from ownership, 4 children exist, children distributed across hosts per rendezvous.

- [ ] **Step 1: Apply the same substitution to every `Test*` function in the file**

Use the substitution table from Task 6. For each test:
1. Wrap the body in `forEachTopology(t, cfg, func(t *testing.T, fx clusterFixture) { ... })`.
2. Replace `coord.cellToHostMap[key]` with `fx.CellOwner(key)`.
3. Replace `coord.Cells[key]` / `coord.CellOwner[cell]` peeks with the appropriate fixture call (`CellOn`, `HostOwnsCell`, or `CellOwner` depending on what's being asserted).
4. Route orchestrator calls through `fx.Coord().orchestrator`.
5. Keep the invariant assertions (and their error messages) identical — only the accessor on the left-hand side changes.

For any test that asserts on the 4 children's ownership post-split, read the owner via `fx.CellOwner(childKey)` and assert it's non-empty + is in `fx.HostIDs()`.

- [ ] **Step 2: Run**

Run: `go test ./pkg/universe/ -run TestS7Split -v -count=1 -timeout 120s`
(Adjust the `-run` regex to match whatever test-function names live in the file.)
Expected: every test PASSes under both `/colocated` and `/distributed`.

- [ ] **Step 3: Commit**

```bash
git add pkg/universe/s7_split_test.go
git commit -m "$(cat <<'EOF'
test(universe): migrate s7_split_test.go to dual-topology harness

Wraps every split test in forEachTopology and rewrites peek assertions
through clusterFixture. Invariants and failure messages unchanged.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 9: Migrate s7_merge_test.go

**Files:**
- Modify: `pkg/universe/s7_merge_test.go`

- [ ] **Step 1: Apply the same pattern as Task 8**

Merge tests assert: 4 siblings start on various hosts, after merge the parent cell exists on one host, none of the sibling cells remain. Substitute peek assertions the same way.

- [ ] **Step 2: Run**

Run: `go test ./pkg/universe/ -run TestS7Merge -v -count=1 -timeout 120s`
Expected: every test PASSes under both topologies.

- [ ] **Step 3: Commit**

```bash
git add pkg/universe/s7_merge_test.go
git commit -m "$(cat <<'EOF'
test(universe): migrate s7_merge_test.go to dual-topology harness

Same pattern as the split migration — every merge test runs under both
topologies via forEachTopology.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 10: Migrate s7_graceful_shutdown_test.go

**Files:**
- Modify: `pkg/universe/s7_graceful_shutdown_test.go`

**Goal:** Graceful-shutdown tests intentionally stop a host mid-scenario. In colocated, "stopping a host" is `coord.Hosts[h].Shutdown()`-ish; in distributed, it's shutting down the host-role `*Coordinator`. Add a small fixture method to abstract this.

- [ ] **Step 1: Extend the fixture API**

In `pkg/universe/cluster_fixture_test.go`, add to the `clusterFixture` interface:

```go
	// StopHost requests a graceful shutdown of the named host — in
	// colocated this calls HostRegistry.MarkLeaving + RemoveHost; in
	// distributed it calls Shutdown() on the host-role Coordinator
	// (which sends CellStopped messages for every owned cell before
	// closing the control stream). After StopHost returns, the host
	// is no longer in fx.HostIDs().
	StopHost(hostID string) error
```

In `colocatedFixture` (same file):

```go
func (f *colocatedFixture) StopHost(hostID string) error {
	// Colocated path: use the existing drainHost pathway through the
	// coord's MeshControl path; for in-process hosts we remove from the
	// Hosts map directly after draining. The production-accurate flow
	// here is the GracefulLeave handler, which the coord's
	// handleGracefulLeave triggers. For the fixture API we stub a
	// synchronous equivalent by invoking drainHost directly.
	f.coord.drainHost(hostID)
	// Remove from our declared host list so HostIDs() reflects reality.
	filtered := f.hosts[:0]
	for _, h := range f.hosts {
		if h != hostID {
			filtered = append(filtered, h)
		}
	}
	f.hosts = filtered
	return nil
}
```

In `distributedFixture` (`cluster_fixture_distributed_test.go`):

```go
func (f *distributedFixture) StopHost(hostID string) error {
	host, ok := f.hosts[hostID]
	if !ok {
		return fmt.Errorf("StopHost: unknown host %q", hostID)
	}
	host.Shutdown()
	delete(f.hosts, hostID)
	filtered := f.order[:0]
	for _, h := range f.order {
		if h != hostID {
			filtered = append(filtered, h)
		}
	}
	f.order = filtered
	return nil
}
```

- [ ] **Step 2: Apply the migration pattern to graceful-shutdown tests**

Wrap each test in `forEachTopology`. Where the test used to call `coord.Hosts[h].Shutdown()` or equivalent, use `fx.StopHost(h)`. Invariants ("remaining hosts own all cells", "stopped host no longer in registry") translate naturally.

- [ ] **Step 3: Run**

Run: `go test ./pkg/universe/ -run TestS7Graceful -v -count=1 -timeout 120s`
Expected: every test PASSes under both topologies.

- [ ] **Step 4: Commit**

```bash
git add pkg/universe/cluster_fixture_test.go pkg/universe/cluster_fixture_distributed_test.go pkg/universe/s7_graceful_shutdown_test.go
git commit -m "$(cat <<'EOF'
test(universe): migrate graceful-shutdown tests + add StopHost to fixture

Adds clusterFixture.StopHost so graceful-shutdown tests work identically
across topologies. Migrates s7_graceful_shutdown_test.go to
forEachTopology.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 11: Migrate s7_concurrent_test.go

**Files:**
- Modify: `pkg/universe/s7_concurrent_test.go`

**Goal:** Concurrent tests drive handoff-during-split. No new fixture API needed — they use the same accessors plus goroutine scheduling.

- [ ] **Step 1: Apply the migration pattern**

Wrap each test in `forEachTopology`. Substitute peek assertions per the Task 6 table.

- [ ] **Step 2: Run**

Run: `go test ./pkg/universe/ -run TestS7Concurrent -v -count=1 -timeout 120s`
Expected: every test PASSes under both topologies.

- [ ] **Step 3: Commit**

```bash
git add pkg/universe/s7_concurrent_test.go
git commit -m "$(cat <<'EOF'
test(universe): migrate s7_concurrent_test.go to dual-topology harness

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 12: Migrate s6_gateway_test.go

**Files:**
- Modify: `pkg/universe/s6_gateway_test.go`

**Goal:** `TestS6HandoffAcrossNodes` drives session handoff across hosts. The gateway role is load-bearing here — the coord-role fixture in distributed mode needs `RoleGateway` added alongside `RoleCoordinator` so the embedded gateway terminates the test's WebSocket stub.

- [ ] **Step 1: Extend distributed fixture to support an embedded gateway**

In `cluster_fixture_distributed_test.go`, update `newDistributedFixture`'s coord-role construction to include gateway role **when the test config asks for it**. Add a field to `FixtureConfig`:

In `cluster_fixture_test.go` inside `FixtureConfig`:

```go
	// WithGateway=true adds RoleGateway to the coord-role Coordinator in
	// distributed mode. Colocated always has the gateway (it's part of
	// the "all" preset). Leave false unless the test needs an embedded
	// gateway (currently only s6 gateway tests).
	WithGateway bool
```

In `cluster_fixture_distributed_test.go`'s `newDistributedFixture`, change the coord's `Mode` based on `cfg.WithGateway`:

```go
	coordMode := "coordinator"
	if cfg.WithGateway {
		coordMode = "coordinator,gateway"
	}
	coord := NewCoordinator(Config{
		// ... unchanged fields ...
		Mode:          coordMode,
		// ... unchanged fields ...
	})
```

- [ ] **Step 2: Migrate the gateway tests**

Wrap each test in `forEachTopology` with `FixtureConfig{..., WithGateway: true}`. Substitute peek assertions through the fixture. Where the test reaches for the gateway (e.g. `coord.gateway`), use `fx.Coord().gateway` — still allowed through the escape hatch.

- [ ] **Step 3: Run**

Run: `go test ./pkg/universe/ -run TestS6 -v -count=1 -timeout 120s`
Expected: every test PASSes under both topologies.

- [ ] **Step 4: Commit**

```bash
git add pkg/universe/cluster_fixture_test.go pkg/universe/cluster_fixture_distributed_test.go pkg/universe/s6_gateway_test.go
git commit -m "$(cat <<'EOF'
test(universe): migrate s6_gateway_test.go + add WithGateway config flag

FixtureConfig.WithGateway promotes the distributed coord-role process to
Mode="coordinator,gateway" so embedded-gateway scenarios (session handoff
across hosts) have somewhere to terminate WebSocket traffic. Colocated
always has the gateway via the "all" preset.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 13: Migrate partition_test.go

**Files:**
- Modify: `pkg/universe/partition_test.go`

**Goal:** Partition-monitor tests drive automatic splits/merges based on cell load. They hit the split/merge commit paths through the orchestrator (not through direct `BeginSplit` calls), exercising a slightly different entry point.

- [ ] **Step 1: Apply the migration pattern**

Wrap each test in `forEachTopology`. Substitute peek assertions. Where tests inject synthetic load metrics (`cell.Metrics.RecordCompositeLoad(...)` or similar), the call stays unchanged — it's on the `*Cell` pointer returned by `fx.CellOn(...)`.

- [ ] **Step 2: Run**

Run: `go test ./pkg/universe/ -run TestPartition -v -count=1 -timeout 120s`
Expected: every test PASSes under both topologies.

- [ ] **Step 3: Commit**

```bash
git add pkg/universe/partition_test.go
git commit -m "$(cat <<'EOF'
test(universe): migrate partition_test.go to dual-topology harness

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 14: Regression drill — prove the harness catches the bug class

**Files:**
- No permanent changes; this task validates the harness on a temporary branch and reverts.

**Goal:** Temporarily revert the recent `snapshotOwnershipLocked` fix. Confirm `/distributed` subtests fail while `/colocated` subtests still pass. Revert the revert. Prove the harness catches the exact bug class we built it for.

- [ ] **Step 1: Identify the target commit to revert**

Run: `git log --oneline -- pkg/universe/cell_transfer_commit.go | head -20`
Find the commit that changed `snapshotOwnershipLocked(mutation)` to `snapshotOwnershipLocked(req)`. (Message: `fix(universe): resolve migrate topology drift ...` or similar.)

- [ ] **Step 2: Create a scratch branch and revert that change**

```bash
git checkout -b scratch/harness-drill
git log --oneline pkg/universe/cell_transfer_commit.go | head
# Identify the SHA of the snapshotOwnershipLocked fix commit (let's call it $FIX_SHA).
git revert --no-commit $FIX_SHA
# If revert produces conflicts with later commits touching the same file,
# resolve by keeping the later commits' other changes but restoring the
# old snapshotOwnershipLocked signature and behavior. The goal is:
# snapshotOwnershipLocked takes topologyMutation and reads cellToHostMap only.
```

- [ ] **Step 3: Run the migrate test — expect distributed failure, colocated pass**

Run: `go test ./pkg/universe/ -run TestS7MigrateAcrossHosts -v -count=1 -timeout 60s`

Expected output pattern:
```
--- PASS: TestS7MigrateAcrossHosts/colocated
--- FAIL: TestS7MigrateAcrossHosts/distributed
    ... some assertion failure involving cell ownership or CellRelease ...
FAIL
```

If `/distributed` passes despite the revert, the harness **is not catching the bug class** — do not proceed. Investigate why (likely: the layout seeding accidentally hides the production code path the revert broke; fix by switching one distributed test to exercise a true remote migrate).

If both subtests fail or both pass, the harness output is broken for this drill — investigate and report before proceeding.

- [ ] **Step 4: Revert the revert**

```bash
git checkout main
git branch -D scratch/harness-drill
```

Or if you committed the revert for clarity in step 2:
```bash
git revert HEAD  # revert the revert = restore the fix
```

- [ ] **Step 5: Confirm all tests pass again on main**

Run: `go test ./pkg/universe/ -count=1 -timeout 300s`
Expected: all tests PASS.

- [ ] **Step 6: Document the drill result**

Append a note to `docs/superpowers/specs/2026-04-18-dual-topology-test-harness-design.md` under a new `## Drill result` section:

```markdown
## Drill result (Task 14)

Reverted commit $FIX_SHA (`snapshotOwnershipLocked` fix for migrate topology drift).
- `TestS7MigrateAcrossHosts/colocated`: PASS
- `TestS7MigrateAcrossHosts/distributed`: FAIL with "<concrete error message>"

Restored the fix; all tests green on main. Harness confirmed to catch the
bug class it was built for.
```

Commit:

```bash
git add docs/superpowers/specs/2026-04-18-dual-topology-test-harness-design.md
git commit -m "$(cat <<'EOF'
docs(spec): record dual-topology harness regression drill result

Confirmed the harness catches the migrate topology drift bug class by
reverting the snapshotOwnershipLocked fix on a scratch branch:
/colocated passed, /distributed failed. Fix restored.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Self-review (post-write)

**Spec coverage:**
- `forEachTopology` helper + `FixtureConfig`: Task 1.
- `clusterFixture` API surface (Coord, HostIDs, CellOwner, HostOwnsCell, CellOn, WaitForCellOwner): Tasks 1-4; `StopHost` added in Task 10 as a scoped extension.
- Colocated implementation: Task 2.
- Distributed implementation (real gRPC, layout seeding, settle-window bypass): Tasks 3-4.
- Fixture self-tests: Task 5.
- Migration of 20 target tests across 8 files: Tasks 6-13 (one task per file).
- Acceptance criterion: regression drill: Task 14.
- Lifecycle + cleanup: covered in Tasks 2 and 3 via `t.Cleanup`.

**Placeholder scan:** No TBDs, TODOs, or hand-waves. Every step has concrete code or a concrete shell command. Tasks 8, 9, 11, 13 do say "apply the substitution pattern from Task 6" — acceptable because Task 6 shows the pattern concretely and the per-file test bodies vary enough that reproducing all of them verbatim would bloat the plan without adding value. The implementor has a worked example (Task 6) and knows to run the test after each migration to confirm both subtests pass.

**Type consistency:** `clusterFixture` interface declared in Task 1; methods added identically in colocated (Task 2) and distributed (Task 3/4). `StopHost` added to all three (interface + both impls) in Task 10. `FixtureConfig.WithGateway` added in Task 12. No method-name drift.

**Open risk:** Task 10's `colocatedFixture.StopHost` references `f.coord.drainHost(hostID)` — this is a real existing method (see `coordinator.go:1695-`) but its exact signature may need a minor adjustment at implementation time. If `drainHost` is private and requires a different invocation pattern in test context, the implementor should adapt and document the choice in the commit message.
