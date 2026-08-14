# Slither Bot AI & Cross-Cell Body Fix Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix snake body warping during cross-cell transfers and rewrite bot AI with priority-based behaviors for natural slither.io-like movement.

**Architecture:** Two independent changes sharing one commit boundary. Part 1 adds PreSerialize/PostSerialize hooks to the generic `pkg/universe/` layer so games can adjust component data during transfer serialization. Part 2 rewrites `system_bot.go` with a priority-evaluated behavior list that replaces the state machine.

**Tech Stack:** Go, Ark ECS, existing spatial hash grid for queries.

**Spec:** `docs/superpowers/specs/2026-03-31-slither-bot-ai-body-fix-design.md`

---

## File Map

| File | Action | Responsibility |
|------|--------|----------------|
| `pkg/universe/world_base.go` | Modify | Add PreSerialize/PostSerialize hook fields + setters |
| `pkg/universe/boundary_system.go` | Modify | Call hooks around position normalization in transfer path |
| `examples/slither/world.go` | Modify | Register body-shift hooks |
| `examples/slither/component.go` | Modify | Update `Bot` struct, remove bot state constants |
| `examples/slither/system_bot.go` | Rewrite | Priority-based bot behaviors |

---

## Task 1: Add PreSerialize/PostSerialize hooks to WorldBase

**Files:**
- Modify: `pkg/universe/world_base.go:68-101` (struct fields) and `:155-189` (setters)

- [ ] **Step 1: Add hook fields to WorldBase struct**

In `pkg/universe/world_base.go`, add two new hook fields after the existing `onPlayerTransferReceived` field (around line 82):

```go
	onTransferReceived       func(entity ecs.Entity, frame *TransferFrame)
	onPlayerTransferReceived func(entity ecs.Entity, frame *TransferFrame)

	// Called before/after SerializeEntity during cross-node transfers.
	// dx, dy is the coordinate delta applied to the entity's position.
	onPreSerialize  func(entity ecs.Entity, dx, dy float32)
	onPostSerialize func(entity ecs.Entity, dx, dy float32)
```

- [ ] **Step 2: Add setter methods**

After the `SetOnPlayerTransferReceived` method (around line 189), add:

```go
// SetPreSerialize sets a hook called before entity serialization during transfers.
// dx, dy is the coordinate delta that will be applied to the position.
// Use this to adjust game-specific components (e.g. body segment ring buffers)
// that store absolute positions and need the same offset.
func (b *WorldBase) SetPreSerialize(fn func(ecs.Entity, float32, float32)) {
	b.onPreSerialize = fn
}

// SetPostSerialize sets a hook called after entity serialization during transfers.
// dx, dy is the inverse delta — use this to restore adjusted components.
func (b *WorldBase) SetPostSerialize(fn func(ecs.Entity, float32, float32)) {
	b.onPostSerialize = fn
}
```

- [ ] **Step 3: Verify compilation**

Run: `go vet ./pkg/universe/...`
Expected: no errors

- [ ] **Step 4: Commit**

```bash
git add pkg/universe/world_base.go
git commit -m "feat(universe): add PreSerialize/PostSerialize hooks to WorldBase"
```

---

## Task 2: Call hooks in BoundarySystem transfer path

**Files:**
- Modify: `pkg/universe/boundary_system.go:130-190` (transfer loop)

- [ ] **Step 1: Add BoundaryWorld interface method**

The BoundarySystem needs access to the hooks. The simplest approach: add two optional methods to BoundaryWorld. But since WorldBase already implements BoundaryWorld and the hooks are on WorldBase, we can use a type assertion. Instead, extend BoundaryWorld with a narrower approach — call the hooks via a new interface checked at runtime.

In `boundary_system.go`, add a local interface before the `BoundarySystem` struct:

