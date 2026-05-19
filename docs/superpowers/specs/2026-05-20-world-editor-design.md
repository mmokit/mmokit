# World Editor — Hand-Placed Skeleton, Procgen Interiors

**Status:** Design
**Date:** 2026-05-20
**Branch target:** new feature branch off `main`

## 1. Overview

Replace procgen placement of POIs, stations, dungeon anchors, belt centers, regions, and decorations with hand-authored JSON manifests, edited live through a new `/world-editor` admin route. Procgen survives only **inside** placed entities — asteroids inside a belt, NPC roster scatter inside a POI, dungeon chamber layout inside a dungeon anchor.

The editor is a Svelte page in the existing admin SPA, talking to the server through existing `cmdsys` verbs. Every edit writes JSON to disk synchronously and mutates the running ECS world in the same handler. Git tracks the JSON; there is no separate "save" or "deploy" step.

Day-one world state is **empty**. The designer hand-places every gameplay anchor starting from scratch.

## 2. Goals & Non-Goals

### Goals

- Hand-author every gameplay-anchor location in the world via a visual editor.
- Source of truth for world data is JSON in `world/` at the repo root, git-tracked.
- Editor is live: a click in the browser writes the JSON file AND spawns the entity in the running cell in one server verb.
- Data layout is cell-size-agnostic: every coordinate is world-absolute; the runtime buckets entities into whichever cell layout exists at boot.
- Reuse the existing admin shell (auth, SSE, cmdsys, audit log) rather than building a parallel system.
- Procgen for interiors (belt asteroid scatter, POI NPC scatter, dungeon chamber graph) stays deterministic per-entity-id seed.

### Non-Goals

