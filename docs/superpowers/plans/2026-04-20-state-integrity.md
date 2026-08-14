# State Integrity Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the bug classes we hit this session (ordering mistakes, duplications, silent failures, ad-hoc state-shape bugs) structurally impossible to recur, and turn major topology events into a queryable operational surface available in dev and prod.

**Architecture:** Four layers, bottom-up: (A) an invariant framework that makes wrong states crash loud, (B) commit paths refactored from imperative functions into explicit `CommitPlan` step lists interpreted by a single executor, (C) an event-sourced ring that captures every plan step + invariant violation + host event and exposes them via the console and HTTP, (D) a per-cell `netIDIndex` enforcing "at most one ECS entity per netID per cell" with a documented transition policy.

**Tech Stack:** Go (server), existing category-based logger + admin console + HTTP mux, existing S7 test fixture as the regression oracle. No client-side changes.

**Source spec:** [`docs/superpowers/specs/2026-04-20-state-integrity-design.md`](../specs/2026-04-20-state-integrity-design.md)

**Rollout order (strict):** A → B → C → D → E. Each phase ends with a reviewable commit, passes the existing S7 suite, and is independently shippable.

---

## Lessons carried forward from Time & Transparency

T&T (Spec 1) shipped with four post-implementation bugs caught only during manual smoke-testing. Those bugs have concrete analogues here; read before implementing.

1. **Tests must exercise the real commit path, not a shape-alike.** T&T's Phase G regression test instantiated a fresh `FrameEncoder` and round-tripped it — which tested the same contract a different file already covered, and would not have caught `BinaryFrameWriter.WriteFrame` passing `0` for `serverTimeMs`. In this plan, any test that asserts on an invariant or plan-step outcome must either (a) drive a real `SplitCell`/`MergeCell`/`MigrateCell` through `forEachTopology`, or (b) call `ExecuteCommitPlan` with a real plan against a real `*Process`. Hand-built `&Process{...}` fixtures for pure unit tests are fine, but each invariant needs at least one real-commit integration test too. Specifically flagged in Tasks A2–A7, B5, D7.

2. **Asymmetric symptoms are the diagnosis.** T&T's hitch was "only entering split cells, never exiting" — that one sentence narrowed a hardware-phase-drift theory into a first-tick-physics bug. Every event logged by this plan's Phase C must carry enough metadata for operators to filter on the asymmetry axes: `Scenario` (split/merge/migrate), `StepIndex`, and host pair. Flagged in Task C1.

3. **Search for parallel code paths when applying a rule.** T&T's State Integrity prerequisite excluded `selfNetID` from the farewell-Removed list; a *parallel* code path (the per-tick `exited` list in `ReplicationSystem.Update`) was missed and caused a visible hop on every handoff. For this plan, any invariant or transition-policy rule that governs one spawn path must be cross-checked against every spawn path. A literal grep-for-all-spawn-sites verification step is added in Task D6.

4. **Wall-clock timestamps drift across hosts; don't trust them for ordering across processes.** Name fields for what they mean, not the generic "Timestamp." Flagged in Task C1.

5. **Finish with bot-load smoke testing before declaring done.** T&T's Go + bun test suites all passed; the real bugs surfaced under actual gameplay. Phase E is new and mandatory: drive 10+ real splits, merges, and migrates under `bot spawn 60` load with invariants in Panic mode and confirm zero panics.

---

## Phase A — Invariant framework

### Task A1: Create the `Invariant` type and `CheckInvariants` mechanism

**Files:**
- Create: `pkg/universe/integrity.go`
- Modify: `pkg/universe/coordinator.go` (add config fields + logger category)

- [ ] **Step 1: Create `pkg/universe/integrity.go`**

```go
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
```

- [ ] **Step 2: Add `invariantMode` field and `Config.InvariantMode`**

Open `pkg/universe/coordinator.go`. In the `Config` struct, add (near other operational knobs):

```go
	// InvariantMode controls how invariant-check violations are handled.
	// Zero value is InvariantOff; tests and dev should set Panic, prod
	// typically sets Log. See integrity.go for the full enum.
	InvariantMode InvariantMode
```

In the `Process` struct, add (near other runtime fields):

```go
	invariantMode InvariantMode
```

In `New(cfg Config) *Process`, near where other config is materialized, add:

```go
	c.invariantMode = cfg.InvariantMode
```

- [ ] **Step 3: Verify compile**

Run: `go vet ./pkg/universe/ 2>&1 | head -5`

Expected: no errors.

- [ ] **Step 4: Commit**

```bash
git add pkg/universe/integrity.go pkg/universe/coordinator.go
git commit -m "feat(integrity): add Invariant type and CheckInvariants mechanism"
```

---

### Task A2: Implement `coord-maps-consistent` invariant + test

**Files:**
- Modify: `pkg/universe/integrity.go`
- Create: `pkg/universe/integrity_test.go`

- [ ] **Step 1: Write the failing test**

Create `pkg/universe/integrity_test.go`:

```go
package universe

import (
	"strings"
	"testing"
)

func TestInvariant_CoordMapsConsistent_OK(t *testing.T) {
	c := &Process{
		Cells:     make(map[string]*Cell),
		CellOwner: make(map[CellID]string),
	}
	cell := CellID{X: 0, Y: 0}
	c.Cells["cell_0_0"] = &Cell{Cell: cell, ID: "cell_0_0"}
	c.CellOwner[cell] = "cell_0_0"

	if err := invCoordMapsConsistent.Check(c); err != nil {
		t.Fatalf("expected OK, got %v", err)
	}
}

func TestInvariant_CoordMapsConsistent_MissingCellOwner(t *testing.T) {
	c := &Process{
		Cells:     make(map[string]*Cell),
		CellOwner: make(map[CellID]string),
	}
	cell := CellID{X: 0, Y: 0}
	c.Cells["cell_0_0"] = &Cell{Cell: cell, ID: "cell_0_0"}
	// Deliberately leave CellOwner empty.

	err := invCoordMapsConsistent.Check(c)
	if err == nil {
		t.Fatal("expected violation, got nil")
	}
	if !strings.Contains(err.Error(), "cell_0_0") {
		t.Fatalf("error should mention the offending cell, got %v", err)
	}
}
```

- [ ] **Step 2: Run the test and verify it fails**

Run: `go test ./pkg/universe/ -run TestInvariant_CoordMapsConsistent -v 2>&1 | tail -10`

Expected: `invCoordMapsConsistent undefined`.

- [ ] **Step 3: Implement the invariant**

Append to `pkg/universe/integrity.go`:

```go
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
```

- [ ] **Step 4: Verify tests pass**

Run: `go test ./pkg/universe/ -run TestInvariant_CoordMapsConsistent -v 2>&1 | tail -10`

Expected: both `_OK` and `_MissingCellOwner` pass.

- [ ] **Step 5: Commit**

```bash
git add pkg/universe/integrity.go pkg/universe/integrity_test.go
git commit -m "feat(integrity): invCoordMapsConsistent invariant + tests"
```

---

### Task A3: Implement `host-ownership-matches-coord` invariant + test

**Files:**
- Modify: `pkg/universe/integrity.go`
- Modify: `pkg/universe/integrity_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `pkg/universe/integrity_test.go`:

```go
func TestInvariant_HostOwnershipMatchesCoord_OK(t *testing.T) {
	host := &Host{ID: "host-a", Cells: make(map[CellID]*Cell)}
	cell := CellID{X: 0, Y: 0}
	host.Cells[cell] = &Cell{Cell: cell, ID: "cell_0_0"}
	c := &Process{
		Cells:     map[string]*Cell{"cell_0_0": host.Cells[cell]},
		CellOwner: map[CellID]string{cell: "cell_0_0"},
		Hosts:     map[string]*Host{"host-a": host},
	}
	c.Control = &ControlPlane{cellToHostMap: map[string]string{"cell_0_0": "host-a"}}

	if err := invHostOwnershipMatchesCoord.Check(c); err != nil {
		t.Fatalf("expected OK, got %v", err)
	}
}

func TestInvariant_HostOwnershipMatchesCoord_HostMissingCell(t *testing.T) {
	host := &Host{ID: "host-a", Cells: make(map[CellID]*Cell)}
	// Deliberately don't register the cell on the host.
	cell := CellID{X: 0, Y: 0}
	c := &Process{
		Cells:     map[string]*Cell{"cell_0_0": {Cell: cell, ID: "cell_0_0"}},
		CellOwner: map[CellID]string{cell: "cell_0_0"},
		Hosts:     map[string]*Host{"host-a": host},
	}
	c.Control = &ControlPlane{cellToHostMap: map[string]string{"cell_0_0": "host-a"}}

	err := invHostOwnershipMatchesCoord.Check(c)
	if err == nil {
		t.Fatal("expected violation, got nil")
	}
}
```

- [ ] **Step 2: Verify the tests fail (invariant undefined)**

Run: `go test ./pkg/universe/ -run TestInvariant_HostOwnershipMatchesCoord -v 2>&1 | tail -5`

Expected: `invHostOwnershipMatchesCoord undefined`.

- [ ] **Step 3: Implement**

Append to `pkg/universe/integrity.go`:

```go
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
```

- [ ] **Step 4: Verify tests pass + commit**

Run: `go test ./pkg/universe/ -run TestInvariant_HostOwnershipMatchesCoord -v`

Expected: both tests pass.

```bash
git add pkg/universe/integrity.go pkg/universe/integrity_test.go
git commit -m "feat(integrity): invHostOwnershipMatchesCoord invariant + tests"
```

---

### Task A4: Implement `topology-neighbors-owned` invariant + test

**Files:**
- Modify: `pkg/universe/integrity.go`
- Modify: `pkg/universe/integrity_test.go`

- [ ] **Step 1: Write failing test**

Append to `pkg/universe/integrity_test.go`:

```go
func TestInvariant_TopologyNeighborsOwned_OK(t *testing.T) {
	a := CellID{X: 0, Y: 0}
	b := CellID{X: 1, Y: 0}
	c := &Process{
		CellOwner: map[CellID]string{a: "cell_0_0", b: "cell_1_0"},
	}
	c.Control = &ControlPlane{
		Topology: Topology{Neighbors: map[CellID][]CellID{a: {b}, b: {a}}},
	}
	if err := invTopologyNeighborsOwned.Check(c); err != nil {
		t.Fatalf("expected OK, got %v", err)
	}
}

