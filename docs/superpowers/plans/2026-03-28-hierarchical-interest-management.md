# Hierarchical Interest Management Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add per-entity-type AoI tiers, priority accumulation, and dormancy to the ReplicationSystem so games can control visibility radius, update frequency, and priority weight per entity type.

**Architecture:** Single max-radius spatial query with post-filtering by per-type tier radius. Priority accumulator tracks per-viewer per-entity importance (type weight * distance * staleness). Dormancy skips all replication work for entities unchanged for N ticks. Optional `TierProvider` interface on `EntityReplicator`; games that don't implement it get current behavior unchanged.

**Tech Stack:** Go, ECS (Ark), existing `pkg/system/` replication framework

**Spec:** `docs/superpowers/specs/2026-03-28-hierarchical-interest-management-design.md`

---

## File Structure

| File | Role | Action |
|---|---|---|
| `pkg/system/replication.go` | Core replication loop, interfaces, tier types | Modify |
| `pkg/system/baseline.go` | Per-connection state, priority state | Modify |
| `pkg/system/baseline_test.go` | Unit tests for priority state | Modify |
| `pkg/system/replication_test.go` | Unit tests for tier caching, update loop behavior | Create |
| `examples/slither/replication.go` | Food replicator tier config | Modify |
| `examples/slither/system_network.go` | Dormancy threshold config | Modify |

---

### Task 1: Add Priority State to Connection State

**Files:**
- Modify: `pkg/system/baseline.go`
- Modify: `pkg/system/baseline_test.go`

- [ ] **Step 1: Write tests for entityPriorityState and connectionState.priorities**

Add to `pkg/system/baseline_test.go`:

```go
func TestConnectionState_GetAndRemovePriorityState(t *testing.T) {
	conn := newConnectionState()

	ps := conn.getPriorityState(42)
	if ps == nil {
		t.Fatal("expected non-nil priority state")
	}
	if ps.accumulator != 0 || ps.lastSentTick != 0 || ps.unchangedTicks != 0 {
		t.Fatal("expected zero-value priority state")
	}

	// Get again — should return same instance.
	ps2 := conn.getPriorityState(42)
	if ps2 != ps {
		t.Fatal("expected same priority state instance")
	}

	// Modify and verify.
	ps.accumulator = 1.5
	ps.lastSentTick = 10
	ps.unchangedTicks = 5
	ps3 := conn.getPriorityState(42)
	if ps3.accumulator != 1.5 || ps3.lastSentTick != 10 || ps3.unchangedTicks != 5 {
		t.Fatal("expected modified priority state")
	}

	conn.removePriorityState(42)
	ps4 := conn.getPriorityState(42)
	if ps4 == ps {
		t.Fatal("expected new priority state after remove")
	}
	if ps4.accumulator != 0 {
		t.Fatal("expected zero accumulator after remove")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd . && go test ./pkg/system/ -run TestConnectionState_GetAndRemovePriorityState -v`
Expected: FAIL — `getPriorityState` and `removePriorityState` undefined

- [ ] **Step 3: Implement entityPriorityState and extend connectionState**

In `pkg/system/baseline.go`, add the priority state type and extend `connectionState`:

```go
// entityPriorityState tracks per-entity replication priority for one connection.
type entityPriorityState struct {
	accumulator    float32 // accumulated priority since last send
	lastSentTick   uint32  // tick when last update was sent
	unchangedTicks uint32  // consecutive ticks with same hash (dormancy tracking)
}

// In connectionState, add:
type connectionState struct {
	ackedSeq   uint32
	nextSeq    uint32
	baselines  map[uint32]*entityBaseline
	lastHash   map[uint32]uint64
	priorities map[uint32]*entityPriorityState
}
```

Update `newConnectionState()`:

```go
func newConnectionState() *connectionState {
	return &connectionState{
		baselines:  make(map[uint32]*entityBaseline),
		lastHash:   make(map[uint32]uint64),
		priorities: make(map[uint32]*entityPriorityState),
	}
}
```

Add methods:

```go
func (c *connectionState) getPriorityState(netID uint32) *entityPriorityState {
	ps, ok := c.priorities[netID]
	if !ok {
		ps = &entityPriorityState{}
		c.priorities[netID] = ps
	}
	return ps
}

func (c *connectionState) removePriorityState(netID uint32) {
	delete(c.priorities, netID)
}
```

Update `removeBaseline` to also clean up priority state (so exit cleanup at lines 493-499 needs no additional changes):

```go
func (c *connectionState) removeBaseline(netID uint32) {
	delete(c.baselines, netID)
	delete(c.lastHash, netID)
	delete(c.priorities, netID)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd . && go test ./pkg/system/ -run TestConnectionState -v`
