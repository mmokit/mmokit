# Hierarchical Interest Management Design

## Context

The `ReplicationSystem` in `pkg/system/replication.go` uses a single `AoIRadius` for all entity types. Every entity within that radius gets 20Hz updates regardless of type or importance. A static food pellet 2000 units away gets identical treatment to a nearby enemy player. This wastes bandwidth on low-priority entities and provides no mechanism for games to control replication frequency per entity type.

This feature adds a three-part enhancement to the replication pipeline:

1. **Per-entity-type AoI tiers** — each replicator declares its own visibility radius and update frequency
2. **Priority accumulator** — entities accumulate priority based on type weight, distance, and staleness; foundation for future bandwidth budgeting
3. **Dormancy** — entities unchanged for N ticks skip all per-tick replication work (hash, snapshot, priority)

The existing `PriorityProvider` interface (defined but never integrated) gets wired into the accumulator as a per-entity multiplier.

**Industry basis:** Unreal Engine's actor relevancy + NetUpdateFrequency, Halo Reach's priority accumulator algorithm (GDC 2011), Gaffer on Games state synchronization model, SpatialOS QBI distance-tiered updates.

## Decisions

- **Scope:** Stages 1 (hard relevancy) + 2 (priority accumulator). No bandwidth budget cap — that's a future extension.
- **Priority formula:** `typeWeight * distanceFactor * timeSinceLastSent`. Proven by Unreal/Halo.
- **No hysteresis:** Hard cutoffs per tier. This is a PvP 2D game where AoI exceeds camera view — leaking entity info via soft boundaries is unacceptable.
- **Config via optional interface:** `TierProvider` on `EntityReplicator`. Games that don't implement it get defaults (global radius, every tick, weight 1.0). Keeps boilerplate down for simple games.
- **PriorityProvider as multiplier:** Existing interface kept, integrated as a multiplicative factor on the base formula. Games can boost specific entities (e.g., target lock).
- **Dormancy:** Configurable threshold on `ReplicationConfig`. Entities with unchanged hash for N consecutive ticks skip all replication work until state changes.

## Design

### New Types

**`pkg/system/replication.go`** — alongside existing interfaces:

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

Default tier (when `TierProvider` not implemented): `{Radius: 0, UpdateDivisor: 1, BaseWeight: 1.0}` — identical to current behavior.

**`pkg/system/baseline.go`** — new per-entity priority state inside `connectionState`:

```go
type entityPriorityState struct {
    accumulator    float32 // accumulated priority since last send
    lastSentTick   uint32  // tick of last snapshot send
    unchangedTicks uint32  // consecutive ticks with same hash (dormancy tracking)
}
```

Add to `connectionState`:

```go
type connectionState struct {
    ackedSeq   uint32
    nextSeq    uint32
    baselines  map[uint32]*entityBaseline
    lastHash   map[uint32]uint64
    priorities map[uint32]*entityPriorityState  // NEW
}
```

**`ReplicationConfig`** — new field:

```go
DormancyThreshold uint32 // ticks unchanged before dormant (0 = disabled)
```

### Cached Tier State on ReplicationSystem

In `NewReplicationSystem`, after building delta encoders:

```go
type ReplicationSystem struct {
    // ... existing fields ...
    tierConfigs map[uint8]ReplicationTier // cached per entity type
    maxRadius   float32                  // max(AoIRadius, all tier radii)
}
```

Initialization:

1. Iterate registered replicators
2. If replicator implements `TierProvider`, cache its `ReplicationTier()`
3. Otherwise, store the default tier
4. Compute `maxRadius = max(cfg.AoIRadius, all tier radii)`

### Update Loop Changes

The existing loop structure at `replication.go:294-530` stays intact. Changes are surgical insertions at specific points in the per-entity inner loop (lines 349-476).

**Query change** (line 341):

```go
// Before:
s.results = s.cfg.Grid.QueryRadius(viewer.X, viewer.Y, s.cfg.AoIRadius, s.results[:0])
// After:
s.results = s.cfg.Grid.QueryRadius(viewer.X, viewer.Y, s.maxRadius, s.results[:0])
```

If no replicator declares a custom radius, `maxRadius == AoIRadius` and this is a no-op change.

**Per-entity flow** (annotated with insertion points):

```
for _, entry := range s.results:
    [EXISTING] alive, ghost, netID, dedup checks
    [EXISTING] resolve entityType, get replicator
    [EXISTING] RelevancyProvider check

    [NEW: TIER RADIUS CUTOFF]
    tier := s.tierConfigs[entityType]
    tierRadius := tier.Radius  (or AoIRadius if 0)
    dx, dy := entry.X - viewer.X, entry.Y - viewer.Y
    dist2 := dx*dx + dy*dy
    if dist2 > tierRadius*tierRadius → continue

    [EXISTING] isNew check (enter tracking)

    [NEW: DORMANCY CHECK]
    ps := conn.getPriorityState(netID)
    if !isNew && DormancyThreshold > 0 && ps.unchangedTicks >= DormancyThreshold:
        currentVisible[netID] = true
        continue  // dormant — skip hash, snapshot, everything

    [EXISTING] hash computation

    [MODIFIED: HASH UNCHANGED PATH]
    if !isNew && !isKeyframe && hash == lastHash:
        ps.unchangedTicks++
        currentVisible[netID] = true  // BUG FIX: was missing, caused false exits
        continue
    ps.unchangedTicks = 0  // state changed, exit dormancy

    [NEW: UPDATE DIVISOR GATE]
    if !isNew && tier.UpdateDivisor > 1 && tick % tier.UpdateDivisor != 0:
        currentVisible[netID] = true
        // Accumulate priority for when we do send
        dist := sqrt(dist2)
        distFactor := 1.0 - (dist / tierRadius)
        basePriority := tier.BaseWeight * distFactor
        if PriorityProvider → basePriority *= pp.NetPriority(viewer, entry)
        ps.accumulator += basePriority
        continue

    [NEW: RECORD SEND]
    ps.lastSentTick = tick
    ps.accumulator = 0

    [EXISTING] snapshot, delta encode, baseline management (unchanged)
```

