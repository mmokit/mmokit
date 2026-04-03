package main

import (
	"math"
	"math/rand"

	"github.com/mlange-42/ark/ecs"
	"github.com/zenion/mmoserver/pkg/mmokit"
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
			gw.Engine().Log.Log(CatGameBot, "bot respawned at (%.0f,%.0f)", x, y)
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
		bot.WanderBias += (rand.Float32() - 0.5) * 0.5 * dt
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
	huntMinMass   float32 = 150 // bot must be this large to hunt
	huntMassRatio float32 = 3   // bot must be Nx the target's mass
	huntRange     float32 = 600 // detection range
	huntCooldown  float32 = 2   // seconds between target picks
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
