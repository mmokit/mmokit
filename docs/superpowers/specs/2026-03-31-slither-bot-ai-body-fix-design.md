# Slither Bot AI Improvement & Cross-Cell Body Fix

**Date:** 2026-03-31
**Status:** Approved

## Overview

Two related changes to the slither example game:

1. **Cross-cell body fix** — Snake body segments warp when crossing cell boundaries because the ring buffer isn't coordinate-adjusted during transfer serialization.
2. **Bot AI rewrite** — Replace the 3-state machine with priority-based behaviors that produce natural slither.io-like movement, smart body dodging, and escalating aggression.

## Part 1: Cross-Cell Snake Body Fix

### Problem

When a snake's head crosses a cell boundary, `BoundarySystem` temporarily normalizes the head position for serialization (e.g. 8200 → 8). But the `SnakeBody` ring buffer segments retain old-cell absolute coordinates (8100, 8000, etc.).

`marshalSnakeBodyRelative` computes offsets as `seg.X - pos.X`, producing `8100 - 8 = 8092` instead of the correct `-100`. On the receiving node, these enormous offsets reconstruct body segments way outside the cell, causing a visible warp/stretch until new segments replace the old ones.

### Solution

Add `PreSerialize` / `PostSerialize` hooks on `WorldBase`. `BoundarySystem` calls them with the coordinate delta (the difference between original and normalized position) before and after serialization. The slither game registers a hook that shifts all `SnakeBody` ring buffer segments by the delta.

### Changes

**`pkg/universe/world_base.go`:**
- Add `onPreSerialize func(entity ecs.Entity, dx, dy float32)` field
- Add `onPostSerialize func(entity ecs.Entity, dx, dy float32)` field
- Add `SetPreSerialize(fn)` and `SetPostSerialize(fn)` setter methods

**`pkg/universe/boundary_system.go`:**
- Before temporary position normalization + serialization, call `PreSerialize(entity, deltaX, deltaY)` where `deltaX = newX - origX`, `deltaY = newY - origY`
- After serialization and position restore, call `PostSerialize(entity, -deltaX, -deltaY)`

**`examples/slither/world.go`:**
- Register pre-serialize hook that shifts all `SnakeBody` segments by `(dx, dy)`
- Register post-serialize hook that shifts them back by `(dx, dy)` (which is already the inverse)

## Part 2: Bot AI Rewrite

### Problem

Current bot AI has three issues:
1. **Boost-loop** — Bots boost toward food, then eat the food they shed from boosting, creating a wasteful cycle.
2. **No body awareness** — Bots only detect larger snake heads, not body segments in their path, so they frequently collide with bodies.
3. **No hunting** — No concept of the core slither.io kill strategy (cutting across smaller snakes' paths).

### Solution

Replace the 3-state machine (Wander/SeekFood/Evade) with a priority-evaluated behavior list. Each tick, behaviors are checked top-to-bottom; the first one that fires sets `SnakeState.TargetAngle` and `SnakeState.Boosting`.

### Behavior Priority (highest to lowest)

| # | Behavior | Trigger | Output |
|---|----------|---------|--------|
| 1 | **Wall Avoidance** | Position within 300u of cell edge | Steer toward center, no boost |
| 2 | **Body Dodge** | Body segment within ~200u in forward cone (±60°) | Steer perpendicular to obstacle (shorter turn), brief boost if very close |
| 3 | **Head Evade** | Larger snake head within 400u | Turn away, boost for 1-2s |
| 4 | **Hunt/Circle** | Mass > 3x nearby smaller snake AND bot mass > 150 | Steer to cut across target's path, boost to get ahead, then arc |
| 5 | **Seek Food** | Food within 500u in forward cone (±90°) | Steer toward highest-value food, **never boost** |
| 6 | **Wander** | Default fallback | Gentle sinusoidal drift every 2-4s, **never boost** |

### Key Design Decisions

- **No boost in Seek Food or Wander.** This eliminates the boost-loop entirely. Only Body Dodge (emergency), Head Evade, and Hunt use boost — all strategically.
- **Body Dodge uses spatial grid.** Queries `LayerSnakeBody` entries in a forward cone. Steers perpendicular to the nearest obstacle, picking the shorter turn direction. Has a brief cooldown (`LastDodge`) to prevent oscillation.
- **Hunt is simple.** Large bots steer to cut across a smaller snake's heading, boost ahead, then arc in front. Re-evaluated each tick so the bot adapts if the target changes direction. No complex path planning needed.
- **Smooth wandering.** Instead of random waypoints (which cause jittery direction changes), wander uses a slowly drifting angle bias that produces flowing, natural movement.
- **Escalating aggression.** Hunt only activates above 150 mass and requires 3x mass advantage. Small bots are passive foragers; large bots become hunters naturally.

### Component Changes

**`examples/slither/component.go` — `Bot` struct:**
```go
type Bot struct {
    HuntTarget ecs.Entity  // current hunt target (zero = none)
    HuntTimer  float32     // cooldown before picking new target
    WanderBias float32     // slow-drifting angle offset for smooth wandering
    LastDodge  float32     // cooldown to avoid jittery dodge oscillation
}
```

The current `State`, `Timer`, `TargetX`, `TargetY` fields are removed — behaviors are evaluated fresh each tick.

### Bot State Constants

Removed. No state machine; behavior priority handles transitions implicitly.

### Files Touched

| File | Change |
|------|--------|
| `pkg/universe/world_base.go` | Add PreSerialize/PostSerialize hook fields + setters |
| `pkg/universe/boundary_system.go` | Call hooks around position normalization |
| `examples/slither/world.go` | Register body-shift hooks |
| `examples/slither/component.go` | Update `Bot` struct, remove bot state constants |
| `examples/slither/system_bot.go` | Full rewrite — priority-based behaviors |

### Not Touched

MovementSystem, CollisionSystem, NetworkSystem, SpatialSystem, client code, config. The bot AI still outputs `SnakeState.TargetAngle` and `SnakeState.Boosting`, which MovementSystem already consumes. The body fix is transparent to the client.

### Testing

- Run slither server with `make dev`, observe bots in web client
- Bots should flow smoothly, dodge body segments, rarely boost wastefully
- Large bots (>150 mass) should occasionally hunt smaller snakes
- Cross a cell border and verify body doesn't warp/stretch
