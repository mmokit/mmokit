# Dungeon POI Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the sprawling-asteroid dungeon POI specced in [docs/superpowers/specs/2026-05-17-dungeon-design.md](../specs/2026-05-17-dungeon-design.md), plus the LOS + pathfinding engine primitives it depends on.

**Architecture:** Engine first — add a DDA raycast to `pkg/spatial.HashGrid` and an A* pathfinder in a new `pkg/pathfinding/` package, then hook them into existing NPC AI / ability / selection systems. Once primitives are live, scaffold a new `KindDungeon` + `KindDungeonWall` entity pair, procgen-generate the cave geometry deterministically per cell, and run an autonomous per-chamber lifecycle (mob pack → chest → cooldown → respawn) entirely server-side. Bosses are mostly data-only elite variants of existing archetypes, with one new `BossGuardian` archetype for the terminal chamber.

**Tech Stack:** Go (server, `pkg/` engine + `internal/game`), TypeScript (`web-pixi` client), PostgreSQL not touched (no new persistent state). Existing `mmokit` facade + ECS (Ark v0.7.1) + cmdsys + sdkgen pipelines.

---

## Codebase Adaptation Notes (deviations from the spec)

The spec was approved before two recent landings on `pve-v2`:

1. The **Selection refactor** (memory: 2026-05-15) replaced parallel-slot `TargetLock` with a single `gamecomp.Selection{EntityNetID uint32}`. The spec's LOS surface #4 ("auto-clear on LOS loss") is implemented against `Selection`, not against `TargetLockSystem`. (Spec already amended in commit `79f9523`.)
2. The current NPC archetype set is **Brawler / Artillery / Lancer** (not Sniper/Swarmer as the older 2026-05-12 combat-POI spec mentioned). The dungeon roster uses these. MotionPolicy currently has only `MotionCharge` + `MotionStationary`; this plan adds `MotionPathfind` (new) and leaves the existing two intact.

All other spec sections apply as written.

## File Structure

**New files (server):**
- `pkg/spatial/layers.go` — `LayerStatic` / `LayerProp` / `LayerEntity` constants
- `pkg/spatial/raycast.go` — DDA `HashGrid.Raycast(from, to, layerMask)`
- `pkg/spatial/raycast_test.go`
- `pkg/pathfinding/` (new package)
  - `pkg/pathfinding/navgrid.go`
  - `pkg/pathfinding/navgrid_test.go`
  - `pkg/pathfinding/astar.go`
  - `pkg/pathfinding/astar_test.go`
  - `pkg/pathfinding/smooth.go`
  - `pkg/pathfinding/smooth_test.go`
  - `pkg/pathfinding/doc.go`
- `internal/game/entity_dungeon.go` — `DungeonBundle` + `SpawnDungeon`
- `internal/game/entity_dungeon_wall.go` — `DungeonWallBundle` + `SpawnDungeonWall`
- `internal/game/dungeon_config.go` — dungeon name pool, roster table, boss-def table
- `internal/game/dungeon_gen.go` — procgen pipeline (graph + layout + walls + entrances + rosters)
- `internal/game/dungeon_gen_test.go`
- `internal/game/dungeon_chamber.go` — `ChamberState` type + `ChamberRole`/`ChamberStatus` enums
- `internal/game/dungeon_navgrid.go` — NavGrid rasterization at procgen time
- `internal/game/dungeon_navgrid_test.go`
- `internal/game/system_dungeon_chamber.go` — `DungeonChamberSystem`
- `internal/game/system_dungeon_chamber_test.go`
- `internal/game/system_selection_los.go` — `SelectionLOSSystem`
- `internal/game/commands/dungeon.go` — `dungeon.list/respawn/regenerate/spawn` verbs
- `web-pixi/src/entities/dungeon.ts` — asteroid silhouette + cave-mouth rendering
- `web-pixi/src/entities/dungeon-wall.ts` — wall rectangle rendering
- `web-pixi/src/dungeon-overlay.ts` — inside-dungeon visual effect

**Modified files (server):**
- `internal/component/components.go` — add `Dungeon`, `DungeonWall`, `Pathing` components; rename `POIAnchor` → `DungeonAnchor`
- `internal/component/components.go` — bump kind enum: `KindDungeon`, `KindDungeonWall`
- `internal/game/entity_kinds.go` — register `KindDungeon`, `KindDungeonWall`; existing `POIAnchor` consumers consume `DungeonAnchor`
- `internal/game/entity_poi.go` — switch `gamecomp.POIAnchor` → `gamecomp.DungeonAnchor` (field rename only)
- `internal/game/entity_npc.go` — same (also: SpawnNPC arg semantics unchanged, just renamed field)
- `internal/game/entity_station.go` — set `Collider.Layer = spatial.LayerStatic`
- `internal/game/entity_asteroid.go` — set `Collider.Layer = spatial.LayerProp`
- `internal/game/system_npc_ai.go` — LOS gate on Acquire; periodic LOS recheck in Engage; new `MotionPathfind` policy
- `internal/game/system_ability.go` — beam/hitscan path clips at LOS hit
- `internal/game/npc_archetype.go` — add `MotionPathfind` constant; new `ArchetypeBossGuardian`
- `internal/game/factory.go` — register `DungeonChamberSystem` + `SelectionLOSSystem` in `GameSetup`
- `internal/game/config.go` — add tunables per spec §9
- `internal/game/logcat.go` — add `CatDungeon`, `CatDungeonGen`, `CatLOS`, `CatPathfind`
- `internal/game/gameworld.go` — wire dungeon-related fields into `GameWorld` if needed (chamber state map, dungeon-by-cell)
- `internal/game/hooks.go` — wire `OnEntityRemoved` to prune chambers (or fold into `DungeonChamberSystem`)

**Modified files (client):**
- `web-pixi/src/entities/index.ts` (or equivalent registry) — register new entity renderers
- `web-pixi/src/map.ts` (or equivalent) — add dungeon map marker

---

## Phase 1 — Engine Primitive: LOS Raycast

Adds a DDA raycast to the spatial hash grid plus layer constants. Pure-library work — no game wiring yet. Each task is testable in isolation.

### Task 1: Layer constants

**Files:**
- Create: `pkg/spatial/layers.go`
- Test: `pkg/spatial/layers_test.go`

- [ ] **Step 1: Write the failing test**

```go
// pkg/spatial/layers_test.go
package spatial

import "testing"

func TestLayerBits(t *testing.T) {
    if LayerStatic == 0 {
        t.Fatal("LayerStatic must be non-zero")
    }
    if LayerStatic == LayerProp {
        t.Fatal("LayerStatic and LayerProp must be distinct")
    }
    if LayerProp == LayerEntity {
        t.Fatal("LayerProp and LayerEntity must be distinct")
    }
    // mask must allow OR-combination
    mask := LayerStatic | LayerProp
    if mask&LayerStatic == 0 || mask&LayerProp == 0 {
        t.Fatal("masks must be combinable with OR")
    }
}
```

- [ ] **Step 2: Run to verify it fails**

```
go test ./pkg/spatial/ -run TestLayerBits
```
Expected: FAIL (LayerStatic undefined).

- [ ] **Step 3: Implement**

```go
// pkg/spatial/layers.go
package spatial

// Layer bits for Collider.Layer. Layers are masks so multiple bits can
// be combined when querying (e.g. LayerStatic|LayerProp = "everything
// that blocks a projectile").
//
// Layer 0 is reserved as "no membership" — entities with Layer=0 are
// invisible to all layer-masked queries. Existing entities that pre-date
// the layer assignments default to 0; they will be brought into the
// scheme on a per-kind basis (see entity_*.go in internal/game).
const (
    LayerStatic uint8 = 1 << iota // walls, stations — block movement/sight/locks/shots
    LayerProp                      // asteroids — block shots; transparent to sight + selection
    LayerEntity                    // ships, NPCs — do not block anything
)
```

- [ ] **Step 4: Run to verify it passes**

```
go test ./pkg/spatial/ -run TestLayerBits
```
Expected: PASS.

- [ ] **Step 5: Commit**

```
git add pkg/spatial/layers.go pkg/spatial/layers_test.go
git commit -m "feat(spatial): add layer constants for LOS + collision masks"
```

### Task 2: Raycast against circle entities

**Files:**
- Create: `pkg/spatial/raycast.go`
- Test: `pkg/spatial/raycast_test.go`

- [ ] **Step 1: Write the failing test**

```go
// pkg/spatial/raycast_test.go
package spatial

import (
    "math"
    "testing"

    "github.com/mlange-42/ark/ecs"
)

func TestRaycast_CircleHit(t *testing.T) {
    g := NewHashGrid(100)
    w := ecs.NewWorld()
    e := w.NewEntity()
    g.Insert(Entry{
        Entity: e, X: 200, Y: 0,
        Radius: 50, Shape: ShapeCircle, Layer: LayerStatic,
    })
    // ray from origin straight along +X
    hit, hitPt, dist, ok := g.Raycast(Vec2{0, 0}, Vec2{500, 0}, LayerStatic)
    if !ok {
        t.Fatal("expected hit, got miss")
    }
    if hit != e {
        t.Fatalf("expected hit entity %v, got %v", e, hit)
    }
    // surface of circle at X=200,R=50 → first intersect at X=150
    if math.Abs(float64(hitPt.X-150)) > 0.5 {
        t.Fatalf("hit X expected ~150, got %.2f", hitPt.X)
    }
    if math.Abs(float64(dist-150)) > 0.5 {
        t.Fatalf("dist expected ~150, got %.2f", dist)
    }
}

func TestRaycast_LayerMaskFiltering(t *testing.T) {
    g := NewHashGrid(100)
    w := ecs.NewWorld()
    // entity is LayerProp; we query LayerStatic → must miss
    e := w.NewEntity()
    g.Insert(Entry{
        Entity: e, X: 200, Y: 0,
        Radius: 50, Shape: ShapeCircle, Layer: LayerProp,
    })
    _, _, _, ok := g.Raycast(Vec2{0, 0}, Vec2{500, 0}, LayerStatic)
    if ok {
        t.Fatal("expected miss when layer mask excludes entity")
    }
}

func TestRaycast_Miss(t *testing.T) {
    g := NewHashGrid(100)
    _, _, _, ok := g.Raycast(Vec2{0, 0}, Vec2{500, 0}, LayerStatic)
    if ok {
        t.Fatal("expected miss on empty grid")
    }
}
```

- [ ] **Step 2: Run to verify it fails**

```
go test ./pkg/spatial/ -run TestRaycast
```
Expected: FAIL (`Vec2` undefined, `Raycast` undefined).

- [ ] **Step 3: Implement Vec2 + Raycast (circles only for now)**

```go
// pkg/spatial/raycast.go
package spatial

import (
    "math"

    "github.com/mlange-42/ark/ecs"
)

// Vec2 is a 2D vector in world coordinates.
type Vec2 struct {
    X, Y float32
}

// Raycast walks the spatial hash buckets along the segment from→to and
// returns the nearest collider whose Layer&layerMask != 0.
//
// hitPoint is the precise contact point on the collider surface. dist
// is the distance from `from` to hitPoint. ok is false on miss; the
// other return values are zero on miss.
func (g *HashGrid) Raycast(from, to Vec2, layerMask uint8) (ecs.Entity, Vec2, float32, bool) {
    dx := to.X - from.X
    dy := to.Y - from.Y
    rayLen := float32(math.Sqrt(float64(dx*dx + dy*dy)))
    if rayLen < 1e-6 {
        return ecs.Entity{}, Vec2{}, 0, false
    }

    var bestEntity ecs.Entity
    var bestHit Vec2
    bestDist := rayLen + 1 // sentinel: anything < this wins
    found := false

    // Walk every bucket the ray crosses via a 2D DDA. For each bucket,
    // narrow-test every entry whose Layer matches the mask.
    for _, key := range g.bucketsAlongRay(from, to) {
        b := g.buckets[key]
        if b == nil {
            continue
        }
        for _, e := range b.tracked {
            if e.Layer&layerMask == 0 {
                continue
            }
            if e.Shape == ShapeCircle {
                t, ok := rayCircleHit(from, to, e.X, e.Y, e.Radius)
                if !ok {
                    continue
                }
                d := t * rayLen
                if d < bestDist {
                    bestDist = d
                    bestEntity = e.Entity
                    bestHit = Vec2{from.X + dx*t, from.Y + dy*t}
                    found = true
                }
            }
            // Rect handled in Task 3.
        }
    }
    if !found {
        return ecs.Entity{}, Vec2{}, 0, false
    }
    return bestEntity, bestHit, bestDist, true
}

// bucketsAlongRay returns the bucket keys touched by the segment from→to
// in order of increasing distance from `from`. Uses Amanatides-Woo DDA.
func (g *HashGrid) bucketsAlongRay(from, to Vec2) []BucketKey {
    bx0 := int32(math.Floor(float64(from.X * g.invBucketSize)))
    by0 := int32(math.Floor(float64(from.Y * g.invBucketSize)))
    bx1 := int32(math.Floor(float64(to.X * g.invBucketSize)))
    by1 := int32(math.Floor(float64(to.Y * g.invBucketSize)))

    keys := []BucketKey{{bx0, by0}}
    if bx0 == bx1 && by0 == by1 {
        return keys
    }

    dx := to.X - from.X
    dy := to.Y - from.Y
    stepX, stepY := int32(1), int32(1)
    if dx < 0 {
        stepX = -1
    }
    if dy < 0 {
        stepY = -1
    }

    nextBoundaryX := float32(bx0+(stepX+1)/2) * g.bucketSize
    nextBoundaryY := float32(by0+(stepY+1)/2) * g.bucketSize

    tMaxX := float32(math.Inf(1))
    tMaxY := float32(math.Inf(1))
    if dx != 0 {
        tMaxX = (nextBoundaryX - from.X) / dx
    }
    if dy != 0 {
        tMaxY = (nextBoundaryY - from.Y) / dy
    }
    tDeltaX := float32(math.Inf(1))
    tDeltaY := float32(math.Inf(1))
    if dx != 0 {
        tDeltaX = g.bucketSize / float32(math.Abs(float64(dx)))
    }
    if dy != 0 {
        tDeltaY = g.bucketSize / float32(math.Abs(float64(dy)))
    }

    bx, by := bx0, by0
    for {
        if tMaxX < tMaxY {
            bx += stepX
            tMaxX += tDeltaX
        } else {
            by += stepY
            tMaxY += tDeltaY
        }
        keys = append(keys, BucketKey{bx, by})
        if bx == bx1 && by == by1 {
            return keys
        }
        // Safety: stop after a generous upper bound.
        if len(keys) > 4096 {
            return keys
        }
    }
}

// rayCircleHit returns the parametric t∈[0,1] of the nearest entry into
// the circle along the segment from→to, or (0,false) if no hit.
func rayCircleHit(from, to Vec2, cx, cy, r float32) (float32, bool) {
    dx := to.X - from.X
    dy := to.Y - from.Y
    fx := from.X - cx
    fy := from.Y - cy
    a := dx*dx + dy*dy
    b := 2 * (fx*dx + fy*dy)
    c := fx*fx + fy*fy - r*r
    disc := b*b - 4*a*c
    if disc < 0 {
        return 0, false
    }
    sq := float32(math.Sqrt(float64(disc)))
    t1 := (-b - sq) / (2 * a)
    t2 := (-b + sq) / (2 * a)
    // Want the nearest hit in [0,1].
    if t1 >= 0 && t1 <= 1 {
        return t1, true
    }
    if t2 >= 0 && t2 <= 1 {
        return t2, true
    }
    return 0, false
}
```