Expected: PASS (both existing and new tests)

- [ ] **Step 5: Commit**

```bash
git add pkg/system/baseline.go pkg/system/baseline_test.go
git commit -m "feat(replication): add per-entity priority state to connectionState"
```

---

### Task 2: Add ReplicationTier Type and TierProvider Interface

**Files:**
- Modify: `pkg/system/replication.go`
- Create: `pkg/system/replication_test.go`

- [ ] **Step 1: Write test for tier caching in NewReplicationSystem**

Create `pkg/system/replication_test.go`:

```go
package system

import (
	"testing"

	"github.com/mlange-42/ark/ecs"
	"github.com/zenion/mmoserver/pkg/component"
	"github.com/zenion/mmoserver/pkg/quantize"
	"github.com/zenion/mmoserver/pkg/spatial"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// testReplicator is a minimal EntityReplicator for testing.
type testReplicator struct {
	entityType uint8
}

func (r *testReplicator) EntityType() uint8 { return r.entityType }
func (r *testReplicator) Hash(h *Hasher, viewer *ViewerInfo, entry spatial.Entry) {
	h.Float32(entry.X)
	h.Float32(entry.Y)
}
func (r *testReplicator) Snapshot(w *quantize.SnapshotWriter, viewer *ViewerInfo, entry spatial.Entry) {
	w.Float32(entry.X)
	w.Float32(entry.Y)
}
func (r *testReplicator) SnapshotLayout() []int { return []int{4, 4} }
func (r *testReplicator) InitialData(viewer *ViewerInfo, entry spatial.Entry) []byte { return nil }

// tieredReplicator implements TierProvider.
type tieredReplicator struct {
	testReplicator
	tier ReplicationTier
}

func (r *tieredReplicator) ReplicationTier() ReplicationTier { return r.tier }

// Stub implementations for required interfaces.
type stubViewerSource struct{}

func (s *stubViewerSource) ActiveViewers() []ViewerInfo { return nil }

type stubFrameWriter struct {
	frames []ReplicationFrame
}

func (s *stubFrameWriter) WriteFrame(frame *ReplicationFrame) {
	s.frames = append(s.frames, *frame)
}

type fixedViewerSource struct {
	viewers []ViewerInfo
}

func (s *fixedViewerSource) ActiveViewers() []ViewerInfo { return s.viewers }

// testEntityMapper creates entities with Position + NetworkID + EntityKind.
type testEntityMapper struct {
	mapper *ecs.Map3[component.Position, component.NetworkID, component.EntityKind]
}

func newTestEntityMapper(world *ecs.World) *testEntityMapper {
	return &testEntityMapper{
		mapper: ecs.NewMap3[component.Position, component.NetworkID, component.EntityKind](world),
	}
}

func (m *testEntityMapper) spawn(x, y float32, netID uint32, kind uint8) ecs.Entity {
	return m.mapper.NewEntity(
		&component.Position{X: x, Y: y},
		&component.NetworkID{ID: netID},
		&component.EntityKind{Type: kind},
	)
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestNewReplicationSystem_TierCaching(t *testing.T) {
	world := ecs.NewWorld()
	grid := spatial.NewGrid(100)

	reg := NewReplicatorRegistry()
	// Type 0: no tier (default)
	reg.Register(&testReplicator{entityType: 0})
	// Type 1: custom tier with smaller radius
	reg.Register(&tieredReplicator{
		testReplicator: testReplicator{entityType: 1},
		tier:           ReplicationTier{Radius: 500, UpdateDivisor: 3, BaseWeight: 0.3},
	})
	// Type 2: custom tier with larger radius
	reg.Register(&tieredReplicator{
		testReplicator: testReplicator{entityType: 2},
		tier:           ReplicationTier{Radius: 5000, UpdateDivisor: 1, BaseWeight: 1.5},
	})

	s := NewReplicationSystem(ReplicationConfig{
		World:       &world,
		Grid:        grid,
		Viewers:     &stubViewerSource{},
		Frame:       &stubFrameWriter{},
		Replicators: reg,
		AoIRadius:   3000,
		GetTick:     func() uint32 { return 0 },
	})

	// maxRadius should be max(3000, 500, 5000) = 5000
	if s.maxRadius != 5000 {
		t.Fatalf("expected maxRadius=5000, got %v", s.maxRadius)
	}

	// Type 0 should get default tier.
	tier0 := s.tierConfigs[0]
	if tier0.Radius != 0 || tier0.UpdateDivisor != 1 || tier0.BaseWeight != 1.0 {
		t.Fatalf("expected default tier for type 0, got %+v", tier0)
	}

	// Type 1 should get custom tier.
	tier1 := s.tierConfigs[1]
	if tier1.Radius != 500 || tier1.UpdateDivisor != 3 {
		t.Fatalf("expected custom tier for type 1, got %+v", tier1)
	}

	// Type 2 should get custom tier.
	tier2 := s.tierConfigs[2]
	if tier2.Radius != 5000 || tier2.BaseWeight != 1.5 {
		t.Fatalf("expected custom tier for type 2, got %+v", tier2)
	}
}

func TestNewReplicationSystem_NoTiers_MaxRadiusEqualsAoI(t *testing.T) {
	world := ecs.NewWorld()
	grid := spatial.NewGrid(100)

	reg := NewReplicatorRegistry()
	reg.Register(&testReplicator{entityType: 0})

	s := NewReplicationSystem(ReplicationConfig{
		World:       &world,
		Grid:        grid,
		Viewers:     &stubViewerSource{},
		Frame:       &stubFrameWriter{},
		Replicators: reg,
		AoIRadius:   3000,
		GetTick:     func() uint32 { return 0 },
	})

	if s.maxRadius != 3000 {
		t.Fatalf("expected maxRadius=3000 (same as AoIRadius), got %v", s.maxRadius)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd . && go test ./pkg/system/ -run TestNewReplicationSystem -v`
