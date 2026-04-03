package main

import "github.com/mlange-42/ark/ecs"

// MaxSegments is the ring buffer capacity for snake body segments.
const MaxSegments = 512

// Entity kind constants.
const (
	KindPlayerSnake uint8 = 0
	KindBotSnake    uint8 = 1
	KindNaturalFood uint8 = 2
	KindDeathFood   uint8 = 3
)

// Collision layers.
const (
	LayerSnakeHead uint8 = 1
	LayerFood      uint8 = 2
	LayerSnakeBody uint8 = 3
)

// Cross-node action types.
const (
	ActionEat       uint16 = 1
	ActionSpawnFood uint16 = 2
)

// Segment is a single point in the snake's body trail.
type Segment struct {
	X, Y float32
}

// SnakeBody stores the snake's body as a ring buffer of past head positions.
type SnakeBody struct {
	Segments [MaxSegments]Segment
	Head     int // write index (points to most recently written slot)
	Length   int // number of active segments
}

// PushHead records a new head position in the ring buffer.
func (b *SnakeBody) PushHead(x, y float32) {
	b.Head = (b.Head + 1) % MaxSegments
	b.Segments[b.Head] = Segment{X: x, Y: y}
}

// GetSegment returns the ith segment (0 = newest/head, Length-1 = tail).
func (b *SnakeBody) GetSegment(i int) Segment {
	idx := (b.Head - i + MaxSegments) % MaxSegments
	return b.Segments[idx]
}

// SnakeState holds gameplay state for a snake.
type SnakeState struct {
	Mass        float32
	TargetAngle float32
	Speed       float32
	Boosting    bool
	TurnRate    float32
	SkinID      uint8
	Name        string
}

// SnakeInput holds per-tick client input.
type SnakeInput struct {
	TargetAngle float32
	Boost       bool
	Sequence    uint32
}

// Food marks an entity as a food pellet.
type Food struct {
	Value    float32
	ColorIdx uint8
}

// Bot marks an entity as AI-controlled.
// Behaviors are evaluated by priority each tick — no state machine.
type Bot struct {
	HuntTarget ecs.Entity // current hunt target (zero = none)
	HuntTimer  float32    // cooldown before picking new target
	WanderBias float32    // slow-drifting angle offset for smooth wandering
	LastDodge  float32    // cooldown to prevent dodge oscillation
}

// KillInfo is queued when a snake dies.
type KillInfo struct {
	Victim    ecs.Entity
	Killer    ecs.Entity
	VictimNet uint32
	KillerNet uint32
	Mass      float32
	PosX      float32
	PosY      float32
	HasKiller bool
}

// KillFeedEntry is a recent kill for broadcast to clients.
type KillFeedEntry struct {
	VictimName string
	KillerName string
	VictimMass float32
}

// LeaderEntry is a single leaderboard row.
type LeaderEntry struct {
	Name   string
	Mass   float32
	SkinID uint8
}

// BotRespawn is queued when a bot needs to respawn after a delay.
type BotRespawn struct {
	Delay float32 // seconds remaining
	NodeX float32 // cell-local spawn position
	NodeY float32
}