- [ ] **Step 4: Run to verify it passes**

```
go test ./pkg/spatial/ -run TestRaycast
```
Expected: PASS for `_CircleHit`, `_LayerMaskFiltering`, `_Miss`.

- [ ] **Step 5: Commit**

```
git add pkg/spatial/raycast.go pkg/spatial/raycast_test.go
git commit -m "feat(spatial): add DDA raycast for circles + layer-masked hit testing"
```

### Task 3: Raycast against rect entities

**Files:**
- Modify: `pkg/spatial/raycast.go`
- Modify: `pkg/spatial/raycast_test.go`

- [ ] **Step 1: Add failing tests for rects (axis-aligned + rotated)**

Append to `pkg/spatial/raycast_test.go`:

```go
func TestRaycast_AxisAlignedRect(t *testing.T) {
    g := NewHashGrid(100)
    w := ecs.NewWorld()
    e := w.NewEntity()
    // Rect at (200,0) with width=80 forward, height=40 side, no rotation.
    // OBB extents are (40,20); surface along ray at X=160.
    g.Insert(Entry{
        Entity: e, X: 200, Y: 0,
        Radius: 50, Width: 80, Height: 40, Rotation: 0,
        Shape: ShapeRect, Layer: LayerStatic,
    })
    _, hitPt, dist, ok := g.Raycast(Vec2{0, 0}, Vec2{500, 0}, LayerStatic)
    if !ok {
        t.Fatal("expected hit")
    }
    if math.Abs(float64(hitPt.X-160)) > 0.5 {
        t.Fatalf("hit X expected ~160, got %.2f", hitPt.X)
    }
    if math.Abs(float64(dist-160)) > 0.5 {
        t.Fatalf("dist expected ~160, got %.2f", dist)
    }
}

func TestRaycast_RotatedRect(t *testing.T) {
    g := NewHashGrid(100)
    w := ecs.NewWorld()
    e := w.NewEntity()
    // 90° rotated rect at (200,0): width-axis now points +Y, so the rect
    // along X is 40 wide (the original Height). Surface at X=180.
    g.Insert(Entry{
        Entity: e, X: 200, Y: 0,
        Radius: 50, Width: 80, Height: 40,
        Rotation: float32(math.Pi / 2),
        Shape: ShapeRect, Layer: LayerStatic,
    })
    _, hitPt, _, ok := g.Raycast(Vec2{0, 0}, Vec2{500, 0}, LayerStatic)
    if !ok {
        t.Fatal("expected hit")
    }
    if math.Abs(float64(hitPt.X-180)) > 0.5 {
        t.Fatalf("hit X expected ~180, got %.2f", hitPt.X)
    }
}
```

- [ ] **Step 2: Run to verify they fail**

```
go test ./pkg/spatial/ -run TestRaycast_AxisAlignedRect -run TestRaycast_RotatedRect
```
Expected: FAIL — raycast doesn't yet handle rects.

- [ ] **Step 3: Implement rayRectHit and wire into Raycast**

In `pkg/spatial/raycast.go`, add the rect branch in the inner loop right after the `ShapeCircle` branch:

```go
if e.Shape == ShapeRect {
    t, ok := rayRectHit(from, to, e)
    if !ok {
        continue
    }
    d := t * rayLen
    if d < bestDist {
        bestDist = d
        bestEntity = e.Entity
        bestHit = Vec2{from.X + dx*t, from.Y + dy*t}
        found = true
    }
}
```

Add the helper at the bottom of the file:

```go
// rayRectHit returns t∈[0,1] for the nearest ray-vs-OBB entry, or false.
// Transforms the ray into the rect's local frame and runs a 2D slab test.
func rayRectHit(from, to Vec2, e Entry) (float32, bool) {
    cosA := float32(math.Cos(float64(-e.Rotation)))
    sinA := float32(math.Sin(float64(-e.Rotation)))
    fx := from.X - e.X
    fy := from.Y - e.Y
    tx := to.X - e.X
    ty := to.Y - e.Y
    lfx := cosA*fx - sinA*fy
    lfy := sinA*fx + cosA*fy
    ltx := cosA*tx - sinA*ty
    lty := sinA*tx + cosA*ty
    dx := ltx - lfx
    dy := lty - lfy

    hx := e.Width / 2
    hy := e.Height / 2

    tMin := float32(-math.MaxFloat32)
    tMax := float32(math.MaxFloat32)

    // X slab
    if math.Abs(float64(dx)) < 1e-6 {
        if lfx < -hx || lfx > hx {
            return 0, false
        }
    } else {
        t1 := (-hx - lfx) / dx
        t2 := (hx - lfx) / dx
        if t1 > t2 {
            t1, t2 = t2, t1
        }
        if t1 > tMin {
            tMin = t1
        }
        if t2 < tMax {
            tMax = t2
        }
        if tMin > tMax {
            return 0, false
        }
    }
    // Y slab
    if math.Abs(float64(dy)) < 1e-6 {
        if lfy < -hy || lfy > hy {
            return 0, false
        }
    } else {
        t1 := (-hy - lfy) / dy
        t2 := (hy - lfy) / dy
        if t1 > t2 {
            t1, t2 = t2, t1
        }
        if t1 > tMin {
            tMin = t1
        }
        if t2 < tMax {
            tMax = t2
        }
        if tMin > tMax {
            return 0, false
        }
    }
    if tMin < 0 || tMin > 1 {
        return 0, false
    }
    return tMin, true
}
```

- [ ] **Step 4: Run to verify all raycast tests pass**

```
go test ./pkg/spatial/
```
Expected: PASS.

- [ ] **Step 5: Commit**

```
git add pkg/spatial/raycast.go pkg/spatial/raycast_test.go
git commit -m "feat(spatial): extend raycast to handle oriented rectangles"
```

---

## Phase 2 — Engine Primitive: Pathfinding

A new `pkg/pathfinding/` package with `NavGrid`, A*, and LOS smoothing. Pure-library, fully tested before any consumer wires it in.

### Task 4: NavGrid type

**Files:**
- Create: `pkg/pathfinding/navgrid.go`
- Create: `pkg/pathfinding/navgrid_test.go`
- Create: `pkg/pathfinding/doc.go`

- [ ] **Step 1: Write failing test**

```go
// pkg/pathfinding/navgrid_test.go
package pathfinding

import "testing"

func TestNavGrid_CoordRoundTrip(t *testing.T) {
    g := NewNavGrid(Vec2{0, 0}, 10, 5, 5)
    for x := 0; x < 5; x++ {
        for y := 0; y < 5; y++ {
            wp := g.CellToWorld(x, y)
            cx, cy, ok := g.WorldToCell(wp)
            if !ok {
                t.Fatalf("cell (%d,%d) → world %v → not ok", x, y, wp)
            }
            if cx != x || cy != y {
                t.Fatalf("round-trip mismatch: (%d,%d) → (%d,%d)", x, y, cx, cy)
            }
        }
    }
}

func TestNavGrid_OutOfBounds(t *testing.T) {
    g := NewNavGrid(Vec2{0, 0}, 10, 5, 5)
    _, _, ok := g.WorldToCell(Vec2{-5, 0})
    if ok {
        t.Fatal("expected out-of-bounds for negative x")
    }
    _, _, ok = g.WorldToCell(Vec2{100, 100})
    if ok {
        t.Fatal("expected out-of-bounds beyond grid extent")
    }
}

func TestNavGrid_BlockUnblock(t *testing.T) {
    g := NewNavGrid(Vec2{0, 0}, 10, 5, 5)
    if !g.Walkable(2, 2) {
        t.Fatal("default cell must be walkable")
    }
    g.Block(2, 2)
    if g.Walkable(2, 2) {
        t.Fatal("expected (2,2) to be blocked")
    }
}
```

- [ ] **Step 2: Run to verify failure**

```
go test ./pkg/pathfinding/ -run TestNavGrid
```
Expected: FAIL (package undefined).

- [ ] **Step 3: Implement**

```go
// pkg/pathfinding/doc.go
// Package pathfinding provides A* over a rasterized 2D walkability bitmap
// (NavGrid) plus a LOS-based path smoother. Used by NPC AI to navigate
// hand-authored or procgen environments with non-circular static colliders.
package pathfinding
```

```go
// pkg/pathfinding/navgrid.go
package pathfinding

// Vec2 mirrors spatial.Vec2; kept package-local so pathfinding has no
// dependency on pkg/spatial. Callers convert at the boundary.
type Vec2 struct {
    X, Y float32
}

// NavGrid is a rasterized walkability bitmap. Origin is the world-space
// position of cell (0,0)'s lower-left corner. Walkable cells default to
// true; call Block(x,y) to mark a cell as obstructed.
type NavGrid struct {
    Origin   Vec2
    CellSize float32
    Width    int
    Height   int
    walkable []bool
}

// NewNavGrid allocates a grid with every cell defaulted to walkable.
func NewNavGrid(origin Vec2, cellSize float32, width, height int) *NavGrid {
    g := &NavGrid{
        Origin:   origin,
        CellSize: cellSize,
        Width:    width,
        Height:   height,
        walkable: make([]bool, width*height),
    }
    for i := range g.walkable {
        g.walkable[i] = true
    }
    return g
}

// CellToWorld returns the center of cell (x,y) in world space.
func (g *NavGrid) CellToWorld(x, y int) Vec2 {
    return Vec2{
        X: g.Origin.X + (float32(x)+0.5)*g.CellSize,
        Y: g.Origin.Y + (float32(y)+0.5)*g.CellSize,
    }
}

// WorldToCell returns the integer cell containing the given world-space
// point. ok=false if the point falls outside the grid.
func (g *NavGrid) WorldToCell(p Vec2) (int, int, bool) {
    dx := p.X - g.Origin.X
    dy := p.Y - g.Origin.Y
    if dx < 0 || dy < 0 {
        return 0, 0, false
    }
    cx := int(dx / g.CellSize)
    cy := int(dy / g.CellSize)
    if cx >= g.Width || cy >= g.Height {
        return 0, 0, false
    }
    return cx, cy, true
}

// Walkable reports whether cell (x,y) is traversable.
func (g *NavGrid) Walkable(x, y int) bool {
    if x < 0 || y < 0 || x >= g.Width || y >= g.Height {
        return false
    }
    return g.walkable[y*g.Width+x]
}

// Block marks cell (x,y) as obstructed.
func (g *NavGrid) Block(x, y int) {
    if x < 0 || y < 0 || x >= g.Width || y >= g.Height {
        return
    }
    g.walkable[y*g.Width+x] = false
}
```

- [ ] **Step 4: Run to verify pass**

```
go test ./pkg/pathfinding/ -run TestNavGrid
```
Expected: PASS.

- [ ] **Step 5: Commit**

```
git add pkg/pathfinding/
git commit -m "feat(pathfinding): add NavGrid walkability bitmap"
```

### Task 5: A* algorithm

**Files:**
- Create: `pkg/pathfinding/astar.go`
- Create: `pkg/pathfinding/astar_test.go`

- [ ] **Step 1: Write failing tests**

```go
// pkg/pathfinding/astar_test.go
package pathfinding

import "testing"

// TestAStar_StraightLine: empty 10×10 grid, start (0,0) → goal (9,0).
// Expect 10 waypoints monotonically increasing in X.
func TestAStar_StraightLine(t *testing.T) {
    g := NewNavGrid(Vec2{0, 0}, 1, 10, 10)
    path := AStar(g, Vec2{0.5, 0.5}, Vec2{9.5, 0.5})
    if len(path) == 0 {
        t.Fatal("expected non-empty path")
    }
    // First and last waypoints should be at start and goal cell centers.
    if path[0].X != 0.5 || path[0].Y != 0.5 {
        t.Fatalf("start waypoint expected (0.5,0.5), got %v", path[0])
    }
    if path[len(path)-1].X != 9.5 || path[len(path)-1].Y != 0.5 {
        t.Fatalf("goal waypoint expected (9.5,0.5), got %v", path[len(path)-1])
    }
}

// TestAStar_AvoidsWall: a vertical wall at x=2 forces a detour.
func TestAStar_AvoidsWall(t *testing.T) {
    g := NewNavGrid(Vec2{0, 0}, 1, 5, 5)
    for y := 0; y < 4; y++ {
        g.Block(2, y) // wall from (2,0) to (2,3); (2,4) is the gap
    }
    path := AStar(g, Vec2{0.5, 0.5}, Vec2{4.5, 0.5})
    if path == nil {
        t.Fatal("expected reachable path through the gap")
    }
    // No waypoint should be at (2,0)..(2,3) (the blocked cells).
    for _, w := range path {
        cx, cy, _ := g.WorldToCell(w)
        if cx == 2 && cy < 4 {
            t.Fatalf("path crosses blocked cell (2,%d)", cy)
        }
    }
}

// TestAStar_Unreachable: goal walled in on all sides → nil.
func TestAStar_Unreachable(t *testing.T) {
    g := NewNavGrid(Vec2{0, 0}, 1, 5, 5)
    g.Block(1, 2)
    g.Block(2, 1)
    g.Block(2, 3)
    g.Block(3, 2)
    g.Block(1, 1)
    g.Block(1, 3)
    g.Block(3, 1)
    g.Block(3, 3)
    path := AStar(g, Vec2{0.5, 0.5}, Vec2{2.5, 2.5})
    if path != nil {
        t.Fatalf("expected nil path to walled-in goal, got %v", path)
    }
}
```

- [ ] **Step 2: Run to verify failure**

```
go test ./pkg/pathfinding/ -run TestAStar
```
Expected: FAIL (`AStar` undefined).

- [ ] **Step 3: Implement**