- Bootstrapping the world from current procgen output (we are intentionally starting from an empty world).
- Multi-user collaborative editing with cursor sharing (solo dev; SSE notification of changes is enough).
- Distributed-mode shared-filesystem semantics (deferred; v1 assumes `--mode=all` single-process or single-coordinator-host shared FS).
- Embedded `//go:embed` of world data for production binaries (deferred).
- File-system watcher for external JSON edits (deferred; designer uses the editor or runs `world.reload`).
- Per-region game-rule enforcement (regions in v1 are annotations only; PvP/safe-zone behavior wires in later).
- Asset pipeline for decoration sprites (decorations carry a `kind` string; rendering is the client's problem).
- Versioned migrations of the JSON schema (`version` field reserved; v1 ships at `version: 1` and never bumps).

## 3. Architecture

Four layers, each with a clear interface:

```text
┌─ Editor UI (Svelte SPA) ────────────────────────────────────┐
│  /world-editor route · palette · canvas · inspector         │
│  POSTs cmdsys verbs · subscribes to world.changed SSE topic │
└──────────────────────────────────────────────────────────────┘
                       │ HTTP
┌─ Server: cmdsys verbs in internal/game/commands/world.go ───┐
│  world.place · world.move · world.update · world.delete     │
│  world.list · world.reload · world.export                   │
│  Capability "world.edit" · RBAC + audit log free            │
└──────────────────────────────────────────────────────────────┘
                       │ in-process
┌─ pkg/world (new package, no ark imports) ───────────────────┐
│  Manifest types (per entity type) · Repository interface     │
│  JSON file impl · atomic write · per-file sync.Mutex         │
│  In-memory Snapshot · BucketByCell(cellSize) helper          │
└──────────────────────────────────────────────────────────────┘
                       │ on cell boot
┌─ Spawn pipeline (internal/game/factory.go) ─────────────────┐
│  OnCellReady hook: pull bucket for this cell from Snapshot  │
│  Call game.SpawnStation/POI/Dungeon/Belt/Decoration per def │
│  Each spawn function runs interior procgen with id-seed     │
└──────────────────────────────────────────────────────────────┘
```

### 3.1 Code that gets deleted

- `internal/game/poi_gen.go::GeneratePOIs` (FNV-seed-based POI placement)
- `internal/game/belts.go` belt-center placement (interior asteroid scatter is kept and refactored)
- Hardcoded `StationLocalX, StationLocalY` constants in `entity_station.go`
- Auto-spawn of testsite dungeon in cell (0,0)
- `tierForCellCenter`, `tierForDist` — tier becomes an explicit per-POI manifest field
- `POIPerCellProbability` and any remaining placement-tuning config knobs

### 3.2 Code that survives or moves

- `tierTable` and `rosters` in `internal/game/poi_config.go` stay as lookup tables; indices now come from explicit manifest references.
- `placePOI` rejection-sampling logic — refactored and reused for **NPC scatter inside a POI's spread radius**.
- Belt-interior asteroid scatter (`belts.go`) — refactored to take a placed belt def, run FNV(id) RNG, emit asteroids.
- Dungeon chamber-graph procgen and navgrid stay as-is; the dungeon anchor's `seed` field feeds the existing pipeline.
- All combat / AI / replication / handoff / persistence infrastructure is untouched.

## 4. Data Model

### 4.1 File layout

At the repo root, git-tracked, writable at runtime:

```text
world/
├── stations.json
├── pois.json
├── dungeons.json
├── belts.json
├── decorations.json
└── regions.json
```

One file per entity type. No spatial bucketing — files exist to scope git diffs by entity type and reduce write contention, not to represent geography.

Server flag `--world-dir=world` (default), so test setups can override.

### 4.2 Schemas

**`world/stations.json`:**

```json
{
  "version": 1,
  "stations": [
    {"id": "trade-0", "name": "Trade Station", "world_pos": [8100, 8100], "radius": 100}
  ]
}
```

**`world/pois.json`:**

```json
{
  "version": 1,
  "pois": [
    {"id": "0_0_poi_1", "world_pos": [2000, 3000], "type": "combat",
     "tier": 1, "roster": "StarterArena", "spread_radius": 250}
  ]
}
```

**`world/dungeons.json`:**

```json
{
  "version": 1,
  "dungeons": [
    {"id": "testsite", "name": "Test Site", "world_pos": [3500, 5000], "seed": 12345}
  ]
}
```

**`world/belts.json`:**

```json
{
  "version": 1,
  "belts": [
    {"id": "0_0_belt_1", "world_pos": [1200, 1200], "radius": 80, "density": 1.0}
  ]
}
```

**`world/decorations.json`:**

```json
{
  "version": 1,
  "decorations": [
    {"id": "wreck_01", "world_pos": [6000, 6000], "kind": "wreck", "variant": "destroyer-01"}
  ]
}
```

**`world/regions.json`:**

```json
{
  "version": 1,
  "regions": [
    {"id": "inner-sanctum", "name": "Inner Sanctum", "kind": "safe",
     "shape": "annulus", "center": [0, 0], "inner_r": 0, "outer_r": 16384},
    {"id": "north-claim", "name": "Northern Claim", "kind": "faction",
     "faction": "Solaris", "shape": "polygon",
     "vertices": [[0, -32768], [16384, -32768], [16384, -16384], [0, -16384]]}
  ]
}
```

### 4.3 Go types — `pkg/world/`

```go
type Snapshot struct {
    Stations    *Stations
    POIs        *POIs
    Dungeons    *Dungeons
    Belts       *Belts
    Decorations *Decorations
    Regions     *Regions
}

type Stations struct {
    Version  int       `json:"version"`
    Stations []Station `json:"stations"`
}

type Station struct {
    ID       string     `json:"id"`
    Name     string     `json:"name"`
    WorldPos [2]float32 `json:"world_pos"`
    Radius   float32    `json:"radius,omitempty"`
}

type POIs struct {
    Version int   `json:"version"`
    POIs    []POI `json:"pois"`
}

type POI struct {
    ID           string     `json:"id"`
    WorldPos     [2]float32 `json:"world_pos"`
    Type         string     `json:"type"`           // "combat" for v1
    Tier         uint8      `json:"tier"`           // 1..3
    Roster       string     `json:"roster"`         // RosterDef name lookup
    SpreadRadius float32    `json:"spread_radius,omitempty"`
}

type Dungeon struct {
    ID       string     `json:"id"`
    Name     string     `json:"name"`
    WorldPos [2]float32 `json:"world_pos"`
    Seed     int64      `json:"seed,omitempty"`
}

type Belt struct {
    ID       string     `json:"id"`
    WorldPos [2]float32 `json:"world_pos"`
    Radius   float32    `json:"radius"`
    Density  float32    `json:"density"`
}

type Decoration struct {
    ID       string     `json:"id"`
    WorldPos [2]float32 `json:"world_pos"`
    Kind     string     `json:"kind"`
    Variant  string     `json:"variant,omitempty"`
}

type Region struct {
    ID       string       `json:"id"`
    Name     string       `json:"name"`
    Kind     string       `json:"kind"`
    Shape    string       `json:"shape"`              // "polygon" | "annulus"
    Vertices [][2]float32 `json:"vertices,omitempty"` // polygon
    Center   [2]float32   `json:"center,omitempty"`   // annulus
    InnerR   float32      `json:"inner_r,omitempty"`
    OuterR   float32      `json:"outer_r,omitempty"`
    Faction  string       `json:"faction,omitempty"`
}
```

`omitempty` keeps written JSON minimal (no `"radius": 0` noise). All array fields are non-nil slices on read (empty when absent) so caller code never needs nil checks.

### 4.4 Repository

```go
type Repository interface {
    LoadAll() (*Snapshot, error)

    SaveStations(*Stations) error
    SavePOIs(*POIs) error
    SaveDungeons(*Dungeons) error
    SaveBelts(*Belts) error
    SaveDecorations(*Decorations) error
    SaveRegions(*Regions) error

    AddPOI(POI) error
    UpdatePOI(id string, mut func(*POI)) error
    DeletePOI(id string) error
    // same shape for the other entity types
}
```

**JSON impl** (`pkg/world/jsonrepo/`):

- One `sync.Mutex` per file, held during read-mutate-write.
- Atomic write: write to `<file>.tmp` → fsync → rename. Crash-safe.
- In-memory `Snapshot` cache; invalidated on save and on `world.reload`.
- Pretty-printed JSON (2-space indent) so git diffs are readable.

### 4.5 Bucketing for spawn

```go
type CellBucket struct {
    Stations    []Station
    POIs        []POI
    Dungeons    []Dungeon
    Belts       []Belt
    Decorations []Decoration
}

func (s *Snapshot) BucketByCell(cellSize float32) map[CellID]*CellBucket
```

This is the *only* place in the codebase that translates world coords → (cell, local). Called once at boot to populate per-cell buckets; called again only when cell size changes (rare, expensive op out of scope).

Regions are world-coord polygons and are NOT bucketed — they live globally and any system that cares about region membership does point-in-polygon tests with world coords.

## 5. Server

### 5.1 cmdsys verbs

All verbs live in `internal/game/commands/world.go`. Capability tag `world.edit`. Default `admin` operator's `*.*` grant covers it.

| Verb           | Args                                     | Effect                                                           |
|----------------|------------------------------------------|------------------------------------------------------------------|
| `world.list`   | `{type?: string, near?: [wx,wy,r]?}`     | enumerate placed entities (table-rendered for console)           |
| `world.place`  | `{type, world_pos, props}`               | write JSON + spawn in target cell + publish `world.changed`      |
| `world.move`   | `{id, world_pos}`                        | write JSON + reposition (same cell) or despawn+respawn (across)  |
| `world.update` | `{id, props}` (map[string]any)           | write JSON + despawn+respawn entity                              |
| `world.delete` | `{id}`                                   | write JSON + despawn (children of POIs/dungeons go with parent)  |
| `world.reload` | `{}`                                     | re-read JSON from disk + diff against snapshot + apply add/rm    |
| `world.export` | `{}` (gated by `--world-allow-export`)   | safety net: serialize in-memory snapshot back to JSON files      |

### 5.2 RouteKind and handler shape

Verbs run on the **coordinator process** which owns the world directory and the canonical in-memory `Snapshot`. For solo `--mode=all` this is the only process. For distributed mode (deferred), the coordinator dispatches the spawn side-effect to the cell-owning host via existing `RunOnHost` machinery.

Illustrative handler for `world.place` of a POI:

```go
func handlePlace(ctx context.Context, env *cmdsys.Env, args PlaceArgs) (PlaceResult, error) {
    if err := validate(args); err != nil { return PlaceResult{}, err }

    id := args.ID
    if id == "" { id = nextID(args.Type, args.WorldPos) }
    poi := world.POI{ ID: id, WorldPos: args.WorldPos, Type: "combat", ... }

    if err := env.WorldRepo.AddPOI(poi); err != nil { return ..., err }

    cellID, local := coords.WorldToCell(poi.WorldPos[0], poi.WorldPos[1])
    target := env.Coord.Cells[cellID]                                // local cell lookup
    netID, err := cmdsys.OnLoop(ctx, target.Engine, func() (uint32, error) {
        return game.SpawnPOI(target.Stage, local, poi).NetID(), nil
    })
    if err != nil { return ..., err }

    env.Topics.Publish("world.changed", WorldChangeEvent{Op: "place", Type: "poi", ID: poi.ID, ...})
    return PlaceResult{ID: poi.ID, NetID: netID}, nil
}
```

ID generation: `<cell_x>_<cell_y>_<type>_<n>` where `n` is the next unused integer suffix in the cell's bucket. Designer-renameable.

### 5.3 Spawn pipeline at cell boot

A new `OnCellReady(cell *mmokit.Cell)` lifecycle hook on `mmokit.Process` fires once per cell after construction, before any player can enter. The game registers:

```go
coord.OnCellReady(func(cell *mmokit.Cell) {
    if cell.Stage.FromSplit() { return }  // split-created cell, entities transferred from parent
    bucket := worldSnapshot.BucketForCell(cell.ID, coords.CellSize())
    for _, st := range bucket.Stations    { game.SpawnStation(cell.Stage, st) }
    for _, p  := range bucket.POIs        { game.SpawnPOI(cell.Stage, p) }
    for _, d  := range bucket.Dungeons    { game.SpawnDungeon(cell.Stage, d) }
    for _, b  := range bucket.Belts       { game.SpawnBelt(cell.Stage, b) }
    for _, dc := range bucket.Decorations { game.SpawnDecoration(cell.Stage, dc) }
})
```

`OnCellReady` replaces today's implicit "first OnEnter triggers procgen" pattern. Cell-boot becomes deterministic and explicit.

### 5.4 Interior procgen — seeded by entity id

Per-entity spawn functions consume the placed def and run interior procgen with a stable seed derived from the entity's id:

```go
func SpawnPOI(stage *mmokit.Stage, local Pos, def world.POI) mmokit.Entity {
    e := stage.Spawn(/* POI components from def */)
    rng := rand.New(rand.NewPCG(fnv64(def.ID), 0))
    roster := game.RosterForName(def.Roster)
    scatterNPCsInRadius(stage, local, rng, def.SpreadRadius, roster)
    return e
}
```

The key invariant: **outer placement is manifest. Inner procgen is stable per-id seed.** Renaming a POI's id changes its NPC layout. Moving a POI does not.

### 5.5 Mutation classification

| Operation                      | Live action                                              |
|--------------------------------|----------------------------------------------------------|
| `move` within same cell        | mutate `Position` component in place                     |
| `move` across cells            | despawn at source + respawn at destination               |
| `update` non-position props    | despawn + respawn (cheap for static entities)            |
| `delete`                       | despawn entity + all children (roster, belt asteroids)   |

No live handoff machinery is engaged — these are static entities, and the simpler model is correct.

### 5.6 Concurrency

- `pkg/world` uses one `sync.Mutex` per file (six mutexes for v1).
- ECS mutations go through `engine.RunOnLoop` (standard pattern).
- Two designers editing the same entity → last write wins. SSE notifies both clients; the inspector shows "edited externally" if local edits are still pending.

### 5.7 Audit + SSE

Every successful `world.*` verb writes one audit-log entry through cmdsys's default path (operator name, verb, args, result). Every mutation publishes to a new SSE topic `world.changed` with `{op, type, id, world_pos, ...}`. The editor SPA subscribes via `/admin/api/stream`.

## 6. Editor UI

### 6.1 Route

`web-admin/src/routes/world-editor.svelte`. New sidebar entry between "Events" and "Audit." Capability gate: requires `world.edit` grant; non-authorized operators see a "no access" placeholder.

### 6.2 Layout

Three-pane shell, LDtk-inspired:

```text
┌─ TopBar ─────────────────────────────────────────────────────┐
│ admin · world editor   ● live   3 files clean   export reload │
├─────────┬────────────────────────────────────┬───────────────┤
│ PALETTE │           WORLD CANVAS             │   INSPECTOR   │
│ (200px) │           (flex, pan/zoom)         │   (260px)     │
│ + layers│                                    │               │
├─────────┴────────────────────────────────────┴───────────────┤
│ StatusBar: tool · cursor (wx, wy) · zoom% · entity counts    │
└──────────────────────────────────────────────────────────────┘
```

### 6.3 Palette and layer toggles

Seven tools as mutually-exclusive modes:

| Hotkey | Tool       | Effect                                             |
|--------|------------|----------------------------------------------------|
| `V`    | Select     | Default. Click → select. Drag → move.              |
| `1`    | Station    | Click empty space → place station + open inspector |
| `2`    | POI        | Click empty space → place POI + open inspector     |
| `3`    | Dungeon    | Click empty space → place dungeon anchor           |
| `4`    | Belt       | Click empty space → place belt                     |
| `5`    | Region     | Multi-click polygon mode; `Shift` for annulus      |
| `6`    | Decoration | Click empty space → place decoration               |

Layer toggles (independent visibility): cell boundaries, tier rings, grid, stations, POIs, dungeons, belts, regions, decorations.

### 6.4 Canvas

A new `WorldCanvas.svelte` component (NOT a fork of `CellMap.svelte` — the interaction state diverges enough to justify a sibling). Shares low-level draw primitives (grid, pan/zoom math, viewport culling) extracted into a small util module.

Interactions:

- **Shift-drag** canvas: pan
- **Wheel**: zoom around cursor, range 0.05× to 10×
- **Click empty space (place mode)**: spawn at cursor world pos
- **Click entity (select mode)**: select + open inspector
- **Drag selected entity**: ghost preview; commits `world.move` on release
- **Right-click entity**: context menu (delete / duplicate / rename id)
- **Esc**: deselect / exit place mode
- **⌘Z / ⌘⇧Z**: undo / redo (session-local stack)

Cursor world coords always shown in status bar. Cell boundaries are a layer overlay, never authoritative — clicks always resolve to world coords.

### 6.5 Inspector

Right pane. Per-type form:

- **Station:** id, world_pos (x and y numeric inputs), name, radius
- **POI:** id, world_pos, type (dropdown "combat"), tier (1/2/3 segmented control), roster (dropdown of roster names from server), spread_radius
- **Dungeon:** id, world_pos, name, seed (number + regen button)
- **Belt:** id, world_pos, radius, density slider
- **Region:** id, name, kind, shape, vertices (editable table) or center/inner_r/outer_r
- **Decoration:** id, world_pos, kind (dropdown), variant

Footer buttons: `[duplicate]` `[delete]`.

**Save behavior: explicit Apply button.** Edits buffer locally in the inspector until the operator clicks `Apply`. Dirty fields are visually marked (subtle border highlight). Discard button reverts to last server state. Rationale: avoids accidental edits to placement data; trade-off is one extra click per change.

### 6.6 Region tool flow

Click in palette → polygon mode. Click in canvas adds vertex; preview line follows cursor. Click first vertex (or press Enter) closes polygon and opens inspector. Hold `Shift` and click in palette → annulus mode: first click center, drag to set outer radius, drag again for inner radius.

### 6.7 Undo / redo

Session-local stack in the SPA. Each action records the inverse cmdsys verb:

- `place` ↔ `delete`
- `move A→B` ↔ `move B→A`
- `update {tier: 1 → 2}` ↔ `update {tier: 2 → 1}`

Undo dispatches the inverse via the same cmdsys path — no special server primitive. Stack cleared on page navigation.

### 6.8 Live updates

SPA subscribes to SSE topic `world.changed`. Other admins' edits appear in real time. If you're editing an entity that another admin modifies, the inspector shows an "edited externally — refresh?" banner. Local edits are NOT silently overwritten.

## 7. Migration

**Day one: empty world.** No bootstrap from procgen. The first designer task is `world.place station 8100 8100 --name "Trade Station"` to recreate today's hardcoded station.

The deleted procgen-placement code is removed in the same PR as the editor lands; there is no transition period where both run. The editor must be functional before procgen can be removed.

For load testing / bots: hand-place a small "test world" of POIs and belts (likely committed under `world/` in the same PR), so `just dev` produces a runnable world after the change.

## 8. Open Questions / Deferred

- **Distributed mode shared FS.** v1 assumes single-process or single-coordinator-host shared filesystem. Multi-coordinator-host writes will need a single-writer rule. Out of scope.
- **File-system watcher.** Deferred. v1 ships `world.reload` for manual external-edit reconciliation.
- **Embedded production binaries.** A future build step can `//go:embed` the `world/` tree for shipping read-only worlds. Out of scope.
- **Schema versioning + migrations.** `version` field reserved; v1 ships `version: 1` and never bumps.
- **Region rules enforcement.** Regions are annotations only in v1. PvP/safe-zone gameplay rules come later.
- **Region cell-membership cache.** Naive point-in-polygon on every relevant query is fine until counts grow.
- **Multi-select edits.** Future. v1 is single-entity inspector.
- **Decoration asset pipeline.** v1 supports placement of decorations with a `kind` string; client rendering of decoration kinds is out of scope.
- **Player tools for placing things in-world.** Not a designer tool, but eventually players may place beacons / structures. Reuses the same JSON path with a different repository scope. Out of scope.

## 9. Testing

- `pkg/world/jsonrepo` unit tests: atomic write under fault injection (kill between tmp and rename → file is intact); per-file mutex serialization.
- `pkg/world/snapshot_test.go`: `BucketByCell` correctness across cell-size changes (entities re-bucket without data loss).
- `internal/game/commands/world_test.go`: each verb's happy path + validation failures (bad tier, unknown roster, malformed coords).
- Integration: spin up a one-cell test world, call `world.place` → `world.move` → `world.delete` → assert ECS state and JSON file state match.
- `web-admin` smoke: `bun run build` succeeds and the new route renders without console errors.
- Schema validation at boot: malformed `world/*.json` files fail loudly with a line-numbered error, not a silent half-load.
