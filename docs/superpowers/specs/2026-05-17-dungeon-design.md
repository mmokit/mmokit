# Dungeon POI — Sprawling Asteroid Cave System

**Status:** Design
**Date:** 2026-05-17
**Branch target:** new feature branch off `pve-v2`

## 1. Overview

The dungeon is a new PvE content type: a large, walled, procedurally generated cave system carved into a "giant asteroid" in the open world. Players physically fly into the asteroid through one of several cave mouths, navigate corridors past mob packs, and clear chambers containing bosses (a mix of solo-tier and a single group-tier main boss). Each chamber drops its own loot chest and respawns on its own cooldown — there is no global "dungeon cleared" state.

The feature ships with two cross-cutting engine additions that future PvE content will reuse:

1. **LOS raycasting** — `pkg/spatial.Grid.Raycast` becomes a first-class primitive used by NPC AI, abilities, and target locking.
2. **Real pathfinding** — `pkg/pathfinding/` package with A* + LOS smoothing, consumed by NPCs to navigate cave corridors.

The v1 testsite is a single dungeon spawned in cell `(0,0)`, ≈4500u left of the trade station. Other cells get nothing for v1.

## 2. Goals & Non-Goals

### Goals

- A first sprawling group-able PvE site — Albion group-dungeon DNA, not the existing single-arena combat POI.
- Mixed solo + group bosses scattered through multiple chambers, with no hard gating on progression order.
- Per-chamber autonomous lifecycle (mob pack → clear → chest → cooldown → respawn), no global dungeon-wide state.
- Real walls — rectangular collider geometry forming corridors and rooms; cave mouths as entry chokepoints.
- LOS raycasting + A* pathfinding as reusable engine primitives, not dungeon-internal hacks.
- Procedural layout — graph + room positions + roster contents generated deterministically from cell seed.

### Non-Goals (v1)

- Per-cell dungeon spawn (v1 spawns only in the station cell as a testsite).
- Multiple dungeon difficulty tiers or biomes.
- Re-rolling the cave layout — layout is generated once per cell and is permanent.
- Group / party loot fairness (loot is contested like the existing combat POI).
- Item drops beyond Flux (chest entities already carry `Inventory`; item-system phase will populate them).
- Dungeon scanner / probe discovery (dungeon auto-reveals on the map).
- Boss enrage timers.
- PvP-specific rules inside the dungeon (existing PvP rules apply).
- Per-player or per-group instancing.
- A separate "warp in" animation for entering the cave mouth.
- Stealth / cloak NPCs.

## 3. World Presence & Spatial Structure

A dungeon is a `KindDungeon` ECS entity at a fixed cell-local position. From outside it renders as a large irregular asteroid silhouette (radius ≈1800u) with **2–3 jagged cave mouths** cut out of the perimeter.

The interior is filled with `KindDungeonWall` entities — static rectangular colliders forming the corridor + chamber geometry. Ships physically cannot pass through walls (same engine collision used today for stations). Cave mouths are gaps in the perimeter wall ring that align to the entry-foyer corridor.

The dungeon is **shared open-world**: any number of players can be inside simultaneously. Loot, mob aggro, and chest pickup are all contested — same model as the existing combat POI.

The entire dungeon fits inside a single cell. Cell handoff / replication / AoI logic is unchanged.

## 4. Procgen Pipeline

All dungeon geometry and contents are derived deterministically from `FNV(cellX, cellY, "dungeon")`. The layout is generated once when the cell first bootstraps and never re-rolls (per-chamber RNG for respawn rosters uses a derivative seed; see §6).

### 4.1 Pipeline stages

1. **Graph generation** — build a tree of `N` chambers where `N` ∈ `[DungeonChamberCountMin, DungeonChamberCountMax]` (default 5–8). Designate one node as the **entry foyer** (the root, placed nearest the asteroid surface on the station-facing side) and one leaf as the **terminal** (the leaf with the longest graph distance from the entry, holding the main boss). From the remaining leaves, randomly select 2–3 to designate as **side-boss chambers**. Non-leaf nodes plus any leftover leaves become **mob-pack chambers**.

2. **Radial layout** — convert the graph to 2D coordinates. The entry foyer is placed near the asteroid perimeter facing the station. The terminal is placed deepest (opposite side or near the center, whichever is farther from the foyer). Other chambers fan out from the trunk. Each chamber gets a position + radius drawn from `[DungeonChamberRadiusMin, DungeonChamberRadiusMax]` (120–240u).

