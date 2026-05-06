# Entity-Kind SDK Export and Proto Pipeline Retirement

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Migrate `gamepb.EntityType` to a native Go const block, teach sdkgen to emit a TypeScript const for entity kinds from the existing schema, and retire the now-empty proto pipeline (`proto/gamepb/`, the C# and ES emitters, `gen/csharp/`, `gen/es/`).

**Architecture:** Three independent workstreams sharing a branch: (1) native Go consts in `internal/component`, (2) one new sdkgen emitter that consumes the schema's existing `entities[]` array, (3) cleanup deleting the proto pipeline. After this lands, `proto/` contains only `meshpb/` (server-internal); the client-facing wire stack is fully self-contained in the generated SDK.

**Tech Stack:** Go 1.21+, existing `cmd/sdkgen` (Go-AST-driven TS code generator), `cmd/server --dump-schema` JSON pipeline, `coder/websocket`-based web-pixi TypeScript client, `bun` for TS typechecks.

**Spec:** [docs/superpowers/specs/2026-05-07-entity-kind-sdk-export-design.md](../specs/2026-05-07-entity-kind-sdk-export-design.md)

**Plans this builds on (commits on main):**
- `c340a79` — Bucket A cleanup (delete enginepb + basicpb + placeholder enums)
- `3384d69` — Plan 3 merge (protobuf residue cleanup)

---

## Phase 0 — Setup

### Task 0.1: Branch + clean baseline

**Files:** none (git only)

- [ ] **Step 1: Verify clean tree on main**

```bash
cd .
git status
git log --oneline -3
```

Expected: `On branch main`, `nothing to commit, working tree clean`. `c340a79` should be the most recent commit.

- [ ] **Step 2: Create branch**

```bash
git checkout -b feat/entity-kind-sdk-export
```

- [ ] **Step 3: Verify build is clean**

```bash
go vet ./...
go test ./... -count=1 2>&1 | grep -E "FAIL|panic" | head
(cd web-pixi && bunx tsc --noEmit) && (cd examples/4node-basic/web && bunx tsc --noEmit)
```

Expected: zero output from go vet, zero `FAIL` lines, both TS typechecks clean.

---

## Phase 1 — Native Go consts in `internal/component`

### Task 1.1: Replace proto-derived consts with native Go consts

**Files:**
- Modify: `internal/component/components.go`

The proto enum currently encodes: `_SHIP=0`, `_ASTEROID=1`, `_PROJECTILE=2`, `_STATION=3`, `_LOOT_CRATE=4`, `_NPC=5`. We compact (drop the unused `_PROJECTILE` slot) — clients regenerate their SDK in lockstep so old wire bytes are not preserved.

- [ ] **Step 1: Read current state**

```bash
sed -n '1,25p' internal/component/components.go
```

Expected (lines 1-22):

```go
package component

import (
	"github.com/mlange-42/ark/ecs"
	gamepb "github.com/zenion/mmoserver/gen/go/gamepb"
	"github.com/zenion/mmoserver/internal/item"
)

// Collision layers (game-specific assignments)
const (
	LayerPlayer  uint8 = 1
	LayerTerrain uint8 = 2
)

// Entity types (derived from protobuf enums)
const (
	TypeShip      = uint8(gamepb.EntityType_ENTITY_TYPE_SHIP)
	TypeAsteroid  = uint8(gamepb.EntityType_ENTITY_TYPE_ASTEROID)
	TypeStation   = uint8(gamepb.EntityType_ENTITY_TYPE_STATION)
	TypeLootCrate = uint8(gamepb.EntityType_ENTITY_TYPE_LOOT_CRATE)
	TypeNPC       = uint8(gamepb.EntityType_ENTITY_TYPE_NPC)
)
```

- [ ] **Step 2: Replace the import and const block**

Use Edit to replace the import + const block:

```go
package component

import (
	"github.com/mlange-42/ark/ecs"
	"github.com/zenion/mmoserver/internal/item"
)

// Collision layers (game-specific assignments)
const (
	LayerPlayer  uint8 = 1
	LayerTerrain uint8 = 2
)

// Entity kinds — wire byte that identifies an entity's kind on the
// replication channel. Names match the second arg passed to
// mmokit.RegisterKind in internal/game/entity_kinds.go; sdkgen emits a
// matching TypeScript const block from the same kind registry, so the
// authoritative source of truth is the RegisterKind call sites.
const (
	KindShip      uint8 = iota // 0
	KindAsteroid               // 1
	KindStation                // 2
	KindLootCrate              // 3
	KindNPC                    // 4
)
```

- [ ] **Step 3: Verify the file compiles standalone**

```bash
cd .
go build ./internal/component/... 2>&1 | head
```

Expected: zero output (compile succeeds — the rest of `internal/component` doesn't reference `Type*`).

- [ ] **Step 4: Inventory all callers of the old names**

```bash
grep -rn "gamecomp\.Type\(Ship\|Asteroid\|Station\|LootCrate\|NPC\)\|component\.Type\(Ship\|Asteroid\|Station\|LootCrate\|NPC\)" --include="*.go" .
```

Save the output — Tasks 1.2 and 1.3 update these. The grep should return ~10-20 hits across `internal/game/`, `internal/bot/`, `cmd/botclient/`.

The build is broken at this point (other files still reference `gamecomp.TypeShip` etc.) — Task 1.2/1.3 fix it. Don't commit yet.

### Task 1.2: Update `internal/game` callers

**Files:**
- Modify: `internal/game/*.go` (all callers of `gamecomp.Type*`)

- [ ] **Step 1: Find all callers in internal/game**

```bash
grep -rn "gamecomp\.Type\(Ship\|Asteroid\|Station\|LootCrate\|NPC\)" --include="*.go" internal/game/
```

- [ ] **Step 2: Rename in each file**

For each file in the grep output, rename:
- `gamecomp.TypeShip` → `gamecomp.KindShip`
- `gamecomp.TypeAsteroid` → `gamecomp.KindAsteroid`
- `gamecomp.TypeStation` → `gamecomp.KindStation`
- `gamecomp.TypeLootCrate` → `gamecomp.KindLootCrate`
- `gamecomp.TypeNPC` → `gamecomp.KindNPC`

These are mechanical renames — use Edit's `replace_all` per file.

- [ ] **Step 3: Verify the package compiles**

```bash
cd .
go vet ./internal/game/... 2>&1 | head
```

Expected: zero output (compile + vet clean).

### Task 1.3: Update `internal/bot` and `cmd/botclient`

**Files:**
- Modify: `internal/bot/world.go`
- Modify: `internal/bot/bot.go`
- Modify: `internal/bot/typed_decoder.go`
- Modify: `cmd/botclient/duel.go`
- Modify: `cmd/botclient/miners.go`

The bot uses both `gamepb.EntityType_ENTITY_TYPE_*` (full proto enum names) AND has its own derived `typeShip`/`typeAsteroid` constants in `world.go`. All migrate to `gamecomp.Kind*`.

- [ ] **Step 1: Update `internal/bot/world.go`**

Read [internal/bot/world.go](internal/bot/world.go) lines 1-20 and 50-80 and 230-245 to find:
- The `gamepb` import
- The `typeShip`/`typeAsteroid`/etc. derived constants (lines 11-17)
- The `Type gamepb.EntityType` field on `EntitySnapshot` (line 58)
- The `gamepb.EntityType(entityType)` cast at line 238

Edit the file:
1. Add `gamecomp "github.com/zenion/mmoserver/internal/component"` to the import block; remove the `gamepb "github.com/zenion/mmoserver/gen/go/gamepb"` import.
2. Delete the `typeShip`/`typeAsteroid`/`typeStation`/`typeLootCrate`/`typeNPC` const block — those were thin re-derivations of the proto enum and are no longer needed; the rest of `world.go` references them, so the rename also applies there.
3. Replace every `typeShip` / `typeAsteroid` / `typeStation` / `typeLootCrate` / `typeNPC` reference inside `world.go` with `gamecomp.KindShip` / etc.
4. Change the `EntitySnapshot.Type` field from `gamepb.EntityType` to `uint8`.
5. Replace `gamepb.EntityType(entityType)` with `entityType` (it's already `uint8` in the wire shape; the cast was the only reason for the field's typed wrapper).

Verify with:
```bash
grep -n "gamepb\|typeShip\|typeAsteroid\|typeStation\|typeLootCrate\|typeNPC" internal/bot/world.go
```
Expected: zero hits.

- [ ] **Step 2: Update `internal/bot/bot.go`**

Read lines 360-390 to find the `gamepb.EntityType_ENTITY_TYPE_ASTEROID` and `gamepb.EntityType_ENTITY_TYPE_STATION` references (around lines 363 and 386).

Edit:
1. Drop the `gamepb` import (line 16: `gamepb "github.com/zenion/mmoserver/gen/go/gamepb"`).
2. Replace `gamepb.EntityType_ENTITY_TYPE_ASTEROID` → `gamecomp.KindAsteroid`.
3. Replace `gamepb.EntityType_ENTITY_TYPE_STATION` → `gamecomp.KindStation`.
4. Add `gamecomp "github.com/zenion/mmoserver/internal/component"` to the import block (if not already added by world.go's edits — the bot package imports work file-locally).

Verify:
```bash
grep -n "gamepb" internal/bot/bot.go
```
Expected: zero hits.

- [ ] **Step 3: Update `internal/bot/typed_decoder.go`**

Read line 246 area: `if e.Type == gamepb.EntityType_ENTITY_TYPE_SHIP && e.PilotName == b.name {`.

Edit:
1. Drop the `gamepb` import (if present).
2. Replace `gamepb.EntityType_ENTITY_TYPE_SHIP` → `gamecomp.KindShip`.
3. Add `gamecomp` import if needed.

Verify:
```bash
grep -n "gamepb" internal/bot/typed_decoder.go
```
Expected: zero hits.

- [ ] **Step 4: Update `cmd/botclient/duel.go`**

Find the reference: line 79: `return e.Type == gamepb.EntityType_ENTITY_TYPE_SHIP && e.PilotName != b.Name()`.

Edit: replace `gamepb.EntityType_ENTITY_TYPE_SHIP` → `gamecomp.KindShip`. Add `gamecomp "github.com/zenion/mmoserver/internal/component"` import. Drop `gamepb` import.

- [ ] **Step 5: Update `cmd/botclient/miners.go`**

Find line 192: `return e.Type == gamepb.EntityType_ENTITY_TYPE_ASTEROID && e.ResourceRemaining > 0`.

Edit: replace `gamepb.EntityType_ENTITY_TYPE_ASTEROID` → `gamecomp.KindAsteroid`. Update imports (drop `gamepb`, add `gamecomp` if needed).

- [ ] **Step 6: Verify nothing else references gamepb in bot/botclient**

```bash
grep -rn "gamepb" internal/bot cmd/botclient --include="*.go" | grep -v "/gen/"
```
Expected: zero hits.

- [ ] **Step 7: Compile-verify**

```bash
cd .
go vet ./...
```
Expected: zero output.

### Task 1.4: Verify Phase 1 + commit

- [ ] **Step 1: Run tests**

```bash
cd .
go test ./... -count=1 2>&1 | grep -E "FAIL|panic" | head
```
Expected: zero output.

- [ ] **Step 2: Confirm no remaining proto callers in production Go**

```bash
grep -rn "gamepb" . --include="*.go" | grep -v "/gen/"
```
Expected: zero hits. (`gen/go/gamepb/` itself still exists — that's deleted in Phase 3.)

- [ ] **Step 3: Update `internal/component/README.md`**

```bash
grep -n "TypeShip\|TypeAsteroid\|TypeProjectile\|TypeStation\|TypeLootCrate\|TypeNPC\|LayerProjectile" internal/component/README.md
```

Find the doc lines mentioning the old `Type*` names and the stale `LayerProjectile = 4`. Update:
- `TypeShip, TypeAsteroid, TypeProjectile, TypeStation, TypeLootCrate` → `KindShip, KindAsteroid, KindStation, KindLootCrate, KindNPC` (drop Projectile, add NPC which is the actual full set).
- `LayerProjectile = 4` and any "Projectiles collide with..." prose: delete (no Projectile kind exists).

- [ ] **Step 4: Commit**

```bash
git add internal/component/components.go internal/component/README.md \
        internal/game/ \
        internal/bot/world.go internal/bot/bot.go internal/bot/typed_decoder.go \
        cmd/botclient/duel.go cmd/botclient/miners.go
git commit -m "$(cat <<'EOF'
refactor(component): replace gamepb.EntityType with native Kind* consts

Drop the gamepb import from internal/component, internal/bot, and
cmd/botclient. The entity-kind wire bytes are now plain uint8 consts in
internal/component/components.go (KindShip/KindAsteroid/KindStation/
KindLootCrate/KindNPC), matching the framework's RegisterKind name
convention from CLAUDE.md.

Wire values compacted (drop the unused _PROJECTILE = 2 slot from the old
proto enum). Clients regenerate the SDK in lockstep (Phase 2), so old
wire bytes are not preserved across this change. The web-pixi
constants.ts color table loses the no-op Projectile entry.

Bot's EntitySnapshot.Type changes from gamepb.EntityType to plain uint8
(matches the wire shape).

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Phase 2 — sdkgen TS emitter for `EntityType`

### Task 2.1: Create the emitter

**Files:**
- Create: `cmd/sdkgen/entitytype.go`

The emitter produces `entityType.ts` from the schema dump's `entities[]` array (each entry has a `kind uint8` field and a `name string` field — already populated from `RegisterKind` calls).

- [ ] **Step 1: Read existing emitter shape**

Read [cmd/sdkgen/operations.go](../../../cmd/sdkgen/operations.go) (the simplest existing emitter) and [cmd/sdkgen/main.go](../../../cmd/sdkgen/main.go) to understand the pattern.

Note: `g.schema` is `*Schema` (defined in `cmd/sdkgen/schema.go`); `g.schema.Entities` is `[]EntitySchema` where each `EntitySchema` has at least `Kind uint8` and `Name string` (verify by reading `schema.go`).

- [ ] **Step 2: Verify the EntitySchema shape**

```bash
cat cmd/sdkgen/schema.go
```

Locate the `EntitySchema` struct definition. Confirm it has `Kind` (uint8 or int) and `Name` (string). If the field name is different (e.g. `KindID`), use what's actually there in the Write below.

- [ ] **Step 3: Create the emitter**

Write `cmd/sdkgen/entitytype.go`:

```go
package main

import (
	"fmt"
	"sort"
	"strings"
)

// genEntityType emits entityType.ts containing one TS const block keyed
// by the entity-kind names + numeric IDs registered via
// mmokit.RegisterKind[T](p, kindID, name, ...). The schema dump's
// Entities array carries both fields, so this emitter consumes the same
// data the broadcasts/inputs/operations emitters use for entity classes.
//
// Output schema:
//
//	export const EntityType = {
//	    Ship: 0,
//	    Asteroid: 1,
//	    Station: 2,
//	    LootCrate: 3,
//	    NPC: 4,
//	} as const;
//	export type EntityTypeValue = typeof EntityType[keyof typeof EntityType];
//
// `as const` chosen over `enum` for tree-shakeability + zero runtime
// overhead. Consumers do forward-lookup only (no reverse-mapping needs).
//
// Returns "" when the schema has zero registered entity kinds — caller
// (main.go's files map) uses that to skip writing the file.
func (g *Generator) genEntityType() string {
	if len(g.schema.Entities) == 0 {
		return ""
	}

	// Sort by Kind value to keep the output stable regardless of the
	// schema dump's iteration order.
	entries := make([]EntitySchema, len(g.schema.Entities))
	copy(entries, g.schema.Entities)
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Kind < entries[j].Kind
	})

	var b strings.Builder
	b.WriteString("// GENERATED by sdkgen — do not edit.\n\n")
	b.WriteString("// Entity-kind wire bytes. The values match the kindID arg passed to\n")
	b.WriteString("// mmokit.RegisterKind[T] on the Go side; the names match the second\n")
	b.WriteString("// arg (display name) of the same call.\n\n")
	b.WriteString("export const EntityType = {\n")
	for _, e := range entries {
		fmt.Fprintf(&b, "    %s: %d,\n", e.Name, e.Kind)
	}
	b.WriteString("} as const;\n\n")
	b.WriteString("export type EntityTypeValue = typeof EntityType[keyof typeof EntityType];\n")

	return b.String()
}
```

If `EntitySchema`'s field name in `cmd/sdkgen/schema.go` is `KindID` rather than `Kind`, substitute that name in the sort + `Fprintf`. If it's `Kind`, the code above stands.

- [ ] **Step 4: Compile-verify**

```bash
cd .
go vet ./cmd/sdkgen/...
```
Expected: zero output.

### Task 2.2: Wire the emitter into main.go's files map

**Files:**
- Modify: `cmd/sdkgen/main.go`

- [ ] **Step 1: Read the existing files map**

Read [cmd/sdkgen/main.go](../../../cmd/sdkgen/main.go) lines 60-100 to find the `files` map literal and its conditional registration of `broadcasts.ts`/`inputs.ts`/`operations.ts`.

- [ ] **Step 2: Add the entityType.ts entry**

Below the existing conditional registrations (after `operations.ts`), add:

```go
	// entityType.ts holds the EntityType const block (one entry per
	// kind registered via mmokit.RegisterKind[T]). Skipped when no
	// entity kinds are registered.
	if len(g.schema.Entities) > 0 {
		files["entityType.ts"] = g.genEntityType
	}
```

- [ ] **Step 3: Compile-verify**

```bash
go vet ./cmd/sdkgen/...
```
Expected: zero output.

### Task 2.3: Re-export EntityType from index.ts

**Files:**
- Modify: `cmd/sdkgen/generate.go::genIndex`

- [ ] **Step 1: Read genIndex**

Read [cmd/sdkgen/generate.go](../../../cmd/sdkgen/generate.go) lines 812-870 (the `genIndex` function — already inspected above).

- [ ] **Step 2: Add EntityType re-export**

After the existing operations re-export block (around line 866 — `fmt.Fprintf(&b, "export { %s } from \"./operations.js\";\n", strings.Join(names, ", "))`), and before the final `return b.String()`, add:

```go
	if len(g.schema.Entities) > 0 {
		// Re-export the EntityType const + type alias so app code
		// imports them via the SDK's public surface.
		b.WriteString("export { EntityType } from \"./entityType.js\";\n")
		b.WriteString("export type { EntityTypeValue } from \"./entityType.js\";\n")
	}
```

- [ ] **Step 3: Compile-verify**

```bash
go vet ./cmd/sdkgen/...
```
Expected: zero output.

### Task 2.4: Regenerate SDKs

**Files (auto-generated):**
- web-pixi/sdk/entityType.ts (new)
- web-pixi/sdk/index.ts (modified — adds EntityType re-export)
- examples/4node-basic/web/sdk/entityType.ts (new — 4node-basic doesn't use it but the emitter runs uniformly)
- examples/4node-basic/web/sdk/index.ts (modified)

- [ ] **Step 1: Verify the dev server isn't holding port 9100**

```bash
ss -tlnp 2>/dev/null | grep -E ":(9100|8080)" | head
```

If a dev server is bound, skip the live regen and use the static `--dump-schema` path instead (Step 2 alt below).

- [ ] **Step 2: Regenerate (live server path)**

If port 9100 is free:

```bash
cd .
just space-sdk
just client-sdk examples/4node-basic
```

- [ ] **Step 2 alt: Regenerate (static path, when dev server is running)**

If port 9100 is taken, run sdkgen against the schema dump directly. Build the server binary first to a non-root location:

```bash
cd .
mkdir -p bin
go build -o bin/server ./cmd/server
go build -o bin/sdkgen ./cmd/sdkgen
go build -o bin/example-4node ./examples/4node-basic

# space-sdk equivalent:
bin/server --dump-schema | bin/sdkgen \
    --out web-pixi/sdk \
    --core pkg/quantize/ts/delta-decoder-core.ts

# 4node-basic client-sdk equivalent (uses 4node-basic's own binary):
bin/example-4node --dump-schema "--postgres-url=postgres://mmo:mmo@localhost:5432/mmo?sslmode=disable" | bin/sdkgen \
    --out examples/4node-basic/web/sdk \
    --core pkg/quantize/ts/delta-decoder-core.ts
```

NOTE: `--dump-schema` exits before binding any port, so it does not collide with a running dev server.

- [ ] **Step 3: Verify the regenerated output**

```bash
cat web-pixi/sdk/entityType.ts
```

Expected: contains `export const EntityType = { Ship: 0, Asteroid: 1, Station: 2, LootCrate: 3, NPC: 4, } as const;`.

```bash
grep "EntityType" web-pixi/sdk/index.ts
```

Expected: two re-export lines — `export { EntityType } from "./entityType.js";` and the type alias.

- [ ] **Step 4: Commit Phase 2 work**

```bash
cd .
git add cmd/sdkgen/entitytype.go cmd/sdkgen/main.go cmd/sdkgen/generate.go \
        web-pixi/sdk/entityType.ts web-pixi/sdk/index.ts \
        examples/4node-basic/web/sdk/entityType.ts examples/4node-basic/web/sdk/index.ts
git commit -m "$(cat <<'EOF'
feat(sdkgen): emit EntityType const from entity-kind registry

The schema dump's Entities[] array carries (kind, name) pairs from every
mmokit.RegisterKind[T](p, kindID, name, ...) call site. sdkgen already
consumes this for entity-class generation; one new emitter
(entitytype.ts) writes a parallel TS const block:

    export const EntityType = {
        Ship: 0,
        Asteroid: 1,
        Station: 2,
        LootCrate: 3,
        NPC: 4,
    } as const;

`as const` over `enum` for tree-shakeability + zero runtime overhead.
Re-exported from the SDK index so app code imports via the public
surface (no reaching into entityType.ts directly).

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Phase 3 — Retire the proto pipeline

### Task 3.1: Update web-pixi consumer

**Files:**
- Modify: `web-pixi/src/constants.ts`

- [ ] **Step 1: Read current state**

```bash
cat web-pixi/src/constants.ts | head -20
```

Expected (lines 1-15):

```typescript
import { EntityType } from "@gen/game_pb.js";

export const TICK_INTERVAL = 50; // 20Hz = 50ms
export const CELL_SIZE = 8192; // must match pkg/coords CellSize
export const MAX_CHAT_DISPLAY = 50;
export const MAX_THRUSTER_PARTICLES = 20;
export const TOAST_DURATION = 3000;

export const ENTITY_COLORS: Record<number, number> = {
  [EntityType.SHIP]: 0x44aaff,
  [EntityType.ASTEROID]: 0xaa8866,
  [EntityType.PROJECTILE]: 0xffff44,
  [EntityType.STATION]: 0x88ff88,
  [EntityType.NPC]: 0xff4444,
};
```

- [ ] **Step 2: Update imports + casing + drop projectile**

Edit:
1. Replace `import { EntityType } from "@gen/game_pb.js";` → `import { EntityType } from "../sdk/index.js";`
2. Inside `ENTITY_COLORS`:
   - `[EntityType.SHIP]` → `[EntityType.Ship]`
   - `[EntityType.ASTEROID]` → `[EntityType.Asteroid]`
   - `[EntityType.STATION]` → `[EntityType.Station]`
   - `[EntityType.NPC]` → `[EntityType.NPC]` (this stays — `NPC` is the kind name)
   - **Delete** the `[EntityType.PROJECTILE]: 0xffff44,` line entirely

Final shape:

```typescript
import { EntityType } from "../sdk/index.js";

export const TICK_INTERVAL = 50; // 20Hz = 50ms
export const CELL_SIZE = 8192; // must match pkg/coords CellSize
export const MAX_CHAT_DISPLAY = 50;
export const MAX_THRUSTER_PARTICLES = 20;
export const TOAST_DURATION = 3000;

export const ENTITY_COLORS: Record<number, number> = {
  [EntityType.Ship]: 0x44aaff,
  [EntityType.Asteroid]: 0xaa8866,
  [EntityType.Station]: 0x88ff88,
  [EntityType.NPC]: 0xff4444,
};
```

- [ ] **Step 3: Verify TS typecheck**

```bash
cd web-pixi
bunx tsc --noEmit 2>&1 | tail -10
```
Expected: zero output.

### Task 3.2: Drop @gen Vite + tsconfig aliases

**Files:**
- Modify: `web-pixi/vite.config.ts`
- Modify: `web-pixi/tsconfig.json`

- [ ] **Step 1: Inspect current aliases**

```bash
grep -n "@gen" web-pixi/vite.config.ts web-pixi/tsconfig.json
```

Expected: each file has one `@gen/game_pb.js` alias entry (other `@gen/*` aliases were dropped in Plan 3 cleanup commit `28cdffc`).

- [ ] **Step 2: Delete the alias from vite.config.ts**

Read the file. Find the `resolve.alias` block. Delete the line aliasing `@gen/game_pb.js` (and any sibling alias lines that became dead — verify by reading the whole `alias` array).

If the `alias` array becomes empty after deletion, delete the entire `resolve: { alias: { ... } }` block too.

- [ ] **Step 3: Delete the alias from tsconfig.json**

Read the file. Find the `paths` block under `compilerOptions`. Delete the `@gen/game_pb.js` mapping. If `paths` becomes empty after deletion, delete `paths` (and `baseUrl` if it was only there for `paths`).

- [ ] **Step 4: Verify TS typecheck still passes**

```bash
cd web-pixi
bunx tsc --noEmit 2>&1 | tail
```
Expected: zero output.

- [ ] **Step 5: Inspect 4node-basic's vite/tsconfig**

```bash
grep -n "@gen" examples/4node-basic/web/vite.config.ts examples/4node-basic/web/tsconfig.json
```

If any `@gen/*` aliases remain in the 4node-basic config, delete them too. (Plan 3 cleanup left this file in a clean state, so this should report zero hits — but verify.)

### Task 3.3: Delete `proto/gamepb/` and `gen/{csharp,es}/`

**Files:**
- Delete: `proto/gamepb/` (whole directory)

- [ ] **Step 1: Verify zero remaining gamepb consumers**

```bash
cd .
grep -rn "gamepb\|@gen/game_pb" --include="*.go" --include="*.ts" . 2>/dev/null | grep -v "/gen/" | grep -v "/docs/"
```
Expected: zero hits.

- [ ] **Step 2: Delete proto/gamepb**

```bash
rm -rf proto/gamepb/
ls proto/
```
Expected: `meshpb` (only).

- [ ] **Step 3: Delete gen/csharp and gen/es**

These are gitignored (per `.gitignore: gen/`), so no `git rm` needed — just remove from disk:

```bash
rm -rf gen/csharp/
rm -rf gen/es/
ls gen/
```
Expected: `go` (only).

### Task 3.4: Drop C# + ES emitters from buf.gen.yaml

**Files:**
- Modify: `buf.gen.yaml`

- [ ] **Step 1: Read current buf.gen.yaml**

```bash
cat buf.gen.yaml
```

Expected: `version: v2`, `inputs:`, `plugins:` with 4 entries (`protocolbuffers/go`, `grpc/go`, `protocolbuffers/csharp`, `bufbuild/es`).

- [ ] **Step 2: Edit to keep only Go plugins**

Use Edit to replace the file. Final shape:

```yaml
version: v2
inputs:
  - directory: proto
plugins:
  - remote: buf.build/protocolbuffers/go
    out: gen/go
    opt: paths=source_relative
  - remote: buf.build/grpc/go
    out: gen/go
    opt: paths=source_relative
```

- [ ] **Step 3: Verify just proto still works (regenerates only Go meshpb)**

```bash
cd .
just proto
ls gen/go/
```
Expected: `gen/go/` contains only `meshpb/`.

```bash
ls gen/
```
Expected: `gen/` contains only `go/`.

### Task 3.5: Drop Unity references from CLAUDE.md and README.md

**Files:**
- Modify: `CLAUDE.md`
- Modify: `README.md`

- [ ] **Step 1: Find Unity / gen/csharp / gen/es references**

```bash
grep -n "Unity\|gen/csharp\|gen/es\|Engine\.cs\|Game\.cs" CLAUDE.md README.md
```

- [ ] **Step 2: Update CLAUDE.md**

For each match:
- Lines describing "Unity client (Engine.cs + Game.cs)" or "gen/csharp/" — delete or rewrite to drop the Unity reference.
- Lines describing "Web client (web-pixi/dist + Vite)" — keep.
- "Server-authoritative — the Unity client (and web canvas test client) are dumb renderers" — rewrite to "Server-authoritative — the web client is a dumb renderer."
- The "Proto Codegen" section's `gen/csharp/` and `gen/es/...` bullets — delete the whole bullet for csharp; for ES, mention it's gone.

Aim for a CLAUDE.md that accurately reflects the post-merge state: only Go server + web-pixi TS client.

- [ ] **Step 3: Update README.md**

Same treatment — drop Unity references; keep web client references.

- [ ] **Step 4: Verify**

```bash
grep -n "Unity\|gen/csharp\|gen/es\|Engine\.cs\|Game\.cs" CLAUDE.md README.md
```
Expected: zero hits (or only false positives in unrelated contexts).

### Task 3.6: Final verification + commit

- [ ] **Step 1: Full Go test suite**

```bash
cd .
go vet ./...
go test ./... -count=1 2>&1 | grep -E "FAIL|panic" | head
```
Expected: zero output from both.

- [ ] **Step 2: Both TS typechecks**

```bash
(cd web-pixi && bunx tsc --noEmit) && echo "web-pixi clean"
(cd examples/4node-basic/web && bunx tsc --noEmit) && echo "4node-basic clean"
```
Expected: both "clean" lines printed.

- [ ] **Step 3: Final acceptance greps**

```bash
cd .
echo "=== gamepb in production code ==="
grep -rn "gamepb" --include="*.go" --include="*.ts" . 2>/dev/null | grep -v "/gen/" | grep -v "/docs/"
echo "=== @gen/game_pb in TS ==="
grep -rn "@gen/game_pb" --include="*.ts" .
echo "=== Type{Ship,Asteroid,...} in production code ==="
grep -rn "gamecomp\.Type\(Ship\|Asteroid\|Station\|LootCrate\|NPC\)" --include="*.go" .
echo "=== proto layout ==="
ls proto/
echo "=== gen layout ==="
ls gen/
echo "=== buf.gen.yaml plugins ==="
grep "remote:" buf.gen.yaml
```

Expected:
- All four greps: empty.
- `proto/` contains only `meshpb`.
- `gen/` contains only `go`.
- `buf.gen.yaml` shows only the two Go plugins.

- [ ] **Step 4: Commit Phase 3 cleanup**

```bash
cd .
git add web-pixi/src/constants.ts web-pixi/vite.config.ts web-pixi/tsconfig.json \
        examples/4node-basic/web/vite.config.ts examples/4node-basic/web/tsconfig.json \
        proto/ buf.gen.yaml CLAUDE.md README.md
git commit -m "$(cat <<'EOF'
chore(proto,docs): retire proto/gamepb + Unity/ES emitters

After Phase 1+2, gamepb has zero callers — the entity-kind enum is now a
native Go const block + sdkgen-emitted TS const. Remove the proto file,
the C# + ES buf plugins, the gen/csharp + gen/es output trees, the
@gen/game_pb.js Vite/tsconfig aliases, and the Unity references in
CLAUDE.md / README.md (Unity has been unused).

End state:
- proto/ contains only meshpb (server-internal mesh data plane)
- gen/ contains only go (only meshpb regenerates)
- buf.gen.yaml has only the two Go plugins
- web-pixi has zero @gen/* imports — the entire client-facing wire stack
  is self-contained in the generated SDK (web-pixi/sdk/)

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Phase 4 — Merge to main

### Task 4.1: Final smoke + merge

- [ ] **Step 1: Full validation**

```bash
cd .
go vet ./...
go test ./... -count=1 2>&1 | grep -E "FAIL|panic" | head
(cd web-pixi && bunx tsc --noEmit)
(cd examples/4node-basic/web && bunx tsc --noEmit)
just proto    # confirms regen succeeds with new buf.gen.yaml
```

Expected: zero output from go vet, zero `FAIL`, both TS clean, `just proto` succeeds.

- [ ] **Step 2: Update spec status**

Edit [docs/superpowers/specs/2026-05-07-entity-kind-sdk-export-design.md](../specs/2026-05-07-entity-kind-sdk-export-design.md) line 3 (the `**Status:** Draft 2026-05-07` line) to `**Status:** Landed YYYY-MM-DD` (use today's date).

```bash
git add docs/superpowers/specs/2026-05-07-entity-kind-sdk-export-design.md
git commit -m "docs(spec): mark entity-kind SDK export landed

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

- [ ] **Step 3: Merge to main**

```bash
git checkout main
git merge --no-ff feat/entity-kind-sdk-export -m "$(cat <<'EOF'
Merge branch 'feat/entity-kind-sdk-export'

Migrates the last cross-language proto enum (gamepb.EntityType) to a
native Go const block + sdkgen-emitted TS const. Retires the proto
pipeline supporting it: proto/gamepb/ deleted; buf.gen.yaml drops the
C# and ES emitters; gen/csharp/ and gen/es/ trees gone; Unity refs
removed from CLAUDE.md / README.md.

End state: proto/ contains only meshpb (server-internal). The entire
client-facing wire stack is self-contained in the sdkgen-emitted SDK —
web-pixi has zero @gen/* imports.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

- [ ] **Step 4: Verify post-merge**

```bash
git log --oneline -3
go vet ./...
```
Expected: merge commit at HEAD; zero vet output.

---

## Acceptance Criteria

- `proto/` contains only `meshpb/`
- `gen/` contains only `go/` (and `gen/go/` contains only `meshpb/`)
- `buf.gen.yaml` has only the two Go plugins (`protocolbuffers/go` + `grpc/go`)
- Zero `gamepb` references in production Go (excluding `/gen/`, `/docs/`)
- Zero `@gen/*` imports in TypeScript (production code)
- Zero `gamecomp.Type{Ship,Asteroid,Station,LootCrate,NPC}` references (renamed to `Kind*`)
- `web-pixi/sdk/entityType.ts` exists and exports the const block
- web-pixi imports `EntityType` from `../sdk/index.js`
- `web-pixi/src/constants.ts` no longer has the Projectile color entry
- `go vet ./...` clean
- `go test ./...` clean
- Both TS typechecks clean
- `just proto` succeeds
- CLAUDE.md and README.md no longer mention Unity / `gen/csharp/` / `gen/es/`
- Spec marked Landed
- Branch merged to main
