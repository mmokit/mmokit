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

type FieldSystem struct {
	mmokit.SystemBase
	Baseline float32 `tune:"default=0,min=-200,max=200,step=10"`
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
		msg.Positions = append(msg.Positions, WavePos{X: e.Pos.X, Y: e.Pos.Y + s.Baseline})
	}
	mmokit.SendEventToAll(s.Engine(), &msg)
}