3. **Corridor carving** — for every parent→child edge in the graph, the area between the two chamber centers is a corridor of width `DungeonCorridorWidth` (100u). Corridors are clear space.

4. **Wall materialization (outline-only)** — wall colliders are spawned only *along* the perimeter of chambers and the long edges of corridors. The negative space outside this perimeter is "void" — the spatial grid never queries it because nothing spawns there. Target wall count: < 40 entities per dungeon.

5. **Roster placement** — each chamber gets a roster from `dungeonRosterTable`:
   - **Mob-pack chambers**: 3–6 NPCs from existing archetypes (Brawler / Lancer / Artillery).
   - **Side-boss chambers**: 1 solo-tier boss (elite variant of an existing archetype) + 1–2 escorts.
   - **Terminal chamber**: 1 `BossGuardian` (new archetype) + 2–3 escorts.

### 4.2 Entrance placement

`DungeonEntranceCount` cave mouths (default 3) are cut into the perimeter wall ring. The first cave mouth aligns to the entry foyer's outward-facing corridor; the remaining mouths are placed at evenly-spaced angles around the asteroid and connected by short additional corridors to the trunk.

### 4.3 Reachability invariant

The procgen output must satisfy: every chamber is reachable on a NavGrid path from every entrance. A `dungeon_gen_test.go` test asserts this for ≥1000 random seeds.

## 5. Encounter Design

### 5.1 Mob archetypes

Reuses existing Brawler / Lancer / Artillery / Swarmer archetypes from the current POI combat system. No new mob AI code — they leash, target-switch, and de-escalate via the existing `NPCAISystem`.

### 5.2 Solo-tier mini-bosses

Data-only variants of existing archetypes — no new AI code. Stat multipliers:

- HP: `BossSoloHPMultiplier` × 3.0
- Damage: `BossSoloDmgMultiplier` × 1.5
- Speed: `BossSoloSpeedMultiplier` × 1.2
- One archetype-themed signature ability drawn from the existing ability registry (e.g. a Brawler-tier boss gets the existing `ChargeLine` line-cone; an Artillery-tier boss gets a larger-radius AoE).

Visually distinguished by sprite tint + 1.25× scale until art lands.

### 5.3 Group-tier main boss (`BossGuardian` archetype)

The only genuinely new combat entity. Lives in the terminal chamber.

- HP: `BossMainHPMultiplier` × 10.0
- Damage: `BossMainDmgMultiplier` × 2.0
- Motion policy: `MotionPolicy.Stationary` (parks in the chamber center, rotates to face target, never moves). This is a *design* choice — telegraphed AoE attacks are easier to read against a non-moving caster, which fits a tutorial-tier boss.
- Periodic AoE telegraph: a `LineTelegraph` or `AoEMarker` (both shipped already) fires every ~6s with a 2s windup.
- Add-spawns: at `BossMainAddSpawnThresholds = [0.75, 0.5, 0.25]` HP fractions, summons 2 Swarmers anchored to the same chamber.

### 5.4 Loot

- **Mob-pack chamber clear**: `ChamberMobPackFluxBase` (default 200) in a single `LootCrate` at chamber center.
- **Side-boss chamber clear**: `ChamberSideBossFluxBase` (default 1500) in a single `LootCrate` where the boss died.
- **Terminal chamber clear**: `ChamberTerminalBossFluxBase` (default 6000) in a single `LootCrate` where the main boss died.

All chests are contested (existing `LootCrate` pickup logic — no soulbound, no per-player crates).

### 5.5 Chamber-local aggro

NPC aggro is gated by LOS (§8.1). An NPC in chamber A cannot see — and therefore cannot aggro — a player in chamber B, even if Euclidean distance is inside `AggroRadius`, because the wall colliders block the raycast.

This gives the natural "pull one room at a time" Albion feel without per-chamber state. Players can also peek into a chamber from a corridor and pull individual mobs whose LOS to the corridor mouth is clear.

## 6. Per-Chamber Lifecycle & ECS Model

### 6.1 Entity kinds

```go
KindDungeon       // one per dungeon (the asteroid).
KindDungeonWall   // rect wall colliders.
```

Chambers are **not** ECS entities — they're tracked in a server-side
`map[chamberID]*ChamberState` on the dungeon. Clients infer chamber geometry from NPC positions + visible wall geometry.