```go
// pkg/pathfinding/astar.go
package pathfinding

import (
    "container/heap"
    "math"
)

// AStar runs 8-connected A* with octile heuristic on g, from the cell
// containing start to the cell containing goal. Returns world-space
// waypoints (cell centers) including start and goal. Returns nil if
// the goal cell is unreachable or out of bounds.
func AStar(g *NavGrid, start, goal Vec2) []Vec2 {
    sx, sy, ok := g.WorldToCell(start)
    if !ok || !g.Walkable(sx, sy) {
        return nil
    }
    gx, gy, ok := g.WorldToCell(goal)
    if !ok || !g.Walkable(gx, gy) {
        return nil
    }
    if sx == gx && sy == gy {
        return []Vec2{g.CellToWorld(sx, sy)}
    }

    width := g.Width
    cameFromX := make([]int, width*g.Height)
    cameFromY := make([]int, width*g.Height)
    gScore := make([]float32, width*g.Height)
    for i := range gScore {
        gScore[i] = float32(math.Inf(1))
        cameFromX[i] = -1
    }
    startIdx := sy*width + sx
    gScore[startIdx] = 0

    open := &openHeap{}
    heap.Init(open)
    heap.Push(open, openItem{x: sx, y: sy, f: octile(sx, sy, gx, gy)})

    diagCost := float32(math.Sqrt2)
    cardinal := [4][2]int{{1, 0}, {-1, 0}, {0, 1}, {0, -1}}
    diag := [4][2]int{{1, 1}, {-1, 1}, {1, -1}, {-1, -1}}

    for open.Len() > 0 {
        cur := heap.Pop(open).(openItem)
        if cur.x == gx && cur.y == gy {
            return reconstruct(g, cameFromX, cameFromY, gx, gy, sx, sy)
        }
        curIdx := cur.y*width + cur.x
        for _, d := range cardinal {
            nx, ny := cur.x+d[0], cur.y+d[1]
            if !g.Walkable(nx, ny) {
                continue
            }
            tentative := gScore[curIdx] + 1
            if tentative < gScore[ny*width+nx] {
                gScore[ny*width+nx] = tentative
                cameFromX[ny*width+nx] = cur.x
                cameFromY[ny*width+nx] = cur.y
                heap.Push(open, openItem{x: nx, y: ny, f: tentative + octile(nx, ny, gx, gy)})
            }
        }
        for _, d := range diag {
            nx, ny := cur.x+d[0], cur.y+d[1]
            if !g.Walkable(nx, ny) {
                continue
            }
            // Don't cut corners through a wall.
            if !g.Walkable(cur.x+d[0], cur.y) || !g.Walkable(cur.x, cur.y+d[1]) {
                continue
            }
            tentative := gScore[curIdx] + diagCost
            if tentative < gScore[ny*width+nx] {
                gScore[ny*width+nx] = tentative
                cameFromX[ny*width+nx] = cur.x
                cameFromY[ny*width+nx] = cur.y
                heap.Push(open, openItem{x: nx, y: ny, f: tentative + octile(nx, ny, gx, gy)})
            }
        }
    }
    return nil
}

func octile(x0, y0, x1, y1 int) float32 {
    dx := abs(x0 - x1)
    dy := abs(y0 - y1)
    if dx > dy {
        return float32(dx-dy) + float32(math.Sqrt2)*float32(dy)
    }
    return float32(dy-dx) + float32(math.Sqrt2)*float32(dx)
}

func abs(x int) int {
    if x < 0 {
        return -x
    }
    return x
}

func reconstruct(g *NavGrid, cfx, cfy []int, gx, gy, sx, sy int) []Vec2 {
    var rev []Vec2
    cx, cy := gx, gy
    for !(cx == sx && cy == sy) {
        rev = append(rev, g.CellToWorld(cx, cy))
        idx := cy*g.Width + cx
        cx, cy = cfx[idx], cfy[idx]
        if cx == -1 {
            return nil // safety
        }
    }
    rev = append(rev, g.CellToWorld(sx, sy))
    // reverse
    for i, j := 0, len(rev)-1; i < j; i, j = i+1, j-1 {
        rev[i], rev[j] = rev[j], rev[i]
    }
    return rev
}

// openHeap implements a min-heap on openItem.f.
type openItem struct {
    x, y int
    f    float32
}
type openHeap []openItem

func (h openHeap) Len() int            { return len(h) }
func (h openHeap) Less(i, j int) bool  { return h[i].f < h[j].f }
func (h openHeap) Swap(i, j int)       { h[i], h[j] = h[j], h[i] }
func (h *openHeap) Push(x any)         { *h = append(*h, x.(openItem)) }
func (h *openHeap) Pop() any {
    old := *h
    n := len(old)
    x := old[n-1]
    *h = old[:n-1]
    return x
}
```

- [ ] **Step 4: Run to verify pass**

```
go test ./pkg/pathfinding/ -run TestAStar
```
Expected: PASS.

- [ ] **Step 5: Commit**

```
git add pkg/pathfinding/astar.go pkg/pathfinding/astar_test.go
git commit -m "feat(pathfinding): add 8-connected A* with octile heuristic"
```

### Task 6: LOS smoothing

**Files:**
- Create: `pkg/pathfinding/smooth.go`
- Create: `pkg/pathfinding/smooth_test.go`

- [ ] **Step 1: Write failing test**

```go
// pkg/pathfinding/smooth_test.go
package pathfinding

import "testing"

// LOSChecker is a minimal interface so smooth doesn't depend on pkg/spatial.
// Real callers wrap spatial.HashGrid.Raycast in a closure that returns
// true on clear LOS, false if blocked.
type fakeLOS struct {
    blocks []struct{ a, b Vec2 }
}

func (f fakeLOS) Clear(a, b Vec2) bool {
    for _, blk := range f.blocks {
        if blk.a == a && blk.b == b {
            return false
        }
    }
    return true
}

// All-clear LOS collapses a 3-point straight line to its endpoints.
func TestSmoothLOS_CollapsesStraightLine(t *testing.T) {
    in := []Vec2{{0, 0}, {1, 0}, {2, 0}, {3, 0}}
    out := SmoothLOS(in, fakeLOS{})
    if len(out) != 2 {
        t.Fatalf("expected 2 waypoints (collapsed), got %d: %v", len(out), out)
    }
    if out[0] != in[0] || out[1] != in[len(in)-1] {
        t.Fatalf("expected start/end preserved, got %v", out)
    }
}

// When LOS from 0 → 3 is blocked but 0 → 2 is clear, mid stop is at index 2.
func TestSmoothLOS_PreservesCorner(t *testing.T) {
    in := []Vec2{{0, 0}, {1, 0}, {2, 0}, {2, 1}}
    los := fakeLOS{blocks: []struct{ a, b Vec2 }{
        {{0, 0}, {2, 1}},
        {{0, 0}, {2, 0}}, // can't see (2,0) either
    }}
    // smoother walks: from (0,0), try (2,1) → blocked, try (2,0) → blocked,
    // accept (1,0) as next stop. From (1,0) try (2,1) → assume clear.
    out := SmoothLOS(in, los)
    if len(out) != 3 {
        t.Fatalf("expected 3 waypoints (corner preserved), got %d: %v", len(out), out)
    }
}

// Idempotency: smoothing a smooth path returns the same path.
func TestSmoothLOS_Idempotent(t *testing.T) {
    in := []Vec2{{0, 0}, {2, 2}}
    out1 := SmoothLOS(in, fakeLOS{})
    out2 := SmoothLOS(out1, fakeLOS{})
    if len(out2) != len(out1) {
        t.Fatalf("expected idempotent smoothing")
    }
}
```

- [ ] **Step 2: Run to verify failure**

```
go test ./pkg/pathfinding/ -run TestSmoothLOS
```
Expected: FAIL (`SmoothLOS` undefined).

- [ ] **Step 3: Implement**

```go
// pkg/pathfinding/smooth.go
package pathfinding

// LOSChecker reports whether a straight line from a to b is unobstructed.
// In production callers pass a closure that wraps spatial.HashGrid.Raycast
// with the appropriate layer mask.
type LOSChecker interface {
    Clear(a, b Vec2) bool
}

// SmoothLOS post-processes a waypoint list by greedily skipping waypoints
// reachable from an earlier waypoint via direct LOS. Returns a new slice;
// the input is unmodified. The first and last waypoints are always
// preserved.
//
// O(n²) in the worst case; n is the path length so this is fine in
// practice for the path lengths AStar produces (≤ a few hundred).
func SmoothLOS(in []Vec2, los LOSChecker) []Vec2 {
    if len(in) <= 2 {
        return append([]Vec2(nil), in...)
    }
    out := []Vec2{in[0]}
    i := 0
    for i < len(in)-1 {
        // farthest j reachable from i with LOS clear
        far := i + 1
        for j := len(in) - 1; j > i; j-- {
            if los.Clear(in[i], in[j]) {
                far = j
                break
            }
        }
        out = append(out, in[far])
        i = far
    }
    return out
}
```

- [ ] **Step 4: Run to verify pass**

```
go test ./pkg/pathfinding/ -run TestSmoothLOS
```
Expected: PASS.

- [ ] **Step 5: Commit**

```
git add pkg/pathfinding/smooth.go pkg/pathfinding/smooth_test.go
git commit -m "feat(pathfinding): add SmoothLOS for any-angle path post-processing"
```

---

## Phase 3 — LOS Hookup into Combat & Selection

The primitives now exist. Wire them into the four combat surfaces from spec §8.1 and add `LayerStatic`/`LayerProp` to existing entities.

### Task 7: Adapter — LOS helper inside `internal/game`

**Files:**
- Create: `internal/game/los.go`
- Create: `internal/game/los_test.go`

- [ ] **Step 1: Write failing test**

```go
// internal/game/los_test.go
package game

import (
    "testing"

    "github.com/zenion/mmokit/pkg/spatial"
)

// hasLOS uses the stage's spatial grid to test sight between two world points.
// This wraps spatial.HashGrid.Raycast with the LayerStatic mask. The test
// constructs a minimal scenario via a helper grid (no full game world needed).
func TestHasLOS_ClearAndBlocked(t *testing.T) {
    g := spatial.NewHashGrid(100)
    if !hasLOSOnGrid(g, vec2(0, 0), vec2(500, 0)) {
        t.Fatal("expected clear LOS on empty grid")
    }
    // place a static wall in the line
    e := newTestEntity(t)
    g.Insert(spatial.Entry{
        Entity: e, X: 250, Y: 0,
        Radius: 60, Width: 80, Height: 40, Rotation: 0,
        Shape: spatial.ShapeRect, Layer: spatial.LayerStatic,
    })
    if hasLOSOnGrid(g, vec2(0, 0), vec2(500, 0)) {
        t.Fatal("expected blocked LOS through wall")
    }
}
```

(A `newTestEntity` helper that calls `ecs.NewWorld().NewEntity()` and `vec2` constructor — implementer adds these locally if not already present.)

- [ ] **Step 2: Run to verify failure**

```
go test ./internal/game/ -run TestHasLOS
```
Expected: FAIL.

- [ ] **Step 3: Implement**

```go
// internal/game/los.go
package game

import "github.com/zenion/mmokit/pkg/spatial"

// vec2 is a local shorthand for converting (x,y) pairs to spatial.Vec2.
func vec2(x, y float32) spatial.Vec2 { return spatial.Vec2{X: x, Y: y} }

// hasLOSOnGrid reports whether a straight line of sight exists between
// `from` and `to` against `g`, considering only LayerStatic colliders.
//
// Wraps spatial.HashGrid.Raycast for the common "sight / lock / aggro"
// case. For projectile/beam checks (which also block on props), call
// Raycast directly with LayerStatic|LayerProp.
func hasLOSOnGrid(g *spatial.HashGrid, from, to spatial.Vec2) bool {
    _, _, _, hit := g.Raycast(from, to, spatial.LayerStatic)
    return !hit
}
```

- [ ] **Step 4: Run to verify pass**

```
go test ./internal/game/ -run TestHasLOS
```
Expected: PASS.

- [ ] **Step 5: Commit**

```
git add internal/game/los.go internal/game/los_test.go
git commit -m "feat(game): add hasLOSOnGrid helper for combat LOS checks"
```

### Task 8: NPC AI — LOS gate on Acquire

**Files:**
- Modify: `internal/game/system_npc_ai.go`
- Modify: `internal/game/system_npc_ai_test.go`

- [ ] **Step 1: Write failing test**

Add to `system_npc_ai_test.go`:

```go
// An NPC inside aggro radius of a ship but with a wall between them
// must NOT transition Idle → Acquire.
func TestNPCAI_LOSBlockedTargetNotAcquired(t *testing.T) {
    // Use the existing test harness in system_npc_ai_test.go (build a
    // GameWorld with a manually placed NPC + ship + wall). Pseudocode:
    //   gw := newTestGW(t)
    //   npc := gw.spawnNPCAt(0, 0, ArchetypeBrawler, 0)
    //   ship := gw.spawnShipAt(200, 0)
    //   // wall midway, blocking LOS
    //   gw.SpawnDungeonWall(100, 0, 40, 40, 0)
    //   gw.tick()
    //   require.Equal(t, AIStateIdle, getNPCAI(npc).State)
    // ... (concrete details depend on existing test scaffolding)
}
```

Implementer note: study the existing tests in `system_npc_ai_test.go` to see how the harness boots a `GameWorld` for unit tests. Mirror that pattern. `SpawnDungeonWall` lands in Task 14 — until then, insert a raw `spatial.Entry` directly into `gw.stage.SpatialGrid()` for this test.

- [ ] **Step 2: Run to verify failure**

```
go test ./internal/game/ -run TestNPCAI_LOSBlocked
```
Expected: FAIL (NPC still acquires through wall).

- [ ] **Step 3: Implement**

In `system_npc_ai.go`, find the Acquire-phase target search. Add an LOS check on each candidate:

```go
// Inside the target-candidate loop in Acquire:
selfPos := /* existing self position */
candPos := /* existing candidate position */
if !hasLOSOnGrid(s.stage.SpatialGrid(), spatial.Vec2{X: selfPos.X, Y: selfPos.Y}, spatial.Vec2{X: candPos.X, Y: candPos.Y}) {
    continue
}
```

The exact insertion point is in the existing `Acquire` block; the implementer locates it via the file's existing `AggroRadius` comparison and inserts the check immediately AFTER the radius pass.

- [ ] **Step 4: Run to verify pass**

```
go test ./internal/game/ -run TestNPCAI
```
Expected: All existing AI tests + the new test pass.

- [ ] **Step 5: Commit**

```
git add internal/game/system_npc_ai.go internal/game/system_npc_ai_test.go
git commit -m "feat(npc-ai): gate target acquisition on LOS to candidate"
```

### Task 9: NPC AI — periodic LOS recheck in Engage

**Files:**
- Modify: `internal/game/system_npc_ai.go`
- Modify: `internal/component/components.go` (add LastLOSCheckAt field to NPCAI)
- Modify: `internal/game/system_npc_ai_test.go`

- [ ] **Step 1: Write failing test**

```go
// NPC engaging a target that walks behind a wall must drop the target
// within ~3 seconds (existing de-escalation timer).
func TestNPCAI_LOSLostDropsTargetAfterTimeout(t *testing.T) {
    // Pseudocode mirroring existing tests:
    //   gw := newTestGW(t)
    //   npc, ship := /* place and let NPC engage ship */
    //   placeWallBetween(npc, ship)
    //   tickFor(gw, 3.1*time.Second)
    //   require.Equal(t, AIStateIdle, getNPCAI(npc).State)
}
```

- [ ] **Step 2: Run to verify failure**

```
go test ./internal/game/ -run TestNPCAI_LOSLost
```
Expected: FAIL (NPC still engages through wall).

- [ ] **Step 3: Implement**

Add a `LastLOSCheckAt float32` field to the `NPCAI` component. In `system_npc_ai.go`'s Engage branch, every `AILosRecheckIntervalMs` (new tunable; default 500ms):

```go
if stageTime - ai.LastLOSCheckAt >= 0.5 {
    ai.LastLOSCheckAt = stageTime
    if !hasLOSOnGrid(s.stage.SpatialGrid(), npcPos, targetPos) {
        // Don't drop the target immediately — feed into the existing
        // aggro-de-escalation timer by NOT updating LastCombatActivityAt.
        // After AggroDeescalationSec without combat activity, NPC drops
        // target on its own.
    } else {
        ai.LastCombatActivityAt = stageTime // optional: only on damage
    }
}
```

Implementer note: the de-escalation timer is `AggroDeescalationSec` (default 6s in spec §7 of combat-POI design — confirm value in `config.go`). Adjust to ~3s for the dungeon use case by tuning `AILosLossDropSec = 3.0` in `GameConfig` and having Engage check `if !hasLOS && stageTime - ai.LastDamageAt > 3.0 { transitionToIdle() }`.

