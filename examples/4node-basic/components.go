package main

import "github.com/zenion/mmoserver/pkg/mmokit"

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
}

type BotComponents struct {
	Name       *PlayerName
	MoveTarget *mmokit.MoveTarget
	Pos        *mmokit.Position
	Behavior   *BotBehavior
}