func TestInvariant_TopologyNeighborsOwned_OrphanNeighbor(t *testing.T) {
	a := CellID{X: 0, Y: 0}
	b := CellID{X: 1, Y: 0}
	c := &Process{
		CellOwner: map[CellID]string{a: "cell_0_0"}, // deliberately omit b
	}
	c.Control = &ControlPlane{
		Topology: Topology{Neighbors: map[CellID][]CellID{a: {b}}},
	}
	err := invTopologyNeighborsOwned.Check(c)
	if err == nil {
		t.Fatal("expected violation, got nil")
	}
}
```

- [ ] **Step 2: Implement**

Append to `pkg/universe/integrity.go`:

```go
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
```

- [ ] **Step 3: Verify + commit**

Run: `go test ./pkg/universe/ -run TestInvariant_TopologyNeighborsOwned -v`

```bash
git add pkg/universe/integrity.go pkg/universe/integrity_test.go
git commit -m "feat(integrity): invTopologyNeighborsOwned invariant + tests"
```

---

### Task A5: Implement `session-route-host-live` invariant + test

**Files:**
- Modify: `pkg/universe/integrity.go`
- Modify: `pkg/universe/integrity_test.go`

- [ ] **Step 1: Write failing test**

Append to `pkg/universe/integrity_test.go`:

```go
func TestInvariant_SessionRouteHostLive_OK(t *testing.T) {
	c := &Process{
		hostRegistry: NewHostRegistry(),
		sessionRoutes: newSessionRoutes(),
	}
	c.hostRegistry.Set("host-a", &registeredHost{ID: "host-a"})
	c.sessionRoutes.Set(&SessionRoute{
		Key:    SessionKey{GatewayID: "gw", ConnID: 1},
		HostID: "host-a",
		CellID: "cell_0_0",
	})
	if err := invSessionRouteHostLive.Check(c); err != nil {
		t.Fatalf("expected OK, got %v", err)
	}
}

func TestInvariant_SessionRouteHostLive_OrphanHost(t *testing.T) {
	c := &Process{
		hostRegistry: NewHostRegistry(),
		sessionRoutes: newSessionRoutes(),
	}
	// host-a is NOT registered.
	c.sessionRoutes.Set(&SessionRoute{
		Key:    SessionKey{GatewayID: "gw", ConnID: 1},
		HostID: "host-a",
		CellID: "cell_0_0",
	})
	err := invSessionRouteHostLive.Check(c)
	if err == nil {
		t.Fatal("expected violation, got nil")
	}
}
```

If `NewHostRegistry` / `newSessionRoutes` / `registeredHost` have different names or signatures, adapt the test accordingly by inspecting `pkg/universe/host_registry.go` and `pkg/universe/session_routes.go`.

- [ ] **Step 2: Implement**

Append to `pkg/universe/integrity.go`:

```go
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
		c.sessionRoutes.ForEach(func(key SessionKey, r *SessionRoute) bool {
			if r.HostID == "" {
				return true
			}
			if c.hostRegistry.Get(r.HostID) == nil {
				violation = fmt.Errorf("sessionRoutes[%v].HostID=%q but host is not registered",
					key, r.HostID)
				return false
			}
			return true
		})
		return violation
	},
}
```

If `sessionRoutes` doesn't have `ForEach`, check `session_routes.go` for the existing iteration pattern and adapt. A minimal alternative is direct map iteration under `r.mu.RLock()`.

- [ ] **Step 3: Verify + commit**

Run: `go test ./pkg/universe/ -run TestInvariant_SessionRouteHostLive -v`

```bash
git add pkg/universe/integrity.go pkg/universe/integrity_test.go
git commit -m "feat(integrity): invSessionRouteHostLive invariant + tests"
```

---

### Task A6: Define the `defaultInvariants` set and wire CheckInvariants into commit entry/exit

**Files:**
- Modify: `pkg/universe/integrity.go`
- Modify: `pkg/universe/cell_transfer_commit.go`

- [ ] **Step 1: Define the default set**

Append to `pkg/universe/integrity.go`:

```go
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
	// invNoDuplicatePresencePerCell added in Phase D.
}
```

- [ ] **Step 2: Wire CheckInvariants into commit entry/exit**

Open `pkg/universe/cell_transfer_commit.go`. At the top of `applySplitCommit`, `applyMergeCommit`, and `applyMigrateCommit`, insert at the very start:

```go
	c.CheckInvariants(defaultInvariants, fmt.Sprintf("commit %d entry (%s)", req.ID, req.Kind))
```

And at the very end (before the closing brace, after `broadcastPeerListIfReady`):

```go
	c.CheckInvariants(defaultInvariants, fmt.Sprintf("commit %d exit (%s)", req.ID, req.Kind))
```

Import `fmt` in the file if not already imported.

- [ ] **Step 3: Verify compile + run the full S7 suite in Log mode**

Run: `go test ./pkg/universe/ -run '^TestS7' -count=1 -timeout 180s 2>&1 | tail -5`

Expected: all S7 tests pass. If any invariant violation fires during a commit, the test will log the violation via `c.Log.Log` (default `InvariantMode=Off` so no panic yet). We flip tests to panic mode in the next task.

- [ ] **Step 4: Commit**

```bash
git add pkg/universe/integrity.go pkg/universe/cell_transfer_commit.go
git commit -m "feat(integrity): wire CheckInvariants into commit entry/exit"
```

---

### Task A7: Flip test-fixture default to `InvariantPanic`

**Files:**
- Modify: `pkg/universe/cluster_fixture_test.go`

- [ ] **Step 1: Locate fixture Config construction**

Open `pkg/universe/cluster_fixture_test.go`. Locate `newColocatedFixture` and `newDistributedFixture`. Both construct a `Config` struct passed to `New`.

- [ ] **Step 2: Add `InvariantMode: InvariantPanic` to both Config structs**

In each `Config{...}` literal, add:

```go
		InvariantMode: InvariantPanic,
```

Placement: right after `Headless: true,` or wherever operational flags sit.

- [ ] **Step 3: Run the full S7 suite**

Run: `go test ./pkg/universe/ -run '^TestS7' -count=1 -timeout 180s 2>&1 | tail -10`

**Expected outcomes:**

- If all tests pass: the four invariants are currently holding throughout every S7 scenario. Good baseline.
- If any test panics with "invariant X violated": the codebase has a latent inconsistency (not a regression from this change; the invariant just revealed it). **Do not skip this signal.** Investigate the specific invariant, identify the commit step where state went wrong, and either fix the commit path or tighten the invariant if it was over-broad.

A likely candidate for an early-discovered violation is `invSessionRouteHostLive` in the transition window during handoff (stale routes for a fraction of a tick). If so, the fix is either to ensure routes are cleaned up synchronously or to relax the invariant to check only at commit boundaries.

- [ ] **Step 4: Commit**

```bash
git add pkg/universe/cluster_fixture_test.go
git commit -m "test(integrity): set InvariantPanic as fixture default"
```

---

## Review checkpoint — Phase A complete

The invariant framework is in place with four invariants running at commit entry/exit. Tests panic loudly on any violation. Any violations discovered in Task A7 should have been fixed inline or noted as known follow-ups.

Git log should show 7 focused commits on this branch. Verify with:

```bash
git log --oneline origin/main..HEAD | head -10
```

---

## Phase B — Commit plan refactor

### Task B1: Define `CommitPlan`, `PlanStep`, `CommitContext`, and `ExecuteCommitPlan`

**Files:**
- Create: `pkg/universe/commit_plan.go`

- [ ] **Step 1: Create `pkg/universe/commit_plan.go`**

```go
package universe

import (
	"fmt"
	"time"
)

// CommitKind identifies the shape of a commit (which plan was built).
type CommitKind uint8

const (
	CommitKindSplit CommitKind = iota
	CommitKindMerge
	CommitKindMigrate
)

func (k CommitKind) String() string {
	switch k {
	case CommitKindSplit:
		return "Split"
	case CommitKindMerge:
		return "Merge"
	case CommitKindMigrate:
		return "Migrate"
	default:
		return fmt.Sprintf("CommitKind(%d)", uint8(k))
	}
}

// CommitPlan describes a commit as a sequence of PlanSteps interpreted
// by ExecuteCommitPlan. It replaces the imperative
// applySplitCommit/applyMergeCommit/applyMigrateCommit functions with
// a value: the shape of a commit becomes inspectable, testable, and
// instrumentable without changing control flow.
type CommitPlan struct {
	ID    uint64
	Kind  CommitKind
	Req   *CellTransferRequest
	Steps []PlanStep
	Ctx   *CommitContext
}

