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
// each tick; when it hits zero the bot picks a new MoveTarget. The countdown
// is registered as a KindComponent so it serializes across cell handoffs —
// a bot crossing a seam mid-wander keeps its remaining countdown intact
// rather than resetting at the boundary.
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

// BotComponents is the kind bundle for KindBot entities. Includes all
// player-shared components plus BotBehavior for bot-specific state.
type BotComponents struct {
	Name       *PlayerName
	Debug      *DebugInfo
	MoveTarget *mmokit.MoveTarget
	Behavior   *BotBehavior
}