Expected: FAIL — `ReplicationTier`, `TierProvider`, `tierConfigs`, `maxRadius` undefined

- [ ] **Step 3: Implement ReplicationTier, TierProvider, and tier caching**

In `pkg/system/replication.go`, add types after `PriorityProvider`:

```go
// ReplicationTier configures per-entity-type replication behavior.
type ReplicationTier struct {
	Radius        float32 // AoI radius for this type (0 = use global AoIRadius)
	UpdateDivisor uint32  // send every N ticks (1 = every tick, 3 = every 3rd)
	BaseWeight    float32 // priority accumulator weight (higher = more important)
}

// TierProvider is an optional interface on EntityReplicator.
// Implement it to override the default replication tier for an entity type.
type TierProvider interface {
	ReplicationTier() ReplicationTier
}
```

Add `DormancyThreshold` to `ReplicationConfig`:

```go
// DormancyThreshold is the number of consecutive unchanged ticks before an
// entity becomes dormant (skips all replication work). 0 disables dormancy.
DormancyThreshold uint32
```

Add fields to `ReplicationSystem`:

```go
tierConfigs map[uint8]ReplicationTier
maxRadius   float32
```

In `NewReplicationSystem`, after building delta encoders, add tier caching:

```go
// Cache tier configs and compute max radius.
defaultTier := ReplicationTier{Radius: 0, UpdateDivisor: 1, BaseWeight: 1.0}
tierConfigs := make(map[uint8]ReplicationTier)
maxRadius := cfg.AoIRadius
for entityType, rep := range cfg.Replicators.replicators {
	if tp, ok := rep.(TierProvider); ok {
		tier := tp.ReplicationTier()
		if tier.UpdateDivisor == 0 {
			tier.UpdateDivisor = 1
		}
		if tier.BaseWeight == 0 {
			tier.BaseWeight = 1.0
		}
		tierConfigs[entityType] = tier
		if tier.Radius > maxRadius {
			maxRadius = tier.Radius
		}
	} else {
		tierConfigs[entityType] = defaultTier
	}
}
```

Store in the returned struct:

```go
tierConfigs: tierConfigs,
maxRadius:   maxRadius,
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd . && go test ./pkg/system/ -run TestNewReplicationSystem -v`
Expected: PASS

- [ ] **Step 5: Verify compilation**

Run: `cd . && go vet ./...`
Expected: No errors

- [ ] **Step 6: Commit**

```bash
git add pkg/system/replication.go pkg/system/replication_test.go
git commit -m "feat(replication): add ReplicationTier type, TierProvider interface, and tier caching"
```

---

### Task 3: Modify Update Loop — Tier Radius Cutoff and Hash-Unchanged Fix

This is the core hot-path change. We modify the per-entity inner loop to:
1. Use `maxRadius` for the spatial query
2. Post-filter by per-type tier radius
3. Fix the hash-unchanged visibility bug (entities must stay in `currentVisible`)

**Files:**
- Modify: `pkg/system/replication.go`
- Modify: `pkg/system/replication_test.go`

- [ ] **Step 1: Write test for tier radius cutoff**

Add to `pkg/system/replication_test.go`:

