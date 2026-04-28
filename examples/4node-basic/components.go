package main

import "github.com/zenion/mmoserver/pkg/mmokit"

// PlayerName stores a player's display name (replicated to other nodes).
type PlayerName struct {
	Name string `net:"initial"`
}

// DebugInfo holds per-entity game-specific debug state replicated to clients.
type DebugInfo struct {
	AoIRadius float32 `net:"f32"` // server's current AoI radius (for debug overlay)
}

// BotBehavior holds per-bot wandering state. TicksUntilRetarget counts down
// each tick; when it hits zero the bot picks a new MoveTarget. Registered
// via mmokit.RegisterKind[BotComponents] so cross-cell handoffs preserve
// the countdown — a bot crossing a seam mid-wander keeps its remaining
// countdown intact rather than resetting at the boundary.
type BotBehavior struct {
	TicksUntilRetarget uint16
	Mode               uint8
}

// PlayerComponents is the kind bundle for KindPlayer entities. Used for
// kind registration via mmokit.RegisterKind, query iteration via
// mmokit.Query, and spawn-time initialization via mmokit.Init.
type PlayerComponents struct {
	Name       *PlayerName
	Debug      *DebugInfo
	MoveTarget *mmokit.MoveTarget
}

// BotComponents is the kind bundle for KindBot entities. Bots don't carry
// DebugInfo — the AoI radius overlay is only meaningful on the local
// player's entity, and replicating a constant per-bot is wire waste.
type BotComponents struct {
	Name       *PlayerName
	MoveTarget *mmokit.MoveTarget
	Pos        *mmokit.Position
	Behavior   *BotBehavior
}
