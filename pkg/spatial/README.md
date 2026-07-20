# `pkg/spatial`

Incremental spatial hash grid for area-of-interest queries, broad-phase
collision detection, shape-aware overlap tests, and segment raycasts.

`HashGrid` is not synchronized. A cell owns and updates its grid on the cell
loop goroutine.

## Creating and maintaining a grid

```go
grid := spatial.NewHashGrid(128)

grid.Register(spatial.Entry{
    Entity: entity,
    X:      100,
    Y:      200,
    Radius: 12,
    Layer:  spatial.LayerEntity,
    Shape:  spatial.ShapeCircle,
})

movedBucket := grid.Update(updatedEntry)
grid.Deregister(entity)
```

`Register` panics when the entity is already tracked. `Update` requires a
registered entity, overwrites its stored entry, and returns whether it crossed
a hash-bucket boundary. `Deregister` is a no-op for an unknown entity.

For data rebuilt every tick and not tracked one-to-one with an ECS entity:

```go
grid.InsertTransient(entry)
grid.ClearTransient()
```

`Reset` clears tracked entries, transient entries, and tracking metadata.
`TrackedCount` counts only registered persistent entries.

When using the universe `Stage`, prefer its spawn, movement, and deferred
despawn paths; those keep the stage-owned grid synchronized. Do not remove Ark
entities directly.

## Radius queries

```go
scratch = grid.QueryRadius(x, y, radius, scratch[:0])
```

Results are appended to the supplied slice and include both tracked and
transient entries. The test is center-to-center distance; an entry's own
`Radius` does not expand the query.

## Collision queries

```go
matrix := map[uint8]uint8{
    spatial.LayerEntity: spatial.LayerStatic | spatial.LayerProp,
}

grid.QueryCollisions(matrix, func(a, b spatial.Entry) {
    // Each reported pair overlaps and is enabled by at least one matrix side.
})
```

Collision queries include tracked and transient entries. They first test
bounding circles, then use the appropriate narrow phase:

- circle against circle
- circle against oriented rectangle
- oriented rectangle against oriented rectangle using the separating axis
  theorem

For rectangles, `Radius` is the broad-phase bounding radius; `Width`,
`Height`, and `Rotation` define the oriented box.

## Raycasts

```go
entity, point, distance, ok := grid.Raycast(
    spatial.Vec2{X: 0, Y: 0},
    spatial.Vec2{X: 500, Y: 0},
    spatial.LayerStatic|spatial.LayerProp,
)
```

`Raycast` walks buckets touched by the segment and returns the nearest circle
or oriented-rectangle surface whose `Entry.Layer` intersects the mask.
Raycasts currently inspect tracked entries only, not transient entries.

## Entry fields and layers

```go
type Entry struct {
    Entity   ecs.Entity
    X, Y     float32
    Radius   float32
    Width    float32
    Height   float32
    Rotation float32
    Layer    uint8
    Shape    uint8
}
```

`Layer` is a bit mask. The package reserves zero for no membership and
provides `LayerStatic`, `LayerProp`, and `LayerEntity`. `Shape` is either
`ShapeCircle` or `ShapeRect`.