```go
func TestReplicationSystem_TierRadiusCutoff(t *testing.T) {
	world := ecs.NewWorld()
	grid := spatial.NewGrid(100)
	em := newTestEntityMapper(&world)

	// Create two entity types: type 0 at full radius, type 1 at half radius.
	reg := NewReplicatorRegistry()
	reg.Register(&testReplicator{entityType: 0})
	reg.Register(&tieredReplicator{
		testReplicator: testReplicator{entityType: 1},
		tier:           ReplicationTier{Radius: 500, UpdateDivisor: 1, BaseWeight: 1.0},
	})

	fw := &stubFrameWriter{}
	tick := uint32(0)

	s := NewReplicationSystem(ReplicationConfig{
		World:       &world,
		Grid:        grid,
		Viewers:     &stubViewerSource{},
		Frame:       fw,
		Replicators: reg,
		AoIRadius:   3000,
		GetTick:     func() uint32 { return tick },
	})

	// Spawn viewer at origin.
	viewerEntity := em.spawn(0, 0, 100, 0)
	grid.Register(spatial.Entry{Entity: viewerEntity, X: 0, Y: 0, NetID: 100, Kind: 0})

	// Spawn type-0 entity at distance 1000 (within both radii).
	e0 := em.spawn(1000, 0, 1, 0)
	grid.Register(spatial.Entry{Entity: e0, X: 1000, Y: 0, NetID: 1, Kind: 0})

	// Spawn type-1 entity at distance 1000 (outside type-1's 500 radius).
	e1 := em.spawn(1000, 0, 2, 1)
	grid.Register(spatial.Entry{Entity: e1, X: 1000, Y: 0, NetID: 2, Kind: 1})

	// Spawn type-1 entity at distance 400 (within type-1's 500 radius).
	e1close := em.spawn(400, 0, 3, 1)
	grid.Register(spatial.Entry{Entity: e1close, X: 400, Y: 0, NetID: 3, Kind: 1})

	// Override viewers to return our test viewer.
	viewers := []ViewerInfo{{ConnID: 1, Entity: viewerEntity, X: 0, Y: 0}}
	s.cfg.Viewers = &fixedViewerSource{viewers: viewers}

	tick = 1
	s.Update(0)

	if len(fw.frames) != 1 {
		t.Fatalf("expected 1 frame, got %d", len(fw.frames))
	}
	frame := fw.frames[0]

	// Should have: entity 1 (type 0, within 3000), entity 3 (type 1, within 500).
	// Should NOT have: entity 2 (type 1, at 1000 > 500), entity 100 (viewer itself gets filtered by dedup/self).
	fullNetIDs := make(map[uint32]bool)
	for _, f := range frame.Full {
		fullNetIDs[f.NetID] = true
	}
	if !fullNetIDs[1] {
		t.Error("expected entity 1 (type 0 at 1000 < 3000) to be visible")
	}
	if fullNetIDs[2] {
		t.Error("expected entity 2 (type 1 at 1000 > 500) to be filtered out")
	}
	if !fullNetIDs[3] {
		t.Error("expected entity 3 (type 1 at 400 < 500) to be visible")
	}
}
```

- [ ] **Step 2: Write test for hash-unchanged visibility fix**

Add to `pkg/system/replication_test.go`:

```go
func TestReplicationSystem_HashUnchanged_StaysVisible(t *testing.T) {
	world := ecs.NewWorld()
	grid := spatial.NewGrid(100)
	em := newTestEntityMapper(&world)

	reg := NewReplicatorRegistry()
	reg.Register(&testReplicator{entityType: 0})

	fw := &stubFrameWriter{}
	tick := uint32(0)

	s := NewReplicationSystem(ReplicationConfig{
		World:       &world,
		Grid:        grid,
		Viewers:     &stubViewerSource{},
		Frame:       fw,
		Replicators: reg,
		AoIRadius:   3000,
		GetTick:     func() uint32 { return tick },
	})

	// Spawn viewer.
	viewerEntity := em.spawn(0, 0, 100, 0)
	grid.Register(spatial.Entry{Entity: viewerEntity, X: 0, Y: 0, NetID: 100, Kind: 0})

	// Spawn a static entity.
	e := em.spawn(100, 0, 1, 0)
	grid.Register(spatial.Entry{Entity: e, X: 100, Y: 0, NetID: 1, Kind: 0})

	viewers := []ViewerInfo{{ConnID: 1, Entity: viewerEntity, X: 0, Y: 0}}
	s.cfg.Viewers = &fixedViewerSource{viewers: viewers}

	// Tick 1: entity enters visibility (full payload).
	tick = 1
	s.Update(0)
	if len(fw.frames) != 1 || len(fw.frames[0].Full) == 0 {
		t.Fatal("expected entity to enter on tick 1")
	}

	// Tick 2: entity unchanged — should NOT generate exit event.
	fw.frames = nil
	tick = 2
	s.Update(0)
	if len(fw.frames) != 1 {
		t.Fatalf("expected 1 frame on tick 2, got %d", len(fw.frames))
	}
	frame := fw.frames[0]
	if len(frame.Exited) > 0 {
		t.Errorf("expected no exited entities on tick 2, got %v", frame.Exited)
	}
	if len(frame.Removed) > 0 {
		t.Errorf("expected no removed entities on tick 2, got %v", frame.Removed)
	}

	// Tick 3: still unchanged — should still NOT exit.
	fw.frames = nil
	tick = 3
	s.Update(0)
	frame = fw.frames[0]
	if len(frame.Exited) > 0 {
		t.Errorf("expected no exited entities on tick 3, got %v", frame.Exited)
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `cd . && go test ./pkg/system/ -run "TestReplicationSystem_TierRadius|TestReplicationSystem_HashUnchanged" -v`
Expected: FAIL — tests either won't compile (missing component vars) or fail due to missing tier logic / hash-unchanged bug

Note: If the test infrastructure needs adjustment for ECS component registration (Ark requires component IDs), check how existing tests in the codebase handle this and adapt accordingly. The key structure is correct; the exact ECS API may need `ecs.ComponentID[T]()` calls.

- [ ] **Step 4: Implement tier radius cutoff and hash-unchanged fix in Update loop**

In `pkg/system/replication.go`, modify the `Update` method:

**Line 341** — change `AoIRadius` to `maxRadius`:

```go
s.results = s.cfg.Grid.QueryRadius(viewer.X, viewer.Y, s.maxRadius, s.results[:0])
```

**After line 380** (after RelevancyProvider check) — add tier radius cutoff:

```go
// Per-type AoI radius cutoff.
tier := s.tierConfigs[entityType]
tierRadius := tier.Radius
if tierRadius == 0 {
	tierRadius = s.cfg.AoIRadius
}
dx := entry.X - viewer.X
dy := entry.Y - viewer.Y
dist2 := dx*dx + dy*dy
if dist2 > tierRadius*tierRadius {
	continue
}
```

**Lines 396-400** — fix hash-unchanged path to mark entity visible:

```go
if !isNew && !isKeyframe {
	if lastHash, ok := conn.lastHash[netID]; ok && lastHash == hash {
		currentVisible[netID] = true // keep visible — don't generate false exit
		continue
	}
}
```

Add `"math"` to imports for `math.Sqrt` (needed in Task 4).

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd . && go test ./pkg/system/ -run "TestReplicationSystem" -v`
Expected: PASS

- [ ] **Step 6: Verify full compilation**

Run: `cd . && go vet ./...`
Expected: No errors

- [ ] **Step 7: Commit**

```bash
git add pkg/system/replication.go pkg/system/replication_test.go
git commit -m "feat(replication): tier radius cutoff and hash-unchanged visibility fix"
```

---

### Task 4: Add Dormancy and Update Divisor to Update Loop

**Files:**
- Modify: `pkg/system/replication.go`
- Modify: `pkg/system/replication_test.go`

- [ ] **Step 1: Write test for update divisor**

Add to `pkg/system/replication_test.go`:

```go
func TestReplicationSystem_UpdateDivisor(t *testing.T) {
	world := ecs.NewWorld()
	grid := spatial.NewGrid(100)
	em := newTestEntityMapper(&world)

	reg := NewReplicatorRegistry()
	// Type 0: update every 3rd tick.
	reg.Register(&tieredReplicator{
		testReplicator: testReplicator{entityType: 0},
		tier:           ReplicationTier{Radius: 3000, UpdateDivisor: 3, BaseWeight: 1.0},
	})

	fw := &stubFrameWriter{}
	tick := uint32(0)

	s := NewReplicationSystem(ReplicationConfig{
		World:       &world,
		Grid:        grid,
		Viewers:     &stubViewerSource{},
		Frame:       fw,
		Replicators: reg,
		AoIRadius:   3000,
		GetTick:     func() uint32 { return tick },
	})

	viewerEntity := em.spawn(0, 0, 100, 0)
	grid.Register(spatial.Entry{Entity: viewerEntity, X: 0, Y: 0, NetID: 100, Kind: 0})

	// Spawn entity that changes position each tick.
	e := em.spawn(100, 0, 1, 0)
	grid.Register(spatial.Entry{Entity: e, X: 100, Y: 0, NetID: 1, Kind: 0})

	viewers := []ViewerInfo{{ConnID: 1, Entity: viewerEntity, X: 0, Y: 0}}
	s.cfg.Viewers = &fixedViewerSource{viewers: viewers}

	// Tick 1: first seen, always sends full payload regardless of divisor.
	tick = 1
	s.Update(0)
	if len(fw.frames[0].Full) == 0 {
		t.Fatal("expected full payload on first tick")
	}

	// Ticks 2-7: entity changes each tick. Only ticks divisible by 3 should send.
	sentTicks := []uint32{}
	for i := uint32(2); i <= 7; i++ {
		fw.frames = nil
		tick = i
		// Move entity to change hash.
		grid.Update(spatial.Entry{Entity: e, X: float32(100 + i), Y: 0, NetID: 1, Kind: 0})
		s.Update(0)
		frame := fw.frames[0]
		if len(frame.Deltas) > 0 || len(frame.Full) > 0 {
			sentTicks = append(sentTicks, i)
		}
		// Should never exit.
		if len(frame.Exited) > 0 {
			t.Errorf("unexpected exit on tick %d", i)
		}
	}

	// With divisor=3, should send on ticks 3 and 6.
	expected := []uint32{3, 6}
	if len(sentTicks) != len(expected) {
		t.Fatalf("expected sends on ticks %v, got %v", expected, sentTicks)
	}
	for i, e := range expected {
		if sentTicks[i] != e {
			t.Fatalf("expected send on tick %d, got %d", e, sentTicks[i])
		}
	}
}
```

- [ ] **Step 2: Write test for dormancy**

Add to `pkg/system/replication_test.go`:

```go
func TestReplicationSystem_Dormancy(t *testing.T) {
	world := ecs.NewWorld()
	grid := spatial.NewGrid(100)
	em := newTestEntityMapper(&world)

	reg := NewReplicatorRegistry()
	reg.Register(&testReplicator{entityType: 0})

	fw := &stubFrameWriter{}
	tick := uint32(0)

	s := NewReplicationSystem(ReplicationConfig{
		World:             &world,
		Grid:              grid,
		Viewers:           &stubViewerSource{},
		Frame:             fw,
		Replicators:       reg,
		AoIRadius:         3000,
		GetTick:           func() uint32 { return tick },
		DormancyThreshold: 3, // dormant after 3 unchanged ticks
	})

	viewerEntity := em.spawn(0, 0, 100, 0)
	grid.Register(spatial.Entry{Entity: viewerEntity, X: 0, Y: 0, NetID: 100, Kind: 0})

	e := em.spawn(100, 0, 1, 0)
	grid.Register(spatial.Entry{Entity: e, X: 100, Y: 0, NetID: 1, Kind: 0})

	viewers := []ViewerInfo{{ConnID: 1, Entity: viewerEntity, X: 0, Y: 0}}
	s.cfg.Viewers = &fixedViewerSource{viewers: viewers}

	// Tick 1: enters visibility.
	tick = 1
	s.Update(0)

	// Ticks 2-4: hash unchanged, building up unchangedTicks counter.
	// After tick 4, unchangedTicks should be 3 (ticks 2, 3, 4).
	for i := uint32(2); i <= 4; i++ {
		fw.frames = nil
		tick = i
		s.Update(0)
	}

	// Tick 5: now dormant (unchangedTicks >= 3). Verify entity still visible (no exit).
	fw.frames = nil
	tick = 5
	s.Update(0)
	frame := fw.frames[0]
	if len(frame.Exited) > 0 {
		t.Error("dormant entity should not exit")
	}

	// Verify the entity is dormant by checking priority state.
	conn := s.connections[1]
	ps := conn.getPriorityState(1)
	if ps.unchangedTicks < 3 {
		t.Errorf("expected unchangedTicks >= 3, got %d", ps.unchangedTicks)
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `cd . && go test ./pkg/system/ -run "TestReplicationSystem_UpdateDivisor|TestReplicationSystem_Dormancy" -v`
Expected: FAIL — no update divisor or dormancy logic in the loop yet

- [ ] **Step 4: Implement dormancy and update divisor in the Update loop**

In `pkg/system/replication.go`, modify the `Update` method. The changes go into the per-entity loop, after the tier radius cutoff (added in Task 3) and after the `isNew` check:

**After the `isNew` check (around line 389) and before hash computation — add dormancy check:**

```go
// Dormancy check — skip hash and all replication work for long-unchanged entities.
ps := conn.getPriorityState(netID)
if !isNew && s.cfg.DormancyThreshold > 0 && ps.unchangedTicks >= s.cfg.DormancyThreshold {
	currentVisible[netID] = true
	continue
}
```

**Modify the hash-unchanged path (where we added `currentVisible[netID] = true` in Task 3) — add dormancy counter increment:**

```go
if !isNew && !isKeyframe {
	if lastHash, ok := conn.lastHash[netID]; ok && lastHash == hash {
		ps.unchangedTicks++
		currentVisible[netID] = true
		continue
	}
}
ps.unchangedTicks = 0 // state changed, reset dormancy counter
conn.lastHash[netID] = hash
```

**After `conn.lastHash[netID] = hash` and before snapshot generation — add update divisor gate:**

```go
// Update divisor gate — skip snapshot but keep visible and accumulate priority.
if !isNew && tier.UpdateDivisor > 1 && tick%tier.UpdateDivisor != 0 {
	currentVisible[netID] = true
	// Accumulate priority for when we do send.
	dist := float32(math.Sqrt(float64(dist2)))
	distFactor := float32(1.0) - (dist / tierRadius)
	if distFactor < 0 {
		distFactor = 0
	}
	basePriority := tier.BaseWeight * distFactor
	if pp, ok := rep.(PriorityProvider); ok {
		basePriority *= pp.NetPriority(viewer, entry)
	}
	ps.accumulator += basePriority
	continue
}

