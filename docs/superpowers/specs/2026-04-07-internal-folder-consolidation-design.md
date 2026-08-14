# Internal Folder Consolidation

## Problem

The internal game code is split across three packages -- `game/`, `system/`, `universe/` -- that are tightly coupled around the same game logic. The split exists to satisfy Go's circular import rules, not because of natural domain boundaries. This creates:

- Navigation friction: "which of 3 packages is this in?"
- Artificial boilerplate: `unwrapGW()` type assertion hack, `gwProvider` interface, adapter indirection
- Decision overhead: "which package should new code go in?"

## Design

Merge `internal/system/` and `internal/universe/` into `internal/game/`. Keep `component/`, `item/`, `marketplace/`, `bot/` as separate packages.

### File naming after merge

System files get a `system_` prefix. All other files keep existing names or existing prefixes (`entity_`, `nethandler_`).

| Source | Destination |
|--------|-------------|
| `system/ability.go` | `game/system_ability.go` |
| `system/collision.go` | `game/system_collision.go` |
| `system/docking.go` | `game/system_docking.go` |
| `system/economy.go` | `game/system_economy.go` |
| `system/equipment.go` | `game/system_equipment.go` |
| `system/mining.go` | `game/system_mining.go` |
| `system/network.go` | `game/system_network.go` |
| `system/shipcontrol.go` | `game/system_shipcontrol.go` |
| `system/shieldregen.go` | `game/system_shieldregen.go` |
| `system/statuseffect.go` | `game/system_statuseffect.go` |
| `system/targetlock.go` | `game/system_targetlock.go` |
| `system/wander.go` | `game/system_wander.go` |
| `system/input_handlers.go` | `game/input_handlers.go` |
| `system/nethandler_ship.go` | `game/nethandler_ship.go` |
| `system/nethandler_npc.go` | `game/nethandler_npc.go` |
| `system/nethandler_asteroid.go` | `game/nethandler_asteroid.go` |
| `system/nethandler_lootcrate.go` | `game/nethandler_lootcrate.go` |
| `system/nethandler_station.go` | `game/nethandler_station.go` |
| `system/nethandler_shared.go` | `game/nethandler_shared.go` |
| `system/replication_adapters.go` | `game/replication_adapters.go` |
| `universe/adapter.go` | `game/adapter.go` |
| `universe/factory.go` | `game/factory.go` |
| `universe/replicators.go` | `game/replicators.go` |
| `universe/side_effects.go` | `game/side_effects.go` |
| `universe/*_test.go` | `game/*_test.go` |

### Files deleted

- `system/unwrap.go` -- `unwrapGW()` and `gwProvider` interface no longer needed
- `system/README.md` -- heavily outdated (references removed systems like CombatSystem, DamageSystem, old filter patterns)

### Code changes

1. **All moved files**: change `package system` / `package universe` to `package game`

2. **Remove cross-package imports**: delete all `"github.com/zenion/mmokit/internal/system"` and `"github.com/zenion/mmokit/internal/game"` imports from moved files (now same package)

3. **Replace `unwrapGW()`**: add a package-level helper:
   ```go
   func gwFromSystem(s mmokit.SystemBase) *GameWorld {
       return s.GameWorld().(*gameWorldAdapter).gw
   }
   ```
   Update all system `Init()` methods: `s.gw = unwrapGW(s.GameWorld())` becomes `s.gw = gwFromSystem(s.SystemBase)`

4. **`factory.go`**: remove `system.` and `game.` prefixes from all type references (now same package). E.g. `&system.AbilitySystem{}` becomes `&AbilitySystem{}`, `game.NewGameWorld(...)` becomes `NewGameWorld(...)`.

5. **`adapter.go`**: remove `game.` prefix from all GameWorld references. `UnwrapGameWorld()` stays exported for external callers (e.g. `cmd/server/`).

6. **Import alias cleanup**: files that had `gamecomp "internal/component"` aliased to avoid collision with `game` package can drop the alias if desired (they're now IN the game package, so there's no `game` import to collide with). However, keeping `gamecomp` for `internal/component` is fine for clarity.

7. **Test files**: change package declaration. Test imports of `game.` and `universe.` become direct references. The `testutil_test.go` helper that builds test coordinators should work since it only uses the public mmokit API.

### External callers

- `cmd/server/main.go` -- change `internaluniverse "internal/universe"` import to use `"internal/game"` directly. `internaluniverse.GameSetup` becomes `game.GameSetup`, `internaluniverse.UnwrapGameWorld` becomes `game.UnwrapGameWorld`.
- `CLAUDE.md` -- update `internal/system/` and `internal/universe/` references in the Architecture section to reflect the merged `internal/game/` structure.

### What stays unchanged

- `internal/component/` -- separate package, imported as `gamecomp`
- `internal/item/` -- separate package, clean dependency
- `internal/marketplace/` -- separate package, wraps `pkg/orderbook`
- `internal/bot/` -- separate package, standalone load test client
- All `pkg/` packages -- untouched

### Dependency graph after merge

```
cmd/server  → internal/game
internal/game → internal/component, internal/item, pkg/mmokit, pkg/*
internal/marketplace → internal/game (for GameWorld), pkg/orderbook
internal/bot → pkg/net (standalone client)
```

## Verification

1. `go vet ./...` passes
2. `go test ./internal/...` passes
3. `make build` succeeds
4. No circular imports
