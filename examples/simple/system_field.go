package main

import (
	"math"

	"github.com/mmokit/mmokit"
)

type WavePos struct {
	X, Y float32
}

type WaveStateMsg struct {
	Positions []WavePos
}

type FieldSystem struct {
	mmokit.SystemBase
	Amplitude float32 `tune:"default=220,min=60,max=420,step=10"`
	FreqHz    float32 `tune:"default=0.6,min=0.1,max=3,step=0.1"`
	Spread    float32 `tune:"default=0.012,min=0,max=0.05,step=0.001"`
	Baseline  float32 `tune:"default=0,min=-200,max=200,step=10"`
	Phase     float32
	entities  mmokit.Query[struct {
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
	s.Phase += dt
	t := 2 * math.Pi * float64(s.FreqHz) * float64(s.Phase)

	msg := WaveStateMsg{Positions: make([]WavePos, 0, 64)}
	for _, e := range s.entities.Iter {
		e.Pos.Y = s.Amplitude * float32(math.Sin(t+float64(e.Pos.X)*float64(s.Spread)))
		msg.Positions = append(msg.Positions, WavePos{X: e.Pos.X, Y: e.Pos.Y + s.Baseline})
	}
	mmokit.SendEventToAll(s.Engine(), &msg)
}