// Record send and reset accumulator.
ps.lastSentTick = tick
ps.accumulator = 0
```

Add `"math"` to the import block.

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd . && go test ./pkg/system/ -v`
Expected: ALL PASS

- [ ] **Step 6: Verify full compilation**

Run: `cd . && go vet ./...`
Expected: No errors

- [ ] **Step 7: Commit**

```bash
git add pkg/system/replication.go pkg/system/replication_test.go
git commit -m "feat(replication): add dormancy and update divisor to replication loop"
```

---

### Task 5: Integrate PriorityProvider into Accumulator

The `PriorityProvider` interface already exists but was never wired in. We need to apply it as a multiplier during the send path too (not just the divisor-skip path from Task 4).

**Files:**
- Modify: `pkg/system/replication.go`
- Modify: `pkg/system/replication_test.go`

- [ ] **Step 1: Write test for PriorityProvider integration**

Add to `pkg/system/replication_test.go`:

```go
// priorityReplicator implements both TierProvider and PriorityProvider.
type priorityReplicator struct {
	testReplicator
	tier     ReplicationTier
	priority float32
}

func (r *priorityReplicator) ReplicationTier() ReplicationTier { return r.tier }
func (r *priorityReplicator) NetPriority(viewer *ViewerInfo, entry spatial.Entry) float32 {
	return r.priority
}

func TestReplicationSystem_PriorityProviderMultiplier(t *testing.T) {
	world := ecs.NewWorld()
	grid := spatial.NewGrid(100)
	em := newTestEntityMapper(&world)

	reg := NewReplicatorRegistry()
	reg.Register(&priorityReplicator{
		testReplicator: testReplicator{entityType: 0},
		tier:           ReplicationTier{Radius: 3000, UpdateDivisor: 2, BaseWeight: 1.0},
		priority:       2.5,
	})

	fw := &stubFrameWriter{}
	tick := uint32(0)

	s := NewReplicationSystem(ReplicationConfig{
		World:       &world,
		Grid:        grid,
		Viewers:     &stubViewerSource{},
		Frame:       fw,
		Replicators: reg,
		AoIRadius:   3000,
		GetTick:     func() uint32 { return tick },
	})

	viewerEntity := em.spawn(0, 0, 100, 0)
	grid.Register(spatial.Entry{Entity: viewerEntity, X: 0, Y: 0, NetID: 100, Kind: 0})

	e := em.spawn(100, 0, 1, 0)
	grid.Register(spatial.Entry{Entity: e, X: 100, Y: 0, NetID: 1, Kind: 0})

	viewers := []ViewerInfo{{ConnID: 1, Entity: viewerEntity, X: 0, Y: 0}}
	s.cfg.Viewers = &fixedViewerSource{viewers: viewers}

	// Tick 1: enters.
	tick = 1
	s.Update(0)

	// Tick 2: divisor=2, tick 2 is a send tick. Move entity to change hash.
	grid.Update(spatial.Entry{Entity: e, X: 105, Y: 0, NetID: 1, Kind: 0})
	tick = 2
	fw.frames = nil
	s.Update(0)

	// Verify accumulator was reset (entity was sent).
	conn := s.connections[1]
	ps := conn.getPriorityState(1)
	if ps.accumulator != 0 {
		t.Errorf("expected accumulator=0 after send, got %v", ps.accumulator)
	}
	if ps.lastSentTick != 2 {
		t.Errorf("expected lastSentTick=2, got %d", ps.lastSentTick)
	}

	// Tick 3: divisor=2, tick 3 is a skip tick. Move entity again.
	grid.Update(spatial.Entry{Entity: e, X: 110, Y: 0, NetID: 1, Kind: 0})
	tick = 3
	fw.frames = nil
	s.Update(0)

	// Verify accumulator was increased with priority multiplier.
	ps = conn.getPriorityState(1)
	if ps.accumulator <= 0 {
		t.Errorf("expected positive accumulator after skip, got %v", ps.accumulator)
	}
	// The accumulator should include the 2.5x priority multiplier.
	// distFactor = 1.0 - (100/3000) ≈ 0.967
	// basePriority = 1.0 * 0.967 * 2.5 ≈ 2.417
	expectedMin := float32(2.0) // rough lower bound
	if ps.accumulator < expectedMin {
		t.Errorf("expected accumulator >= %v (with 2.5x multiplier), got %v", expectedMin, ps.accumulator)
	}
}
```

