# pkg/spatial

Incremental spatial hash grid for broad-phase collision detection and area-of-interest queries. Supports circles and oriented bounding boxes (OBBs).

## Grid (`grid.go`)

```go
grid := spatial.NewGrid(512) // 512-unit cells
```

The grid uses **entity-tracked incremental updates**: entities register once on spawn and are updated each tick. Only cell-boundary crossings trigger rehashing. Static entities (food, stations) are zero-cost after registration.

### Tracked Entities (persistent)

```go
grid.Register(entry)            // add on spawn
grid.Update(entry)              // update each tick (rehashes only on cell change)
grid.Deregister(entity)         // remove on despawn
grid.IsRegistered(entity) bool  // check if tracked
grid.TrackedCount() int         // number of tracked entities
```

### Transient Entries (per-tick derived data)

For derived spatial data not 1:1 with entities (e.g. snake body segment checkpoints):

```go
grid.InsertTransient(entry)  // add derived entry
grid.ClearTransient()        // clear all transient entries (tracked untouched)
```

### Queries

Both tracked and transient entries appear in query results.

```go
results = grid.QueryRadius(cx, cy, radius, results[:0])
```

Returns all entries within `radius` of the point `(cx, cy)`. Uses center-to-center distance. The `results` slice is appended to — pass `results[:0]` to reuse allocation.

```go
grid.QueryCollisions(collisionMatrix, func(a, b Entry) {
    // handle collision pair
})
```

Finds all overlapping pairs filtered by a collision matrix (`layer → bitmask of layers it collides with`).

**Two-phase detection:**

1. **Broad-phase:** Bounding circle overlap check (fast reject)
2. **Narrow-phase:** Shape-aware test based on the pair:
   - Circle vs Circle — broad-phase is sufficient
   - OBB vs Circle — transform circle into rect's local space, clamp to half-extents
   - OBB vs OBB — Separating Axis Theorem (4 axes)

### Reset

```go
grid.Reset()  // clear everything: tracked + transient + tracking map
```

### Entry

```go
type Entry struct {
    Entity   ecs.Entity
    X, Y     float32    // world position
    Radius   float32    // bounding radius (circle shape or broad-phase for rects)
    Width    float32    // rect forward axis (0 for circles)
    Height   float32    // rect side axis (0 for circles)
    Rotation float32    // entity rotation in radians
    Layer    uint8      // collision layer bitmask
    Shape    uint8      // ShapeCircle or ShapeRect
}
```

## Entity Removal

Hook `Engine.OnEntityRemoved` to call `grid.Deregister()` so entities are removed from the grid when despawned via `MarkForRemoval`/`FlushRemovals`. For direct `ECS.RemoveEntity()` calls (e.g. player state transitions), call `grid.Deregister()` explicitly.

## Performance

- Static entities are zero-cost after `Register` (~94% reduction for games with many static entities)
- `Update` same-cell (hot path): 1 map lookup + 1 comparison + 1 slice write
- `Update` cross-cell (rare): swap-delete from old cell + append to new cell
- Cell size should roughly match the largest entity radius for good distribution
- `QueryRadius` appends to a caller-provided slice (no allocation per call)
- The half-neighbor traversal in `QueryCollisions` ensures each pair is checked exactly once