// PlanStep is one mutation within a commit. Run performs the mutation;
// the executor runs CheckInvariants(step.Invariants or defaultInvariants)
// after each step and before proceeding to the next.
type PlanStep struct {
	Name       string
	Run        func(c *Process, ctx *CommitContext) error
	Invariants []Invariant // empty = defaultInvariants
}

// CommitContext carries state shared across steps within one commit.
// Replaces the bag of local variables each applyXxxCommit threads
// manually today. Only a subset of fields are relevant to any given
// commit kind — the unused fields stay zero.
type CommitContext struct {
	// Common.
	PreOwnership map[string]string
	Mutation     topologyMutation

	// Split.
	ParentKey  string
	Children   [4]CellID
	ParentCell *Cell

	// Merge.
	SurvivorKey string
	DonorIDs    []string
	DonorCells  []*Cell
	Survivor    *Cell

	// Migrate.
	SrcCellKey string
	SrcHost    string
	DestHost   string
	SrcCell    *Cell
}

// ExecuteCommitPlan runs every step in plan.Steps in order, checking
// invariants at entry, between steps, and at exit. Commit log hooks
// are stubbed here and wired in Phase C.
func (c *Process) ExecuteCommitPlan(plan *CommitPlan) error {
	c.CheckInvariants(defaultInvariants,
		fmt.Sprintf("commit %d entry (%s)", plan.ID, plan.Kind))

	for _, step := range plan.Steps {
		start := time.Now()
		err := step.Run(c, plan.Ctx)
		_ = time.Since(start) // commit log Phase C consumes this

		if err != nil {
			return fmt.Errorf("commit %d step %q: %w",
				plan.ID, step.Name, err)
		}

		invs := step.Invariants
		if len(invs) == 0 {
			invs = defaultInvariants
		}
		c.CheckInvariants(invs,
			fmt.Sprintf("commit %d after %s", plan.ID, step.Name))
	}

	c.CheckInvariants(defaultInvariants,
		fmt.Sprintf("commit %d exit (%s)", plan.ID, plan.Kind))
	return nil
}
```

- [ ] **Step 2: Verify compile**

Run: `go vet ./pkg/universe/ 2>&1 | head -5`

- [ ] **Step 3: Commit**

```bash
git add pkg/universe/commit_plan.go
git commit -m "feat(integrity): CommitPlan, PlanStep, CommitContext, ExecuteCommitPlan scaffolding"
```

---

### Task B2: Refactor `applySplitCommit` to use a plan

**Files:**
- Create: `pkg/universe/commit_builders_split.go`
- Modify: `pkg/universe/cell_transfer_commit.go`

- [ ] **Step 1: Create `pkg/universe/commit_builders_split.go` with `buildSplitPlan` and step functions**

Read the current `applySplitCommit` function body in `pkg/universe/cell_transfer_commit.go` end-to-end. Each distinct block of mutations becomes one `stepXxx` function. The ordered step list is the same as today's imperative sequence.

Write the builder and steps by lifting each existing code block into its own function that takes `(c *Process, ctx *CommitContext) error`. The goal is **pure relocation** — no logic changes, no reordering, no new behavior. The existing S7 suite is the oracle.

Skeleton:

```go
package universe

import (
	"context"
	"time"

	"github.com/zenion/mmokit/pkg/coords"
)

func buildSplitPlan(c *Process, req *CellTransferRequest) *CommitPlan {
	parent := req.SrcCell
	children := parent.Children()
	ctx := &CommitContext{
		ParentKey: MeshCellID(parent),
		Children:  children,
		Mutation:  req.mutation,
	}
	return &CommitPlan{
		ID: req.ID, Kind: CommitKindSplit, Req: req, Ctx: ctx,
		Steps: []PlanStep{
			{Name: "snapshot-pre-ownership", Run: stepSplitSnapshotPreOwnership},
			{Name: "apply-cell-to-host-map", Run: stepSplitApplyCellToHostMap},
			{Name: "detach-parent-from-coord", Run: stepSplitDetachParentFromCoord},
			{Name: "update-topology", Run: stepSplitUpdateTopology},
			{Name: "compute-rewire-directives", Run: stepSplitComputeRewireDirectives},
			{Name: "remap-sessions", Run: stepSplitRemapSessions},
			{Name: "apply-registry-delta", Run: stepSplitApplyRegistryDelta},
			{Name: "release-parent-host", Run: stepSplitReleaseParentHost},
			{Name: "apply-rewire-directives", Run: stepSplitApplyRewireDirectives},
			{Name: "prime-cooldowns", Run: stepSplitPrimeCooldowns},
			{Name: "broadcast-peer-list", Run: stepSplitBroadcastPeerList},
		},
	}
}

func stepSplitSnapshotPreOwnership(c *Process, ctx *CommitContext) error {
	c.mu.Lock()
	ctx.PreOwnership = c.snapshotOwnershipLocked(ctx.Mutation.removeKeys())
	c.mu.Unlock()
	return nil
}

// ... remaining stepXxx functions lift the corresponding block from the
// current applySplitCommit body. See that body as you implement — every
// conditional, lock acquisition, and error handler must be preserved
// verbatim. A side-by-side comparison during review is expected.
```

**Important:** do NOT shorten the lifted code by removing error-ignored calls or `log.Log(...)` lines. Behavior must be bit-for-bit equivalent.

Consult the current `applySplitCommit` body for each step's exact content. The comment inline should identify which block of the original it corresponds to (e.g. `// lifted from applySplitCommit: Remove parent from coord-level maps (pre-rewrite)`).

- [ ] **Step 2: Replace `applySplitCommit` with a thin dispatcher**

In `pkg/universe/cell_transfer_commit.go`, replace the entire body of `applySplitCommit` with:

```go
func (c *Process) applySplitCommit(req *CellTransferRequest) {
	if err := c.ExecuteCommitPlan(buildSplitPlan(c, req)); err != nil {
		c.Log.Log(CatMeshCell, "applySplitCommit: %v", err)
	}
}
```

Delete the invariant-check calls at entry/exit that Task A6 added — they're now inside `ExecuteCommitPlan`.

- [ ] **Step 3: Run the full S7 suite**

Run: `go test ./pkg/universe/ -run '^TestS7' -count=1 -timeout 180s 2>&1 | tail -10`

Expected: all split-related tests pass (`TestS7SplitAcrossHosts`, `TestS7SplitPreservesPlayerSessionsOnDest`, `TestS7SplitShutsDownParentCell`). Any failure indicates the lift was not behavior-preserving — go back and compare the step functions against the original block-by-block.

- [ ] **Step 4: Commit**

```bash
git add pkg/universe/commit_builders_split.go pkg/universe/cell_transfer_commit.go
git commit -m "refactor(integrity): applySplitCommit → buildSplitPlan + ExecuteCommitPlan"
```

---

### Task B3: Refactor `applyMergeCommit` to use a plan

**Files:**
- Create: `pkg/universe/commit_builders_merge.go`
- Modify: `pkg/universe/cell_transfer_commit.go`

- [ ] **Step 1: Build `buildMergePlan` with step functions**