- [ ] **Step 4: Run to verify pass**

```
go test ./internal/game/ -run TestNPCAI
```
Expected: PASS.

- [ ] **Step 5: Commit**

```
git add internal/game/system_npc_ai.go internal/component/components.go internal/game/system_npc_ai_test.go
git commit -m "feat(npc-ai): drop target on sustained LOS loss in Engage"
```

### Task 10: Ability beam/hitscan LOS clipping

**Files:**
- Modify: `internal/game/system_ability.go`
- Modify: `internal/game/system_ability_test.go`

- [ ] **Step 1: Write failing test**

```go
// A beam fired at a target with a wall between source and target must
// either: deal 0 damage to the target, OR clip at the wall (no damage
// past the wall). Verify by reading target health after a beam tick.
func TestAbility_BeamClipsAtLOSWall(t *testing.T) {
    // Pseudocode: place caster, target, and a wall between them.
    // Cast a hitscan beam; assert target health unchanged.
}
```

- [ ] **Step 2: Run to verify failure**

```
go test ./internal/game/ -run TestAbility_BeamClipsAtLOSWall
```
Expected: FAIL (beam still hits through wall).

- [ ] **Step 3: Implement**

In `system_ability.go`, locate the hitscan/sustained-beam damage application path (search for `BeamDamage` or similar). Before applying damage, raycast from caster position to target position with mask `LayerStatic|LayerProp`. If the hit is a wall (not the target itself), suppress the damage:

```go
hitEnt, _, hitDist, blocked := stage.SpatialGrid().Raycast(
    spatial.Vec2{X: srcX, Y: srcY},
    spatial.Vec2{X: tgtX, Y: tgtY},
    spatial.LayerStatic|spatial.LayerProp,
)
if blocked && hitEnt != targetEntity.Handle() {
    _ = hitDist // optional: emit beam-clip event for client visual
    return // beam hit a wall/prop — no damage
}
```

The implementer locates the exact ability function (likely `tickChannels` in `system_ability.go` for sustained beams).

- [ ] **Step 4: Run to verify pass**

```
go test ./internal/game/ -run TestAbility
```
Expected: PASS.

- [ ] **Step 5: Commit**

```
git add internal/game/system_ability.go internal/game/system_ability_test.go
git commit -m "feat(ability): clip beam/hitscan damage at first LOS-blocking collider"
```

### Task 11: Selection auto-clear on LOS loss