```go
// transferHooker is optionally implemented by BoundaryWorld to adjust
// game-specific components during transfer serialization.
type transferHooker interface {
	PreSerialize(entity ecs.Entity, dx, dy float32)
	PostSerialize(entity ecs.Entity, dx, dy float32)
}
```

Then add these methods to `WorldBase` in `world_base.go` (after the setters):

```go
// PreSerialize calls the pre-serialize hook if registered.
func (b *WorldBase) PreSerialize(entity ecs.Entity, dx, dy float32) {
	if b.onPreSerialize != nil {
		b.onPreSerialize(entity, dx, dy)
	}
}

// PostSerialize calls the post-serialize hook if registered.
func (b *WorldBase) PostSerialize(entity ecs.Entity, dx, dy float32) {
	if b.onPostSerialize != nil {
		b.onPostSerialize(entity, dx, dy)
	}
}
```

- [ ] **Step 2: Call hooks in the transfer loop**

In `boundary_system.go`, inside the `for _, t := range transfers` loop, find the block that temporarily sets normalized position (lines 166-174). Wrap it with hook calls:

Replace this block (lines 143-174):

```go
		// Read position and cell (pointers valid until archetype change)
		pos := posMap.Get(t.entity)
		sec := cellMap.Get(t.entity)
		origX, origY := pos.X, pos.Y
		origSX, origSY := sec.SX, sec.SY

		// Compute normalized position for destination
		newX, newY := pos.X, pos.Y
		newSX, newSY := s.bw.Cell().SX, s.bw.Cell().SY
		for newX >= coords.CellSize {
			newX -= coords.CellSize
			newSX++
		}
		for newX < 0 {
			newX += coords.CellSize
			newSX--
		}
		for newY >= coords.CellSize {
			newY -= coords.CellSize
			newSY++
		}
		for newY < 0 {
			newY += coords.CellSize
			newSY--
		}

		// Temporarily set normalized position for serialization
		pos.X, pos.Y = newX, newY
		sec.SX, sec.SY = newSX, newSY

		data, err := s.bw.SerializeEntity(t.entity)

		// Restore original position for ghost visual continuity
		pos.X, pos.Y = origX, origY
		sec.SX, sec.SY = origSX, origSY
```

With:

```go
		// Read position and cell (pointers valid until archetype change)
		pos := posMap.Get(t.entity)
		sec := cellMap.Get(t.entity)
		origX, origY := pos.X, pos.Y
		origSX, origSY := sec.SX, sec.SY

		// Compute normalized position for destination
		newX, newY := pos.X, pos.Y
		newSX, newSY := s.bw.Cell().SX, s.bw.Cell().SY
		for newX >= coords.CellSize {
			newX -= coords.CellSize
			newSX++
		}
		for newX < 0 {
			newX += coords.CellSize
			newSX--
		}
		for newY >= coords.CellSize {
			newY -= coords.CellSize
			newSY++
		}
		for newY < 0 {
			newY += coords.CellSize
			newSY--
		}

		dx, dy := newX-origX, newY-origY

		// Notify game to adjust components that store absolute positions
		if th, ok := s.bw.(transferHooker); ok {
			th.PreSerialize(t.entity, dx, dy)
		}

		// Temporarily set normalized position for serialization
		pos.X, pos.Y = newX, newY
		sec.SX, sec.SY = newSX, newSY

		data, err := s.bw.SerializeEntity(t.entity)

		// Restore original position for ghost visual continuity
		pos.X, pos.Y = origX, origY
		sec.SX, sec.SY = origSX, origSY

		// Restore game components
		if th, ok := s.bw.(transferHooker); ok {
			th.PostSerialize(t.entity, -dx, -dy)
		}
```

- [ ] **Step 3: Verify compilation**

Run: `go vet ./pkg/universe/...`
Expected: no errors

- [ ] **Step 4: Commit**

```bash
git add pkg/universe/boundary_system.go pkg/universe/world_base.go
git commit -m "feat(universe): call PreSerialize/PostSerialize hooks during transfers"
```

