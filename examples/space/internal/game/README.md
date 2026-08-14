# Space-game runtime

This package composes the reusable MMOKIT engine into the space game. It owns
space-game systems, entity bundles and spawn paths, typed client messages,
combat verbs, player lifecycle behavior, world-manifest realization, and the
per-cell game state.

Current source and tests are authoritative. Historical plans under
`docs/superpowers` explain why some designs exist but may describe APIs that
have since changed.

## Composition

The production composition root in [`cmd/server/main.go`](../../main.go)
performs two game-specific steps for processes that simulate cells:

1. `mmokit.AddState` installs the factory returned by
   [`NewGameWorldStateFactory`](factory.go).
2. [`GameSetup`](factory.go) registers entity kinds, typed inputs and
   operations, internal verbs, player-join behavior, and the ordered system
   pipeline.

`GameWorld` is cell-local state registered through `mmokit.AddState`; it does
not embed `Stage`. Resolve it with `mmokit.State[GameWorld](stage)` and use its
stage through package-local access or [`GetStage`](gameworld.go). Shared
dependencies such as `GameConfig`, `PlayerRepo`, player-session lookup, and the
world repository are supplied by the state factory.

System registration order in [`factory.go`](factory.go) is semantic. In
particular, ability processing precedes projectile simulation, lifetime runs
before AoE resolution, spatial indexing precedes collision, and networking is
last. Preserve those relationships when adding a system.

## Source map

| Area | Primary files |
| --- | --- |
| Per-cell state and setup | [`gameworld.go`](gameworld.go), [`game.go`](game.go), [`factory.go`](factory.go) |
| Lifecycle and transfer repair | [`hooks.go`](hooks.go), [`transfer.go`](transfer.go) |
| Entity bundles and spawn paths | `entity_*.go`, [`entity_kinds.go`](entity_kinds.go) |
| Systems | `system_*.go` |
| Cross-cell gameplay messages | `verb_*.go` |
| Client input and events | [`input_messages.go`](input_messages.go), [`input_handlers.go`](input_handlers.go), [`event_messages.go`](event_messages.go) |
| Typed operations | `op_*.go` |
| Persistent player state | [`playerdata.go`](playerdata.go), [`playerdb.go`](playerdb.go), [`player_flusher.go`](player_flusher.go) |
| Operator commands | [`commands/`](commands/) |
| World content and dungeons | `entity_poi.go`, `entity_dungeon*.go`, `dungeon_*.go`, [`belts.go`](belts.go) |

## ECS rules

Game systems embed `mmokit.SystemBase`. Declare `mmokit.Query` bundle fields,
configure non-default filtering in `Init`, and iterate them in `Update`.
Queries exclude Ghost and Replica entities unless explicitly widened.

Component pointers returned by a query may be mutated in place. Structural
changes must go through `s.Commands()` or `stage.Commands()` because the Ark
world is locked during query iteration. The engine flushes the command buffer
after each system, so changes from one system are visible to the next system in
the same tick.

Production code in this package must not import Ark directly except
[`entity_kinds.go`](entity_kinds.go) and
[`var_tail_bindings.go`](var_tail_bindings.go). Tests are exempt. Prefer the
MMOKIT wrappers for spawning, lookup, queries, and component mutation.

## Entities, transfer, and replication

Each `entity_*.go` file declares the registered bundle and canonical spawn path
for one kind. [`Stage.Spawn`](../../../../pkg/universe/spawn.go) injects framework
state such as NetworkID and cell coordinates; spawn functions provide the kind,
position, collider, and required game components.

[`RegisterEntityKinds`](entity_kinds.go) defines which components are required,
optional, local-only, transferred, and replicated to clients. Bundle or
`net:"..."` changes are protocol changes: update the registration source,
regenerate affected SDKs, and add transfer/replication coverage.

Cross-cell gameplay effects use typed verbs registered with
`mmokit.HandleAllInternal` and routed with `mmokit.Send`; do not add bespoke
cell-routing logic to damage, healing, status, mining, or death call sites.
Authority changes are handled by the framework transfer path. Transfer receive
hooks in [`hooks.go`](hooks.go) reconstruct configuration-derived local state.

## Player lifecycle and persistence

The engine `PlayerManager` owns sessions and transitions. Space-game states
(`dead`, `docking`, and `docked`) are declared in [`game.go`](game.go); their
constant order must match their registration order. Player spawn and reconnect
behavior is installed through `Process.OnPlayerJoin` in [`factory.go`](factory.go).

`PlayerRepo` is a thread-safe in-memory working set backed by PostgreSQL
repositories. Identity data belongs to `pkg/persist`, while space-game state
belongs to `internal/persist`. Mutations that must survive restart need to mark
the player dirty so `PlayerFlusher` can batch both halves into one transaction.

Runtime world content comes from `pkg/world` repositories and the startup
snapshot. Operator mutations go through the `world.*` command path so the
JSON-backed manifest and live cell state remain synchronized.

## Messaging and operator actions

- Register client input with `mmokit.HandleClient`; continuous messages may
  update components directly, while structural/discrete actions are deferred
  through the stage command buffer.
- Register server events in [`event_messages.go`](event_messages.go) and emit
  them with `mmokit.SendEvent`. Field order and registered Go type names are
  client wire contracts.
- Register request/response operations with `mmokit.RegisterOp` and choose the
  appropriate route instead of creating ad hoc frames.
- Implement operator mutations as typed cmdsys verbs in [`commands/`](commands/)
  so console and admin HTTP callers share routing, RBAC, and audit behavior.

## Testing

Use `stage.TickOne(system, dt)` when a unit test depends on the production
`Update`-then-command-flush contract. Tests for cross-cell behavior should
assert authority, transfer repair, and stale-epoch handling rather than only
checking that an entity exists.

Minimum validation for changes here is the nearest Go test, `go vet ./...`, and
`just lint-no-ark`. Regenerate and test client SDKs when a kind, replicated
field, typed event, typed input, broadcast, or operation schema changes.