- [ ] **Step 2: Run test to verify it fails (or passes if Task 4 already handled PriorityProvider in skip path)**

Run: `cd . && go test ./pkg/system/ -run TestReplicationSystem_PriorityProviderMultiplier -v`

If the test passes, the PriorityProvider is already wired from Task 4. If not, proceed to Step 3.

- [ ] **Step 3: Verify PriorityProvider is applied in both paths**

The divisor-skip path in Task 4 already applies `PriorityProvider`. Verify the send path also records it correctly (accumulator reset to 0, lastSentTick set). No additional code changes should be needed if Task 4 was implemented correctly.

- [ ] **Step 4: Run all tests**

Run: `cd . && go test ./pkg/system/ -v`
Expected: ALL PASS

- [ ] **Step 5: Commit**

```bash
git add pkg/system/replication_test.go
git commit -m "test(replication): verify PriorityProvider integration with accumulator"
```

---

### Task 6: Slither Example — Add Food Tier and Dormancy

**Files:**
- Modify: `examples/slither/replication.go`
- Modify: `examples/slither/system_network.go`

- [ ] **Step 1: Add TierProvider to foodReplicator**

In `examples/slither/replication.go`, add the `ReplicationTier` method to `foodReplicator`:

```go
func (r *foodReplicator) ReplicationTier() system.ReplicationTier {
	return system.ReplicationTier{
		Radius:        2000, // food visible at shorter range than snakes
		UpdateDivisor: 3,    // food updates every 3rd tick
		BaseWeight:    0.3,  // low priority
	}
}
```

No changes to `snakeReplicator` — it uses the default tier (full radius, every tick, weight 1.0).

- [ ] **Step 2: Add DormancyThreshold to slither ReplicationConfig**

In `examples/slither/system_network.go`, add `DormancyThreshold` to the `ReplicationConfig`:

```go
s.replSys = mmokit.NewReplicationSystem(system.ReplicationConfig{
	// ... existing fields ...
	DormancyThreshold: 60, // 3 seconds at 20Hz
})
```

- [ ] **Step 3: Verify slither builds**

Run: `cd examples/slither && go build -o /dev/null`
Expected: Build succeeds

- [ ] **Step 4: Verify main game still builds**

Run: `cd . && make build`
Expected: Build succeeds (main game uses default tiers, no changes needed)

- [ ] **Step 5: Commit**

```bash
git add examples/slither/replication.go examples/slither/system_network.go
git commit -m "feat(slither): add food tier (radius 2000, divisor 3) and dormancy (60 ticks)"
```

---

### Task 7: Full Verification

- [ ] **Step 1: Run all tests**

Run: `cd . && go test ./...`
Expected: ALL PASS

- [ ] **Step 2: Run go vet**

Run: `cd . && go vet ./...`
Expected: No errors

- [ ] **Step 3: Build both targets**

Run: `cd . && make build && cd examples/slither && go build -o /dev/null`
Expected: Both succeed

- [ ] **Step 4: Manual smoke test with slither**

Run slither: `cd examples/slither && go run .`

Connect web client and verify:
- Snakes appear and move normally at full radius
- Food appears when close (within ~2000 units) and disappears when far
- No enter/exit flicker on static food
- Food updates are visibly less frequent than snake updates (every 3rd tick)
- Game is playable with no visual regressions

- [ ] **Step 5: Update roadmap**

In `docs/planning/mmokit-roadmap.md`, update item #8 to mark implementation details and link to the spec. Follow the pattern of other completed items.
