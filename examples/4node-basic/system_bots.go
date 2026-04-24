package main

import (
	"math/rand"
	"strings"

	"github.com/zenion/mmoserver/pkg/mmokit"
)

// BotSystem drives "bot" entities on whichever cell it runs on. One instance
// per cell is instantiated by the system factory in main.go. Bots are spawned
// on demand via the `bot spawn` console command (see bot_console.go); no
// Init-time spawning. After a split, every child cell's BotSystem picks up
// whichever bots landed on its quadrant and keeps retargeting them.
//
// Bots use KindPlayer so they render as ordinary player circles in the web
// client. They're identified by a `bot_` prefix on PlayerName.Name so Update()
// can cleanly separate them from real human players sharing the same kind.
type BotSystem struct {
	mmokit.SystemBase

	gw *World

	bots mmokit.Query[struct {
		Name *PlayerName
		MT   *mmokit.MoveTarget
		Pos  *mmokit.Position
	}]

	// retargetEvery is how many ticks between random MoveTarget refreshes.
	// At 20Hz, 100 ticks = 5s.
	retargetEvery int
	tickCounter   int
}

func (s *BotSystem) Init() {
	w, ok := s.GameWorld().(*World)
	if !ok || w == nil {
		return
	}
	s.gw = w
	s.retargetEvery = 100 // 5s at 20Hz
	s.bots.Init(s, mmokit.IncludeAll())
}

func (s *BotSystem) Update(dt float32) {
	if s.gw == nil {
		return
	}
	s.tickCounter++
	period := s.retargetEvery
	if period <= 0 {
		period = 100
	}

	cellSize := mmokit.CellSize()
	// Target the depth-0 ANCESTOR of this cell rather than the cell's own
	// bounds. A bot living in cell_d1_0_1 (child of 0_0) still picks
	// targets anywhere inside 0_0's full world area. This keeps bots
	// wandering across the entire original cell space even after it
	// splits, so they naturally cross child-cell boundaries and exercise
	// the cross-cell handoff protocol. A bot that drifts into a
	// neighboring depth-0 cell (e.g. 0_0 → 1_0 via handoff) inherits
	// that neighbor's area as its new roaming domain on the next
	// retarget — which is fine, they just become "1_0 bots".
	origin := s.gw.Cell()
	for origin.Depth > 0 {
		origin = origin.Parent()
	}
	minX, minY, maxX, maxY := origin.WorldBounds(cellSize)
	sizeX := maxX - minX
	sizeY := maxY - minY
	padX := sizeX * 0.05
	padY := sizeY * 0.05

	for e, b := range s.bots.All() {
		if !strings.HasPrefix(b.Name.Name, "bot_") {
			continue
		}
		// Phase offset by entity ID so different bots retarget on
		// different ticks — avoids visible herding when many bots
		// picked their previous targets on the same tick.
		if (s.tickCounter+int(e.ID()))%period != 0 {
			continue
		}
		tx := minX + padX + rand.Float32()*(sizeX-2*padX)
		ty := minY + padY + rand.Float32()*(sizeY-2*padY)
		mmokit.SetMoveTarget(b.MT, tx, ty)
	}
}
