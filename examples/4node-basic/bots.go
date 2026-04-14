package main

import (
	"fmt"
	"math/rand"

	"github.com/mlange-42/ark/ecs"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// botConfig is populated from main.go CLI flags before Coordinator.Build().
// Read by BotSystem.Init on whichever cell matches botConfig.Cell.
var botConfig struct {
	Count int             // total bots to spawn; 0 disables the system
	Cell  mmokit.CellID   // target cell (depth 0) to overload
}

// BotSystem is the S7 demo's synthetic load generator. One instance is
// created per cell via the AddSystem factory; the instance running on the
// cell that matches botConfig.Cell spawns botConfig.Count entities at
// Init time and re-targets them periodically to exercise the full
// ClickToMove + Physics + Spatial + Network pipeline.
//
// Only the cell matching botConfig.Cell spawns anything — every other
// cell's BotSystem instance is a harmless no-op. When botConfig.Count == 0
// the system is entirely inert, so the flag is free to wire
// unconditionally in main.go.
//
// Bots use KindPlayer so they show up on the web client as ordinary
// player circles — a nice visual confirmation that the loaded cell is
// crammed full of entities before it splits.
type BotSystem struct {
	mmokit.SystemBase

	gw   *World
	bots []ecs.Entity

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
	if w.Cell() != botConfig.Cell {
		return
	}
	s.gw = w
	s.retargetEvery = 100 // 5s at 20Hz

	cellSize := mmokit.CellSize()
	s.bots = make([]ecs.Entity, 0, botConfig.Count)

	rng := rand.New(rand.NewSource(int64(botConfig.Cell.X*1000 + botConfig.Cell.Y + 17)))
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
		s.bots = append(s.bots, e)

		// Seed the first move target so bots start walking immediately
		// instead of standing still for the first 5s.
		mt := w.MoveTargetMap.Get(e)
		tx := cellSize*0.1 + rng.Float32()*cellSize*0.8
		ty := cellSize*0.1 + rng.Float32()*cellSize*0.8
		originX, originY := botConfig.Cell.WorldOrigin(cellSize)
		mmokit.SetMoveTarget(mt, originX+tx, originY+ty)
	}
}

func (s *BotSystem) Update(dt float32) {
	if s.gw == nil || len(s.bots) == 0 {
		return
	}
	s.tickCounter++
	if s.tickCounter < s.retargetEvery {
		return
	}
	s.tickCounter = 0

	ecsW := s.gw.ECSWorld()
	cellSize := mmokit.CellSize()
	originX, originY := botConfig.Cell.WorldOrigin(cellSize)

	// Compact in place: drop any bots whose entities were reaped or
	// transferred out of this cell (post-split, entities on the losing
	// quadrant may live on a different cell; BotSystem on that cell has
	// its own independent slice, so we just forget them here).
	write := 0
	for _, e := range s.bots {
		if !ecsW.Alive(e) {
			continue
		}
		if !s.gw.MoveTargetMap.HasAll(e) {
			continue
		}
		mt := s.gw.MoveTargetMap.Get(e)
		tx := cellSize*0.1 + rand.Float32()*cellSize*0.8
		ty := cellSize*0.1 + rand.Float32()*cellSize*0.8
		mmokit.SetMoveTarget(mt, originX+tx, originY+ty)
		s.bots[write] = e
		write++
	}
	s.bots = s.bots[:write]
}