---

## Task 3: Register body-shift hooks in slither

**Files:**
- Modify: `examples/slither/world.go` (in `NewSlitherWorld`, after replication registry setup around line 162)

- [ ] **Step 1: Register the hooks**

In `NewSlitherWorld`, after `gw.SetReplicationRegistry(reg)` (line 162) and before the `SetOnTransferReceived` call (line 165), add:

```go
	// Shift body segments alongside head during cross-cell transfers.
	// BoundarySystem normalizes the head position by (dx, dy); body segments
	// in the ring buffer are absolute and need the same offset.
	gw.SetPreSerialize(func(entity ecs.Entity, dx, dy float32) {
		if !gw.SnakeBodyMap.HasAll(entity) {
			return
		}
		body := gw.SnakeBodyMap.Get(entity)
		for i := 0; i < body.Length; i++ {
			idx := (body.Head - i + MaxSegments) % MaxSegments
			body.Segments[idx].X += dx
			body.Segments[idx].Y += dy
		}
	})
	gw.SetPostSerialize(func(entity ecs.Entity, dx, dy float32) {
		if !gw.SnakeBodyMap.HasAll(entity) {
			return
		}
		body := gw.SnakeBodyMap.Get(entity)
		for i := 0; i < body.Length; i++ {
			idx := (body.Head - i + MaxSegments) % MaxSegments
			body.Segments[idx].X += dx
			body.Segments[idx].Y += dy
		}
	})
```

- [ ] **Step 2: Verify compilation**

Run: `go vet ./examples/slither/...`
Expected: no errors

- [ ] **Step 3: Commit**

```bash
git add examples/slither/world.go
git commit -m "fix(slither): shift body segments during cross-cell transfers"
```

---

## Task 4: Update Bot component

**Files:**
- Modify: `examples/slither/component.go:78-90`

- [ ] **Step 1: Replace Bot struct and remove state constants**

Replace the `Bot` struct and bot state constants (lines 78-90):

```go
// Bot marks an entity as AI-controlled.
type Bot struct {
	State   uint8   // 0=wander, 1=seek_food, 2=evade
	Timer   float32 // time remaining in current state
	TargetX float32
	TargetY float32
}

// Bot states.
const (
	BotWander   uint8 = 0
	BotSeekFood uint8 = 1
	BotEvade    uint8 = 2
)
```

With:

```go
// Bot marks an entity as AI-controlled.
// Behaviors are evaluated by priority each tick — no state machine.
type Bot struct {
	HuntTarget ecs.Entity // current hunt target (zero = none)
	HuntTimer  float32    // cooldown before picking new target
	WanderBias float32    // slow-drifting angle offset for smooth wandering
	LastDodge  float32    // cooldown to prevent dodge oscillation
}
```

- [ ] **Step 2: Check for remaining references to old bot constants**

Run: `grep -rn 'BotWander\|BotSeekFood\|BotEvade\|bot\.State\|bot\.Timer\|bot\.TargetX\|bot\.TargetY' examples/slither/`

These should only appear in `system_bot.go` (which we rewrite next). If any appear in other files, they need updating too.

- [ ] **Step 3: Verify compilation (expect errors in system_bot.go only)**

Run: `go vet ./examples/slither/ 2>&1 | head -20`
Expected: errors only in `system_bot.go` referencing removed fields/constants. This is fine — we rewrite it next.

- [ ] **Step 4: Commit (component change only)**

```bash
git add examples/slither/component.go
git commit -m "refactor(slither): update Bot component for priority-based AI"
```

---

## Task 5: Rewrite BotSystem with priority-based behaviors

**Files:**
- Rewrite: `examples/slither/system_bot.go`

- [ ] **Step 1: Write the new BotSystem**

Replace the entire contents of `examples/slither/system_bot.go` with:

