package main

import (
	"github.com/zenion/mmoserver/pkg/mmokit"
)

type WavePos struct {
	X, Y float32
}

type WaveStateMsg struct {
	Positions []WavePos
}

// FieldSystem is the native scaffold: it spawns the field of dots and broadcasts
// their positions to every client each tick. The actual MOTION (each dot's Y) is
// computed by the hot-swappable `wave` wasm module, which runs just before this
// system — so the positions broadcast here are already this tick's.
type FieldSystem struct {
	mmokit.SystemBase
	entities mmokit.Query[struct {
		Pos *mmokit.Position
	}]
}

func (s *FieldSystem) Init() {
	mmokit.RegisterEvent[WaveStateMsg]()

	const count = 60
	const span = 1200.0
	for i := range count {
		x := float32(i) * (span / float32(count-1))
		s.Stage().Spawn(mmokit.Position{X: x, Y: 0})
	}
}

func (s *FieldSystem) Update(dt float32) {
	msg := WaveStateMsg{Positions: make([]WavePos, 0, 64)}
	for _, e := range s.entities.Iter {
		msg.Positions = append(msg.Positions, WavePos{X: e.Pos.X, Y: e.Pos.Y})
	}
	mmokit.SendEventToAll(s.Engine(), &msg)
}
