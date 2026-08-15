# Space-game components

This package owns ECS state that is specific to the space game. Framework
components such as `Position`, `Velocity`, `NetworkID`, `EntityKind`,
`Collider`, `PlayerConn`, `CellCoord`, `Ghost`, and `Replica` live in
[`pkg/component`](../../../../pkg/component/) and are normally consumed through
`mmokit`.

The component declarations are the source of truth. This README documents the
rules for changing them rather than duplicating every field.

## Package boundaries

- Put reusable engine state in `pkg/component`; put space-game state here.
- Keep gameplay behavior in `internal/game` systems and typed-message handlers.
  Small value helpers on collection-like components, such as `Inventory` and
  `StatusEffects`, are appropriate here.
- Do not store process-local pointers, services, or unsynchronized shared state
  in a component.
- Ark entity handles are process-local. Clear or reconstruct them when a
  component crosses a cell boundary; see the transfer options in
  [`internal/game/entity_kinds.go`](../game/entity_kinds.go).

## Entity kinds and bundles

The `Kind*` constants in [`components.go`](components.go) are client wire
values. Their numeric values are stable protocol contracts. Do not reorder or
reuse them.

The matching kind names and component sets are registered in
[`internal/game/entity_kinds.go`](../game/entity_kinds.go). Bundle structs live
beside their spawn code in `internal/game/entity_*.go`:

- An untagged bundle field is required at spawn and transferred between cells.
- `mmokit:"local"` creates zero-valued state on the receiving cell instead of
  serializing it.
- `mmokit:"optional"` transfers the component when present but does not require
  every entity of that kind to carry it.

If a component contains maps or other state that the reflection transfer codec
cannot represent correctly, provide a custom codec through the kind
registration. `Inventory` is the current example.

## Client replication

Fields tagged `net:"..."` participate in the generated client snapshot schema.
Fields without a `net` tag remain server-only even when their containing
component transfers between cells.

- `net:"initial"` sends immutable presentation data when an entity becomes
  visible.
- Encodings such as `f32`, `u32`, `u8`, `bool`, and `qnorm` select the snapshot
  representation.
- Declaration order, encoding, and field names affect the generated client
  contract. Treat changes as wire changes and regenerate the affected SDKs.
- Variable-size state needs a deterministic custom binding. Inventory and
  status effects are implemented in
  [`internal/game/var_tail_bindings.go`](../game/var_tail_bindings.go).

Collision layers and shapes are defined by [`pkg/spatial`](../../../../pkg/spatial/).
Use `LayerStatic`, `LayerProp`, and `LayerEntity`; do not introduce a second set
of game-local layer values.

## Access from game code

Gameplay code should use MMOKIT queries and mutation helpers:

```go
type combatBundle struct {
    Health *gamecomp.Health
}

type CombatSystem struct {
    mmokit.SystemBase
    query mmokit.Query[combatBundle]
}

func (s *CombatSystem) Update(dt float32) {
    for entity, bundle := range s.query.Iter {
        if bundle.Health.Current <= 0 {
            s.Commands().Despawn(entity)
        }
    }
}
```

Do not perform structural ECS mutation while a query is open. Queue despawns,
component additions/removals, and other structural work through the stage's
command buffer. Direct Ark access in production `internal/game` code is limited
to the existing entity-kind and variable-tail binding glue.

## Validation

After changing a component or kind bundle:

1. Run the nearest `internal/game` tests and `just lint-no-ark`.
2. Regenerate every affected client SDK and inspect the schema diff.
3. Run the corresponding TypeScript and/or C# codec tests when wire fields
   changed.
4. Add a transfer round-trip test when server-side state must survive handoff.
