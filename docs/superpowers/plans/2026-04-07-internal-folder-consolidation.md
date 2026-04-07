# Internal Folder Consolidation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Merge `internal/system/` and `internal/universe/` into `internal/game/` to eliminate artificial package boundaries and simplify the codebase.

**Architecture:** Move all files from `system/` and `universe/` into `game/`, renaming system files with a `system_` prefix. Change package declarations, remove cross-package imports, replace `unwrapGW()` with a same-package helper, and update external callers.

**Tech Stack:** Go (package restructure, no new dependencies)

---

### Task 1: Move system files into game/ with system_ prefix

Move all ECS system files from `internal/system/` to `internal/game/` with `system_` prefix. Non-system files (nethandlers, replication adapters, input handlers) keep their existing names.

**Files:**
- Move: `internal/system/ability.go` -> `internal/game/system_ability.go`
- Move: `internal/system/collision.go` -> `internal/game/system_collision.go`
- Move: `internal/system/docking.go` -> `internal/game/system_docking.go`
- Move: `internal/system/economy.go` -> `internal/game/system_economy.go`
- Move: `internal/system/equipment.go` -> `internal/game/system_equipment.go`
- Move: `internal/system/mining.go` -> `internal/game/system_mining.go`
- Move: `internal/system/network.go` -> `internal/game/system_network.go`
- Move: `internal/system/shipcontrol.go` -> `internal/game/system_shipcontrol.go`
- Move: `internal/system/shieldregen.go` -> `internal/game/system_shieldregen.go`
- Move: `internal/system/statuseffect.go` -> `internal/game/system_statuseffect.go`
- Move: `internal/system/targetlock.go` -> `internal/game/system_targetlock.go`
- Move: `internal/system/wander.go` -> `internal/game/system_wander.go`
- Move: `internal/system/input_handlers.go` -> `internal/game/input_handlers.go`
- Move: `internal/system/nethandler_ship.go` -> `internal/game/nethandler_ship.go`
- Move: `internal/system/nethandler_npc.go` -> `internal/game/nethandler_npc.go`
- Move: `internal/system/nethandler_asteroid.go` -> `internal/game/nethandler_asteroid.go`
- Move: `internal/system/nethandler_lootcrate.go` -> `internal/game/nethandler_lootcrate.go`
- Move: `internal/system/nethandler_station.go` -> `internal/game/nethandler_station.go`
- Move: `internal/system/nethandler_shared.go` -> `internal/game/nethandler_shared.go`
- Move: `internal/system/replication_adapters.go` -> `internal/game/replication_adapters.go`
- Delete: `internal/system/unwrap.go` (replaced in Task 3)
- Delete: `internal/system/README.md` (outdated)

- [ ] **Step 1: Move and rename all system files**

```bash
cd .

# Systems get system_ prefix
for f in ability collision docking economy equipment mining network shipcontrol shieldregen statuseffect targetlock wander; do
  mv internal/system/${f}.go internal/game/system_${f}.go
done

# Non-system files keep their names
for f in input_handlers nethandler_ship nethandler_npc nethandler_asteroid nethandler_lootcrate nethandler_station nethandler_shared replication_adapters; do
  mv internal/system/${f}.go internal/game/${f}.go
done
```

- [ ] **Step 2: Delete unwrap.go and README.md**

```bash
rm internal/system/unwrap.go
rm internal/system/README.md
```

- [ ] **Step 3: Change package declarations from `system` to `game`**

In every moved file, change `package system` to `package game`:

```bash
for f in internal/game/system_*.go internal/game/input_handlers.go internal/game/nethandler_*.go internal/game/replication_adapters.go; do
  sed -i 's/^package system$/package game/' "$f"
done
```

