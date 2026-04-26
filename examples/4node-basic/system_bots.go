package main

import (
	"math/rand"

	"github.com/zenion/mmoserver/pkg/mmokit"
)

// BotSystem drives bot entities on whichever cell it runs on. One instance per
// cell is instantiated by the system factory in main.go. Bots are spawned on
// demand via the `bot spawn` console command (see command_bots.go); no
// Init-time spawning. After a split, every child cell's BotSystem picks up
// whichever bots landed on its quadrant and keeps retargeting them.
//
// Bots use KindBot (distinct from KindPlayer) so the Query selects only bots
// — no name-prefix filter, no possibility of accidentally driving a real
// player. BotBehavior carries the per-bot retarget countdown as a registered
// KindComponent, so a bot crossing a cell seam mid-wander arrives on the
// neighbor with its countdown intact.
type BotSystem struct {
	mmokit.SystemBase

	gw *World

	bots mmokit.Query[struct {
		Behavior *BotBehavior
		MT       *mmokit.MoveTarget
		Pos      *mmokit.Position
	}]
}

func (s *BotSystem) Init() {
	s.gw = mmokit.WorldOf[*World](s)
	s.bots.Init(s, mmokit.IncludeAll())
}

func (s *BotSystem) Update(dt float32) {
	cellSize := mmokit.CellSize()
	// Target the depth-0 ANCESTOR of this cell rather than the cell's own
	// bounds. A bot living in cell_d1_0_1 (child of 0_0) still picks
	// targets anywhere inside 0_0's full world area. This keeps bots
	// wandering across the entire original cell space even after it
	// splits, so they naturally cross child-cell boundaries and exercise
	// the cross-cell handoff protocol.
	origin := s.gw.Cell()
	for origin.Depth > 0 {
		origin = origin.Parent()
	}
	minX, minY, maxX, maxY := origin.WorldBounds(cellSize)
	sizeX := maxX - minX
	sizeY := maxY - minY
	padX := sizeX * 0.05
	padY := sizeY * 0.05

	const retargetPeriod = 100 // 5s at 20Hz

	for _, b := range s.bots.Iter {
		if b.Behavior.TicksUntilRetarget > 0 {
			b.Behavior.TicksUntilRetarget--
			continue
		}
		b.Behavior.TicksUntilRetarget = retargetPeriod
		tx := minX + padX + rand.Float32()*(sizeX-2*padX)
		ty := minY + padY + rand.Float32()*(sizeY-2*padY)
		mmokit.SetMoveTarget(b.MT, tx, ty)
	}
}
