package main

import "github.com/mmokit/mmokit/pkg/mmokit"

type PlayerName struct {
	Name string `net:"initial"`
}

type BotBehavior struct {
	TicksUntilRetarget uint16
	Mode               uint8
}

type PlayerComponents struct {
	Name       *PlayerName
	MoveTarget *mmokit.MoveTarget
	Tint       *mmokit.Tint
}

type BotComponents struct {
	Name       *PlayerName
	MoveTarget *mmokit.MoveTarget
	Tint       *mmokit.Tint
	Behavior   *BotBehavior
}