- [ ] **Step 4: Verify no files remain in internal/system/**

```bash
ls internal/system/
```

Expected: empty directory (or directory not found). If empty, remove it:

```bash
rmdir internal/system
```

---

### Task 2: Move universe files into game/

Move all non-test files from `internal/universe/` to `internal/game/`. Move test files too.

**Files:**
- Move: `internal/universe/adapter.go` -> `internal/game/adapter.go`
- Move: `internal/universe/factory.go` -> `internal/game/factory.go`
- Move: `internal/universe/replicators.go` -> `internal/game/replicators.go`
- Move: `internal/universe/side_effects.go` -> `internal/game/side_effects.go`
- Move: `internal/universe/coordinator_test.go` -> `internal/game/coordinator_test.go`
- Move: `internal/universe/node_test.go` -> `internal/game/node_test.go`
- Move: `internal/universe/replica_test.go` -> `internal/game/replica_test.go`
- Move: `internal/universe/testutil_test.go` -> `internal/game/testutil_test.go`
- Move: `internal/universe/topology_test.go` -> `internal/game/topology_test.go`

- [ ] **Step 1: Move all universe files**

```bash
cd .

mv internal/universe/adapter.go internal/game/adapter.go
mv internal/universe/factory.go internal/game/factory.go
mv internal/universe/replicators.go internal/game/replicators.go
mv internal/universe/side_effects.go internal/game/side_effects.go
mv internal/universe/coordinator_test.go internal/game/coordinator_test.go
mv internal/universe/node_test.go internal/game/node_test.go
mv internal/universe/replica_test.go internal/game/replica_test.go
mv internal/universe/testutil_test.go internal/game/testutil_test.go
mv internal/universe/topology_test.go internal/game/topology_test.go
```

- [ ] **Step 2: Change package declarations**

```bash
for f in internal/game/adapter.go internal/game/factory.go internal/game/replicators.go internal/game/side_effects.go; do
  sed -i 's/^package universe$/package game/' "$f"
done

for f in internal/game/coordinator_test.go internal/game/node_test.go internal/game/replica_test.go internal/game/testutil_test.go internal/game/topology_test.go; do
  sed -i 's/^package universe_test$/package game_test/' "$f"
done
```

- [ ] **Step 3: Remove empty universe directory**

```bash
rmdir internal/universe
```

---

### Task 3: Fix imports and references in moved system files

Now all files are in `internal/game/`. Remove the `internal/game` self-import from moved system files, remove the `game.` prefix from all type references, and add the `gwFromSystem()` helper.

**Files:**
- Create: `internal/game/system_util.go` (gwFromSystem helper)
- Modify: all `system_*.go` files, `input_handlers.go`, `nethandler_*.go`, `replication_adapters.go`

- [ ] **Step 1: Create gwFromSystem helper**

Create `internal/game/system_util.go`:

```go
package game

import "github.com/zenion/mmoserver/pkg/mmokit"

// gwFromSystem extracts a *GameWorld from the mmokit.GameWorld interface
// returned by SystemBase.GameWorld(). All game systems call this in Init().
func gwFromSystem(base mmokit.SystemBase) *GameWorld {
	return base.GameWorld().(*gameWorldAdapter).gw
}
```

- [ ] **Step 2: Fix system files that use unwrapGW() and import game**

For each system file that calls `unwrapGW(s.GameWorld())`, replace with `gwFromSystem(s.SystemBase)`. Also remove the `"github.com/zenion/mmoserver/internal/game"` import and drop all `game.` prefixes from type references.

Files to update (all have `unwrapGW` + `game` import):
- `system_ability.go`: remove game import, `game.GameWorld` -> `GameWorld`, `game.ActionDamage` -> `ActionDamage`, `game.MarshalDamageAction` -> `MarshalDamageAction`, `game.MarshalStatusEffectAction` -> `MarshalStatusEffectAction`, `game.MarshalMiningAction` -> `MarshalMiningAction`, `game.CatCombatAbility` -> `CatCombatAbility`, `game.CatEconomyMining` -> `CatEconomyMining`
- `system_collision.go`: remove game import, `game.GameWorld` -> `GameWorld`, `game.CatWorldCollision` -> `CatWorldCollision`
- `system_docking.go`: remove game import, `game.PendingDockRequest` -> `PendingDockRequest`, `game.StateDocking` -> `StateDocking`, `game.StateDocked` -> `StateDocked`, `game.DockingState` -> `DockingState`, `game.CatPlayerDock` -> `CatPlayerDock`
- `system_economy.go`: remove game import, `game.PendingTransfer` -> `PendingTransfer`, `game.PendingSellRequest` -> `PendingSellRequest`, `game.PendingBankRequest` -> `PendingBankRequest`, `game.PendingShopBuy` -> `PendingShopBuy`, `game.PendingLootItem` -> `PendingLootItem`, `game.PendingLootAll` -> `PendingLootAll`, `game.PlayerData` -> `PlayerData`, `game.StateDocked` -> `StateDocked`
- `system_equipment.go`: remove game import, `game.PendingEquipRequest` -> `PendingEquipRequest`
- `system_mining.go`: remove game import, drop `game.` prefixes from all types
- `system_network.go`: remove game import, `game.GameWorld` -> `GameWorld`, `game.StateDocked` -> `StateDocked`, drop `game.Cat*` prefixes
- `system_shipcontrol.go`: remove game import, `game.StateDocking` -> `StateDocking`
- `system_shieldregen.go`: remove game import, `game.GameWorld` -> `GameWorld`
- `system_statuseffect.go`: remove game import, `game.GameWorld` -> `GameWorld`
- `system_targetlock.go`: remove game import, `game.CatCombatLock` -> `CatCombatLock`

For each file, the mechanical changes are:
1. Delete the `"github.com/zenion/mmoserver/internal/game"` import line
2. Replace `unwrapGW(s.GameWorld())` with `gwFromSystem(s.SystemBase)`
3. Replace all `game.` prefixed references with the bare type name

```bash
# For each moved system file, remove game import and game. prefix
for f in internal/game/system_*.go; do
  # Remove the game import line
  sed -i '/"github.com\/zenion\/mmoserver\/internal\/game"/d' "$f"
  # Replace unwrapGW call
  sed -i 's/unwrapGW(s\.GameWorld())/gwFromSystem(s.SystemBase)/g' "$f"
  # Remove game. prefix from all references
  sed -i 's/game\.//g' "$f"
done
```

**Important:** The `sed 's/game\.//g'` is aggressive -- manually verify it doesn't break `gamecomp.` aliases (those should be untouched since the alias is `gamecomp`, not `game`).

- [ ] **Step 3: Fix input_handlers.go**

This file receives `gw *game.GameWorld` as a parameter. After the merge, it becomes `gw *GameWorld`. Remove the game import and `game.` prefixes.

```bash
sed -i '/"github.com\/zenion\/mmoserver\/internal\/game"/d' internal/game/input_handlers.go
sed -i 's/game\.//g' internal/game/input_handlers.go
```

- [ ] **Step 4: Fix nethandler files**

All 6 nethandler files have `gw *game.GameWorld` fields. Remove game import and `game.` prefixes.

```bash
for f in internal/game/nethandler_*.go; do
  sed -i '/"github.com\/zenion\/mmoserver\/internal\/game"/d' "$f"
  sed -i 's/game\.//g' "$f"
done
```

- [ ] **Step 5: Fix replication_adapters.go**

Has `game.GameWorld` references and imports `"github.com/zenion/mmoserver/pkg/system"` for Hasher/ViewerInfo types. Remove game import, keep pkg/system import.

```bash
sed -i '/"github.com\/zenion\/mmoserver\/internal\/game"/d' internal/game/replication_adapters.go
sed -i 's/game\.//g' internal/game/replication_adapters.go
```

- [ ] **Step 6: Fix wander.go**

This file has NO game import and NO unwrapGW -- no changes needed beyond the package declaration (already done in Task 1).

- [ ] **Step 7: Run go vet to check for compile errors**

```bash
go vet ./internal/game/...
```

Fix any remaining issues (likely stray `game.` references or missing imports).

---

### Task 4: Fix imports and references in moved universe files

Remove `internal/game` and `internal/system` self-imports from files that came from `universe/`. Drop `game.` and `system.` prefixes.

**Files:**
- Modify: `internal/game/adapter.go`
- Modify: `internal/game/factory.go`
- Modify: `internal/game/replicators.go`
- Modify: `internal/game/side_effects.go`

- [ ] **Step 1: Fix adapter.go**

Remove `game` import, drop all `game.` prefixes (types like `game.ActionDamage`, `game.UnmarshalDamageAction`, `game.CatCombatAbility`, etc. become bare names).

```bash
sed -i '/"github.com\/zenion\/mmoserver\/internal\/game"/d' internal/game/adapter.go
sed -i 's/game\.//g' internal/game/adapter.go
```

- [ ] **Step 2: Fix factory.go**

Remove both `game` and `system` imports. Drop `game.` and `system.` prefixes. `system.AbilitySystem` becomes `AbilitySystem`, `game.NewGameWorld` becomes `NewGameWorld`, etc.

```bash
sed -i '/"github.com\/zenion\/mmoserver\/internal\/game"/d' internal/game/factory.go
sed -i '/"github.com\/zenion\/mmoserver\/internal\/system"/d' internal/game/factory.go
sed -i 's/system\.//g' internal/game/factory.go
sed -i 's/game\.//g' internal/game/factory.go
```

- [ ] **Step 3: Fix replicators.go**

Remove `game` import, drop `game.` prefixes. Keep `pkguniverse` import (this is `pkg/universe`, not `internal/universe`).

```bash
sed -i '/"github.com\/zenion\/mmoserver\/internal\/game"/d' internal/game/replicators.go
sed -i 's/game\.//g' internal/game/replicators.go
```

- [ ] **Step 4: Fix side_effects.go**

Remove `game` import, drop `game.` prefixes. Keep `pkguniverse` import.

```bash
sed -i '/"github.com\/zenion\/mmoserver\/internal\/game"/d' internal/game/side_effects.go
sed -i 's/game\.//g' internal/game/side_effects.go
```

- [ ] **Step 5: Run go vet**

```bash
go vet ./internal/game/...
```

Fix any remaining issues.

---

### Task 5: Fix test files

Update test file imports. Tests that imported `internal/game` and `internal/universe` now only need direct references since they're in `game_test` package.

**Files:**
- Modify: `internal/game/coordinator_test.go`
- Modify: `internal/game/node_test.go`
- Modify: `internal/game/testutil_test.go`
- Modify: `internal/game/replica_test.go` (may need no changes)
- Modify: `internal/game/topology_test.go` (may need no changes)

- [ ] **Step 1: Fix testutil_test.go**

This file imported both `"internal/game"` (aliased or not) and references `game.NewGameWorld`, `game.DefaultGameConfig`, etc. Since it's `package game_test`, it still needs to qualify with `game.` -- but the import path changes to just `"github.com/zenion/mmoserver/internal/game"` (which it may already have). Remove any `"internal/universe"` import.

For test files in `package game_test`, they access exported symbols via the `game` package name. So `game.NewGameWorld` stays as-is. Only the `universe.` prefixed calls need updating -- `universe.GameSetup` becomes `game.GameSetup`, etc.

```bash
for f in internal/game/*_test.go; do
  # Remove universe import
  sed -i '/"github.com\/zenion\/mmoserver\/internal\/universe"/d' "$f"
  # Replace universe. prefix with game.
  sed -i 's/universe\.//g' "$f"
done
```

Note: `replica_test.go` and `topology_test.go` don't import `internal/game` or `internal/universe` -- they only use `pkg/` packages and should need no changes beyond the package declaration (already done in Task 2).

- [ ] **Step 2: Check for `UnwrapGameWorld` usage in tests**

The tests may call `universe.UnwrapGameWorld()`. After the merge, this becomes `game.UnwrapGameWorld()` (since tests are `package game_test`).

```bash
grep -r "UnwrapGameWorld" internal/game/*_test.go
```

If found, ensure the import is `"github.com/zenion/mmoserver/internal/game"` and the call uses `game.UnwrapGameWorld()`.

- [ ] **Step 3: Run tests**

```bash
go test ./internal/game/... -v -count=1
```

All tests should pass.

---

### Task 6: Update external callers

Update `cmd/server/main.go` which imports `internal/universe`.

**Files:**
- Modify: `cmd/server/main.go`

- [ ] **Step 1: Update cmd/server/main.go imports**

Remove the `internaluniverse` import and update all references to use `game.`:

```bash
# Remove the universe import line
sed -i '/internaluniverse.*internal\/universe/d' cmd/server/main.go
# Replace internaluniverse. with game.
sed -i 's/internaluniverse\./game./g' cmd/server/main.go
```

The file already imports `"github.com/zenion/mmoserver/internal/game"`, so no new import is needed.

- [ ] **Step 2: Run go vet on cmd/server**

```bash
go vet ./cmd/server/...
```

- [ ] **Step 3: Run full build**

```bash
make build
```

---

### Task 7: Update CLAUDE.md

Update the architecture documentation to reflect the new structure.

**Files:**
- Modify: `CLAUDE.md`

- [ ] **Step 1: Update Package Layout section**

In the "Game-specific (`internal/`)" section of CLAUDE.md, replace the `internal/system/` and `internal/universe/` entries:

Old:
```
- `internal/game/` — GameWorld, entity files, lifecycle, commands, config, player DB, log categories, transfer codec
- `internal/system/` — game systems (executed in registration order)
- `internal/universe/` — game-specific `GameWorld` adapter implementing `pkg/universe.GameWorld`, plus `NodeFactory` that wires game systems
```

New:
```
- `internal/game/` — all game-specific code: GameWorld, entity files, ECS systems (`system_*.go`), lifecycle, network handlers (`nethandler_*.go`), input handlers, mmokit adapter, factory, config, player DB, log categories, transfer codec
```

Also update the "Systems" section header reference from `internal/universe/factory.go` to `internal/game/factory.go`.

- [ ] **Step 2: Verify CLAUDE.md is consistent**

Search for any remaining references to `internal/system/` or `internal/universe/` in CLAUDE.md and update them.

---

### Task 8: Final verification

- [ ] **Step 1: Run go vet on entire project**

```bash
go vet ./...
```

Expected: clean output (no errors).

- [ ] **Step 2: Run all tests**

```bash
go test ./... -count=1
```

Expected: all tests pass.

- [ ] **Step 3: Run make build**

```bash
make build
```

Expected: compiles to `bin/server`.

- [ ] **Step 4: Verify internal/system and internal/universe are gone**

```bash
ls internal/system 2>&1 || echo "system/ removed"
ls internal/universe 2>&1 || echo "universe/ removed"
```

Expected: both directories removed.

- [ ] **Step 5: Commit**

```bash
git add -A
git commit -m "refactor: merge internal/system and internal/universe into internal/game

Consolidates three tightly-coupled packages into one. Systems get system_
prefix for findability. Eliminates unwrapGW() hack and cross-package
adapter indirection."
```
