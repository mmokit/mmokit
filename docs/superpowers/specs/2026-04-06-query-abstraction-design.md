# Query[T] — Bundle-Based ECS Query Abstraction

## Context

Every mmokit system needs ECS filters to query entities. Currently, systems use raw ark ECS types (`ecs.Filter1`–`ecs.Filter5`), requiring verbose boilerplate: arity-specific type declarations, manual `ECSWorld()` calls, repeated `Without(ecs.C[Ghost](), ecs.C[Replica]())` exclusions, and `query.Next()`/`query.Get()` iteration loops.

This design introduces `Query[T]` — a single generic type parameterized on a **component bundle struct** that handles the full lifecycle: declaration, initialization, iteration, optional component access, and common exclusion patterns. It uses `UnsafeFilter` under the hood and Go 1.25 range-over-function iterators for clean iteration.

## Before / After

**Before** (raw ark ECS):
```go
type PhysicsSystem struct {
    mmokit.SystemBase
    filter *ecs.Filter2[component.Position, component.Velocity]
}

func (s *PhysicsSystem) Init() {
    s.filter = ecs.NewFilter2[component.Position, component.Velocity](s.ECSWorld()).
        Without(ecs.C[component.Ghost](), ecs.C[component.Replica]())
}

func (s *PhysicsSystem) Update(dt float32) {
    query := s.filter.Query()
    for query.Next() {
        pos, vel := query.Get()
        pos.X += vel.X * dt
        pos.Y += vel.Y * dt
    }
}
```

**After** (mmokit Query):
```go
type PhysicsSystem struct {
    mmokit.SystemBase
    entities mmokit.Query[struct {
        Pos *comp.Position
        Vel *comp.Velocity
    }]
}

func (s *PhysicsSystem) Init() {
    s.entities.Init(s)
}

func (s *PhysicsSystem) Update(dt float32) {
    for _, b := range s.entities.All() {
        b.Pos.X += b.Vel.X * dt
        b.Pos.Y += b.Vel.Y * dt
    }
}
```

## Core Type

```go
// pkg/mmokit/query.go

// Query wraps an ark UnsafeFilter and provides ergonomic, arity-independent
// iteration over entities matching a component bundle struct T.
type Query[T any] struct {
    filter ecs.UnsafeFilter
    fields []fieldMeta
    bundle T    // reusable allocation, populated each iteration
    inited bool
}

type fieldMeta struct {
    compID   ecs.ID
    offset   uintptr // offset of the pointer field within T
    optional bool
}
```

## Bundle Struct Rules

Bundle structs define which components a query matches. Each field must be a **pointer to a component type**.

```go
// All fields required — entities must have both
type PhysicsBundle struct {
    Pos *comp.Position
    Vel *comp.Velocity
}

// Optional fields — nil when entity lacks the component
type MovementBundle struct {
    Pos    *comp.Position
    Vel    *comp.Velocity
    Params *comp.MoveParams `ecs:"optional"`
}
```

Rules:
- Every field must be a pointer-to-struct (`*T` where T is a component type)
- Required fields (no tag) are included in the filter — entities must have all of them
- Fields tagged `ecs:"optional"` are not in the filter; set to `nil` per-entity when absent
- `Init()` panics if any field is not a pointer-to-struct
- Unexported fields are ignored (allows embedding private state if needed)

## Creation API

### `.Init(s)` — Primary (type inference)

```go
s.entities.Init(s)                                               // excludes Ghost + Replica
s.entities.Init(s, mmokit.Without[Dormant]())                    // excludes Ghost + Replica + Dormant
s.entities.Init(s, mmokit.IncludeAll())                          // no exclusions
s.entities.Init(s, mmokit.IncludeAll(), mmokit.Without[Ghost]()) // only Ghost excluded
```

`Init` is a pointer-receiver method on `*Query[T]`. Since the Query is a struct field on the system, `T` is inferred from the field declaration — no type repetition needed. This is the preferred form, especially for inline anonymous structs.

The `sys` parameter is `interface{ ECSWorld() *ecs.World }` — satisfied by `SystemBase`.