Follow the same pattern as Task B2. Read the current `applyMergeCommit` body and lift each block. Step names (suggested, from the spec's example):

- `snapshot-pre-ownership`
- `apply-cell-to-host-map`
- `collect-donor-cells`
- `rekey-survivor-in-coord`
- `update-topology`
- `compute-rewire-directives`
- `rename-survivor-host`
- `apply-rewire-directives`
- `remap-sessions`
- `apply-registry-delta`
- `drain-donors`
- `release-donors`
- `prime-cooldowns`
- `broadcast-peer-list`

The merge plan is the longest because of the survivor-rename complexity. Preserve every step of the current body.

- [ ] **Step 2: Replace `applyMergeCommit` body with the dispatcher**

```go
func (c *Process) applyMergeCommit(req *CellTransferRequest) {
	if err := c.ExecuteCommitPlan(buildMergePlan(c, req)); err != nil {
		c.Log.Log(CatMeshCell, "applyMergeCommit: %v", err)
	}
}
```

- [ ] **Step 3: Run the full S7 suite (merge tests especially)**

Run: `go test ./pkg/universe/ -run '^TestS7' -count=1 -timeout 180s 2>&1 | tail -10`

Merge-specific tests must pass: `TestS7MergeAcrossHosts`, `TestS7MergeWiresParentNeighbors`, `TestS7MergeShutsDownDonorCells`, `TestS7MergeNoDuplicateNetIDs`.

- [ ] **Step 4: Commit**

```bash
git add pkg/universe/commit_builders_merge.go pkg/universe/cell_transfer_commit.go
git commit -m "refactor(integrity): applyMergeCommit → buildMergePlan + ExecuteCommitPlan"
```

---

### Task B4: Refactor `applyMigrateCommit` to use a plan

**Files:**
- Create: `pkg/universe/commit_builders_migrate.go`
- Modify: `pkg/universe/cell_transfer_commit.go`

- [ ] **Step 1: Build `buildMigratePlan`**

Migrate is the simplest of the three. Step names (suggested):

- `snapshot-pre-ownership`
- `apply-cell-to-host-map`
- `capture-src-cell`
- `rekey-cell-in-coord`
- `release-src-host`
- `remap-sessions`
- `apply-registry-delta`
- `broadcast-peer-list`

Lift blocks from `applyMigrateCommit` verbatim.

- [ ] **Step 2: Replace `applyMigrateCommit` body**

```go
func (c *Process) applyMigrateCommit(req *CellTransferRequest) {
	if err := c.ExecuteCommitPlan(buildMigratePlan(c, req)); err != nil {
		c.Log.Log(CatMeshCell, "applyMigrateCommit: %v", err)
	}
}
```

- [ ] **Step 3: Run the full S7 suite**

Run: `go test ./pkg/universe/ -run '^TestS7' -count=1 -timeout 180s 2>&1 | tail -10`

- [ ] **Step 4: Commit**

```bash
git add pkg/universe/commit_builders_migrate.go pkg/universe/cell_transfer_commit.go
git commit -m "refactor(integrity): applyMigrateCommit → buildMigratePlan + ExecuteCommitPlan"
```

---

### Task B5: Add a plan-structure test

**Files:**
- Create: `pkg/universe/commit_plan_test.go`

- [ ] **Step 1: Write the tests**

```go
package universe

import (
	"testing"
)

func TestBuildSplitPlan_StepOrdering(t *testing.T) {
	req := &CellTransferRequest{
		ID:       17,
		Kind:     CellTransferSplit,
		SrcCell:  CellID{X: 0, Y: 0},
		mutation: topologyMutation{add: map[string]string{}, remove: []string{}},
	}
	plan := buildSplitPlan(nil, req)

	want := []string{
		"snapshot-pre-ownership",
		"apply-cell-to-host-map",
		"detach-parent-from-coord",
		"update-topology",
		"compute-rewire-directives",
		"remap-sessions",
		"apply-registry-delta",
		"release-parent-host",
		"apply-rewire-directives",
		"prime-cooldowns",
		"broadcast-peer-list",
	}
	if len(plan.Steps) != len(want) {
		t.Fatalf("step count = %d, want %d", len(plan.Steps), len(want))
	}
	for i, name := range want {
		if plan.Steps[i].Name != name {
			t.Errorf("Steps[%d] = %q, want %q", i, plan.Steps[i].Name, name)
		}
	}
}

// Similar TestBuildMergePlan_StepOrdering and
// TestBuildMigratePlan_StepOrdering follow the same shape; their `want`
// lists come from the suggested step names in Tasks B3 and B4.
```

Write the two additional tests matching your B3 and B4 step names.

- [ ] **Step 2: Run + commit**

Run: `go test ./pkg/universe/ -run TestBuild.*Plan_StepOrdering -v`

```bash
git add pkg/universe/commit_plan_test.go
git commit -m "test(integrity): assert commit plan step ordering"
```

---

## Review checkpoint — Phase B complete

The three commit paths are now data-driven. The entire body of each is visible in its builder function as an ordered step list; each step is a pure function on `(Process, CommitContext)`. The S7 suite runs green against the refactored paths.

---

## Phase C — Event-sourced commit log

### Task C1: Create `CommitLog` ring + `CommitEvent` type

**Files:**
- Create: `pkg/universe/commit_log.go`
- Create: `pkg/universe/commit_log_test.go`

- [ ] **Step 1: Write failing tests**

```go
package universe

import (
	"testing"
	"time"
)

func TestCommitLog_AppendAndRecent(t *testing.T) {
	l := newCommitLog(4, nil)
	l.Append(CommitEvent{CommitID: 1, Step: "a"})
	l.Append(CommitEvent{CommitID: 1, Step: "b"})
	got := l.Recent(10)
	if len(got) != 2 {
		t.Fatalf("got %d events, want 2", len(got))
	}
	if got[0].Step != "a" || got[1].Step != "b" {
		t.Fatalf("order wrong: %v", got)
	}
}

func TestCommitLog_RingEviction(t *testing.T) {
	l := newCommitLog(3, nil)
	for i := 1; i <= 5; i++ {
		l.Append(CommitEvent{CommitID: uint64(i), Step: "x"})
	}
	got := l.Recent(10)
	if len(got) != 3 {
		t.Fatalf("got %d, want 3 (cap)", len(got))
	}
	// Oldest should be CommitID=3 (1 and 2 evicted).
	if got[0].CommitID != 3 || got[2].CommitID != 5 {
		t.Fatalf("eviction wrong: %v", got)
	}
}

func TestCommitLog_ByCommitID(t *testing.T) {
	l := newCommitLog(10, nil)
	l.Append(CommitEvent{CommitID: 7, Step: "a"})
	l.Append(CommitEvent{CommitID: 8, Step: "a"})
	l.Append(CommitEvent{CommitID: 7, Step: "b"})
	got := l.ByCommitID(7)
	if len(got) != 2 {
		t.Fatalf("got %d, want 2", len(got))
	}
}

func TestCommitLog_Since(t *testing.T) {
	l := newCommitLog(10, nil)
	l.Append(CommitEvent{CommitID: 1, Timestamp: time.Unix(1000, 0)})
	l.Append(CommitEvent{CommitID: 2, Timestamp: time.Unix(2000, 0)})
	l.Append(CommitEvent{CommitID: 3, Timestamp: time.Unix(3000, 0)})
	got := l.Since(time.Unix(1500, 0))
	if len(got) != 2 {
		t.Fatalf("got %d, want 2", len(got))
	}
}
```

Run: `go test ./pkg/universe/ -run TestCommitLog -v`

Expected: all fail (`newCommitLog` undefined).

- [ ] **Step 2: Implement `pkg/universe/commit_log.go`**

```go
package universe

import (
	"sync"
	"time"

	"github.com/zenion/mmokit/pkg/logger"
)

// EventKind identifies the shape of a CommitEvent. Derived from the
// CommitKind for step-level events; distinct values for non-commit
// events (invariant violations, host joins, etc.).
type EventKind uint8

const (
	EventCommitSplit EventKind = iota
	EventCommitMerge
	EventCommitMigrate
	EventInvariantViolation
	EventHostJoin
	EventHostLeave
	EventSessionRouteRemap
)

func (k EventKind) String() string {
	switch k {
	case EventCommitSplit:
		return "split"
	case EventCommitMerge:
		return "merge"
	case EventCommitMigrate:
		return "migrate"
	case EventInvariantViolation:
		return "invariant"
	case EventHostJoin:
		return "host"
	case EventHostLeave:
		return "host"
	case EventSessionRouteRemap:
		return "session"
	default:
		return "unknown"
	}
}

// CommitEvent is the unit of the commit log — one append per plan
// step, one per invariant violation, one per host registry change.
//
// Diagnosing bugs in a distributed commit path is an exercise in
// filtering: the operator knows "split worked but merge didn't" and
// needs to narrow to the specific plan step that diverged. Every
// field below exists to make those queries trivial. If a new bug
// class requires a filter we don't support, add the field here rather
// than stuffing it into Context (which is slow to search).
type CommitEvent struct {
	// SeqNo is the process-local monotonic append counter. Use this
	// (not Timestamp) for ordering events within a single process.
	SeqNo uint64

	// Timestamp is the host's wall clock at append time. In a multi-
	// host deployment this DRIFTS across hosts (ntp skew, clock jumps) —
	// do NOT use for cross-host ordering; use CommitID or the
	// coordinator's SeqNo for that. Kept as time.Time for human-
	// readable display; converted/filtered numerically in queries.
	Timestamp time.Time

	// CommitID identifies the specific commit this event belongs to.
	// All steps of one Split/Merge/Migrate share a CommitID. Filter on
	// this to replay the full step sequence for a single commit.
	CommitID uint64

	// Kind distinguishes event families (commit-step vs invariant vs
	// host-event vs session-remap). Primary filter axis.
	Kind EventKind

	// Scenario identifies which commit scenario produced this event —
	// CommitKindSplit/Merge/Migrate (defined in Phase B). Zero-value
	// for non-commit events (invariant violations, host joins); readers
	// filter those out by checking Kind first. This is the single most
	// useful field when diagnosing asymmetric bugs — see T&T lessons
	// at top of plan ("asymmetric symptoms are the diagnosis").
	Scenario CommitKind

	// StepIndex is 0-based position within the plan. 0 = first step,
	// len(plan)-1 = last step; -1 for non-step events (invariant
	// violations, commit begin/end markers). Filter by step_index=3
	// across many commits to find out which step is the consistently-
	// broken one.
	StepIndex int

	// Step is the PlanStep name (e.g. "apply-cell-to-host-map",
	// "release-parent-cell"). Human-readable label for display.
	Step string

	// Success is true when the step completed without error (or the
	// invariant held). Invariant-violation events set Success=false
	// with Error populated.
	Success bool

	// DurationMs is the wall time spent in the step. Useful for
	// identifying slow steps under load; not used for ordering.
	DurationMs int64

	// Affected is the list of cell IDs touched by this event (for
	// filtering by cell). HostIDs is the list of hosts touched.
	Affected []string
	HostIDs  []string

	// Error is the failure message when Success=false; empty otherwise.
	Error string

	// Context is a free-form bag of key/value strings. Use it for
	// one-off diagnostic data that doesn't merit a top-level field;
	// graduate frequently-queried fields to the struct.
	Context map[string]string
}

// CommitLog is a bounded in-memory ring of CommitEvents. Thread-safe.
// Append also fans out to the category logger so operators can tail
// events live via `log events:<kind>`.
type CommitLog struct {
	mu     sync.RWMutex
	ring   []CommitEvent
	head   int
	size   int
	cap    int
	seq    uint64
	logger *logger.Logger
}

func newCommitLog(cap int, log *logger.Logger) *CommitLog {
	if cap < 1 {
		cap = 1
	}
	return &CommitLog{
		ring:   make([]CommitEvent, cap),
		cap:    cap,
		logger: log,
	}
}

func (l *CommitLog) Append(e CommitEvent) {
	l.mu.Lock()
	l.seq++
	e.SeqNo = l.seq
	if e.Timestamp.IsZero() {
		e.Timestamp = time.Now()
	}
	l.ring[l.head] = e
	l.head = (l.head + 1) % l.cap
	if l.size < l.cap {
		l.size++
	}
	l.mu.Unlock()

	if l.logger != nil {
		category := "events:" + e.Kind.String()
		status := "✓"
		if !e.Success && e.Kind != EventInvariantViolation {
			status = "✗"
		}
		l.logger.Log(category,
			"%s cid=%d step=%s dur=%dms affected=%v err=%s",
			status, e.CommitID, e.Step, e.DurationMs, e.Affected, e.Error)
	}
}

func (l *CommitLog) Recent(n int) []CommitEvent {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if n > l.size {
		n = l.size
	}
	out := make([]CommitEvent, 0, n)
	// Ring is in append order; oldest is at (head - size) mod cap.
	start := (l.head - l.size + l.cap) % l.cap
	for i := l.size - n; i < l.size; i++ {
		out = append(out, l.ring[(start+i)%l.cap])
	}
	return out
}

func (l *CommitLog) ByCommitID(id uint64) []CommitEvent {
	l.mu.RLock()
	defer l.mu.RUnlock()
	var out []CommitEvent
	start := (l.head - l.size + l.cap) % l.cap
	for i := 0; i < l.size; i++ {
		e := l.ring[(start+i)%l.cap]
		if e.CommitID == id {
			out = append(out, e)
		}
	}
	return out
}

func (l *CommitLog) ByCell(cellID string) []CommitEvent {
	l.mu.RLock()
	defer l.mu.RUnlock()
	var out []CommitEvent
	start := (l.head - l.size + l.cap) % l.cap
	for i := 0; i < l.size; i++ {
		e := l.ring[(start+i)%l.cap]
		for _, c := range e.Affected {
			if c == cellID {
				out = append(out, e)
				break
			}
		}
	}
	return out
}

func (l *CommitLog) Since(t time.Time) []CommitEvent {
	l.mu.RLock()
	defer l.mu.RUnlock()
	var out []CommitEvent
	start := (l.head - l.size + l.cap) % l.cap
	for i := 0; i < l.size; i++ {
		e := l.ring[(start+i)%l.cap]
		if e.Timestamp.After(t) || e.Timestamp.Equal(t) {
			out = append(out, e)
		}
	}
	return out
}
```

- [ ] **Step 3: Verify tests pass**

Run: `go test ./pkg/universe/ -run TestCommitLog -v`

Expected: all 4 tests pass.

- [ ] **Step 4: Commit**

```bash
git add pkg/universe/commit_log.go pkg/universe/commit_log_test.go
git commit -m "feat(integrity): CommitLog ring with query APIs + tests"
```

---

### Task C2: Wire `CommitLog` onto `Process` and add config

**Files:**
- Modify: `pkg/universe/coordinator.go`

- [ ] **Step 1: Add config fields**

In `Config`:

```go
	// CommitLogCapacity sets the size of the in-memory commit log ring.
	// 0 = use default (1024).
	CommitLogCapacity int
```

(Optional — defer to a follow-up: `CommitLogPersistPath string`.)

- [ ] **Step 2: Add `commitLog *CommitLog` field to `Process`**

```go
	commitLog *CommitLog
```

- [ ] **Step 3: Initialize in `New`**

```go
	cap := cfg.CommitLogCapacity
	if cap == 0 {
		cap = 1024
	}
	c.commitLog = newCommitLog(cap, c.Log)
```

- [ ] **Step 4: Register logger categories**

Open `internal/game/logcat.go` (or the appropriate package's logcat file). Add the event categories:

```go
	// Event log bridge. Major topology events stream here so operators
	// can tail `log events:*` for live observability.
	CatEventsSplit     = "events:split"
	CatEventsMerge     = "events:merge"
	CatEventsMigrate   = "events:migrate"
	CatEventsInvariant = "events:invariant"
	CatEventsHost      = "events:host"
	CatEventsSession   = "events:session"
```

And if the logger requires explicit category registration (look for existing `RegisterCategory` or similar calls), add one per new category.

- [ ] **Step 5: Verify compile + commit**

Run: `go vet ./... 2>&1 | head -5`

```bash
git add pkg/universe/coordinator.go internal/game/logcat.go
git commit -m "feat(integrity): wire CommitLog onto Process + register event log categories"
```

---

### Task C3: Emit events from `ExecuteCommitPlan` and `CheckInvariants`

**Files:**
- Modify: `pkg/universe/commit_plan.go`
- Modify: `pkg/universe/integrity.go`

- [ ] **Step 1: Emit commit events from `ExecuteCommitPlan`**

Replace the `ExecuteCommitPlan` body with:

```go
func (c *Process) ExecuteCommitPlan(plan *CommitPlan) error {
	eventKind := commitKindToEvent(plan.Kind)

	c.commitLog.Append(CommitEvent{
		CommitID: plan.ID, Kind: eventKind, Scenario: plan.Kind,
		StepIndex: -1, Step: "begin", Success: true,
	})
	c.CheckInvariants(defaultInvariants,
		fmt.Sprintf("commit %d entry (%s)", plan.ID, plan.Kind))

	for i, step := range plan.Steps {
		start := time.Now()
		err := step.Run(c, plan.Ctx)
		dur := time.Since(start)

		c.commitLog.Append(CommitEvent{
			CommitID:   plan.ID,
			Kind:       eventKind,
			Scenario:   plan.Kind,
			StepIndex:  i,
			Step:       step.Name,
			Success:    err == nil,
			DurationMs: dur.Milliseconds(),
			Error:      errString(err),
		})

		if err != nil {
			return fmt.Errorf("commit %d step %q: %w", plan.ID, step.Name, err)
		}

		invs := step.Invariants
		if len(invs) == 0 {
			invs = defaultInvariants
		}
		c.CheckInvariants(invs,
			fmt.Sprintf("commit %d after %s", plan.ID, step.Name))
	}

	c.CheckInvariants(defaultInvariants,
		fmt.Sprintf("commit %d exit (%s)", plan.ID, plan.Kind))
	c.commitLog.Append(CommitEvent{
		CommitID: plan.ID, Kind: eventKind, Scenario: plan.Kind,
		StepIndex: -1, Step: "end", Success: true,
	})
	return nil
}

func commitKindToEvent(k CommitKind) EventKind {
	switch k {
	case CommitKindSplit:
		return EventCommitSplit
	case CommitKindMerge:
		return EventCommitMerge
	case CommitKindMigrate:
		return EventCommitMigrate
	default:
		return EventCommitSplit
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
```

- [ ] **Step 2: Emit invariant violation events from `CheckInvariants`**

In `integrity.go`'s `CheckInvariants`, inside the violation branch, before the panic check:

```go
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
```

- [ ] **Step 3: Run the S7 suite + commit**

```bash
go test ./pkg/universe/ -run '^TestS7' -count=1 -timeout 180s
```

All pass.

```bash
git add pkg/universe/commit_plan.go pkg/universe/integrity.go
git commit -m "feat(integrity): emit commit events for every plan step + invariant violation"
```

---

### Task C4: Emit host events from the host registry

**Files:**
- Modify: `pkg/universe/host_registry.go` (or wherever host registrations and removals live)
- Modify: `pkg/universe/coordinator.go` (if registration/removal hooks need exposing)

- [ ] **Step 1: Find the call sites for host join / leave**

Search for where the coordinator adds and removes entries in `hostRegistry`:

```bash
grep -n "hostRegistry\.Set\|hostRegistry\.Remove\|onHostRegistered\|GracefulLeave" pkg/universe/*.go | head -20
```

Typical sites:
- `RegisterHost` / `onHostRegistered` (join)
- `drainHost` / `hostRegistry.Remove` or equivalent (leave/crash)

- [ ] **Step 2: Append `EventHostJoin` at each join site**

At the end of the successful registration path, insert:

```go
	c.commitLog.Append(CommitEvent{
		Kind:    EventHostJoin,
		Step:    "registered",
		HostIDs: []string{hostID},
		Success: true,
	})
```

- [ ] **Step 3: Append `EventHostLeave` at each leave/crash site**

At the completion of drain/removal, insert:

```go
	c.commitLog.Append(CommitEvent{
		Kind:    EventHostLeave,
		Step:    "removed",
		HostIDs: []string{hostID},
		Success: true,
	})
```

- [ ] **Step 4: Verify S7 graceful-shutdown test still passes + commit**

```bash
go test ./pkg/universe/ -run TestS7GracefulShutdown -v
```

```bash
git add pkg/universe/host_registry.go pkg/universe/coordinator.go
git commit -m "feat(integrity): emit host-join/leave events to commit log"
```

---

### Task C5: Console admin commands (`commit log ...`)

**Files:**
- Create: `pkg/universe/commit_log_console.go`

- [ ] **Step 1: Register admin commands**

Following the existing admin-command pattern (search `pkg/universe/builtins_*.go` for examples — e.g., `builtins_cell.go` registers `cell list/info/split/merge` via `cmdsys.Registry.Register`):

```go
package universe

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/zenion/mmokit/pkg/cmdsys"
)

// registerCommitLogCommands wires `commit log ...` into the dispatcher
// so operators can query recent topology events from any console.
func (c *Process) registerCommitLogCommands(reg *cmdsys.Registry) {
	reg.Register(cmdsys.Command{
		Verb:      "commit.log",
		RouteKind: cmdsys.RouteLocal,
		Args:      &commitLogArgs{},
		Result:    &commitLogResult{},
		Handler: func(ctx context.Context, caller cmdsys.Caller, rawArgs any) (any, error) {
			a := rawArgs.(*commitLogArgs)
			var events []CommitEvent
			switch {
			case a.CommitID != 0:
				events = c.commitLog.ByCommitID(a.CommitID)
			case a.CellID != "":
				events = c.commitLog.ByCell(a.CellID)
			case a.Since != "":
				d, err := time.ParseDuration(a.Since)
				if err != nil {
					return nil, fmt.Errorf("bad since: %w", err)
				}
				events = c.commitLog.Since(time.Now().Add(-d))
			default:
				n := a.N
				if n == 0 {
					n = 20
				}
				events = c.commitLog.Recent(n)
			}
			return &commitLogResult{Events: formatEvents(events)}, nil
		},
	})
}

type commitLogArgs struct {
	N        int    `json:"n,omitempty"`
	CommitID uint64 `json:"commit_id,omitempty"`
	CellID   string `json:"cell_id,omitempty"`
	Since    string `json:"since,omitempty"`
}

type commitLogResult struct {
	Events []string `json:"events"`
}

func formatEvents(events []CommitEvent) []string {
	out := make([]string, 0, len(events))
	for _, e := range events {
		status := "✓"
		if !e.Success {
			status = "✗"
		}
		out = append(out, fmt.Sprintf("%6d  %s  %-3d  %-7s  %-30s  %s  %dms  %s",
			e.SeqNo,
			e.Timestamp.Format("15:04:05"),
			e.CommitID,
			e.Kind.String(),
			e.Step,
			status,
			e.DurationMs,
			e.Error))
	}
	if len(out) == 0 {
		return []string{"(no matching events)"}
	}
	// Header.
	header := strings.Join([]string{
		"SEQ   ", "TIME   ", "CID", "KIND   ", "STEP                          ",
		"OK", "DUR", "ERROR",
	}, "  ")
	return append([]string{header}, out...)
}
```

- [ ] **Step 2: Hook registration from coord startup**

In the coord's admin-command registration section (likely `coordinator.go` or a `builtins_*.go` file that is called during startup), call:

```go
	c.registerCommitLogCommands(registry)
```

Find the pattern by searching:

```bash
grep -n "registerCellCommands\|registerBuiltins" pkg/universe/*.go | head -5
```

- [ ] **Step 3: Build + manual smoke**

Run: `just build`

Start the server (`just dev` or equivalent). From the admin console, run:

```
commit log
```

Expected: output shows at least the initial host-join event, formatted as a table.

- [ ] **Step 4: Commit**

```bash
git add pkg/universe/commit_log_console.go pkg/universe/coordinator.go
git commit -m "feat(integrity): commit log console commands (commit.log)"
```

---

### Task C6: HTTP endpoint for out-of-process tooling

**Files:**
- Create: `pkg/universe/commit_log_http.go`

- [ ] **Step 1: Register HTTP handlers on the existing admin mux**

Find where `/metrics` and `/commands` are registered (likely in `coordinator.go` around startHTTPListener). Add:

```go
package universe

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"
)

func (c *Process) registerCommitLogHTTP(mux *http.ServeMux) {
	mux.HandleFunc("/events", func(w http.ResponseWriter, r *http.Request) {
		var events []CommitEvent
		if id := r.URL.Query().Get("commit_id"); id != "" {
			n, err := strconv.ParseUint(id, 10, 64)
			if err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
			events = c.commitLog.ByCommitID(n)
		} else if since := r.URL.Query().Get("since"); since != "" {
			d, err := time.ParseDuration(since)
			if err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
			events = c.commitLog.Since(time.Now().Add(-d))
		} else {
			nStr := r.URL.Query().Get("n")
			n := 100
			if nStr != "" {
				if v, err := strconv.Atoi(nStr); err == nil && v > 0 {
					n = v
				}
			}
			events = c.commitLog.Recent(n)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(events)
	})
}
```

In the HTTP server startup site, call `c.registerCommitLogHTTP(mux)` alongside the existing handler registrations.

- [ ] **Step 2: Manual smoke**

Start server, hit `http://localhost:8080/events?n=50` in a browser or `curl`.

Expected: JSON array of events.

- [ ] **Step 3: Commit**

```bash
git add pkg/universe/commit_log_http.go pkg/universe/coordinator.go
git commit -m "feat(integrity): /events HTTP endpoint for commit log"
```

---

## Review checkpoint — Phase C complete

Every plan step, invariant violation, and host join/leave is captured in the commit log ring. Operators can query it from the console (`commit log ...`) or HTTP (`/events`) and stream it via the logger (`log events:*`). The S7 suite remains green.

---

## Phase D — Entity transfer state machine

### Task D1: Create `netIDIndex` + transition policy

**Files:**
- Create: `pkg/universe/netid_index.go`
- Create: `pkg/universe/netid_index_test.go`

- [ ] **Step 1: Write the transition tests**

Create `pkg/universe/netid_index_test.go`:

```go
package universe

import (
	"testing"

	"github.com/mlange-42/ark/ecs"
)

func TestNetIDIndex_Transitions(t *testing.T) {
	type tc struct {
		name      string
		existing  EntityPresence
		incoming  EntityPresence
		wantAct   TransitionAction
	}
	// Policy table from Spec 2 § Component 4.
	cases := []tc{
		// current: None
		{"none_to_live", PresenceNone, PresenceLive, ActionInstalled},
		{"none_to_shadow", PresenceNone, PresenceShadow, ActionInstalled},
		{"none_to_replica", PresenceNone, PresenceReplica, ActionInstalled},
		// current: Live
		{"live_to_live", PresenceLive, PresenceLive, ActionDuplicate},
		{"live_to_shadow", PresenceLive, PresenceShadow, ActionRejected},
		{"live_to_replica", PresenceLive, PresenceReplica, ActionRejected},
		// current: Shadow
		{"shadow_to_live", PresenceShadow, PresenceLive, ActionPromoted},
		{"shadow_to_shadow", PresenceShadow, PresenceShadow, ActionRejected},
		{"shadow_to_replica", PresenceShadow, PresenceReplica, ActionReplaced},
		// current: Replica
		{"replica_to_live", PresenceReplica, PresenceLive, ActionReplaced},
		{"replica_to_shadow", PresenceReplica, PresenceShadow, ActionReplaced},
		{"replica_to_replica", PresenceReplica, PresenceReplica, ActionUpdated},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			idx := newNetIDIndex()
			existingEnt := ecs.Entity{} // zero is fine for table-driven logic
			incomingEnt := ecs.Entity{}
			if c.existing != PresenceNone {
				idx.Enter(1, existingEnt, c.existing)
			}
			got := idx.Enter(1, incomingEnt, c.incoming)
			if got.Action != c.wantAct {
				t.Errorf("got %v, want %v", got.Action, c.wantAct)
			}
		})
	}
}
```

Run: `go test ./pkg/universe/ -run TestNetIDIndex_Transitions -v`

Expected: compilation errors (types undefined).

- [ ] **Step 2: Implement the index**

Create `pkg/universe/netid_index.go`:

```go
package universe

import (
	"sync"

	"github.com/mlange-42/ark/ecs"
)

type EntityPresence uint8

const (
	PresenceNone EntityPresence = iota
	PresenceLive
	PresenceShadow
	PresenceReplica
)

type TransitionAction uint8

const (
	ActionInstalled TransitionAction = iota
	ActionPromoted
	ActionReplaced
	ActionUpdated
	ActionRejected
	ActionDuplicate
)

type TransitionResult struct {
	Action     TransitionAction
	PrevEntity ecs.Entity
}

type netIDIndex struct {
	mu    sync.RWMutex
	slots map[uint32]netIDSlot
}

type netIDSlot struct {
	Entity   ecs.Entity
	Presence EntityPresence
}

func newNetIDIndex() *netIDIndex {
	return &netIDIndex{slots: make(map[uint32]netIDSlot)}
}

func (idx *netIDIndex) Lookup(netID uint32) (ecs.Entity, EntityPresence, bool) {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	s, ok := idx.slots[netID]
	if !ok {
		return ecs.Entity{}, PresenceNone, false
	}
	return s.Entity, s.Presence, true
}

func (idx *netIDIndex) Enter(netID uint32, entity ecs.Entity, to EntityPresence) TransitionResult {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	cur, ok := idx.slots[netID]
	if !ok {
		idx.slots[netID] = netIDSlot{Entity: entity, Presence: to}
		return TransitionResult{Action: ActionInstalled}
	}

	switch cur.Presence {
	case PresenceLive:
		switch to {
		case PresenceLive:
			return TransitionResult{Action: ActionDuplicate, PrevEntity: cur.Entity}
		case PresenceShadow, PresenceReplica:
			return TransitionResult{Action: ActionRejected}
		}
	case PresenceShadow:
		switch to {
		case PresenceLive:
			idx.slots[netID] = netIDSlot{Entity: entity, Presence: PresenceLive}
			return TransitionResult{Action: ActionPromoted}
		case PresenceShadow:
			return TransitionResult{Action: ActionRejected}
		case PresenceReplica:
			prev := cur.Entity
			idx.slots[netID] = netIDSlot{Entity: entity, Presence: PresenceShadow}
			return TransitionResult{Action: ActionReplaced, PrevEntity: prev}
		}
	case PresenceReplica:
		switch to {
		case PresenceLive, PresenceShadow:
			prev := cur.Entity
			idx.slots[netID] = netIDSlot{Entity: entity, Presence: to}
			return TransitionResult{Action: ActionReplaced, PrevEntity: prev}
		case PresenceReplica:
			idx.slots[netID] = netIDSlot{Entity: entity, Presence: PresenceReplica}
			return TransitionResult{Action: ActionUpdated}
		}
	}
	return TransitionResult{Action: ActionRejected}
}

func (idx *netIDIndex) Exit(netID uint32) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	delete(idx.slots, netID)
}
```

- [ ] **Step 3: Run tests + commit**

Run: `go test ./pkg/universe/ -run TestNetIDIndex -v`

Expected: all 12 subtests pass.

```bash
git add pkg/universe/netid_index.go pkg/universe/netid_index_test.go
git commit -m "feat(integrity): netIDIndex with transition policy table + tests"
```

---

### Task D2: Attach `netIDIndex` to `WorldBase` + add `Config.StrictNetIDIndex`

**Files:**
- Modify: `pkg/universe/world_base.go`
- Modify: `pkg/universe/coordinator.go`

- [ ] **Step 1: Add `netIDIndex *netIDIndex` field to `WorldBase`**

In `pkg/universe/world_base.go`, near other index fields:

```go
	netIDIdx *netIDIndex
```

In `NewWorldBase` (or the equivalent constructor), initialize:

```go
	w.netIDIdx = newNetIDIndex()
```

- [ ] **Step 2: Add `Config.StrictNetIDIndex`**

In `pkg/universe/coordinator.go` `Config`:

```go
	// StrictNetIDIndex enforces the transition policy table. When false
	// (default during rollout), the index tracks state for observability
	// but transitions are advisory — existing spawn paths run unchanged.
	StrictNetIDIndex bool
```

Add `strictNetIDIndex bool` to `Process` and wire from config in `New`.

- [ ] **Step 3: Plumb the flag to `WorldBase`**

`WorldBase` is per-cell. The flag lives on `Process`. Pass it when creating the WorldBase — find the WorldBase construction site (likely in `createNode` or similar) and set `w.strictNetIDIndex = c.strictNetIDIndex`.

- [ ] **Step 4: Verify compile + commit**

Run: `go vet ./pkg/universe/...`

```bash
git add pkg/universe/world_base.go pkg/universe/coordinator.go
git commit -m "feat(integrity): attach netIDIndex to WorldBase + StrictNetIDIndex flag"
```

---

### Task D3: Integrate with `SpawnFromTransferCore`

**Files:**
- Modify: `pkg/universe/world_base.go`

- [ ] **Step 1: Locate `SpawnFromTransferCore` and identify the insertion point**

The function spawns an entity via `b.spawner.NewEntity(...)` and returns it. The index integration call goes RIGHT BEFORE the final return (after all components are populated).

- [ ] **Step 2: Add index integration**

Just before the final `return entity, frame, nil` in `SpawnFromTransferCore`:

```go
	if b.netIDIdx != nil && frame.NetworkID != 0 {
		res := b.netIDIdx.Enter(frame.NetworkID, entity, PresenceLive)
		switch res.Action {
		case ActionInstalled, ActionPromoted, ActionReplaced:
			if res.Action == ActionReplaced && b.eng.ECS.Alive(res.PrevEntity) {
				b.eng.ECS.RemoveEntity(res.PrevEntity)
			}
		case ActionDuplicate:
			b.eng.Log.Log(CatMeshTransfer,
				"[%s] duplicate live spawn blocked: netID=%d", b.cellID, frame.NetworkID)
			if b.strictNetIDIndex {
				b.eng.ECS.RemoveEntity(entity)
				return ecs.Entity{}, nil, fmt.Errorf("duplicate live netID %d", frame.NetworkID)
			}
		case ActionRejected:
			// Live-into-X paths shouldn't hit Rejected here.
		}
	}
```

Import `fmt` if not already.

- [ ] **Step 3: Run the full S7 suite**

Run: `go test ./pkg/universe/ -run '^TestS7' -count=1 -timeout 180s`

Expected: all pass. The observe-only mode (default) means no behavior changes. Strict mode is exercised in Task D7.

- [ ] **Step 4: Commit**

```bash
git add pkg/universe/world_base.go
git commit -m "feat(integrity): integrate netIDIndex with SpawnFromTransferCore"
```

---

### Task D4: Integrate with `SpawnShadow` and `PromoteShadow`

**Files:**
- Modify: `pkg/universe/world_base.go`

- [ ] **Step 1: `SpawnShadow` — add Enter at shadow presence**

Before `SpawnShadow`'s final `return entity, nil`:

```go
	if b.netIDIdx != nil {
		res := b.netIDIdx.Enter(payload.NetID, entity, PresenceShadow)
		if res.Action == ActionReplaced && b.eng.ECS.Alive(res.PrevEntity) {
			b.eng.ECS.RemoveEntity(res.PrevEntity)
		}
		if res.Action == ActionRejected && b.strictNetIDIndex {
			b.eng.ECS.RemoveEntity(entity)
			return ecs.Entity{}, fmt.Errorf("shadow rejected: netID %d already live", payload.NetID)
		}
	}
```

- [ ] **Step 2: `PromoteShadow` — add atomic Shadow→Live transition**

In `PromoteShadow`, after locating the entity and before `return true`:

```go
	if b.netIDIdx != nil {
		b.netIDIdx.Enter(netID, entity, PresenceLive) // transitions Shadow→Live
	}
```

- [ ] **Step 3: Run S7 + commit**

```bash
go test ./pkg/universe/ -run '^TestS7' -count=1 -timeout 180s
```

```bash
git add pkg/universe/world_base.go
git commit -m "feat(integrity): integrate netIDIndex with SpawnShadow + PromoteShadow"
```

---

### Task D5: Integrate with `upsertBorderReplica`

**Files:**
- Modify: `pkg/universe/world_base.go`

- [ ] **Step 1: Add Enter at replica presence**

In `upsertBorderReplica`, after the entity is created (or updated) and BEFORE the final successful return:

```go
	if b.netIDIdx != nil {
		res := b.netIDIdx.Enter(netID, ent, PresenceReplica)
		if res.Action == ActionRejected {
			b.eng.Log.Log(CatMeshReplica,
				"[%s] replica ignored: netID=%d is already live or shadowed here",
				b.cellID, netID)
			if b.strictNetIDIndex && b.eng.ECS.Alive(ent) {
				b.eng.ECS.RemoveEntity(ent)
			}
			return
		}
	}
```

Place this inside the branch that creates a new replica (not the branch that updates an existing one — updates are already in-place and don't need the index consulted).

- [ ] **Step 2: Run S7 + commit**

```bash
go test ./pkg/universe/ -run '^TestS7' -count=1 -timeout 180s
git add pkg/universe/world_base.go
git commit -m "feat(integrity): integrate netIDIndex with upsertBorderReplica"
```

---

### Task D6: Integrate `Exit` on entity removal

**Files:**
- Modify: `pkg/universe/world_base.go`
- Modify: `pkg/engine/engine.go`

- [ ] **Step 1: Ensure `OnEntityRemoved` hook exists on `Engine`**

Search `pkg/engine/engine.go` for `OnEntityRemoved`. If it already exists (invoked from `FlushRemovals`), proceed to Step 2. If not, add:

```go
	// OnEntityRemoved is called once per entity being removed from the
	// ECS during FlushRemovals. Used by WorldBase to notify the
	// netIDIndex that a netID's slot can be freed.
	OnEntityRemoved func(e ecs.Entity)
```

And call it from `FlushRemovals` for each entity being removed.

- [ ] **Step 2: Wire the hook in WorldBase**

During `WorldBase` setup (constructor), set:

```go
	b.eng.OnEntityRemoved = func(e ecs.Entity) {
		if b.netIDIdx == nil {
			return
		}
		if !b.netIDMap.HasAll(e) {
			return
		}
		netID := b.netIDMap.Get(e).ID
		b.netIDIdx.Exit(netID)
	}
```

Be careful: if the codebase already sets `OnEntityRemoved` (e.g., for `spatialGrid.Deregister`), adapt so both actions fire. Find the existing setter with:

```bash
grep -n "OnEntityRemoved" pkg/ -r
```

If found, wrap both actions into a single closure.

- [ ] **Step 3: Parallel-spawn-path audit**

Tasks D3–D6 enumerated five entry points where netIDIndex is notified:
`SpawnFromTransferCore`, `SpawnShadow`, `PromoteShadow`, `upsertBorderReplica`,
and `OnEntityRemoved`. Before closing Phase D, verify no *sixth* path exists
that creates an ECS entity with a `NetworkID` component without going through
the index — that's exactly the "parallel code path" failure mode that bit
T&T (see lessons at top of plan).

Run this grep and walk every match:

```bash
grep -rn "NetworkID{ID:\|netIDMap\.Add\|spawner\.NewEntity.*NetworkID" pkg/universe/ pkg/engine/ internal/ 2>&1 | grep -v _test.go
```

Expected: every hit either (a) flows through one of the five wired entry
points above, or (b) is documented in a comment explaining why it's exempt
(e.g. "test-only fixture"). Any hit that's neither is a new case to wire
into the index before Task D7 lands.

If you find one, add the integration (analogous to D3's `SpawnFromTransferCore`
wiring) in THIS commit — don't skip it to a follow-up.

- [ ] **Step 4: Run S7 + commit**

```bash
go test ./pkg/universe/ -run '^TestS7' -count=1 -timeout 180s
git add pkg/universe/world_base.go pkg/engine/engine.go
git commit -m "feat(integrity): Exit netIDIndex on entity removal via OnEntityRemoved"
```

---

### Task D7: Add the `no-duplicate-presence-per-cell` invariant

**Files:**
- Modify: `pkg/universe/integrity.go`
- Modify: `pkg/universe/integrity_test.go`

- [ ] **Step 1: Implement the invariant**

Append to `integrity.go`:

```go
// invNoDuplicatePresencePerCell asserts that within each cell, no netID
// has more than one entry in the netIDIndex (the index's internal
// invariant is already "one slot per netID", so this check is
// somewhat redundant — but catches any case where the index is bypassed
// and two ECS entities exist with the same netID unmanaged).
var invNoDuplicatePresencePerCell = Invariant{
	Name: "no-duplicate-presence-per-cell",
	Check: func(c *Process) error {
		for cellKey, cell := range c.Cells {
			if cell.Base == nil || cell.Base.netIDIdx == nil {
				continue
			}
			// Count ECS entities with NetworkID and cross-check against
			// the index. Any netID appearing in ECS but not in the index
			// is a "ghost" spawn path.
			netIDMap := cell.Base.netIDMap
			seen := make(map[uint32]int)
			filter := ecs.NewFilter1[component.NetworkID](cell.Base.eng.ECS)
			q := filter.Query()
			for q.Next() {
				e := q.Entity()
				if !netIDMap.HasAll(e) {
					continue
				}
				id := netIDMap.Get(e).ID
				seen[id]++
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
```

Add imports at top of `integrity.go`:

```go
import (
	"github.com/mlange-42/ark/ecs"
	"github.com/zenion/mmokit/pkg/component"
)
```

- [ ] **Step 2: Add to `defaultInvariants`**

Append to the slice:

```go
var defaultInvariants = []Invariant{
	invCoordMapsConsistent,
	invHostOwnershipMatchesCoord,
	invTopologyNeighborsOwned,
	invSessionRouteHostLive,
	invNoDuplicatePresencePerCell,
}
```

- [ ] **Step 3: Add a simple passing test**

Append to `integrity_test.go`:

```go
func TestInvariant_NoDuplicatePresencePerCell_Smoke(t *testing.T) {
	// Smoke-test: empty Process has no duplicates.
	c := &Process{Cells: make(map[string]*Cell)}
	if err := invNoDuplicatePresencePerCell.Check(c); err != nil {
		t.Fatalf("empty Process should pass: %v", err)
	}
}
```

- [ ] **Step 4: Run S7 + commit**

```bash
go test ./pkg/universe/ -run '^TestS7|TestInvariant' -count=1 -timeout 180s
```

```bash
git add pkg/universe/integrity.go pkg/universe/integrity_test.go
git commit -m "feat(integrity): no-duplicate-presence-per-cell invariant"
```

---

### Task D8: Enable strict mode in tests, verify, and document rollout

**Files:**
- Modify: `pkg/universe/cluster_fixture_test.go`
- Modify: `docs/superpowers/plans/2026-04-20-state-integrity.md` (this file — add rollout note)

- [ ] **Step 1: Set `StrictNetIDIndex: true` in fixture configs**

In each `Config{...}` literal in `cluster_fixture_test.go`:

```go
		StrictNetIDIndex: true,
```

- [ ] **Step 2: Run the full S7 suite**

```bash
go test ./pkg/universe/ -run '^TestS7' -count=1 -timeout 180s -v 2>&1 | tail -20
```

Expected: all pass. If any failure fires with "duplicate live netID X" or similar, that's an edge case where a spawn path is creating duplicates despite the fixes we landed this session. Investigate the specific call stack and either:

- Fix the call site to consult the index first (and short-circuit on Rejected/Duplicate), OR
- Adjust the transition policy if genuinely needed.

- [ ] **Step 3: Document the dev → prod rollout**

Append to this plan file (at the end):

```markdown
## Strict netIDIndex rollout notes (Phase D complete)

StrictNetIDIndex is enabled by default in tests and should remain
`false` in production `Config` initially. Suggested rollout:

1. Ship this plan with `StrictNetIDIndex=false` in prod. The index
   runs in observe-only mode; metrics/logs surface any unexpected
   transitions without enforcement.
2. After one week of clean dev + staging metrics, flip
   `StrictNetIDIndex=true` on a canary shard in prod.
3. Once the canary is stable for another week, flip cluster-wide.

If any production incident correlates with the flag being on,
flip it off immediately — existing spawn paths continue to function
without the enforcement.
```

- [ ] **Step 4: Commit**

```bash
git add pkg/universe/cluster_fixture_test.go docs/superpowers/plans/2026-04-20-state-integrity.md
git commit -m "feat(integrity): enable StrictNetIDIndex in tests + rollout notes"
```

---

## Phase E — Bot-load smoke test

Added after T&T shipped two post-green-tests bugs (see lessons at top of plan). Unit + integration tests exercise the mesh under synthetic conditions; real gameplay with concurrent players produces tick-phase combinations and AoI overlaps that synthetic fixtures miss. Phase E is the end-to-end guardrail before declaring the invariant framework shippable.

### Task E1: Bot-load verification under InvariantPanic

**Files:** (verification only — no new source)

**Preconditions:**

- Phases A–D merged to `main`. `InvariantPanic` + `StrictNetIDIndex=true` set in the test fixture. Production config still at `InvariantLog` + `StrictNetIDIndex=false`.
- `examples/4node-basic` console `bot` command group available (`bot spawn`, `bot clear`, `bot list`).

- [ ] **Step 1: Build and launch the 4node-basic example with invariants in Panic mode**

A config knob or env var to flip `InvariantMode` in examples is out of scope; for this test, temporarily edit `examples/4node-basic/main.go` (or its equivalent config site) to set `InvariantMode: universe.InvariantPanic` on the Config. Revert after testing — prod default stays `InvariantLog`.

Run:

```bash
just build
cd examples/4node-basic && just dev
```

Expected: server starts, vite serves at localhost:8080, no early panics.

- [ ] **Step 2: Drive load**

From the server admin console:

```text
bot spawn 60
```

Wait for bots to fully load (check `bot list`). Open the web client in a browser and log in as a human player.

- [ ] **Step 3: Execute 10 splits, 10 merges, 10 migrates**

From the admin console, drive at least:

- 10 cell splits (`cell split <id>`) — across different cells, not the same one
- 10 merges (`cell merge <id>`)
- 10 migrates (`cell migrate <id> <host>`) — requires `--mode=coordinator --control-listen=:9100` + remote hosts. If running single-process, substitute with 10 more split/merge cycles and document in the test log.

Between operations, drive the human player across cell boundaries — especially into/out of freshly-split sub-cells — to exercise handoff under every topology state.

- [ ] **Step 4: Confirm zero panics**

Expected: no panics in server stderr throughout the ~60s of load. No test assertions to run here; the `InvariantPanic` mode IS the assertion.

If a panic fires: **do not proceed**. Capture the panic trace (it includes the invariant name and the "where" context string), use `commit log --commit_id=<N>` to see the plan-step sequence leading up to it, and diagnose the underlying state bug. A panic here means the invariant framework is doing its job — a bug the synthetic tests didn't catch has been surfaced.

- [ ] **Step 5: Tail the event log during load to verify operational visibility**

While load is running, from a second admin console (or SSH session):

```text
log events:*
```

Expected: every split/merge/migrate produces a visible sequence of plan-step events with timing and affected cells/hosts. If events aren't streaming, Phase C's wiring has a gap; fix before shipping.

Also verify via HTTP:

```bash
curl -s http://localhost:8080/events?n=50 | jq '.[] | {seq: .SeqNo, scenario: .Scenario, step: .Step, success: .Success}'
```

Expected: JSON array with Scenario + Step + Success visible, filterable by kind.

- [ ] **Step 6: Revert local InvariantPanic flip, commit the test-run notes**

Revert the `examples/4node-basic/main.go` edit (InvariantPanic → InvariantLog for prod default). Capture the bot-load test run as a brief note in the PR description or a follow-up memory:

- Number of splits/merges/migrates actually driven
- Any panics observed (should be zero)
- Any operational rough edges in `commit log` / `log events:*` / `/events` (file follow-up issues)

No code commit required for this phase unless panics surfaced and required fixes — in which case the fix is the commit.

---

## Review checkpoint — Implementation complete

**Exit criteria:**

- `go test ./... -count=1 -timeout 300s` — all green.
- `git log --oneline origin/main..HEAD` shows roughly 25 focused commits covering the five phases.
- Manual smoke: `commit log` from the console during a split returns a readable sequence of plan steps; `log events:split` streams them live.
- **Phase E bot-load run completed with zero panics under `InvariantPanic` mode.** This is the gate — no amount of unit-test green replaces it.
- Fixture default: `InvariantPanic` + `StrictNetIDIndex=true` — any state regression in a future change will fail loudly.
- Production default: `InvariantLog` + `StrictNetIDIndex=false` initially, per the rollout notes above.

## Strict netIDIndex rollout notes (Phase D complete)

StrictNetIDIndex is enabled by default in tests and should remain
`false` in production `Config` initially. Suggested rollout:

1. Ship this plan with `StrictNetIDIndex=false` in prod. The index
   runs in observe-only mode; metrics/logs surface any unexpected
   transitions without enforcement.
2. After one week of clean dev + staging metrics, flip
   `StrictNetIDIndex=true` on a canary shard in prod.
3. Once the canary is stable for another week, flip cluster-wide.

If any production incident correlates with the flag being on,
flip it off immediately — existing spawn paths continue to function
without the enforcement.

Ship phases A → B → C → D → E as separate reviewable chunks if the team prefers. Each is independently mergeable and testable.
