package main

import (
	"fmt"
	"math/rand"
	"strings"

	"github.com/zenion/mmoserver/pkg/mmokit"
)

// botConfig is populated from main.go CLI flags before Coordinator.Build().
// Only the cell that matches botConfig.Cell spawns bots; after that every
// cell's BotSystem drives whichever bots currently live on it (post-split
// children inherit the bots on their quadrant).
var botConfig struct {
	Count int           // total bots to spawn; 0 disables the system
	Cell  mmokit.CellID // target cell (depth 0) to overload
}

// BotSystem is the S7 demo's synthetic load generator. One instance is
// created per cell via the AddSystem factory.
//
// Spawning is gated on the configured origin cell: only the cell matching
// botConfig.Cell performs the initial SpawnEntity loop at Init time. Every
// other cell's BotSystem is inert at Init but still runs Update — it
// iterates its local ECS every tick, picks out entities whose PlayerName
// starts with "bot_" (they must have MoveTarget + Position too), and
// periodically refreshes their MoveTarget to a random point inside THIS
// cell's world bounds.
//
// The effect: bots spawn once on the origin cell, a split fires, bots
// migrate into depth-1 children via the normal transfer path, and each
// child's BotSystem seamlessly takes over retargeting the bots that landed
// on its quadrant. Merges work the same way in reverse. No slice of
// tracked entities — every tick we re-read the ECS, which handles
// transfers, reaps, and re-spawns for free.
//
// Bots use KindPlayer so they render as ordinary player circles in the
// web client.
type BotSystem struct {
	mmokit.SystemBase

	gw *World

	bots mmokit.Query[struct {
		Name *PlayerName
		MT   *mmokit.MoveTarget
		Pos  *mmokit.Position
	}]

	// retargetEvery is how many ticks between random MoveTarget
	// refreshes. At 20Hz, 100 ticks = 5s.
	retargetEvery int
	tickCounter   int
}

func (s *BotSystem) Init() {
	if botConfig.Count <= 0 {
		return
	}
	w, ok := s.GameWorld().(*World)
	if !ok || w == nil {
		return
	}
	s.gw = w
	s.retargetEvery = 100 // 5s at 20Hz

	// Every cell drives the bots currently on it — but only the configured
	// origin cell performs the one-shot spawn on Init.
	s.bots.Init(s, mmokit.IncludeAll())

	if w.Cell() != botConfig.Cell {
		return
	}

	cellSize := mmokit.CellSize()
	rng := rand.New(rand.NewSource(int64(botConfig.Cell.X*1000 + botConfig.Cell.Y + 17)))
	originX, originY := botConfig.Cell.WorldOrigin(cellSize)
	for i := 0; i < botConfig.Count; i++ {
		x := cellSize*0.1 + rng.Float32()*cellSize*0.8
		y := cellSize*0.1 + rng.Float32()*cellSize*0.8
		e := w.SpawnEntity(
			mmokit.Position{X: x, Y: y},
			mmokit.WithCollider(PlayerRadius),
			mmokit.WithEntityKind(KindPlayer),
			mmokit.WithComponents(), // auto-adds PlayerName, DebugInfo, MoveTarget
		)
		name := w.NameMap.Get(e)
		name.Name = fmt.Sprintf("bot_%d_%d_%03d", botConfig.Cell.X, botConfig.Cell.Y, i)

		// Seed the first move target so bots start walking immediately
		// instead of standing still for the first 5s.
		mt := w.MoveTargetMap.Get(e)
		tx := cellSize*0.1 + rng.Float32()*cellSize*0.8
		ty := cellSize*0.1 + rng.Float32()*cellSize*0.8
		mmokit.SetMoveTarget(mt, originX+tx, originY+ty)
	}
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
	// Compute bounds of the CURRENT cell (the cell this BotSystem instance
	// runs on), not the origin cell — after splits, bots live on depth-1
	// children with different world-space bounds.
	minX, minY, maxX, maxY := s.gw.Cell().WorldBounds(cellSize)
	sizeX := maxX - minX
	sizeY := maxY - minY
	padX := sizeX * 0.1
	padY := sizeY * 0.1

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