### `NewQuery[T](s)` — Alternative (explicit)

```go
s.entities = mmokit.NewQuery[MyBundle](s)
s.entities = mmokit.NewQuery[MyBundle](s, mmokit.IncludeAll())
```

Returns a `Query[T]` by value. Useful when using named bundle types.

### Query Options

```go
type QueryOption struct { ... } // opaque, functional option

// IncludeAll clears all default exclusions (Ghost, Replica)
func IncludeAll() QueryOption

// Without adds a component type to the exclusion set
func Without[T any]() QueryOption
```

Default exclusions: `component.Ghost`, `component.Replica` — covers 90%+ of game systems.

Options are applied in order: `IncludeAll()` clears the set, `Without[T]()` adds to it. Multiple `Without` calls accumulate.

## Iteration API

### `.All()` — Primary (range iterator)

```go
func (q *Query[T]) All() iter.Seq2[ecs.Entity, *T]
```

Returns a Go 1.23+ range-over-function iterator. The `*T` bundle is a single reusable allocation on the Query struct — pointers inside change each iteration.

```go
for entity, b := range s.entities.All() {
    b.Pos.X += b.Vel.X * dt
    b.Pos.Y += b.Vel.Y * dt
}
```

Early break via `break` just works — the iterator calls `query.Close()` internally.

**Lifetime:** The `*T` bundle pointer is reused across iterations. Component pointers inside are valid until the next iteration or until the world is modified. Do not store the bundle pointer beyond the loop body.

### `.Each()` — Convenience callback

```go
func (q *Query[T]) Each(fn func(e ecs.Entity, b *T))
```

Iterates all matching entities. Cannot break early. Provided for cases where callbacks are preferred.

### `.Count()` and `.Any()`

```go
func (q *Query[T]) Count() int  // number of matching entities (no per-entity iteration)
func (q *Query[T]) Any() bool   // true if at least one entity matches
```

## Implementation Details

### Init-time reflection

`Init()` / `NewQuery()` runs once per system startup:

1. `reflect.TypeFor[T]()` to get the bundle struct type
2. Iterate exported pointer-to-struct fields
3. For each field: `ecs.TypeID(world, field.Type.Elem())` to get the component ID
4. Record `fieldMeta{compID, offset, optional}` for each field
5. Collect required component IDs → `ecs.NewUnsafeFilter(world, requiredIDs...)`
6. Apply `.Without()` exclusions from options
7. Store filter + field metadata on the Query struct

### Per-entity bundle population

```go
func (q *Query[T]) populateBundle(uq *ecs.UnsafeQuery) {
    base := unsafe.Pointer(&q.bundle)
    for i := range q.fields {
        fm := &q.fields[i]
        fieldPtr := (*unsafe.Pointer)(unsafe.Add(base, fm.offset))
        if fm.optional && !uq.Has(fm.compID) {
            *fieldPtr = nil
        } else {
            *fieldPtr = uq.Get(fm.compID)
        }
    }
}
```

Zero allocation. Per-entity cost: N pointer writes where N = number of bundle fields. The `UnsafeQuery.Get()` cost is one array index + pointer arithmetic per component — negligible at game-scale entity counts (< 5K entities at 20Hz).

### Range iterator implementation

```go
func (q *Query[T]) All() iter.Seq2[ecs.Entity, *T] {
    return func(yield func(ecs.Entity, *T) bool) {
        uq := q.filter.Query()
        for uq.Next() {
            q.populateBundle(&uq)
            if !yield(uq.Entity(), &q.bundle) {
                uq.Close()
                return
            }
        }
    }
}
```

## Performance

- **Init time:** Single reflection pass — microseconds
- **Per-entity iteration:** N `UnsafeQuery.Get()` calls (array index + pointer math each). At 2000 entities x 5 components x 20Hz = 200K lookups/sec — well under 100 microseconds per tick
- **UnsafeFilter archetype matching:** Slightly slower than typed filters (no `rareComp` optimization). Negligible with < 100 archetypes
- **Escape hatch:** Systems needing absolute max throughput can use raw `ecs.FilterN[...]` directly. The two approaches coexist freely on the same system