```go
package main

import (
	"math"
	"math/rand"

	"github.com/mlange-42/ark/ecs"
	"github.com/mmokit/mmokit/pkg/mmokit"
)

// BotSystem drives AI-controlled snakes using priority-based behaviors.
// Each tick, behaviors are evaluated top-to-bottom; the first one that fires
// sets SnakeState.TargetAngle and SnakeState.Boosting.
type BotSystem struct {
	mmokit.SystemBase
	gw     *SlitherWorld
	filter *ecs.Filter5[Bot, SnakeState, mmokit.Position, mmokit.Rotation, mmokit.NetworkID]
	buf    []mmokit.SpatialEntry
}

func (s *BotSystem) Init() {
	s.gw = s.GameWorld().(*SlitherWorld)
	s.filter = ecs.NewFilter5[Bot, SnakeState, mmokit.Position, mmokit.Rotation, mmokit.NetworkID](s.ECSWorld()).
		Without(ecs.C[mmokit.Ghost](), ecs.C[mmokit.Replica]())
}

func (s *BotSystem) Update(dt float32) {
	gw := s.gw

	// Process bot respawns from the tick queue.
	for _, br := range mmokit.Drain[BotRespawn](gw.Queue) {
		br.Delay -= dt
		if br.Delay <= 0 {
			x, y := gw.findClearSpawnPos()
			gw.SpawnBotSnake(x, y)
			gw.Engine().Log.Log(CatBot, "bot respawned at (%.0f,%.0f)", x, y)
		} else {
			mmokit.Enqueue(gw.Queue, br)
		}
	}

	query := s.filter.Query()
	for query.Next() {
		bot, state, pos, rot, _ := query.Get()
		entity := query.Entity()

		// Tick cooldowns
		bot.LastDodge -= dt
		bot.HuntTimer -= dt

		// Update wander bias: slow sinusoidal drift
		bot.WanderBias += (rand.Float32()-0.5)*0.5*dt
		if bot.WanderBias > 1 {
			bot.WanderBias = 1
		} else if bot.WanderBias < -1 {
			bot.WanderBias = -1
		}

		// Default: no boost
		state.Boosting = false

		// --- Priority behaviors (highest first) ---

		if s.behaviorWallAvoid(state, pos) {
			continue
		}
		if s.behaviorBodyDodge(bot, state, pos, rot, entity, dt) {
			continue
		}
		if s.behaviorHeadEvade(state, pos, rot, entity) {
			continue
		}
		if s.behaviorHunt(bot, state, pos, rot, entity) {
			continue
		}
		if s.behaviorSeekFood(state, pos, rot) {
			continue
		}
		s.behaviorWander(bot, state, rot)
	}
}

// --- Behavior 1: Wall Avoidance (highest priority) ---

func (s *BotSystem) behaviorWallAvoid(state *SnakeState, pos *mmokit.Position) bool {
	cellSize := mmokit.CellSize()
	const margin float32 = 300

	var steerX, steerY float32
	if pos.X < margin {
		steerX = 1
	} else if pos.X > cellSize-margin {
		steerX = -1
	}
	if pos.Y < margin {
		steerY = 1
	} else if pos.Y > cellSize-margin {
		steerY = -1
	}
	if steerX == 0 && steerY == 0 {
		return false
	}

	state.TargetAngle = float32(math.Atan2(float64(steerY), float64(steerX)))
	state.Boosting = false
	return true
}

// --- Behavior 2: Body Dodge ---

func (s *BotSystem) behaviorBodyDodge(bot *Bot, state *SnakeState, pos *mmokit.Position, rot *mmokit.Rotation, self ecs.Entity, dt float32) bool {
	if bot.LastDodge > 0 {
		return false
	}

	gw := s.gw
	const dodgeRange float32 = 200
	const dodgeCone = math.Pi / 3 // ±60 degrees

	heading := float64(rot.Angle)
	s.buf = gw.Spatial.QueryRadius(pos.X, pos.Y, dodgeRange, s.buf[:0])

	var closestDist float32 = dodgeRange + 1
	var closestX, closestY float32
	found := false

	for _, entry := range s.buf {
		if entry.Layer != LayerSnakeBody {
			continue
		}
		if entry.Entity == self {
			continue
		}
		if !gw.ECSWorld().Alive(entry.Entity) {
			continue
		}

		dx := entry.X - pos.X
		dy := entry.Y - pos.Y
		dist := float32(math.Sqrt(float64(dx*dx + dy*dy)))

		// Check if in forward cone
		angle := math.Atan2(float64(dy), float64(dx))
		angleDiff := math.Abs(math.Atan2(math.Sin(angle-heading), math.Cos(angle-heading)))
		if angleDiff > dodgeCone {
			continue
		}

		if dist < closestDist {
			closestDist = dist
			closestX = entry.X
			closestY = entry.Y
			found = true
		}
	}

	// Also check for snake heads in path (not just larger — any head is dangerous)
	for _, entry := range s.buf {
		if entry.Layer != LayerSnakeHead {
			continue
		}
		if entry.Entity == self {
			continue
		}
		if !gw.ECSWorld().Alive(entry.Entity) {
			continue
		}

		dx := entry.X - pos.X
		dy := entry.Y - pos.Y
		dist := float32(math.Sqrt(float64(dx*dx + dy*dy)))

		angle := math.Atan2(float64(dy), float64(dx))
		angleDiff := math.Abs(math.Atan2(math.Sin(angle-heading), math.Cos(angle-heading)))
		if angleDiff > dodgeCone {
			continue
		}

		if dist < closestDist {
			closestDist = dist
			closestX = entry.X
			closestY = entry.Y
			found = true
		}
	}

	if !found {
		return false
	}

	// Steer perpendicular to the obstacle — pick the shorter turn
	obstacleAngle := math.Atan2(float64(closestY-pos.Y), float64(closestX-pos.X))
	// Two perpendicular options
	perpLeft := float32(obstacleAngle + math.Pi/2)
	perpRight := float32(obstacleAngle - math.Pi/2)

	// Pick whichever is closer to current heading
	diffLeft := math.Abs(math.Atan2(math.Sin(float64(perpLeft)-heading), math.Cos(float64(perpLeft)-heading)))
	diffRight := math.Abs(math.Atan2(math.Sin(float64(perpRight)-heading), math.Cos(float64(perpRight)-heading)))

	if diffLeft < diffRight {
		state.TargetAngle = perpLeft
	} else {
		state.TargetAngle = perpRight
	}

	// Boost if very close
	if closestDist < 80 {
		state.Boosting = true
	}

	bot.LastDodge = 0.15 // small cooldown to prevent jitter
	return true
}

// --- Behavior 3: Head Evade ---

func (s *BotSystem) behaviorHeadEvade(state *SnakeState, pos *mmokit.Position, rot *mmokit.Rotation, self ecs.Entity) bool {
	gw := s.gw
	const evadeRange float32 = 400

	s.buf = gw.Spatial.QueryRadius(pos.X, pos.Y, evadeRange, s.buf[:0])

	for _, entry := range s.buf {
		if entry.Layer != LayerSnakeHead {
			continue
		}
		if entry.Entity == self {
			continue
		}
		if !gw.ECSWorld().Alive(entry.Entity) {
			continue
		}
		if !gw.SnakeStateMap.HasAll(entry.Entity) {
			continue
		}

		otherMass := gw.SnakeStateMap.Get(entry.Entity).Mass
		if otherMass <= state.Mass {
			continue // only evade larger snakes
		}

		// Turn away from threat
		dx := pos.X - entry.X
		dy := pos.Y - entry.Y
		state.TargetAngle = float32(math.Atan2(float64(dy), float64(dx)))
		state.Boosting = true
		return true
	}

	return false
}

// --- Behavior 4: Hunt / Circle ---

const (
	huntMinMass      float32 = 150 // bot must be this large to hunt
	huntMassRatio    float32 = 3   // bot must be Nx the target's mass
	huntRange        float32 = 600 // detection range
	huntCooldown     float32 = 2   // seconds between target picks
)

func (s *BotSystem) behaviorHunt(bot *Bot, state *SnakeState, pos *mmokit.Position, rot *mmokit.Rotation, self ecs.Entity) bool {
	if state.Mass < huntMinMass {
		bot.HuntTarget = ecs.Entity{}
		return false
	}

	gw := s.gw

	// Validate current target
	if bot.HuntTarget != (ecs.Entity{}) {
		if !gw.ECSWorld().Alive(bot.HuntTarget) || !gw.SnakeStateMap.HasAll(bot.HuntTarget) {
			bot.HuntTarget = ecs.Entity{}
		} else {
			targetMass := gw.SnakeStateMap.Get(bot.HuntTarget).Mass
			if targetMass*huntMassRatio > state.Mass {
				bot.HuntTarget = ecs.Entity{} // target grew too big
			}
		}
	}

	// Pick new target if needed
	if bot.HuntTarget == (ecs.Entity{}) {
		if bot.HuntTimer > 0 {
			return false
		}
		bot.HuntTimer = huntCooldown

		s.buf = gw.Spatial.QueryRadius(pos.X, pos.Y, huntRange, s.buf[:0])
		var bestTarget ecs.Entity
		var bestDist float32 = huntRange + 1

		for _, entry := range s.buf {
			if entry.Layer != LayerSnakeHead {
				continue
			}
			if entry.Entity == self {
				continue
			}
			if !gw.ECSWorld().Alive(entry.Entity) || !gw.SnakeStateMap.HasAll(entry.Entity) {
				continue
			}
			otherMass := gw.SnakeStateMap.Get(entry.Entity).Mass
			if otherMass*huntMassRatio > state.Mass {
				continue // not small enough
			}
			dx := entry.X - pos.X
			dy := entry.Y - pos.Y
			dist := float32(math.Sqrt(float64(dx*dx + dy*dy)))
			if dist < bestDist {
				bestDist = dist
				bestTarget = entry.Entity
			}
		}

		if bestTarget == (ecs.Entity{}) {
			return false
		}
		bot.HuntTarget = bestTarget
	}

	// Steer to cut across target's path
	target := bot.HuntTarget
	if !gw.PositionMap().HasAll(target) || !gw.RotationMap.HasAll(target) {
		bot.HuntTarget = ecs.Entity{}
		return false
	}

	targetPos := gw.PositionMap().Get(target)
	targetRot := gw.RotationMap.Get(target)
	targetState := gw.SnakeStateMap.Get(target)

	// Predict where the target will be in ~1 second
	predictX := targetPos.X + float32(math.Cos(float64(targetRot.Angle)))*targetState.Speed*1.0
	predictY := targetPos.Y + float32(math.Sin(float64(targetRot.Angle)))*targetState.Speed*1.0

	// Steer toward predicted position
	dx := predictX - pos.X
	dy := predictY - pos.Y
	dist := float32(math.Sqrt(float64(dx*dx + dy*dy)))

	state.TargetAngle = float32(math.Atan2(float64(dy), float64(dx)))

	// Boost to get ahead of target, but stop boosting when close (to arc in front)
	state.Boosting = dist > 200

	// Give up if target is too far away
	if dist > huntRange*1.5 {
		bot.HuntTarget = ecs.Entity{}
	}

	return true
}

// --- Behavior 5: Seek Food ---

func (s *BotSystem) behaviorSeekFood(state *SnakeState, pos *mmokit.Position, rot *mmokit.Rotation) bool {
	gw := s.gw
	const seekRange float32 = 500
	const seekCone = math.Pi / 2 // ±90 degrees

	heading := float64(rot.Angle)
	s.buf = gw.Spatial.QueryRadius(pos.X, pos.Y, seekRange, s.buf[:0])

	var bestScore float32
	var bestAngle float32
	found := false

	for _, entry := range s.buf {
		if entry.Layer != LayerFood {
			continue
		}

		dx := entry.X - pos.X
		dy := entry.Y - pos.Y

		// Check forward cone
		angle := math.Atan2(float64(dy), float64(dx))
		angleDiff := math.Abs(math.Atan2(math.Sin(angle-heading), math.Cos(angle-heading)))
		if angleDiff > seekCone {
			continue
		}

		dist := float32(math.Sqrt(float64(dx*dx + dy*dy)))
		if dist < 1 {
			dist = 1
		}

		var foodValue float32 = 1
		if gw.ECSWorld().Alive(entry.Entity) && gw.FoodMap.HasAll(entry.Entity) {
			foodValue = gw.FoodMap.Get(entry.Entity).Value
		}

		score := foodValue / dist
		if score > bestScore {
			bestScore = score
			bestAngle = float32(angle)
			found = true
		}
	}

	if !found {
		return false
	}

	state.TargetAngle = bestAngle
	state.Boosting = false // never boost for food
	return true
}

// --- Behavior 6: Wander (lowest priority, always fires) ---

func (s *BotSystem) behaviorWander(bot *Bot, state *SnakeState, rot *mmokit.Rotation) {
	// Gently drift: current heading + slow-moving bias
	state.TargetAngle = rot.Angle + bot.WanderBias*0.5
	state.Boosting = false
}
```