**Files:**
- Create: `internal/game/system_selection_los.go`
- Create: `internal/game/system_selection_los_test.go`
- Modify: `internal/component/components.go` (add `SelectionLOSLostAt float32` field somewhere — either on Selection itself or on a parallel Pathing-like component; implementer's choice. Recommend adding to Selection.)
- Modify: `internal/game/factory.go` (register `SelectionLOSSystem`)

- [ ] **Step 1: Write failing test**

```go
// internal/game/system_selection_los_test.go
package game

import "testing"

// A player whose Selection is on an entity behind a wall must see Selection
// cleared after LockLosLossBreakSec (1s).
func TestSelectionLOS_ClearsOnSustainedLoss(t *testing.T) {
    // Pseudocode: place player ship + target entity, wall between.
    // Set Selection.EntityNetID = target.NetID().
    // Tick for 1.1s.
    // Assert Selection.EntityNetID == 0.
}
```

- [ ] **Step 2: Run to verify failure**

```
go test ./internal/game/ -run TestSelectionLOS
```
Expected: FAIL.

- [ ] **Step 3: Implement**

```go
// internal/game/system_selection_los.go
package game

import (
    "github.com/zenion/mmokit/internal/component"
    "github.com/zenion/mmokit/pkg/mmokit"
    "github.com/zenion/mmokit/pkg/spatial"
)

// SelectionLOSSystem clears a player's Selection when LOS to the
// selected entity has been blocked for LockLosLossBreakSec.
type SelectionLOSSystem struct {
    mmokit.SystemBase
}

func (s *SelectionLOSSystem) Update(dt float32) {
    gw := mmokit.WorldOf[*GameWorld](s)
    stageTime := s.Stage().Time()
    cutoff := gw.Config.LockLosLossBreakSec

    mmokit.ForEach2(s.Stage(), func(e mmokit.Entity, sel *component.Selection, pos *mmokit.Position) {
        if sel.EntityNetID == 0 {
            sel.LOSLostAt = 0
            return
        }
        target, ok := gw.NetIDToEntity[sel.EntityNetID]
        if !ok || !target.IsAlive() {
            sel.EntityNetID = 0
            sel.LOSLostAt = 0
            return
        }
        tpos := mmokit.Get[mmokit.Position](target)
        if hasLOSOnGrid(s.Stage().SpatialGrid(),
            spatial.Vec2{X: pos.X, Y: pos.Y},
            spatial.Vec2{X: tpos.X, Y: tpos.Y}) {
            sel.LOSLostAt = 0
            return
        }
        if sel.LOSLostAt == 0 {
            sel.LOSLostAt = stageTime
            return
        }
        if stageTime - sel.LOSLostAt >= cutoff {
            sel.EntityNetID = 0
            sel.LOSLostAt = 0
            gw.Log.Log(CatLOS, "selection cleared on LOS loss: player=%d", e.NetID())
        }
    })
}
```

Add `LOSLostAt float32` field to `Selection` in `internal/component/selection.go` (tag `mmokit:"local"`).

Register in `factory.go` right after `NPCAISystem`:

```go
coord.AddSystem(mmokit.NewSystem(&SelectionLOSSystem{}))
```

- [ ] **Step 4: Run to verify pass**

```
go test ./internal/game/ -run TestSelectionLOS
```
Expected: PASS.

- [ ] **Step 5: Commit**

```
git add internal/game/system_selection_los.go internal/game/system_selection_los_test.go internal/component/selection.go internal/game/factory.go
git commit -m "feat(selection): auto-clear selection on sustained LOS loss"
```

### Task 12: Station gets LayerStatic

**Files:**
- Modify: `internal/game/entity_station.go`

- [ ] **Step 1: Write failing test**

Add to `entity_station_test.go` (create if missing):

```go
func TestStation_HasLayerStatic(t *testing.T) {
    // Spawn a station, look up its Collider in the spatial grid, assert
    // Layer == spatial.LayerStatic.
}
```

- [ ] **Step 2: Run to verify failure**

```
go test ./internal/game/ -run TestStation_HasLayerStatic
```
Expected: FAIL.

- [ ] **Step 3: Implement**

In `SpawnStation`, change the Collider to include `Layer: spatial.LayerStatic`:

```go
mmokit.Collider{
    Radius: gw.Config.StationRadius,
    Shape:  spatial.ShapeCircle,
    Layer:  spatial.LayerStatic,
},
```

- [ ] **Step 4: Run to verify pass**

```
go test ./internal/game/ -run TestStation
```
Expected: PASS.

- [ ] **Step 5: Commit**

```
git add internal/game/entity_station.go internal/game/entity_station_test.go
git commit -m "feat(station): assign LayerStatic for LOS/lock/beam blocking"
```

### Task 13: Asteroid gets LayerProp

**Files:**
- Modify: `internal/game/entity_asteroid.go`

- [ ] **Step 1: Write failing test**

```go
func TestAsteroid_HasLayerProp(t *testing.T) {
    // Spawn an asteroid, look up Collider in spatial grid, assert
    // Layer == spatial.LayerProp.
}
```

- [ ] **Step 2: Run to verify failure**

```
go test ./internal/game/ -run TestAsteroid_HasLayerProp
```
Expected: FAIL.

- [ ] **Step 3: Implement**

In the asteroid spawn function, add `Layer: spatial.LayerProp` to the Collider component.

- [ ] **Step 4: Run to verify pass**

```
go test ./internal/game/ -run TestAsteroid
```
Expected: PASS.

- [ ] **Step 5: Commit**

```
git add internal/game/entity_asteroid.go internal/game/entity_asteroid_test.go
git commit -m "feat(asteroid): assign LayerProp for projectile/beam blocking"
```

---

## Phase 4 — Anchor Rename

### Task 14: POIAnchor → DungeonAnchor rename

**Files (rename across all):**
- `internal/component/components.go`
- `internal/game/entity_npc.go`
- `internal/game/entity_poi.go`
- `internal/game/system_npc_ai.go`
- `internal/game/config.go`
- Any other files matching `grep -rn POIAnchor`

- [ ] **Step 1: Identify all call sites**

```
grep -rn "POIAnchor\|POINetID" internal/ pkg/
```
Capture the list — every match becomes part of this rename.

- [ ] **Step 2: Rename in `internal/component/components.go`**

Change `POIAnchor` struct to `DungeonAnchor` and its field `POINetID` to `DungeonNetID`. Add a top-of-component doc comment noting the rename rationale (spec §6.7).

- [ ] **Step 3: Update all consumers**

Each `gamecomp.POIAnchor` → `gamecomp.DungeonAnchor`. Each `.POINetID` → `.DungeonNetID`. Add a `ChamberID uint16` field to `DungeonAnchor` (used in Phase 6 by the chamber lifecycle; for combat POIs it stays 0).

- [ ] **Step 4: Verify build + tests**

```
just build
go test ./internal/...
```
Expected: PASS.

- [ ] **Step 5: Commit**

```
git add -u
git commit -m "refactor: rename POIAnchor → DungeonAnchor, add ChamberID field"
```

---

## Phase 5 — Dungeon Entity Scaffolding

### Task 15: Add Dungeon + DungeonWall components

**Files:**
- Modify: `internal/component/components.go`

- [ ] **Step 1: Add kind constants**

Append to the kind enum block in `components.go`:

```go
KindDungeon     uint8 = ... // next available value
KindDungeonWall uint8 = ... // next available
```

(Implementer finds the next free value by reading the existing enum.)

- [ ] **Step 2: Add component types**

```go
// Dungeon is the marker + state component for a dungeon POI (asteroid-cave
// system). One per dungeon entity (KindDungeon). The chamber state is
// tracked server-side in GameWorld.dungeonChambers — only the world-level
// info travels with the entity.
type Dungeon struct {
    Name          string  `net:"initial string"`
    OuterRadius   float32 `net:"initial f32"`
    EntranceCount uint8   `net:"initial u8"`
    EntranceX0    float32 `net:"initial f32"`
    EntranceY0    float32 `net:"initial f32"`
    EntranceX1    float32 `net:"initial f32"`
    EntranceY1    float32 `net:"initial f32"`
    EntranceX2    float32 `net:"initial f32"`
    EntranceY2    float32 `net:"initial f32"`
    Seed          uint64  `mmokit:"local"`
}

// DungeonWall is a static rectangular collider that forms a piece of cave
// geometry. All wire fields are initial-only — walls never move or change.
type DungeonWall struct {
    Width  float32 `net:"initial f32"`
    Height float32 `net:"initial f32"`
}
```

(EntranceX0..2 / Y0..2 flattened because the codec doesn't auto-handle `[N]Vec2` — see spec §14 open questions. Three slots is the max per `DungeonEntranceCount`; unused slots are 0.)

- [ ] **Step 3: Run build**

```
just build
```
Expected: PASS.

- [ ] **Step 4: Commit**

```
git add internal/component/components.go
git commit -m "feat(component): add Dungeon + DungeonWall components and kinds"
```

### Task 16: Register dungeon entity kinds

**Files:**
- Modify: `internal/game/entity_kinds.go`
- Create: `internal/game/entity_dungeon.go`
- Create: `internal/game/entity_dungeon_wall.go`

- [ ] **Step 1: Create DungeonBundle + SpawnDungeon**

```go
// internal/game/entity_dungeon.go
package game

import (
    gamecomp "github.com/zenion/mmokit/internal/component"
    "github.com/zenion/mmokit/pkg/mmokit"
)

// DungeonBundle is the entity-kind bundle for a dungeon POI.
type DungeonBundle struct {
    Dungeon *gamecomp.Dungeon
}

// SpawnDungeon creates the dungeon entity at the given local position
// with the given dungeon-state values. Returns the entity's NetID.
//
// The caller is responsible for spawning the walls + chambers + NPCs.
// SpawnDungeon only creates the world-level marker entity.
func (gw *GameWorld) SpawnDungeon(x, y float32, d gamecomp.Dungeon) uint32 {
    e := gw.stage.Spawn(
        mmokit.Position{X: x, Y: y},
        mmokit.EntityKind{Type: gamecomp.KindDungeon},
        d,
    )
    gw.eng.Log.Log(CatDungeon, "dungeon: spawned netID=%d pos=(%.0f,%.0f) name=%s",
        e.NetID(), x, y, d.Name)
    return e.NetID()
}
```

- [ ] **Step 2: Create DungeonWallBundle + SpawnDungeonWall**

```go
// internal/game/entity_dungeon_wall.go
package game

import (
    "math"

    "github.com/mlange-42/ark/ecs"

    gamecomp "github.com/zenion/mmokit/internal/component"
    "github.com/zenion/mmokit/pkg/mmokit"
    "github.com/zenion/mmokit/pkg/spatial"
    "github.com/zenion/mmokit/pkg/system"
)

// DungeonWallBundle is the entity-kind bundle for a static cave wall.
type DungeonWallBundle struct {
    Wall *gamecomp.DungeonWall
}

// SpawnDungeonWall creates a rectangular wall collider at (x,y) with the
// given dims + rotation (radians). The collider is LayerStatic.
func (gw *GameWorld) SpawnDungeonWall(x, y, width, height, rotation float32) uint32 {
    radius := float32(math.Hypot(float64(width)/2, float64(height)/2))
    e := gw.stage.Spawn(
        mmokit.Position{X: x, Y: y},
        mmokit.Rotation{Angle: rotation},
        mmokit.EntityKind{Type: gamecomp.KindDungeonWall},
        mmokit.Collider{
            Radius: radius,
            Width:  width,
            Height: height,
            Shape:  spatial.ShapeRect,
            Layer:  spatial.LayerStatic,
        },
        gamecomp.DungeonWall{Width: width, Height: height},
    )
    return e.NetID()
}

// dungeonWallRotationBinding hooks Rotation into per-cell replication
// so the client knows the rect orientation.
func dungeonWallRotationBinding(w *ecs.World) system.ComponentBinding {
    return mmokit.QAngle(ecs.NewMap1[mmokit.Rotation](w))
}
```

- [ ] **Step 3: Register both kinds**

Append to `entity_kinds.go` inside `RegisterEntityKinds`:

```go
mmokit.RegisterKind[DungeonBundle](p, gamecomp.KindDungeon, "Dungeon")

mmokit.RegisterKind[DungeonWallBundle](p, gamecomp.KindDungeonWall, "DungeonWall",
    mmokit.WithExtraBindingFn(dungeonWallRotationBinding),
)
```

- [ ] **Step 4: Run build + tests**

```
just build && go test ./internal/...
```
Expected: PASS.

- [ ] **Step 5: Commit**

```
git add internal/game/entity_dungeon.go internal/game/entity_dungeon_wall.go internal/game/entity_kinds.go
git commit -m "feat(game): register Dungeon + DungeonWall entity kinds"
```

### Task 17: Add log categories

**Files:**
- Modify: `internal/game/logcat.go`

- [ ] **Step 1: Add four categories**

In `logcat.go`, append:

```go
CatDungeon    = "dungeon"
CatDungeonGen = "dungeonGen"
CatLOS        = "los"
CatPathfind   = "pathfind"
```

Auto-register them per the existing pattern.

- [ ] **Step 2: Run build**

```
just build
```
Expected: PASS.

- [ ] **Step 3: Commit**

```
git add internal/game/logcat.go
git commit -m "feat(logcat): add dungeon/dungeonGen/los/pathfind categories"
```

### Task 18: Add tunables to GameConfig

**Files:**
- Modify: `internal/game/config.go`

- [ ] **Step 1: Append fields**

Append every tunable from spec §9 with the exact name + default value. The full list:

```go
// Dungeon procgen
DungeonAsteroidRadius           float32 `json:"dungeon_asteroid_radius"`
DungeonChamberCountMin          int     `json:"dungeon_chamber_count_min"`
DungeonChamberCountMax          int     `json:"dungeon_chamber_count_max"`
DungeonChamberRadiusMin         float32 `json:"dungeon_chamber_radius_min"`
DungeonChamberRadiusMax         float32 `json:"dungeon_chamber_radius_max"`
DungeonCorridorWidth            float32 `json:"dungeon_corridor_width"`
DungeonEntranceCount            int     `json:"dungeon_entrance_count"`
DungeonWallThickness            float32 `json:"dungeon_wall_thickness"`
DungeonTestsiteCellX            int     `json:"dungeon_testsite_cell_x"`
DungeonTestsiteCellY            int     `json:"dungeon_testsite_cell_y"`
DungeonTestsiteOffsetX          float32 `json:"dungeon_testsite_offset_x"`
DungeonTestsiteOffsetY          float32 `json:"dungeon_testsite_offset_y"`

// Per-chamber cooldowns (seconds)
ChamberCooldownMobPack          float32 `json:"chamber_cooldown_mob_pack"`
ChamberCooldownSideBoss         float32 `json:"chamber_cooldown_side_boss"`
ChamberCooldownTerminal         float32 `json:"chamber_cooldown_terminal"`

// Boss stats
BossSoloHPMultiplier            float32 `json:"boss_solo_hp_multiplier"`
BossSoloDmgMultiplier           float32 `json:"boss_solo_dmg_multiplier"`
BossSoloSpeedMultiplier         float32 `json:"boss_solo_speed_multiplier"`
BossMainHPMultiplier            float32 `json:"boss_main_hp_multiplier"`
BossMainDmgMultiplier           float32 `json:"boss_main_dmg_multiplier"`
BossMainAddSpawnThresholds      []float32 `json:"boss_main_add_spawn_thresholds"`

// Loot
ChamberMobPackFluxBase          float32 `json:"chamber_mob_pack_flux_base"`
ChamberSideBossFluxBase         float32 `json:"chamber_side_boss_flux_base"`
ChamberTerminalBossFluxBase     float32 `json:"chamber_terminal_boss_flux_base"`

// LOS / pathfinding
AILosRecheckIntervalSec         float32 `json:"ai_los_recheck_interval_sec"`
AILosLossDropSec                float32 `json:"ai_los_loss_drop_sec"`
LockLosLossBreakSec             float32 `json:"lock_los_loss_break_sec"`
NavGridCellSize                 float32 `json:"nav_grid_cell_size"`
PathRepathIntervalSec           float32 `json:"path_repath_interval_sec"`
PathRepathTargetMovedThreshold  float32 `json:"path_repath_target_moved_threshold"`
```

Defaults in `DefaultGameConfig`:

```go
DungeonAsteroidRadius:           1800,
DungeonChamberCountMin:          5,
DungeonChamberCountMax:          8,
DungeonChamberRadiusMin:         120,
DungeonChamberRadiusMax:         240,
DungeonCorridorWidth:            100,
DungeonEntranceCount:            3,
DungeonWallThickness:            30,
DungeonTestsiteCellX:            0,
DungeonTestsiteCellY:            0,
DungeonTestsiteOffsetX:          -4500,
DungeonTestsiteOffsetY:          0,
ChamberCooldownMobPack:          1800,
ChamberCooldownSideBoss:         2700,
ChamberCooldownTerminal:         3600,
BossSoloHPMultiplier:            3.0,
BossSoloDmgMultiplier:           1.5,
BossSoloSpeedMultiplier:         1.2,
BossMainHPMultiplier:            10.0,
BossMainDmgMultiplier:           2.0,
BossMainAddSpawnThresholds:      []float32{0.75, 0.5, 0.25},
ChamberMobPackFluxBase:          200,
ChamberSideBossFluxBase:         1500,
ChamberTerminalBossFluxBase:     6000,
AILosRecheckIntervalSec:         0.5,
AILosLossDropSec:                3.0,
LockLosLossBreakSec:             1.0,
NavGridCellSize:                 30,
PathRepathIntervalSec:           1.5,
PathRepathTargetMovedThreshold:  50,
```

- [ ] **Step 2: Verify build**

```
just build
```
Expected: PASS.

- [ ] **Step 3: Commit**

```
git add internal/game/config.go
git commit -m "feat(config): add dungeon + LOS + pathfinding tunables"
```

---

## Phase 6 — Dungeon Procgen

### Task 19: Chamber + roster types

**Files:**
- Create: `internal/game/dungeon_chamber.go`
- Create: `internal/game/dungeon_config.go`

- [ ] **Step 1: Implement `ChamberState` + enums**

```go
// internal/game/dungeon_chamber.go
package game

import "github.com/zenion/mmokit/pkg/spatial"

// ChamberRole describes a chamber's content tier.
type ChamberRole uint8

const (
    ChamberMobPack  ChamberRole = 0
    ChamberSideBoss ChamberRole = 1
    ChamberTerminal ChamberRole = 2
)

// ChamberStatus is the lifecycle state of a single chamber.
type ChamberStatus uint8

const (
    ChamberActive   ChamberStatus = 0
    ChamberCleared  ChamberStatus = 1
    ChamberCooldown ChamberStatus = 2
)

// ChamberState is the server-side tracking record for one chamber.
type ChamberState struct {
    ID             uint16
    Center         spatial.Vec2
    Radius         float32
    Role           ChamberRole
    Status         ChamberStatus
    RosterDefIdx   uint16
    RespawnCount   uint32
    ClearedAt      int64 // unix nanos; 0 if not yet cleared this cycle
    AliveNetIDs    []uint32
    LastChestNetID uint32
    LastKillPos    spatial.Vec2 // where the last roster NPC died (used as chest position)
}
```

- [ ] **Step 2: Implement `dungeon_config.go` with roster + boss + name tables**

```go
// internal/game/dungeon_config.go
package game

import "github.com/zenion/mmokit/pkg/spatial"

// DungeonRosterDef is the roster template for one chamber, indexed
// from dungeon_config.go::dungeonRosters.
type DungeonRosterDef struct {
    Name    string
    Role    ChamberRole
    Members []DungeonRosterMember
}

type DungeonRosterMember struct {
    Archetype    uint8
    Count        int
    SpreadRadius float32
    Elite        bool // for ChamberSideBoss: marks the boss vs escorts
    Main         bool // for ChamberTerminal: marks the BossGuardian
}

// dungeonRosters is the v1 roster table. Indices are stable. Mob-pack
// rosters use simple combinations; side-boss rosters mark one member
// Elite=true; terminal roster marks one member Main=true.
var dungeonRosters = []DungeonRosterDef{
    // 0: mob pack — brawlers
    {
        Name: "Brawler Pack", Role: ChamberMobPack,
        Members: []DungeonRosterMember{
            {Archetype: ArchetypeBrawler, Count: 3, SpreadRadius: 80},
        },
    },
    // 1: mob pack — mixed
    {
        Name: "Mixed Pack", Role: ChamberMobPack,
        Members: []DungeonRosterMember{
            {Archetype: ArchetypeBrawler, Count: 2, SpreadRadius: 80},
            {Archetype: ArchetypeLancer, Count: 1, SpreadRadius: 60},
        },
    },
    // 2: side boss — elite brawler + escorts
    {
        Name: "Brawler Champion", Role: ChamberSideBoss,
        Members: []DungeonRosterMember{
            {Archetype: ArchetypeBrawler, Count: 1, SpreadRadius: 0, Elite: true},
            {Archetype: ArchetypeLancer, Count: 2, SpreadRadius: 60},
        },
    },
    // 3: side boss — elite artillery + escorts
    {
        Name: "Artillery Champion", Role: ChamberSideBoss,
        Members: []DungeonRosterMember{
            {Archetype: ArchetypeArtillery, Count: 1, SpreadRadius: 0, Elite: true},
            {Archetype: ArchetypeBrawler, Count: 2, SpreadRadius: 60},
        },
    },
    // 4: terminal — BossGuardian + escorts
    {
        Name: "The Guardian", Role: ChamberTerminal,
        Members: []DungeonRosterMember{
            {Archetype: ArchetypeBossGuardian, Count: 1, SpreadRadius: 0, Main: true},
            {Archetype: ArchetypeLancer, Count: 3, SpreadRadius: 100},
        },
    },
}

// dungeonNames is the procgen name pool, hash-selected on dungeon seed.
var dungeonNames = []string{
    "Stillveil Hollow",
    "Ashbore Cradle",
    "Verdigris Maw",
    "Roteye Reach",
    "Brackwhisper Pit",
    "Nethermerge Hollow",
    "Bonelight Drift",
    "Quartzgrave Bound",
}

// dungeonNameForSeed returns a stable name choice from the pool.
func dungeonNameForSeed(seed uint64) string {
    return dungeonNames[int(seed%uint64(len(dungeonNames)))]
}

// rosterIdxForRole picks a roster index for the given chamber role from
// a stable RNG-on-seed source. Deterministic per chamber.
func rosterIdxForRole(role ChamberRole, rngU64 uint64) uint16 {
    var candidates []int
    for i, r := range dungeonRosters {
        if r.Role == role {
            candidates = append(candidates, i)
        }
    }
    if len(candidates) == 0 {
        return 0
    }
    return uint16(candidates[int(rngU64%uint64(len(candidates)))])
}

// ArchetypeBossGuardian is registered in npc_archetype.go (Task 32).
// Declared here for cross-file reference; the actual archetype consts live there.
const ArchetypeBossGuardian uint8 = 3
```

- [ ] **Step 3: Run build**

```
just build
```
Expected: PASS (ArchetypeBossGuardian is forward-declared; the const value 3 must not collide with existing archetypes — confirm `ArchetypeBrawler=0, Artillery=1, Lancer=2`).

- [ ] **Step 4: Commit**

```
git add internal/game/dungeon_chamber.go internal/game/dungeon_config.go
git commit -m "feat(dungeon): add ChamberState + roster + name tables"
```

### Task 20: Procgen graph generation

**Files:**
- Create: `internal/game/dungeon_gen.go`
- Create: `internal/game/dungeon_gen_test.go`

- [ ] **Step 1: Write failing tests**

```go
// internal/game/dungeon_gen_test.go
package game

import (
    "math"
    "math/rand/v2"
    "testing"
)

func TestDungeonGen_DeterministicGraph(t *testing.T) {
    cfg := &GameConfig{
        DungeonChamberCountMin:  5,
        DungeonChamberCountMax:  8,
        DungeonChamberRadiusMin: 120,
        DungeonChamberRadiusMax: 240,
        DungeonAsteroidRadius:   1800,
        DungeonCorridorWidth:    100,
        DungeonEntranceCount:    3,
    }
    g1 := buildDungeonGraph(cfg, 12345)
    g2 := buildDungeonGraph(cfg, 12345)
    if len(g1.chambers) != len(g2.chambers) {
        t.Fatalf("seed 12345 produced different chamber counts")
    }
    for i := range g1.chambers {
        if g1.chambers[i].role != g2.chambers[i].role {
            t.Fatalf("seed 12345 produced different chamber roles at %d", i)
        }
    }
}

func TestDungeonGen_ChamberCountInRange(t *testing.T) {
    cfg := &GameConfig{
        DungeonChamberCountMin:  5,
        DungeonChamberCountMax:  8,
        DungeonChamberRadiusMin: 120,
        DungeonChamberRadiusMax: 240,
        DungeonAsteroidRadius:   1800,
        DungeonCorridorWidth:    100,
        DungeonEntranceCount:    3,
    }
    for seed := uint64(0); seed < 100; seed++ {
        g := buildDungeonGraph(cfg, seed)
        n := len(g.chambers)
        if n < cfg.DungeonChamberCountMin || n > cfg.DungeonChamberCountMax {
            t.Fatalf("seed %d produced chamber count %d outside [%d,%d]", seed, n, cfg.DungeonChamberCountMin, cfg.DungeonChamberCountMax)
        }
    }
}

func TestDungeonGen_ExactlyOneEntryAndOneTerminal(t *testing.T) {
    cfg := minimalDungeonCfg()
    for seed := uint64(0); seed < 50; seed++ {
        g := buildDungeonGraph(cfg, seed)
        entryCount, termCount := 0, 0
        for _, c := range g.chambers {
            if c.isEntry {
                entryCount++
            }
            if c.role == ChamberTerminal {
                termCount++
            }
        }
        if entryCount != 1 {
            t.Fatalf("seed %d: expected 1 entry chamber, got %d", seed, entryCount)
        }
        if termCount != 1 {
            t.Fatalf("seed %d: expected 1 terminal chamber, got %d", seed, termCount)
        }
    }
}

func minimalDungeonCfg() *GameConfig {
    return &GameConfig{
        DungeonChamberCountMin:  5,
        DungeonChamberCountMax:  8,
        DungeonChamberRadiusMin: 120,
        DungeonChamberRadiusMax: 240,
        DungeonAsteroidRadius:   1800,
        DungeonCorridorWidth:    100,
        DungeonEntranceCount:    3,
    }
}

// silence unused import warning during build
var _ = rand.Uint64
var _ = math.Pi
```

- [ ] **Step 2: Run to verify failure**

```
go test ./internal/game/ -run TestDungeonGen
```
Expected: FAIL (`buildDungeonGraph` undefined).

- [ ] **Step 3: Implement graph + layout**

```go
// internal/game/dungeon_gen.go
package game

import (
    "hash/fnv"
    "math"
    "math/rand/v2"

    "github.com/zenion/mmokit/pkg/spatial"
)

// dungeonGraph holds the procgen tree of chambers + their adjacency edges.
type dungeonGraph struct {
    chambers []dungeonChamber
    edges    []dungeonEdge // parent → child connectivity
    name     string
    seed     uint64
}

type dungeonChamber struct {
    id      uint16
    center  spatial.Vec2
    radius  float32
    role    ChamberRole
    isEntry bool
    parent  int // index into chambers; -1 for root/entry
}

type dungeonEdge struct {
    a, b int // chamber indices
}

// DungeonSeed derives the cell-stable seed for a dungeon at (cellX,cellY).
func DungeonSeed(cellX, cellY int32) uint64 {
    h := fnv.New64a()
    var buf [16]byte
    for i, v := range []int32{cellX, cellY} {
        buf[i*4] = byte(v)
        buf[i*4+1] = byte(v >> 8)
        buf[i*4+2] = byte(v >> 16)
        buf[i*4+3] = byte(v >> 24)
    }
    h.Write(buf[:8])
    h.Write([]byte("dungeon"))
    return h.Sum64()
}

// buildDungeonGraph generates a procgen tree of chambers with deterministic
// roles + a radial-layout placement.
func buildDungeonGraph(cfg *GameConfig, seed uint64) *dungeonGraph {
    rng := rand.New(rand.NewPCG(seed, 0))
    n := cfg.DungeonChamberCountMin + rng.IntN(cfg.DungeonChamberCountMax-cfg.DungeonChamberCountMin+1)

    g := &dungeonGraph{
        chambers: make([]dungeonChamber, n),
        name:     dungeonNameForSeed(seed),
        seed:     seed,
    }

    // Build a random spanning tree: chamber 0 = entry (root), each
    // subsequent chamber attaches to a random existing chamber.
    g.chambers[0] = dungeonChamber{id: 0, isEntry: true, parent: -1}
    for i := 1; i < n; i++ {
        parent := rng.IntN(i)
        g.chambers[i] = dungeonChamber{id: uint16(i), parent: parent}
        g.edges = append(g.edges, dungeonEdge{a: parent, b: i})
    }

    // Pick the leaf farthest from entry (graph distance) as terminal.
    distances := bfsDistances(g, 0)
    terminalIdx := 0
    for i, d := range distances {
        if g.isLeaf(i) && d > distances[terminalIdx] {
            terminalIdx = i
        }
    }
    g.chambers[terminalIdx].role = ChamberTerminal

    // Pick 2-3 other leaves as side-boss chambers.
    var leaves []int
    for i := range g.chambers {
        if i == terminalIdx || i == 0 {
            continue
        }
        if g.isLeaf(i) {
            leaves = append(leaves, i)
        }
    }
    // shuffle leaves
    rng.Shuffle(len(leaves), func(i, j int) { leaves[i], leaves[j] = leaves[j], leaves[i] })
    sideBossTarget := 2 + rng.IntN(2) // 2 or 3
    if sideBossTarget > len(leaves) {
        sideBossTarget = len(leaves)
    }
    for i := 0; i < sideBossTarget; i++ {
        g.chambers[leaves[i]].role = ChamberSideBoss
    }
    // remaining chambers (non-entry, non-terminal, non-sideboss): mob packs (default 0)

    // Radial layout: entry at the asteroid surface (south-facing toward station),
    // terminal at the opposite side, others radially distributed.
    layoutGraph(g, cfg, rng)

    // Assign roster indices per chamber role using a derivative RNG.
    for i := range g.chambers {
        roleSeed := seed ^ uint64(i+1)*0x9e3779b97f4a7c15
        g.chambers[i].radius = cfg.DungeonChamberRadiusMin +
            rng.Float32()*(cfg.DungeonChamberRadiusMax-cfg.DungeonChamberRadiusMin)
        _ = roleSeed
    }

    return g
}

func (g *dungeonGraph) isLeaf(idx int) bool {
    if idx == 0 {
        // entry is leaf only if it has no children
    }
    for _, e := range g.edges {
        if e.a == idx {
            return false
        }
    }
    return true
}

func bfsDistances(g *dungeonGraph, root int) []int {
    d := make([]int, len(g.chambers))
    for i := range d {
        d[i] = -1
    }
    d[root] = 0
    queue := []int{root}
    for len(queue) > 0 {
        cur := queue[0]
        queue = queue[1:]
        for _, e := range g.edges {
            var other int
            if e.a == cur {
                other = e.b
            } else if e.b == cur {
                other = e.a
            } else {
                continue
            }
            if d[other] == -1 {
                d[other] = d[cur] + 1
                queue = append(queue, other)
            }
        }
    }
    return d
}

// layoutGraph places each chamber's center inside the asteroid via a
// random radial walk from the entry chamber.
func layoutGraph(g *dungeonGraph, cfg *GameConfig, rng *rand.Rand) {
    // entry at south interior surface
    margin := cfg.DungeonChamberRadiusMax + cfg.DungeonWallThickness
    g.chambers[0].center = spatial.Vec2{X: 0, Y: -(cfg.DungeonAsteroidRadius - margin)}

    // BFS layout: each child placed at offset+random direction from its parent.
    visited := make([]bool, len(g.chambers))
    visited[0] = true
    queue := []int{0}
    for len(queue) > 0 {
        cur := queue[0]
        queue = queue[1:]
        for _, e := range g.edges {
            var child int
            if e.a == cur {
                child = e.b
            } else if e.b == cur {
                child = e.a
            } else {
                continue
            }
            if visited[child] {
                continue
            }
            visited[child] = true
            // child placed at distance ~ 2*chamberRadius + corridorLen from parent
            corridorLen := float32(150) + rng.Float32()*150
            angle := rng.Float64() * 2 * math.Pi
            d := cfg.DungeonChamberRadiusMax + corridorLen
            g.chambers[child].center = spatial.Vec2{
                X: g.chambers[cur].center.X + d*float32(math.Cos(angle)),
                Y: g.chambers[cur].center.Y + d*float32(math.Sin(angle)),
            }
            queue = append(queue, child)
        }
    }
    _ = margin
}
```

- [ ] **Step 4: Run to verify pass**

```
go test ./internal/game/ -run TestDungeonGen
```
Expected: PASS.

- [ ] **Step 5: Commit**

```
git add internal/game/dungeon_gen.go internal/game/dungeon_gen_test.go
git commit -m "feat(dungeon): procgen graph + radial layout"
```

### Task 21: Procgen walls (outline-only)

**Files:**
- Modify: `internal/game/dungeon_gen.go`
- Modify: `internal/game/dungeon_gen_test.go`

- [ ] **Step 1: Write failing test**

```go
func TestDungeonGen_WallCountUnder40(t *testing.T) {
    cfg := minimalDungeonCfg()
    cfg.DungeonWallThickness = 30
    for seed := uint64(0); seed < 30; seed++ {
        g := buildDungeonGraph(cfg, seed)
        walls := generateWalls(g, cfg)
        if len(walls) > 40 {
            t.Fatalf("seed %d produced %d walls (>40)", seed, len(walls))
        }
    }
}
```

- [ ] **Step 2: Run to verify failure**

```
go test ./internal/game/ -run TestDungeonGen_WallCountUnder40
```
Expected: FAIL (`generateWalls` undefined).

- [ ] **Step 3: Implement**

Add to `dungeon_gen.go`:

```go
// wallSpec describes one rect-collider wall to be spawned.
type wallSpec struct {
    Center   spatial.Vec2
    Width    float32
    Height   float32
    Rotation float32 // radians
}

// generateWalls produces the outline walls for the dungeon: a ring of
// segments approximating the asteroid perimeter (with cave-mouth gaps
// for entrances) + per-corridor segments lining each parent→child edge.
//
// Outline-only — interior space outside chambers/corridors is "void"
// and gets no wall colliders. Target total: < 40 walls per dungeon.
func generateWalls(g *dungeonGraph, cfg *GameConfig) []wallSpec {
    var walls []wallSpec

    // Perimeter ring: 16 arc segments around the asteroid, but skip
    // any segment whose angular slot falls inside an entrance gap.
    const ringSegments = 16
    entranceAngles := pickEntranceAngles(g, cfg, ringSegments)
    arcLen := 2 * math.Pi / ringSegments
    segLen := cfg.DungeonAsteroidRadius * float32(arcLen) // chord length approx
    for s := 0; s < ringSegments; s++ {
        if isEntranceSlot(s, entranceAngles) {
            continue
        }
        angle := float32(arcLen)*float32(s) + float32(arcLen)/2
        cx := cfg.DungeonAsteroidRadius * float32(math.Cos(float64(angle)))
        cy := cfg.DungeonAsteroidRadius * float32(math.Sin(float64(angle)))
        walls = append(walls, wallSpec{
            Center: spatial.Vec2{X: cx, Y: cy},
            Width:  segLen + 10, // slight overlap to avoid LOS leaks
            Height: cfg.DungeonWallThickness,
            Rotation: angle + float32(math.Pi/2),
        })
    }

    // Corridor walls: two parallel walls per edge, sitting `CorridorWidth`
    // apart, length = distance between chamber centers minus chamber radii.
    for _, e := range g.edges {
        a := g.chambers[e.a]
        b := g.chambers[e.b]
        dx := b.center.X - a.center.X
        dy := b.center.Y - a.center.Y
        edgeLen := float32(math.Hypot(float64(dx), float64(dy)))
        if edgeLen < 1 {
            continue
        }
        dirX := dx / edgeLen
        dirY := dy / edgeLen
        // perpendicular
        pX := -dirY
        pY := dirX
        // shorten so corridor doesn't extend into chambers
        startGap := a.radius
        endGap := b.radius
        corridorActualLen := edgeLen - startGap - endGap
        if corridorActualLen <= 0 {
            continue
        }
        midX := a.center.X + dirX*(startGap+corridorActualLen/2)
        midY := a.center.Y + dirY*(startGap+corridorActualLen/2)
        rotation := float32(math.Atan2(float64(dirY), float64(dirX)))
        offset := cfg.DungeonCorridorWidth/2 + cfg.DungeonWallThickness/2

        // wall on each side
        walls = append(walls, wallSpec{
            Center: spatial.Vec2{X: midX + pX*offset, Y: midY + pY*offset},
            Width:  corridorActualLen,
            Height: cfg.DungeonWallThickness,
            Rotation: rotation,
        })
        walls = append(walls, wallSpec{
            Center: spatial.Vec2{X: midX - pX*offset, Y: midY - pY*offset},
            Width:  corridorActualLen,
            Height: cfg.DungeonWallThickness,
            Rotation: rotation,
        })
    }

    return walls
}

// pickEntranceAngles returns the ring-slot indices that are entrance gaps.
// Entrance 0 always points toward the entry-chamber's connection to the
// perimeter (south-ish, toward the station).
func pickEntranceAngles(g *dungeonGraph, cfg *GameConfig, ringSegments int) []int {
    // Entrance 0: south (angle ≈ -π/2 → slot at 3π/2)
    base := int(float64(ringSegments) * 0.75) % ringSegments
    out := []int{base}
    for i := 1; i < cfg.DungeonEntranceCount; i++ {
        out = append(out, (base+ringSegments*i/cfg.DungeonEntranceCount)%ringSegments)
    }
    return out
}

func isEntranceSlot(s int, gaps []int) bool {
    for _, g := range gaps {
        if g == s {
            return true
        }
    }
    return false
}
```

- [ ] **Step 4: Run to verify pass**

```
go test ./internal/game/ -run TestDungeonGen_WallCountUnder40
```
Expected: PASS.

- [ ] **Step 5: Commit**

```
git add internal/game/dungeon_gen.go internal/game/dungeon_gen_test.go
git commit -m "feat(dungeon): generate outline-only wall colliders for cave geometry"
```

### Task 22: Procgen reachability test

**Files:**
- Modify: `internal/game/dungeon_gen_test.go`

- [ ] **Step 1: Write the reachability test**

```go
// Every chamber must be reachable from chamber 0 via the edge graph.
func TestDungeonGen_AllChambersReachable(t *testing.T) {
    cfg := minimalDungeonCfg()
    for seed := uint64(0); seed < 1000; seed++ {
        g := buildDungeonGraph(cfg, seed)
        d := bfsDistances(g, 0)
        for i, dist := range d {
            if dist < 0 {
                t.Fatalf("seed %d: chamber %d unreachable", seed, i)
            }
        }
    }
}
```

- [ ] **Step 2: Run**

```
go test ./internal/game/ -run TestDungeonGen_AllChambersReachable
```
Expected: PASS (a tree by construction is connected).

- [ ] **Step 3: Commit**

```
git add internal/game/dungeon_gen_test.go
git commit -m "test(dungeon): assert reachability for 1000 seeds"
```

---

## Phase 7 — Chamber Lifecycle

### Task 23: GameWorld dungeon-chamber map

**Files:**
- Modify: `internal/game/gameworld.go`

- [ ] **Step 1: Add field + init**

```go
// In GameWorld:
type GameWorld struct {
    // ... existing fields ...
    dungeonChambers map[uint32]map[uint16]*ChamberState // dungeonNetID → chamberID → state
}
```

In `NewGameWorld`, initialize:

```go
gw.dungeonChambers = make(map[uint32]map[uint16]*ChamberState)
```

- [ ] **Step 2: Build + commit**

```
just build
git add internal/game/gameworld.go
git commit -m "feat(gameworld): track per-dungeon chamber state map"
```

### Task 24: SpawnDungeonFromGraph orchestration

**Files:**
- Modify: `internal/game/entity_dungeon.go`

- [ ] **Step 1: Write test that spawns a procgen dungeon and asserts entity counts**

```go
func TestSpawnDungeonFromGraph_SpawnsExpectedEntities(t *testing.T) {
    // Pseudocode:
    //   gw := newTestGW(t)
    //   gw.SpawnDungeonFromGraph(0, 0, 12345)
    //   require.Equal(t, 1, count(gw, gamecomp.KindDungeon))
    //   require.GreaterOrEqual(t, count(gw, gamecomp.KindDungeonWall), 10)
    //   require.GreaterOrEqual(t, count(gw, gamecomp.KindNPC), 10)
}
```

- [ ] **Step 2: Implement**

```go
// internal/game/entity_dungeon.go
func (gw *GameWorld) SpawnDungeonFromGraph(centerX, centerY float32, seed uint64) uint32 {
    g := buildDungeonGraph(gw.Config, seed)

    entrance0 := spatial.Vec2{X: 0, Y: -gw.Config.DungeonAsteroidRadius}
    entrance1 := spatial.Vec2{}
    entrance2 := spatial.Vec2{}
    if gw.Config.DungeonEntranceCount >= 2 {
        entrance1 = spatial.Vec2{
            X: gw.Config.DungeonAsteroidRadius * float32(math.Cos(math.Pi/6)),
            Y: -gw.Config.DungeonAsteroidRadius * float32(math.Sin(math.Pi/6)),
        }
    }
    if gw.Config.DungeonEntranceCount >= 3 {
        entrance2 = spatial.Vec2{
            X: -gw.Config.DungeonAsteroidRadius * float32(math.Cos(math.Pi/6)),
            Y: -gw.Config.DungeonAsteroidRadius * float32(math.Sin(math.Pi/6)),
        }
    }

    dungeonNetID := gw.SpawnDungeon(centerX, centerY, gamecomp.Dungeon{
        Name:          g.name,
        OuterRadius:   gw.Config.DungeonAsteroidRadius,
        EntranceCount: uint8(gw.Config.DungeonEntranceCount),
        EntranceX0:    entrance0.X, EntranceY0: entrance0.Y,
        EntranceX1:    entrance1.X, EntranceY1: entrance1.Y,
        EntranceX2:    entrance2.X, EntranceY2: entrance2.Y,
        Seed:          seed,
    })

    // Spawn walls
    for _, w := range generateWalls(g, gw.Config) {
        gw.SpawnDungeonWall(centerX+w.Center.X, centerY+w.Center.Y, w.Width, w.Height, w.Rotation)
    }

    // Spawn chambers + rosters
    chambers := make(map[uint16]*ChamberState)
    for _, c := range g.chambers {
        rngU64 := seed ^ uint64(c.id+1)*0xbf58476d1ce4e5b9
        rosterIdx := rosterIdxForRole(c.role, rngU64)
        state := &ChamberState{
            ID: c.id, Center: spatial.Vec2{X: centerX + c.center.X, Y: centerY + c.center.Y},
            Radius: c.radius, Role: c.role, Status: ChamberActive, RosterDefIdx: rosterIdx,
        }
        chambers[c.id] = state
        gw.spawnChamberRoster(dungeonNetID, state)
    }
    gw.dungeonChambers[dungeonNetID] = chambers

    gw.eng.Log.Log(CatDungeonGen, "dungeon procgen: seed=%d chambers=%d walls=%d entrances=%d",
        seed, len(chambers), len(generateWalls(g, gw.Config)), gw.Config.DungeonEntranceCount)
    return dungeonNetID
}

func (gw *GameWorld) spawnChamberRoster(dungeonNetID uint32, c *ChamberState) {
    roster := dungeonRosters[c.RosterDefIdx]
    rng := rand.New(rand.NewPCG(uint64(dungeonNetID), uint64(c.ID)+uint64(c.RespawnCount)))
    for _, m := range roster.Members {
        for i := 0; i < m.Count; i++ {
            angle := rng.Float64() * 2 * math.Pi
            r := rng.Float32() * m.SpreadRadius
            px := c.Center.X + r*float32(math.Cos(angle))
            py := c.Center.Y + r*float32(math.Sin(angle))
            netID := gw.SpawnNPC(px, py, m.Archetype, dungeonNetID)
            // Tag the NPC's DungeonAnchor with the chamber ID (extend SpawnNPC
            // if needed, or set after spawn).
            if e, ok := gw.NetIDToEntity[netID]; ok {
                anchor := mmokit.Get[gamecomp.DungeonAnchor](e)
                anchor.ChamberID = c.ID
            }
            c.AliveNetIDs = append(c.AliveNetIDs, netID)
        }
    }
}
```

- [ ] **Step 3: Hook into cell bootstrap**

In the place where belts/POIs are spawned (search for `spawnPOIs` or `spawnAsteroids`), add a call:

```go
if gw.cellCoord.CellX == int32(gw.Config.DungeonTestsiteCellX) &&
   gw.cellCoord.CellY == int32(gw.Config.DungeonTestsiteCellY) {
    x := StationLocalX + gw.Config.DungeonTestsiteOffsetX
    y := StationLocalY + gw.Config.DungeonTestsiteOffsetY
    gw.SpawnDungeonFromGraph(x, y, DungeonSeed(gw.cellCoord.CellX, gw.cellCoord.CellY))
}
```

- [ ] **Step 4: Run tests**

```
go test ./internal/game/ -run TestSpawnDungeon
```
Expected: PASS.

- [ ] **Step 5: Commit**

```
git add internal/game/entity_dungeon.go internal/game/gameworld.go
git commit -m "feat(dungeon): orchestrate procgen spawn (entity + walls + rosters)"
```

### Task 25: DungeonChamberSystem skeleton

**Files:**
- Create: `internal/game/system_dungeon_chamber.go`
- Modify: `internal/game/factory.go`

- [ ] **Step 1: Write failing test**

```go
// internal/game/system_dungeon_chamber_test.go
func TestChamberSystem_ActiveToClearedOnRosterEmpty(t *testing.T) {
    // Spawn dungeon, kill every NPC in chamber 0, run a tick.
    // Assert chamber.Status == ChamberCleared and a chest exists at chamber.LastKillPos.
}
```

- [ ] **Step 2: Run failing**

```
go test ./internal/game/ -run TestChamberSystem_ActiveToCleared
```
Expected: FAIL.

- [ ] **Step 3: Implement**

```go
// internal/game/system_dungeon_chamber.go
package game

import (
    "time"

    "github.com/zenion/mmokit/internal/component"
    "github.com/zenion/mmokit/pkg/mmokit"
)

// DungeonChamberSystem drives per-chamber lifecycle: Active → Cleared
// (on roster death) → Cooldown → Active (after cooldown).
type DungeonChamberSystem struct {
    mmokit.SystemBase
}

func (s *DungeonChamberSystem) Update(dt float32) {
    gw := mmokit.WorldOf[*GameWorld](s)
    now := time.Now().UnixNano()

    for dungeonNetID, chambers := range gw.dungeonChambers {
        for _, c := range chambers {
            s.tickChamber(gw, dungeonNetID, c, now)
        }
    }
}

func (s *DungeonChamberSystem) tickChamber(gw *GameWorld, dungeonNetID uint32, c *ChamberState, now int64) {
    switch c.Status {
    case ChamberActive:
        // Prune dead from AliveNetIDs.
        alive := c.AliveNetIDs[:0]
        for _, id := range c.AliveNetIDs {
            if e, ok := gw.NetIDToEntity[id]; ok && e.IsAlive() {
                alive = append(alive, id)
            } else {
                // Record kill position for the chest spawn.
                if e, ok := gw.NetIDToEntity[id]; ok {
                    pos := mmokit.Get[mmokit.Position](e)
                    c.LastKillPos = spatial.Vec2{X: pos.X, Y: pos.Y}
                }
            }
        }
        c.AliveNetIDs = alive
        if len(alive) == 0 {
            c.Status = ChamberCleared
            c.ClearedAt = now
            gw.eng.Log.Log(CatDungeon, "chamber cleared: dungeon=%d chamber=%d role=%d",
                dungeonNetID, c.ID, c.Role)
        }

    case ChamberCleared:
        // Drop the chest, transition to cooldown.
        flux := gw.Config.ChamberMobPackFluxBase
        switch c.Role {
        case ChamberSideBoss:
            flux = gw.Config.ChamberSideBossFluxBase
        case ChamberTerminal:
            flux = gw.Config.ChamberTerminalBossFluxBase
        }
        pos := c.LastKillPos
        if pos.X == 0 && pos.Y == 0 {
            pos = c.Center
        }
        c.LastChestNetID = gw.SpawnLootCrate(pos.X, pos.Y, int64(flux))
        c.Status = ChamberCooldown
        gw.eng.Log.Log(CatDungeon, "chamber chest spawned: dungeon=%d chamber=%d flux=%.0f",
            dungeonNetID, c.ID, flux)

    case ChamberCooldown:
        cooldown := chamberCooldownFor(gw.Config, c.Role)
        if float32(now-c.ClearedAt)/1e9 < cooldown {
            return
        }
        // Despawn old chest if still present.
        if c.LastChestNetID != 0 {
            if e, ok := gw.NetIDToEntity[c.LastChestNetID]; ok && e.IsAlive() {
                gw.stage.Commands().Despawn(e.Handle())
            }
            c.LastChestNetID = 0
        }
        // Respawn roster (using updated RespawnCount for re-roll).
        c.RespawnCount++
        // Re-roll roster index using new seed.
        rngU64 := uint64(dungeonNetID) ^ uint64(c.ID+1)*0xbf58476d1ce4e5b9 ^ uint64(c.RespawnCount)
        c.RosterDefIdx = rosterIdxForRole(c.Role, rngU64)
        c.AliveNetIDs = nil
        c.ClearedAt = 0
        c.LastKillPos = spatial.Vec2{}
        gw.spawnChamberRoster(dungeonNetID, c)
        c.Status = ChamberActive
        gw.eng.Log.Log(CatDungeon, "chamber respawned: dungeon=%d chamber=%d respawn=%d",
            dungeonNetID, c.ID, c.RespawnCount)
    }
}

func chamberCooldownFor(cfg *GameConfig, role ChamberRole) float32 {
    switch role {
    case ChamberSideBoss:
        return cfg.ChamberCooldownSideBoss
    case ChamberTerminal:
        return cfg.ChamberCooldownTerminal
    default:
        return cfg.ChamberCooldownMobPack
    }
}
```

Register in `factory.go` after `POISystem`:

```go
coord.AddSystem(mmokit.NewSystem(&DungeonChamberSystem{}))
```

- [ ] **Step 4: Run tests**

```
go test ./internal/game/ -run TestChamberSystem
```
Expected: PASS.

- [ ] **Step 5: Commit**

```
git add internal/game/system_dungeon_chamber.go internal/game/system_dungeon_chamber_test.go internal/game/factory.go
git commit -m "feat(dungeon): chamber lifecycle system (Active → Cleared → Cooldown → Active)"
```

---

## Phase 8 — NavGrid & Pathfinding Integration

### Task 26: NavGrid construction at dungeon procgen

**Files:**
- Create: `internal/game/dungeon_navgrid.go`
- Create: `internal/game/dungeon_navgrid_test.go`

- [ ] **Step 1: Write failing tests**

```go
// internal/game/dungeon_navgrid_test.go
func TestDungeonNavGrid_AllChambersReachable(t *testing.T) {
    cfg := minimalDungeonCfg()
    cfg.NavGridCellSize = 30
    for seed := uint64(0); seed < 50; seed++ {
        g := buildDungeonGraph(cfg, seed)
        walls := generateWalls(g, cfg)
        ng := buildDungeonNavGrid(g, walls, cfg)
        // From entry chamber's center, A* should reach every other chamber center.
        for i := 1; i < len(g.chambers); i++ {
            path := pathfinding.AStar(ng,
                pathfinding.Vec2{X: g.chambers[0].center.X, Y: g.chambers[0].center.Y},
                pathfinding.Vec2{X: g.chambers[i].center.X, Y: g.chambers[i].center.Y},
            )
            if path == nil {
                t.Fatalf("seed %d: chamber %d unreachable on NavGrid", seed, i)
            }
        }
    }
}
```

- [ ] **Step 2: Run failing**

```
go test ./internal/game/ -run TestDungeonNavGrid
```
Expected: FAIL.

- [ ] **Step 3: Implement**

```go
// internal/game/dungeon_navgrid.go
package game

import (
    "math"

    "github.com/zenion/mmokit/pkg/pathfinding"
    "github.com/zenion/mmokit/pkg/spatial"
)

// buildDungeonNavGrid rasterizes the dungeon's walkable area into a NavGrid.
// A cell is walkable iff its center is inside the asteroid silhouette AND
// not inside any wall rect.
func buildDungeonNavGrid(g *dungeonGraph, walls []wallSpec, cfg *GameConfig) *pathfinding.NavGrid {
    cell := cfg.NavGridCellSize
    r := cfg.DungeonAsteroidRadius
    // bounding square from -r to r, padded by 1 cell.
    w := int(math.Ceil(float64(2*r/cell))) + 2
    h := w
    origin := pathfinding.Vec2{X: -r - cell, Y: -r - cell}
    ng := pathfinding.NewNavGrid(origin, cell, w, h)
    for cy := 0; cy < h; cy++ {
        for cx := 0; cx < w; cx++ {
            center := ng.CellToWorld(cx, cy)
            // Outside asteroid radius → block.
            if center.X*center.X+center.Y*center.Y > r*r {
                ng.Block(cx, cy)
                continue
            }
            // Inside any wall rect → block.
            if cellInsideAnyWall(center, walls) {
                ng.Block(cx, cy)
                continue
            }
        }
    }
    return ng
}

func cellInsideAnyWall(p pathfinding.Vec2, walls []wallSpec) bool {
    for _, w := range walls {
        if pointInRect(p, w) {
            return true
        }
    }
    return false
}

func pointInRect(p pathfinding.Vec2, w wallSpec) bool {
    cosA := float32(math.Cos(float64(-w.Rotation)))
    sinA := float32(math.Sin(float64(-w.Rotation)))
    fx := p.X - w.Center.X
    fy := p.Y - w.Center.Y
    lx := cosA*fx - sinA*fy
    ly := sinA*fx + cosA*fy
    return math.Abs(float64(lx)) <= float64(w.Width/2) &&
        math.Abs(float64(ly)) <= float64(w.Height/2)
}

// _ = spatial.Vec2{} // keep import alive
var _ = spatial.Vec2{}
```

Wire it into `SpawnDungeonFromGraph` to cache the NavGrid on the dungeon — store on the `GameWorld` map keyed by dungeon netID:

```go
// in GameWorld:
dungeonNavGrids map[uint32]*pathfinding.NavGrid

// in NewGameWorld:
gw.dungeonNavGrids = make(map[uint32]*pathfinding.NavGrid)

// in SpawnDungeonFromGraph, after wall generation:
gw.dungeonNavGrids[dungeonNetID] = buildDungeonNavGrid(g, walls, gw.Config)
```

- [ ] **Step 4: Run tests**

```
go test ./internal/game/ -run TestDungeonNavGrid
```
Expected: PASS.

- [ ] **Step 5: Commit**

```
git add internal/game/dungeon_navgrid.go internal/game/dungeon_navgrid_test.go internal/game/gameworld.go internal/game/entity_dungeon.go
git commit -m "feat(dungeon): build per-dungeon NavGrid at procgen time"
```

### Task 27: MotionPolicy.Pathfind in NPC AI

**Files:**
- Modify: `internal/game/npc_archetype.go` (add MotionPathfind constant)
- Modify: `internal/component/components.go` (add `Pathing` component)
- Modify: `internal/game/entity_npc.go` (attach Pathing for dungeon NPCs)
- Modify: `internal/game/system_npc_ai.go` (add Pathfind case to motion switch)

- [ ] **Step 1: Write failing test**

```go
// NPC with MotionPathfind, given a target across a wall, must compute
// a path that goes around the wall rather than directly toward it.
func TestNPCAI_PathfindNavigatesAroundWall(t *testing.T) {
    // Pseudocode mirroring existing test scaffolding.
}
```

- [ ] **Step 2: Run failing**

```
go test ./internal/game/ -run TestNPCAI_Pathfind
```
Expected: FAIL.

- [ ] **Step 3: Implement**

In `npc_archetype.go`:

```go
const (
    MotionCharge     uint8 = 0
    MotionStationary uint8 = 1
    MotionPathfind   uint8 = 2
)
```

In `components.go`:

```go
// Pathing caches the NavGrid-derived waypoint list for an NPC under
// MotionPathfind. Local-only.
type Pathing struct {
    WaypointsX   []float32 `mmokit:"local"`
    WaypointsY   []float32 `mmokit:"local"`
    CurrentIdx   int       `mmokit:"local"`
    TargetX      float32   `mmokit:"local"`
    TargetY      float32   `mmokit:"local"`
    RepathAt     float32   `mmokit:"local"`
    DungeonNetID uint32    `mmokit:"local"`
}
```

In `system_npc_ai.go` Engage motion switch, add:

```go
case MotionPathfind:
    s.tickPathfindMotion(npcE, ai, npcPos, npcVel, targetPos)
```

Implementation:

```go
func (s *NPCAISystem) tickPathfindMotion(npcE mmokit.Entity, ai *gamecomp.NPCAI, pos *mmokit.Position, vel *mmokit.Velocity, targetPos mmokit.Position) {
    gw := mmokit.WorldOf[*GameWorld](s)
    p := mmokit.Get[gamecomp.Pathing](npcE)
    if p == nil {
        return // not anchored to a dungeon → fall back to no motion
    }
    ng, ok := gw.dungeonNavGrids[p.DungeonNetID]
    if !ok {
        return
    }
    stageTime := s.Stage().Time()
    // Repath if target moved significantly or repath timer expired.
    targetMoved := (targetPos.X-p.TargetX)*(targetPos.X-p.TargetX)+
        (targetPos.Y-p.TargetY)*(targetPos.Y-p.TargetY) >
        gw.Config.PathRepathTargetMovedThreshold*gw.Config.PathRepathTargetMovedThreshold
    if len(p.WaypointsX) == 0 || stageTime > p.RepathAt || targetMoved {
        path := pathfinding.AStar(ng,
            pathfinding.Vec2{X: pos.X, Y: pos.Y},
            pathfinding.Vec2{X: targetPos.X, Y: targetPos.Y})
        if path == nil {
            return
        }
        smoothed := pathfinding.SmoothLOS(path, losAdapter{grid: s.Stage().SpatialGrid()})
        p.WaypointsX = p.WaypointsX[:0]
        p.WaypointsY = p.WaypointsY[:0]
        for _, wp := range smoothed {
            p.WaypointsX = append(p.WaypointsX, wp.X)
            p.WaypointsY = append(p.WaypointsY, wp.Y)
        }
        p.CurrentIdx = 0
        p.TargetX = targetPos.X
        p.TargetY = targetPos.Y
        p.RepathAt = stageTime + gw.Config.PathRepathIntervalSec
    }
    if p.CurrentIdx >= len(p.WaypointsX) {
        vel.X, vel.Y = 0, 0
        return
    }
    wpX := p.WaypointsX[p.CurrentIdx]
    wpY := p.WaypointsY[p.CurrentIdx]
    dx := wpX - pos.X
    dy := wpY - pos.Y
    dist := float32(math.Hypot(float64(dx), float64(dy)))
    if dist < gw.Config.NavGridCellSize/2 {
        p.CurrentIdx++
        return
    }
    vel.X = ai.MaxSpeed * dx / dist
    vel.Y = ai.MaxSpeed * dy / dist
}

// losAdapter wraps spatial.HashGrid to satisfy pathfinding.LOSChecker.
type losAdapter struct{ grid *spatial.HashGrid }

func (a losAdapter) Clear(p, q pathfinding.Vec2) bool {
    return hasLOSOnGrid(a.grid, spatial.Vec2{X: p.X, Y: p.Y}, spatial.Vec2{X: q.X, Y: q.Y})
}
```

In `entity_npc.go` `SpawnNPC`, when `dungeonNetID != 0`, attach `Pathing` with `DungeonNetID = dungeonNetID` and switch `MotionPolicy` to `MotionPathfind` (override the archetype default).

- [ ] **Step 4: Run tests**

```
go test ./internal/game/
```
Expected: PASS.

- [ ] **Step 5: Commit**

```
git add internal/game/npc_archetype.go internal/component/components.go internal/game/entity_npc.go internal/game/system_npc_ai.go
git commit -m "feat(npc-ai): add MotionPathfind policy using per-dungeon NavGrid"
```

---

## Phase 9 — Boss Content

### Task 28: Solo elite variants (data only)

**Files:**
- Modify: `internal/game/entity_npc.go`

- [ ] **Step 1: Write failing test**

```go
func TestSpawnNPC_EliteStatsApplied(t *testing.T) {
    // SpawnNPC with an Elite RosterMember should produce an NPC whose
    // HP/damage/speed are multiplied by the configured boss-solo factors.
}
```

- [ ] **Step 2: Implement**

Extend `SpawnNPC` (or a sibling `SpawnNPCElite`) to take an `Elite bool` arg. When true, multiply `HP`, `Damage`, `MaxSpeed` by `BossSoloHPMultiplier`, `BossSoloDmgMultiplier`, `BossSoloSpeedMultiplier`. Scale visual via a tag/flag the client can use.

The `spawnChamberRoster` call site in Task 24 passes `m.Elite` through to the spawn function.

- [ ] **Step 3: Run + commit**

```
go test ./internal/game/
```
Expected: PASS.

```
git add internal/game/entity_npc.go
git commit -m "feat(npc): support elite stat multipliers for side-boss variants"
```

### Task 29: BossGuardian archetype

**Files:**
- Modify: `internal/game/npc_archetype.go`
- Modify: `internal/game/config.go`
- Modify: `internal/game/system_npc_ai.go`

- [ ] **Step 1: Write failing test**

```go
func TestBossGuardian_StationaryNeverMoves(t *testing.T) {
    // Spawn a BossGuardian, give it a target far away, tick for 2s,
    // assert position unchanged.
}
```

- [ ] **Step 2: Implement**

Add archetype constant + defaults:

```go
const ArchetypeBossGuardian uint8 = 3 // (already declared in dungeon_config.go — verify same value)

// In archetypeDefaults switch:
case ArchetypeBossGuardian:
    return ArchetypeDefaults{
        HP:             cfg.NPCBaseHP * cfg.BossMainHPMultiplier,
        Shield:         0,
        MaxSpeed:       0,
        TurnRate:       cfg.NPCBaseTurnRate * 0.5,
        PreferredRange: 400,
        WeaponRange:    600,
        AggroRadius:    1500,
        LockRange:      1500,
        MotionPolicy:   MotionStationary,
        DamagePerShot:  cfg.NPCBaseDamage * cfg.BossMainDmgMultiplier,
        FireRate:       0.5,
    }
```

- [ ] **Step 3: Run + commit**

```
go test ./internal/game/
```
Expected: PASS.

```
git add internal/game/npc_archetype.go internal/game/config.go
git commit -m "feat(npc): add BossGuardian archetype for dungeon terminals"
```

### Task 30: BossGuardian add-spawn mechanic

**Files:**
- Modify: `internal/game/system_npc_ai.go`
- Modify: `internal/component/components.go` (add `AddSpawnThresholdsHit` bitmask field to NPCAI)

- [ ] **Step 1: Write failing test**

```go
func TestBossGuardian_SpawnsAddsAtHPThresholds(t *testing.T) {
    // Spawn a BossGuardian. Damage it to 74% HP. Tick. Assert 2 Swarmer-style
    // NPCs spawned anchored to the same dungeon.
    // Damage further to 49% HP. Tick. Assert 2 more spawned.
}
```

- [ ] **Step 2: Implement**

In NPC AI, every BossGuardian's Engage tick checks HP fraction vs `BossMainAddSpawnThresholds`. For each unfired threshold the boss has crossed, spawn 2 NPCs (use existing `ArchetypeLancer` or fastest existing archetype as a "swarmer" stand-in; the spec mentions Swarmer but adaptation note covers using Lancer here) at the boss position. Mark the threshold "fired" via `AddSpawnThresholdsHit` bitmask.

- [ ] **Step 3: Run + commit**

```
go test ./internal/game/
```
Expected: PASS.

```
git add internal/game/system_npc_ai.go internal/component/components.go
git commit -m "feat(npc): BossGuardian spawns adds at HP thresholds"
```

---

## Phase 10 — Admin / cmdsys

### Task 31: dungeon.list verb

**Files:**
- Create: `internal/game/commands/dungeon.go`
- Modify: `internal/game/commands/registry.go`

- [ ] **Step 1: Implement following `poi.go` template**

Read `internal/game/commands/poi.go` for the existing pattern. Mirror it for `dungeon.list`:

```go
// Args struct + Result struct + Handler that returns []DungeonInfo via cmdsys.OnLoop.
// Each DungeonInfo includes: NetID, Name, ChamberCount, AliveChambers, CooldownsRemaining []float32.
```

- [ ] **Step 2: Register in registry.go**

- [ ] **Step 3: Commit**

```
git add internal/game/commands/dungeon.go internal/game/commands/registry.go
git commit -m "feat(cmd): add dungeon.list verb"
```

### Task 32: dungeon.respawn verb

**Files:**
- Modify: `internal/game/commands/dungeon.go`

- [ ] **Step 1: Add verb**

Args: `NetID uint32`, optional `ChamberID uint16` (0 = all). Handler forces every matching chamber to `Cleared` with `ClearedAt = 0` so the next tick triggers chest spawn + cooldown + respawn cycle immediately. Optionally short-circuit straight to the respawn step.

- [ ] **Step 2: Commit**

```
git add internal/game/commands/dungeon.go
git commit -m "feat(cmd): add dungeon.respawn verb"
```

### Task 33: dungeon.regenerate verb

**Files:**
- Modify: `internal/game/commands/dungeon.go`

- [ ] **Step 1: Add verb**

Args: `NetID uint32`. Handler tears down the existing dungeon entity + walls + NPCs + chests + nav-grid + chamber-state, then calls `SpawnDungeonFromGraph` with a fresh seed (timestamp-derived).

- [ ] **Step 2: Commit**

```
git add internal/game/commands/dungeon.go
git commit -m "feat(cmd): add dungeon.regenerate verb (debug-only re-procgen)"
```

### Task 34: dungeon.spawn verb

**Files:**
- Modify: `internal/game/commands/dungeon.go`

- [ ] **Step 1: Add verb**

Args: `CellX, CellY int32`. Handler dispatches via `cmdsys.OnLoop` to the target cell and calls `SpawnDungeonFromGraph` at the cell's center + offset.

- [ ] **Step 2: Commit**

```
git add internal/game/commands/dungeon.go
git commit -m "feat(cmd): add dungeon.spawn verb"
```

---

## Phase 11 — Client Rendering

### Task 35: Regenerate TS SDK

**Files:**
- (auto-generated by `just build`)

- [ ] **Step 1: Run regen**

```
just build
```

Expected: TS SDK at `web-pixi/sdk/` includes new `Dungeon`, `DungeonWall` types + new server-event payloads. No new wire-type files to author by hand.

- [ ] **Step 2: Commit regenerated SDK if it's in git**

```
git status
# if SDK files are tracked:
git add web-pixi/sdk/
git commit -m "chore(sdk): regenerate TS SDK for dungeon entity kinds"
```

### Task 36: Asteroid silhouette renderer

**Files:**
- Create: `web-pixi/src/entities/dungeon.ts`
- Modify: `web-pixi/src/entities/index.ts` (or equivalent registry)

- [ ] **Step 1: Implement renderer**

Read an existing entity renderer (e.g. `entities/station.ts`) for the pattern. The dungeon renderer draws a large irregular dark-gray circle of radius `OuterRadius` with cave-mouth notches cut at `(EntranceX0, EntranceY0)`, `(EntranceX1, EntranceY1)`, `(EntranceX2, EntranceY2)` (if `EntranceCount >= 2/3`). Each notch is a small triangular gap pointing radially outward.

For v1: a `PIXI.Graphics` shape using `arc()` + cut-out triangles via even-odd fill.

- [ ] **Step 2: Register renderer**

In the entity-renderer registry, map `KindDungeon` → the new renderer factory.

- [ ] **Step 3: Smoke + commit**

Run `just dev` in a separate terminal (NOT here — the user runs it; per memory: "No leftover processes"). Verify in the browser the dungeon renders as a circle with cave mouths.

```
git add web-pixi/src/entities/dungeon.ts web-pixi/src/entities/index.ts
git commit -m "feat(web): render dungeon asteroid silhouette with cave mouths"
```

### Task 37: Dungeon-wall renderer

**Files:**
- Create: `web-pixi/src/entities/dungeon-wall.ts`
- Modify: `web-pixi/src/entities/index.ts`

- [ ] **Step 1: Implement renderer**

Renders a textured (rock-look) rectangle of dimensions `Width × Height` rotated by `Rotation`. Static — no per-tick update needed.

- [ ] **Step 2: Register + commit**

```
git add web-pixi/src/entities/dungeon-wall.ts web-pixi/src/entities/index.ts
git commit -m "feat(web): render dungeon-wall rectangles"
```

### Task 38: Map marker

**Files:**
- Modify: `web-pixi/src/map.ts` (or equivalent map view file)

- [ ] **Step 1: Add dungeon marker**

When iterating entities for the map view, render a distinct icon (e.g. a stylized cave-mouth glyph) at the dungeon position with the dungeon's `Name` as label.

- [ ] **Step 2: Commit**

```
git add web-pixi/src/map.ts
git commit -m "feat(web): add dungeon map marker with name label"
```

### Task 39: Inside-dungeon visual

**Files:**
- Create: `web-pixi/src/dungeon-overlay.ts`
- Modify: main render loop (wherever the camera/world-shader runs)

- [ ] **Step 1: Implement effect**

When the player ship's distance to a dungeon entity's position is less than that dungeon's `OuterRadius`, darken the rendered world outside the asteroid silhouette + apply a subtle warm-glow tint inside. Pure client effect — uses dungeon entity positions only.

- [ ] **Step 2: Commit**

```
git add web-pixi/src/dungeon-overlay.ts <render loop file>
git commit -m "feat(web): apply inside-dungeon darken/glow overlay"
```

### Task 40: Boss sprite distinguishing

**Files:**
- Modify: `web-pixi/src/entities/npc.ts` (or wherever NPC sprites are picked)

- [ ] **Step 1: Use Elite flag**

The NPC entity should carry an `IsElite` or equivalent hint (added in Task 28 server-side). Render elite NPCs at 1.25× scale with a red-tinted shader. Render `ArchetypeBossGuardian` at 2× scale with a distinct color palette.

- [ ] **Step 2: Commit**

```
git add web-pixi/src/entities/npc.ts
git commit -m "feat(web): distinguish elite + BossGuardian NPC sprites"
```

---

## Phase 12 — Final Integration

### Task 41: End-to-end smoke

**Files:**
- (no new files — exercising the existing pipeline)

- [ ] **Step 1: Build clean**

```
just build
go vet ./...
go test ./pkg/... ./internal/...
```
Expected: PASS, no compile warnings.

- [ ] **Step 2: Inline-run smoke (server only, no client)**

This task does NOT start the dev server (per memory: "No leftover processes"). Instead:

```
bin/server --headless --postgres-url=postgres://mmo:mmo@localhost:5432/mmo?sslmode=disable
```

In a separate console: connect via console adapter (`dungeon list` should show the testsite dungeon in cell 0,0).

If the user is running the test session, they'll run `just dev` themselves. The implementer's responsibility ends with: build passes, all tests pass, console reports a dungeon present in cell 0,0.

- [ ] **Step 3: Commit any cleanup**

If anything was tweaked during smoke (e.g. tuning a default that was obviously wrong), commit it as a separate `chore:` commit.

---

## Self-Review

Already done inline. Key adaptations from spec, all documented in the "Codebase Adaptation Notes" at the top:

- `TargetLockSystem` LOS surface → `Selection` auto-clear (spec amended in commit `79f9523`).
- Archetypes are Brawler / Artillery / Lancer (not Sniper / Swarmer).
- Existing MotionPolicy already has `MotionStationary` — only `MotionPathfind` is new.

No placeholders. Types consistent across tasks (e.g. `DungeonAnchor.DungeonNetID` referenced uniformly; `Pathing.DungeonNetID` matches; `ArchetypeBossGuardian = 3` declared once and reused). Tasks are ordered so each is buildable + testable in isolation, with earlier tasks creating types/symbols that later tasks reference.

---

## Execution Notes for Subagent-Driven Development

- **Implementer model:** opus (per user preference — memory key `subagent-implementer-model`).
- **Do NOT start `just dev`, `just distributed`, or any background server process** at task boundaries. Tests only. The user runs the dev server themselves (memory key `no-leftover-processes`).
- **Branch:** all work lands on `pve-v2` (current branch). No PR — solo developer merges to main directly.
- **Per-task review:** after each task's commit, the orchestrating subagent-driven-development flow verifies tests pass + the commit message matches the task. If a task introduces a regression in an unrelated area, flag and pause for user review rather than auto-fixing.
- **Stopping conditions for autonomous execution:**
  1. Any task fails three implementation attempts → pause for user.
  2. Wire-format changes that break the SDK regen → pause for user.
  3. Build fails on `just build` and the root cause isn't immediately obvious → pause for user.
