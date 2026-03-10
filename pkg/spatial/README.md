# pkg/spatial

Spatial hash grid for broad-phase collision detection and area-of-interest queries. Supports circles and oriented bounding boxes (OBBs).

## Grid (`grid.go`)

```go
grid := spatial.NewGrid(512) // 512-unit cells
```

The grid is rebuilt from scratch every tick:

1. `Clear()` — reset all cells (reuses allocated slices)
2. `Insert(entry)` — add each entity to its cell
3. `QueryRadius(...)` / `QueryCollisions(...)` — read queries

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

### QueryRadius

```go
results = grid.QueryRadius(cx, cy, radius, results[:0])
```

Returns all entries within `radius` of the point `(cx, cy)`. Uses center-to-center distance (bounding circle check). The `results` slice is appended to — pass `results[:0]` to reuse allocation.

Used by the NetworkSystem for area-of-interest culling.

### QueryCollisions

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

Only checks same-cell and adjacent-cell pairs (half-neighbor pattern to avoid duplicate pairs).

## Shape Constants

```go
const (
    ShapeCircle uint8 = 0
    ShapeRect   uint8 = 1
)
```

These were extracted from the component package during the engine decoupling. Game code references `spatial.ShapeRect` when creating colliders.

## Performance Notes

- Cell size should roughly match the largest entity radius for good distribution
- `Clear()` reuses allocated cell slices (no GC pressure)
- `QueryRadius` appends to a caller-provided slice (no allocation per call)
- The half-neighbor traversal in `QueryCollisions` ensures each pair is checked exactly once
