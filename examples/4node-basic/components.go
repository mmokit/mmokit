package main

import "github.com/zenion/mmoserver/pkg/mmokit"

// PlayerName stores a player's display name (replicated to other nodes).
type PlayerName struct {
	Name string `net:"initial"`
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
	MoveTarget *mmokit.MoveTarget
}

// BotComponents is the kind bundle for KindBot entities.
type BotComponents struct {
	Name       *PlayerName
	MoveTarget *mmokit.MoveTarget
	Pos        *mmokit.Position
	Behavior   *BotBehavior
}