## File Layout

- `pkg/mmokit/query.go` — `Query[T]`, `NewQuery`, `QueryOption`, `fieldMeta`, bundle reflection, iteration
- `pkg/mmokit/query_test.go` — tests

No changes to existing files required. This is purely additive.

## Testing Plan

1. Basic 2-component bundle iteration (verify component values are correct)
2. Optional field (nil when absent, non-nil when present)
3. Default Without exclusions (Ghost/Replica entities skipped)
4. `IncludeAll` option (Ghost/Replica entities included)
5. Custom `Without` and `IncludeAll` + `Without` combinations
6. `Count()` and `Any()` without full iteration
7. Early break via `break` in range loop (query properly closed)
8. Zero-entity query (no panic, zero iterations)
9. Anonymous struct as type parameter (compile + runtime correctness)
10. Panic on invalid bundle (non-pointer field, non-struct pointer)

## Verification

```bash
go vet ./pkg/mmokit/...
go test ./pkg/mmokit/... -v -run TestQuery
```

Then update `examples/simple/` to use the new API as a smoke test.

## System Migrations

After implementing `Query[T]`, migrate all existing systems to use it. Each system replaces its raw `ecs.FilterN` + `query.Next()`/`query.Get()` loop with a `Query[T]` field + `range q.All()`.

### Systems to migrate

**`pkg/system/`** (generic engine systems):

- `physics.go` — Filter2[Position, Velocity]
- `click_to_move.go` — Filter4[Position, Velocity, MoveTarget, ClickControl]
- `direction_move.go` — Filter3[Position, Velocity, DirectionInput] + Map1[MoveParams]
- `lifetime.go` — Filter2[Lifetime, EntityKind]
- `spatial_system.go` — Filter3[Position, Collider, NetworkID]
- `dead_reckoning.go` — Filter3[Position, Velocity, DeadReckoning]
- `replica_dead_reckoning.go` — Filter3[Position, Velocity, Replica] + Filter3[Position, Velocity, Ghost]

**`examples/simple/`**:

- `main.go` — Filter1[Position]

**`examples/slither/`**:

- `system_movement.go`, `system_boost.go`, `system_bot.go`, `system_collision.go`
- `system_decay.go`, `system_eating.go`, `system_leaderboard.go`, `system_network.go`

**`examples/4node-basic/`**:

- Any systems with raw filters

**`internal/system/`** (game-specific):

- `ability.go`, `docking.go`, `mining.go`, `shieldregen.go`
- `shipcontrol.go`, `statuseffect.go`, `targetlock.go`, `wander.go`

### Migration pattern

Before:

```go
type XxxSystem struct {
    mmokit.SystemBase
    filter *ecs.Filter2[comp.A, comp.B]
}
func (s *XxxSystem) Init() {
    s.filter = ecs.NewFilter2[comp.A, comp.B](s.ECSWorld()).
        Without(ecs.C[comp.Ghost](), ecs.C[comp.Replica]())
}
func (s *XxxSystem) Update(dt float32) {
    query := s.filter.Query()
    for query.Next() {
        a, b := query.Get()
        // ...
    }
}
```

After:

```go
type XxxSystem struct {
    mmokit.SystemBase
    entities mmokit.Query[struct {
        A *comp.A
        B *comp.B
    }]
}
func (s *XxxSystem) Init() {
    s.entities.Init(s)
}
func (s *XxxSystem) Update(dt float32) {
    for _, b := range s.entities.All() {
        // b.A, b.B available
    }
}
```

Systems with Map1 optional lookups convert to `ecs:"optional"` fields. Systems with multiple filters get multiple Query fields. Systems with custom Without exclusions use `Without[T]()` or `IncludeAll()` options.

## Scope Boundaries

**In scope:** Query[T] type, bundle reflection, iteration (All/Each/Count/Any), query options, tests, migrate all existing systems.

**Out of scope:** Spawn bundle wrappers (ark MapN is compile-time typed), deferred mutation buffers (current slice pattern is sufficient).