- [ ] **Step 2: Verify compilation**

Run: `go vet ./examples/slither/...`
Expected: no errors

- [ ] **Step 3: Commit**

```bash
git add examples/slither/system_bot.go examples/slither/component.go
git commit -m "feat(slither): rewrite bot AI with priority-based behaviors"
```

---

## Task 6: Update Bot component in entity_snake.go spawn

**Files:**
- Modify: `examples/slither/entity_snake.go:116-121`

- [ ] **Step 1: Update SpawnBotSnake to use new Bot fields**

In `entity_snake.go`, replace the bot component initialization (lines 116-121):

```go
	gw.BotMap.Add(entity, &Bot{
		State:   BotWander,
		Timer:   2 + rand.Float32()*3,
		TargetX: x + (rand.Float32()-0.5)*1000,
		TargetY: y + (rand.Float32()-0.5)*1000,
	})
```

With:

```go
	gw.BotMap.Add(entity, &Bot{
		WanderBias: (rand.Float32() - 0.5) * 2,
	})
```

- [ ] **Step 2: Verify compilation and run**

Run: `go vet ./examples/slither/...`
Expected: no errors

- [ ] **Step 3: Build and smoke test**

Run: `cd examples/slither && make build`
Expected: binary builds successfully.

- [ ] **Step 4: Commit**

```bash
git add examples/slither/entity_snake.go
git commit -m "refactor(slither): update bot spawn to use new Bot component fields"
```

---

## Task 7: Manual smoke test

- [ ] **Step 1: Run the server**

Run: `cd examples/slither && make dev`

Open `http://localhost:5173` in a browser.

- [ ] **Step 2: Verify bots**

Observe:
- Bots wander smoothly (no jittery direction changes)
- Bots steer around other snake bodies instead of colliding
- Bots never boost while seeking food or wandering
- Large bots (>150 mass) occasionally chase smaller snakes
- Bots turn away from walls near cell edges

- [ ] **Step 3: Verify cross-cell body fix**

Connect as a player, grow your snake, and cross a cell boundary. Verify:
- Body segments do NOT warp/stretch across the cell
- Body smoothly follows the head through the transition
- No visual artifacts

- [ ] **Step 4: Final commit with all files**

If any compilation fixes were needed during testing:

```bash
git add -A
git commit -m "fix(slither): address smoke test issues"
```