### 6.2 Components

```go
// gamecomp.Dungeon — local + replicated. Single component on KindDungeon.
type Dungeon struct {
    Name           string  `net:"initial string"`  // procgen name
    OuterRadius    float32 `net:"initial f32"`     // visual silhouette
    EntranceCount  uint8   `net:"initial u8"`
    Entrances      [3]vec2 `net:"initial"`         // local-coord offsets of cave mouths
    // local-only:
    Seed           uint64
    ChambersCount  uint8
}

// gamecomp.DungeonWall — replicated initial-only.
type DungeonWall struct {
    Width  float32 `net:"initial f32"`
    Height float32 `net:"initial f32"`
    // Position + Rotation come from standard components.
}

// gamecomp.DungeonAnchor — local-only. Replaces and generalizes the existing
// gamecomp.POIAnchor. Attached to every NPC and chest inside a dungeon.
type DungeonAnchor struct {
    DungeonNetID uint32
    ChamberID    uint16
}
```

The existing `POIAnchor` is renamed to `DungeonAnchor` and used by the combat POI system unchanged (a combat POI becomes a 1-chamber-dungeon's degenerate case from the anchor's perspective). No new component needed for the combat POI.

### 6.3 ChamberState (server-side)

```go
type ChamberRole uint8
const (
    ChamberMobPack ChamberRole = iota
    ChamberSideBoss
    ChamberTerminal
)

type ChamberStatus uint8
const (
    ChamberActive ChamberStatus = iota
    ChamberCleared
    ChamberCooldown
)

type ChamberState struct {
    ID            uint16
    Center        vec2
    Radius        float32
    Role          ChamberRole
    Status        ChamberStatus
    RosterDefIdx  uint16
    RespawnCount  uint32
    ClearedAt     int64       // unix nanos
    AliveNetIDs   []uint32    // tracked roster
    LastChestNetID uint32     // for cleanup on respawn
}
```

### 6.4 `DungeonChamberSystem` (new tick system)

Runs after `NPCAISystem`. Per chamber per tick:

1. **Active** — count alive anchored NPCs (using `AliveNetIDs` + `OnEntityRemoved` hook to prune). When zero: transition to `Cleared`.
2. **Cleared** — once: spawn a `LootCrate` at the chamber's "kill location" (terminal/side-boss chambers use the boss's death position; mob-pack chambers use the chamber center). Set `ClearedAt = now`. Record `LastChestNetID`. Transition to `Cooldown`.
3. **Cooldown** — when `now - ClearedAt > ChamberCooldown[role]`:
   - If `LastChestNetID` still exists, despawn it (chest cleanup on respawn — prevents accumulation).
   - Respawn the roster using a per-chamber RNG seeded from `dungeonSeed XOR chamberID XOR RespawnCount` so the *roster* (which archetypes, where placed) re-rolls even though the *layout* is permanent.
   - Increment `RespawnCount`. Transition to `Active`.

### 6.5 Per-chamber cooldowns (`GameConfig`)

- `ChamberCooldownMobPack` = 1800s (30 min)
- `ChamberCooldownSideBoss` = 2700s (45 min)
- `ChamberCooldownTerminal` = 3600s (60 min)

### 6.6 Boot-time spawn

`dungeon_gen.go` runs once during cell bootstrap (alongside `belts.go` / `poi_gen.go`). For v1, only cell `(0,0)` returns a non-empty result; other cells return nil. The dungeon is spawned at:

```text
position = (StationLocalX + DungeonTestsiteOffsetX, StationLocalY + DungeonTestsiteOffsetY)
        = (8100 - 4500, 8100 + 0)
        = (3600, 8100)
```

The `OnEntityRemoved` hook prunes dead NPCs from their chamber's `AliveNetIDs`. The dungeon entity itself never despawns.

### 6.7 Anchor unification rationale

Renaming `POIAnchor` → `DungeonAnchor` is the YAGNI-aligned path: one component captures "anchored to a roster-bearing structure," combat POIs (the existing 2026-05-12 design) become a degenerate single-chamber case, and there's no parallel-component drift to maintain. All call sites that read `POIAnchor.POINetID` rename uniformly to `DungeonAnchor.DungeonNetID`; the field semantic is unchanged.

## 7. Wire Format & Client

### 7.1 Wire types

