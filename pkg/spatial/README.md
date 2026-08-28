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
    Shape:  component.ShapeSphere,
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
scratch = grid.QueryRadius(component.Vec3{X: x, Y: y, Z: z}, radius, scratch[:0])
```

Results are appended to the supplied slice and include both tracked and
transient entries. The test is center-to-center distance in three dimensions —
a **sphere**; an entry's own `Radius` does not expand the query.

Buckets are 2D columns, so `Z` filters inside the bucket scan rather than
partitioning it. A spherical query can therefore only narrow the result set
against the cylinder it replaced, never widen it, and in a 2D profile — where
every `Z` is zero — it returns exactly the same entries.

The centre is a `Vec3` rather than three scalars on purpose: with four adjacent
`float32` parameters, `QueryRadius(pos.X, pos.Y, spec.Radius, pos.Z)` compiles
and is wrong.

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
hit, ok := grid.Raycast(from, to, spatial.LayerStatic, filter)
```

`Raycast` is three-dimensional: it takes `component.Vec3` endpoints and tests
each candidate's real shape, so a wall below the ray no longer blocks it. The
BUCKET WALK stays two-dimensional, and that is a proof rather than a shortcut —
buckets are columns keyed on X and Y, so the set of buckets a 3D ray crosses is
exactly the set its XY projection crosses.

`filter` may be nil. It exists because "ignore my own collider" cannot be done
after the fact: a raycast returns the NEAREST hit, so a caller that casts and
then checks `hit == self` is only correct while self is never actually the
nearest hit — and the moment it is, that check reports a clear line and masks
everything behind it.

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
`component.ShapeSphere` or `component.ShapeBox` — `pkg/spatial` reads the one
shape enum from `pkg/component` rather than carrying a parallel copy.
