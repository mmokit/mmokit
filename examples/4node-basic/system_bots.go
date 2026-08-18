package main

import (
	"math/rand"

	"github.com/mmokit/mmokit"
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

	// RetargetPeriod is a runtime tunable (ticks between a bot picking a new
	// wander target; 20 ticks = 1s). Lower = more frantic, higher = lazier.
	// Tweak it live via `tune set Bot retargetPeriod 40` or the admin
	// /tunables sliders — it shows up alongside the wasm `tint` tunables.
	RetargetPeriod uint16 `tune:"default=100,min=10,max=400,step=10"`

	bots mmokit.Query[BotComponents]
}

// No Init() — defaults (exclude Ghost + Replica) are correct for bots,
// auto-bind handles it.

func (s *BotSystem) Update(dt float32) {
	cellSize := s.Stage().CellSize()
	// Target the depth-0 ANCESTOR of this cell rather than the cell's own
	// bounds. A bot living in cell_d1_0_1 (child of 0_0) still picks
	// targets anywhere inside 0_0's full world area. This keeps bots
	// wandering across the entire original cell space even after it
	// splits, so they naturally cross child-cell boundaries and exercise
	// the cross-cell handoff protocol.
	origin := s.Stage().Cell()
	for origin.Depth > 0 {
		origin = origin.Parent()
	}
	minX, minY, maxX, maxY := origin.WorldBounds(cellSize)
	sizeX := maxX - minX
	sizeY := maxY - minY
	padX := sizeX * 0.05
	padY := sizeY * 0.05

	for _, b := range s.bots.Iter {
		if b.Behavior.TicksUntilRetarget > 0 {
			b.Behavior.TicksUntilRetarget--
			continue
		}
		b.Behavior.TicksUntilRetarget = s.RetargetPeriod
		tx := minX + padX + rand.Float32()*(sizeX-2*padX)
		ty := minY + padY + rand.Float32()*(sizeY-2*padY)
		b.MoveTarget.SetTarget(tx, ty, cellSize)
	}
}