- `KindDungeon` — entity replication. Initial fields: `Name`, `OuterRadius`, `EntranceCount`, `Entrances[3]`. Live fields: `Position` only (essentially static — `Position` is sent once and never changes, but flows through standard channels).
- `KindDungeonWall` — entity replication, all fields `initial`: `Width`, `Height`, `Rotation`. Static after spawn.
- No new server-event codes. Chest spawns + boss kills flow through existing entity-add/remove replication.

### 7.2 Replication budget

Fully-populated dungeon: ~40 walls + ~25–30 NPCs (across all chambers) + 0–8 chests = ~75 entities + the dungeon entity. AoI uses the existing default radius — corridors with collider walls naturally limit visible entity count from any one point. Pop-in across chamber boundaries is acceptable for v1; if it becomes a UX problem, expand AoI to a dungeon-aware radius while a ship is inside the asteroid silhouette.

### 7.3 Web client (`web-pixi`)

- **Asteroid silhouette** — `Dungeon` entity renders as a large irregular textured asteroid. v1: a circle with `Entrances` cut as masks; proper hand-painted sprite later.
- **Walls** — `DungeonWall` entities render as rock-textured oriented rectangles. Rendered once on entry-AoI, never animated.
- **Inside-the-dungeon visual** — client-side: when the player ship's position is inside the dungeon's `OuterRadius`, darken the world outside the silhouette + add a subtle lit-from-within glow inside. Pure rendering trick — no server changes.
- **Map marker** — `Dungeon` entity appears on the map view as a dungeon icon labeled with `Name`.
- **Mob/boss sprites** — solo-tier elites: existing sprites with tint + 1.25× scale. Main boss: larger, distinct tint. Real art deferred.

### 7.4 Admin / cmdsys verbs

All four follow the existing cmdsys pattern (typed Args/Result, capability gate, `cmdsys.OnLoop` for ECS access, audit-logged, surfaced in the admin SPA automatically):

- `dungeon.list` — all dungeons across the cluster, with per-chamber status + cooldown remaining.
- `dungeon.respawn <netID> [chamberID]` — force-respawn a specific chamber, or all chambers if `chamberID` is omitted.
- `dungeon.regenerate <netID>` — force a fresh procgen layout roll (debug/test only — normally never re-rolls).
- `dungeon.spawn <cellID>` — spawn a dungeon in a non-default cell (test/debug).

## 8. Engine Additions (LOS + Pathfinding)

These are `pkg/`-level primitives that the dungeon is the first consumer of. They are not dungeon-specific and will be reused by future PvE features (asteroid cover, station fire-zones, smart targeting, etc.).

### 8.1 LOS Raycast — `pkg/spatial/grid.go`

```go
// Raycast walks the spatial grid along the ray (from→to) using DDA and
// tests every collider whose Layer&layerMask != 0. Returns the nearest hit.
// hitPoint is the precise intersection point on the collider surface.
func (g *Grid) Raycast(from, to vec2, layerMask uint8) (e Entity, hitPoint vec2, dist float32, ok bool)
```

**Layer assignments:**

```go
const (
    LayerStatic  uint8 = 1 << iota  // walls, stations — block movement/sight/locks/shots
    LayerProp                       // asteroids — block shots, do NOT block sight or locks
    LayerEntity                     // ships, NPCs — do not block anything
)
```

Callers pick the mask appropriate to the surface:

- Sight / lock LOS: `LayerStatic`
- Projectile / beam collision: `LayerStatic | LayerProp`

**Combat surfaces using `Raycast`:**

| Surface | Mask | Behavior |
| --- | --- | --- |
| `NPCAISystem.Acquire` | `LayerStatic` | Skip target candidate if blocked. |
| `NPCAISystem.Engage` (re-check) | `LayerStatic` | Every 500ms; sustained loss > 3s → drop target via existing de-escalation. |
| `AbilitySystem` beam/hitscan | `LayerStatic \| LayerProp` | Beam visually clips at hitPoint; no damage past contact. |
| Selection auto-clear | `LayerStatic` | Per-player check each tick: if `Selection.EntityNetID != 0` and LOS to that entity has been blocked for ≥ 1s, clear `EntityNetID`. Mining beams (the main reader of Selection) stop firing as a consequence. |

### 8.2 Pathfinding — `pkg/pathfinding/`