**Exit cleanup** (lines 493-499): Also remove priority state:

```go
conn.removePriorityState(netID)  // alongside removeBaseline
```

**Disconnected viewer cleanup** (lines 322-327): Already deletes the entire `connectionState`, which now includes the priorities map. No additional cleanup needed.

### Bug Fix: Hash-Unchanged Visibility

**Current behavior** (line 396-399): When an entity's hash is unchanged, the loop `continue`s without adding the entity to `currentVisible`. This means the entity appears in the "exited" set on the next tick, then re-enters on the tick after that when it gets a new full snapshot.

**Fix:** Add `currentVisible[netID] = true` before the `continue` on the hash-unchanged path. This is independent of the hierarchical interest feature but discovered during analysis and necessary for correctness.

### Priority Formula

For entities that pass all gates and get sent:

```
distFactor = 1.0 - (dist / tierRadius)   // 1.0 at viewer, 0.0 at tier edge
staleness  = float32(tick - ps.lastSentTick)
if staleness < 1 { staleness = 1 }

basePriority = tier.BaseWeight * distFactor * staleness
if PriorityProvider:
    basePriority *= pp.NetPriority(viewer, entry)
```

The accumulator is reset on send. For the current scope (no bandwidth cap), the accumulator is tracked but not used for send decisions — it becomes the sort key when bandwidth budgeting is added.

For entities gated by `UpdateDivisor` (skipping send this tick), priority accumulates without the staleness factor (since staleness is implicit in the accumulation):

```
basePriority = tier.BaseWeight * distFactor
if PriorityProvider:
    basePriority *= pp.NetPriority(viewer, entry)
ps.accumulator += basePriority
```

### Game-Side Changes

**Space game (`internal/`):** No changes needed. All replicators use the default tier. The system behaves identically to current behavior. Tiers can be added later when needed.

**Slither example (`examples/slither/`):** Implement `TierProvider` on `foodReplicator` to showcase the feature:

```go
func (r *foodReplicator) ReplicationTier() system.ReplicationTier {
    return system.ReplicationTier{
        Radius:        2000,  // food visible at shorter range than snakes
        UpdateDivisor: 3,     // food updates every 3rd tick
        BaseWeight:    0.3,   // low priority
    }
}
```

Snake replicators keep the default (full radius, every tick, weight 1.0).

Set `DormancyThreshold: 60` (3 seconds at 20Hz) on slither's `ReplicationConfig`. Food entities that haven't changed for 3 seconds become dormant — zero per-tick cost.

### Wire Protocol Impact

None. The binary frame format (`pkg/quantize/wireformat.go`) is unchanged. Clients see:
- Some entity types update less frequently (fewer deltas per tick)
- Some entity types have smaller visibility radii (fewer entities total)
- Enter/exit events work the same way

No client code changes required.

### Performance Characteristics

**Tier radius filtering:** One `dist2 > r2` comparison per entity per viewer. ~2ns per entity. For 500 entities in the max-radius query where 300 are food outside their 2000-unit tier radius, this saves 300 hash computations (~10ns each) and potential snapshot generations.

**Update divisor:** One modulo operation per entity per viewer. Entities gated by divisor skip hash + snapshot entirely. For food at divisor=3, this eliminates 2/3 of all food replication work.

**Dormancy:** One integer comparison per entity per viewer. Dormant entities skip hash computation, snapshot, delta encoding, and priority accumulation. For a world with 500 static food entities where a player sees 200, dormancy eliminates ~200 hash computations per tick after the threshold (~3 seconds).

**Priority accumulator:** One multiply-add per skipped entity (during divisor gate). Negligible cost. No sorting in this scope.

**Expected bandwidth reduction:** 20-40% for games with many low-priority static entities (food, asteroids, decorative objects), primarily from reduced update frequency and smaller tier radii.

## Files to Modify

| File | Changes |
|---|---|
| `pkg/system/replication.go` | `ReplicationTier` type, `TierProvider` interface, tier caching in `NewReplicationSystem`, update loop modifications (tier cutoff, dormancy, update divisor, priority accumulation, hash-unchanged visibility fix) |
| `pkg/system/baseline.go` | `entityPriorityState` type, add `priorities` map to `connectionState`, `getPriorityState`/`removePriorityState` methods |
| `examples/slither/replication.go` | `TierProvider` on `foodReplicator`, `DormancyThreshold` on config |
| `examples/slither/system_network.go` | Set `DormancyThreshold: 60` on `ReplicationConfig` |

## Verification

1. `go vet ./...` — compilation check
2. `make build` — main game builds
3. `cd examples/slither && go build` — slither builds
4. Run main game (`make dev`) — connect web client, verify entities appear/disappear correctly, no visual regressions
5. Run slither (`cd examples/slither && go run .`) — connect web client, verify:
   - Snakes visible at full radius, update every tick
   - Food visible at reduced radius (2000 vs 5000), updates every 3rd tick
   - Food far away not visible
   - No enter/exit flicker
   - Static food goes dormant after ~3 seconds (verify via debug logging or metrics)
6. Check bandwidth per player per tick before/after with slither's food-heavy world