```go
// NavGrid is a rasterized walkability bitmap built once at structure-spawn
// time (e.g. dungeon procgen).
type NavGrid struct {
    Origin     vec2     // world-space origin of cell (0,0)
    CellSize   float32  // 30u recommended for the dungeon
    Width      int
    Height     int
    Walkable   []bool   // row-major, len = Width*Height
}

// AStar returns a list of world-space waypoints from start to goal.
// Returns nil if unreachable.
// Octile heuristic, 8-connected, diagonal cost = sqrt(2) * CellSize.
func AStar(g *NavGrid, start, goal vec2) []vec2

// SmoothLOS post-processes a waypoint list by collapsing runs of waypoints
// reachable from an earlier waypoint via direct LOS against the supplied
// spatial grid. Produces any-angle paths instead of grid-aligned zigzag.
func SmoothLOS(waypoints []vec2, grid *spatial.Grid, layerMask uint8) []vec2
```

**NavGrid construction at dungeon procgen:**

After wall materialization, the dungeon rasterizes its walkable area: every `CellSize`×`CellSize` cell of the bounding region is `Walkable = true` iff its center is inside the asteroid silhouette AND not inside any wall collider. ~60×60 grid for an 1800u-radius dungeon at 30u resolution.

**Per-NPC path cache:**

A new `Pathing` component (local-only) on every NPC that uses `MotionPolicy.Pathfind`:

```go
type Pathing struct {
    Waypoints       []vec2
    CurrentIdx      int
    TargetSnapshot  vec2     // pos of target when path was last computed
    RepathAt        float32  // stage-time deadline
}
```

Repath when:

- Target moves > 50u from `TargetSnapshot`, OR
- `stageTime > RepathAt` (default repath every 1.5s), OR
- NPC reaches the final waypoint with target still > `PreferredRange`.

**Tick budget:**

~30 NPCs × ~1 path / 1.5s = ~20 paths/sec. A* on a 60×60 grid runs sub-millisecond per call (conservatively ~500µs). Total ~10ms/sec = ~0.5ms / 20Hz tick average. Comfortably inside budget. If usage grows, pathfinding calls can be queued through the existing `RunOnLoop` mechanism to budget across ticks.

### 8.3 New motion policies

Two new entries to `MotionPolicy` (joining the existing Charge / HoldRange / Encircle from the combat POI design):

- **`MotionPolicy.Pathfind`** — follows a NavGrid-derived waypoint list (described below).
- **`MotionPolicy.Stationary`** — `vel = 0`. Rotation tracks target at archetype turn-rate. Used by the main boss; reusable for any future stationary-caster archetype.

**`NPCAISystem.Engage` motion phase under `Pathfind`:**

1. If `Pathing.Waypoints` is empty or stale → invoke `AStar` (via the dungeon's NavGrid) + `SmoothLOS`. Update `Pathing`.
2. If current waypoint reached (within `CellSize/2`) → advance `CurrentIdx`.
3. If `CurrentIdx >= len(Waypoints)` AND target is within `PreferredRange + tolerance` AND LOS clear → switch to the archetype's natural close-range policy (Charge / HoldRange / Encircle).
4. Otherwise: set velocity toward the current waypoint at `MaxSpeed`, rotate at archetype `TurnRate`.

Dungeon-spawned NPCs use `Pathfind` by default. When target and NPC are in the same chamber with no wall between (LOS clear), they automatically fall back to their archetype-specific close-range policy.

### 8.4 Implications for §3 / §5 / earlier risks

- The `Stationary` main-boss policy is a *design* choice (telegraph-friendly tutorial fight), not a pathfinding workaround.
- "NPCs pile up on walls" risk → **resolved** by pathfinding.
- "Chamber-local aggro" risk → **resolved** by LOS (raycast blocked by wall).
- The leash mechanic stays — leash is the "give up chasing" rule, not a navigation crutch. With pathfinding, `LeashRadius` can bump to 2500u (default was 1500u, sized around naive movement).

### 8.5 Future consumers

- **Asteroid-field cover combat** — flip asteroids to `LayerProp` and they block shots but not sight.
- **Station no-fire zones / safety lines** — stations already carry `LayerStatic`; the same primitive trivially supports "you can't shoot into the station ring."
- **Any future POI / structure that builds a NavGrid** — pathfinding works for free.
- **Player-cast LOS-aware abilities** — channelled beams, smart targeting, etc.

## 9. Tunables (`GameConfig`)

```text
Dungeon procgen:
  DungeonAsteroidRadius              = 1800
  DungeonChamberCountMin             = 5
  DungeonChamberCountMax             = 8
  DungeonChamberRadiusMin            = 120
  DungeonChamberRadiusMax            = 240
  DungeonCorridorWidth               = 100
  DungeonEntranceCount               = 3
  DungeonWallThickness               = 30
  DungeonTestsiteCellX, CellY        = 0, 0
  DungeonTestsiteOffsetX, OffsetY    = -4500, 0    // relative to station

Per-chamber cooldowns:
  ChamberCooldownMobPack             = 1800s
  ChamberCooldownSideBoss            = 2700s
  ChamberCooldownTerminal            = 3600s

Boss stats:
  BossSoloHPMultiplier               = 3.0
  BossSoloDmgMultiplier              = 1.5
  BossSoloSpeedMultiplier            = 1.2
  BossMainHPMultiplier               = 10.0
  BossMainDmgMultiplier              = 2.0
  BossMainAddSpawnThresholds         = [0.75, 0.5, 0.25]

Loot:
  ChamberMobPackFluxBase             = 200
  ChamberSideBossFluxBase            = 1500
  ChamberTerminalBossFluxBase        = 6000

LOS / pathfinding:
  AILosRecheckIntervalMs             = 500
  AILosLossDropSec                   = 3.0
  LockLosLossBreakSec                = 1.0
  NavGridCellSize                    = 30
  PathRepathIntervalSec              = 1.5
  PathRepathTargetMovedThreshold     = 50

Combat (updated):
  AggroRadius                        = 800     (unchanged; now LOS-gated)
  LeashRadius                        = 2500    (bumped from 1500)
```

All live-tunable via the existing `config set` console / admin verb.

## 10. Logging

New categories:

- `dungeon` — dungeon spawn, chamber state transitions, chest spawn/cleanup, roster respawn.
- `dungeonGen` — procgen graph + layout output (verbose, off by default).
- `los` — LOS raycast hits/misses on AI + lock surfaces (verbose, off by default).
- `pathfind` — A* invocations + path lengths (verbose, off by default).

Existing `ai` covers NPC state transitions. Selection auto-clear events log under the new `los` category.

All combat-relevant events log player + NPC NetIDs and quantities per CLAUDE.md convention.

## 11. Testing

### 11.1 Unit tests

- `pkg/spatial/raycast_test.go` — ray hits/misses rects + circles at various angles; layer-mask filtering correctness; empty grid returns no hit.
- `pkg/pathfinding/astar_test.go` — simple grid: start→goal optimal cost; unreachable returns nil; obstacle avoidance; corridor traversal.
- `pkg/pathfinding/smooth_test.go` — LOS smoothing collapses straight-line segments; preserves required waypoints around corners; idempotent on already-smooth paths.
- `internal/game/dungeon_gen_test.go` — deterministic graph from seed; chamber count in range; entry + terminal distinct; no chamber overlap; corridor reachability from every entrance to every chamber (≥1000 seeds).
- `internal/game/dungeon_navgrid_test.go` — NavGrid built from a procgen dungeon: every chamber reachable from every entrance on the grid; corridor centerlines walkable; wall cells correctly blocked.
- `internal/game/dungeon_chamber_lifecycle_test.go` — Active → Cleared → Cooldown → Active per role; chest spawn on clear; old chest cleanup on respawn; roster re-roll on respawn.
- `internal/game/dungeon_wall_collision_test.go` — ship velocity blocked by rect walls; corridor centerline traversable end-to-end.
- `internal/game/system_npc_ai_pathfind_test.go` — NPC in chamber A correctly paths through corridor to player in chamber B; falls back to archetype policy on entering same chamber; drops target on LOS loss > 3s.
- `internal/game/dungeon_anchor_unification_test.go` — combat POI still works after `POIAnchor` → `DungeonAnchor` rename.

### 11.2 Integration (bot-driven)

A new headless test boots cell `(0,0)`, spawns a dungeon, flies a bot from the station to the dungeon, traverses one corridor, kills a mob pack, and asserts:

- Chest entity exists at chamber center after roster death.
- Chamber transitions through `Cleared` → `Cooldown`.
- After `ChamberCooldownMobPack` elapses (test config: 5s), roster respawns.
- Old chest is despawned on respawn.

## 12. Implementation Order

Suggested phasing for the implementation plan (full breakdown handled by the writing-plans skill afterwards):

1. **Engine primitives** — LOS raycast in `pkg/spatial`, A* + SmoothLOS in new `pkg/pathfinding/`. Pure-library code with unit tests; no game wiring yet.
2. **LOS hookup into existing combat** — wire `pkg/spatial.Raycast` into `NPCAISystem.Acquire`/`Engage`, `AbilitySystem` beam/hitscan, and `TargetLockSystem`. Add `LayerStatic` to stations and existing `KindAsteroid` entities (asteroids get `LayerProp`). **Note: this is a behavior change for stations** — they now block locks and beams. Validate with existing single-cell smoke + combat tests before the dungeon depends on it.
3. **Anchor rename** — `POIAnchor` → `DungeonAnchor`, update combat POI call sites. Pure rename + field semantic preservation. Validates the unification path before dungeon work begins.
4. **Wall + dungeon entity scaffolding** — `KindDungeon`, `KindDungeonWall`, basic spawn function that places one dungeon at the testsite position with a hand-coded 3-chamber layout (no procgen yet). Ship rendering on the client.
5. **Procgen** — `dungeon_gen.go`: graph generation + radial layout + outline wall placement + entrance cutting. Reachability tests pass.
6. **Chamber lifecycle** — `DungeonChamberSystem` + `ChamberState`. Roster spawning + respawn + chest cleanup.
7. **NavGrid + pathfinding integration** — rasterize per-dungeon NavGrid at spawn time, add `MotionPolicy.Pathfind` and `MotionPolicy.Stationary` to `NPCAISystem`, wire dungeon NPCs to use `Pathfind` by default.
8. **Boss content** — solo-tier elite variants (data only, no new code), `BossGuardian` archetype using `Stationary` policy + add-spawn mechanic. Wire telegraph attacks.
9. **Admin / cmdsys** — `dungeon.list/respawn/regenerate/spawn` verbs.
10. **Client polish** — asteroid silhouette mask + dungeon-interior visual, map marker, boss sprite distinguishing.

Each step is independently shippable.

## 13. Deferred / v2

- Per-cell dungeon spawn (every cell gets one or more dungeons).
- Multiple difficulty tiers / biomes.
- Layout regeneration mechanics (daily? after full clear?).
- Group / party loot fairness.
- Item drops beyond Flux.
- Dungeon scanner / probe discovery.
- Boss enrage timers.
- Per-player completion tracking / dungeon-progression UI.
- Stealth / cloak NPCs.
- Player-cast LOS-aware abilities (smart targeting, channelled beams) — engine primitive exists, no UX yet.
- Cave-mouth "warp in" animation.

## 14. Open Questions / Risks

- **Procgen output looking same-y on repeated runs** — with a permanent layout per cell and only one dungeon for v1, this is academic. When per-cell dungeons land, will need a name-pool + visual-variation pass to avoid every cave looking identical.
- **NavGrid memory at scale** — one 60×60 NavGrid is trivial; 100 of them (per-cell dungeons) is still trivial. Flag for revisit only if dungeon counts grow beyond hundreds.
- **Beam visuals on LOS clipping** — the client needs to know where to stop drawing a beam. The server-side hit point can be included in the beam-fire event payload; the client renders the beam from origin to that point instead of `Range`-extent. Small wire-protocol addition during step 1.
- **Selection auto-clear UX** — clearing `Selection.EntityNetID` after 1s of LOS loss is mainly visible when mining (mining beams stop firing). Players who don't mine inside dungeons won't notice. Tune threshold if it feels off in playtest.
- **Stations gain `LayerStatic`** — once §12 step 2 lands, stations block sight, locks, and beam damage in addition to the movement they already block. This is a global combat change, not dungeon-scoped. Reasonable as a feature (you can't shoot through a station), but worth a playtest pass with the existing PvE content to confirm nothing breaks.
- **`KindAsteroid` `LayerProp` assignment** — same step. Asteroids will block projectiles + beams but not locks/sight. Players will be able to hide projectile-shooters behind asteroids; mining cycles are unaffected (mining laser is a direct beam to a locked asteroid that the player is in LOS of by construction). Flag for playtest.
- **Naming for procgen dungeons** — Section 6.2 lists `Name` as a procgen field but doesn't specify the generator. Pick a name-pool table in `dungeon_config.go` (e.g. `["Stillveil Hollow", "Ashbore Cradle", "Verdigris Maw", …]`) and hash-select on dungeon seed. Bikeshed-worthy but cheap.
