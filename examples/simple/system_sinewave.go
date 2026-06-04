package main

import (
	"math"

	"github.com/zenion/mmoserver/examples/simple/wavecomp"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

type WavePos struct {
	X, Y, Hue float32
}

type WaveStateMsg struct {
	Positions []WavePos
}

type SineWaveSystem struct {
	mmokit.SystemBase
	entities mmokit.Query[struct {
		Pos *mmokit.Position
		Hue *wavecomp.Hue
	}]
	elapsed float32
}

const (
	waveAmp    = 220.0 // peak vertical excursion
	waveFreqHz = 0.6   // temporal frequency
	waveSpread = 0.012 // radians per pixel of X — controls wavelength
)

func (s *SineWaveSystem) Init() {
	mmokit.RegisterEvent[WaveStateMsg]()

	const count = 60
	const span = 1200.0
	for i := range count {
		x := float32(i) * (span / float32(count-1))
		// Seed a rainbow spread across the field; the colorwave wasm module
		// scrolls it over time (and is hot-swappable).
		hue := float32(i) / float32(count)
		s.Stage().Spawn(mmokit.Position{X: x, Y: 0}, wavecomp.Hue{Value: hue})
	}
}

func (s *SineWaveSystem) Update(dt float32) {
	s.elapsed += dt
	t := 2 * math.Pi * waveFreqHz * float64(s.elapsed)

	msg := WaveStateMsg{Positions: make([]WavePos, 0, 64)}
	for _, e := range s.entities.Iter {
		e.Pos.Y = float32(math.Sin(t+float64(e.Pos.X)*waveSpread)) * waveAmp
		// Hue is driven by the hot-swappable colorwave wasm module (read here a
		// tick later — invisible for color).
		msg.Positions = append(msg.Positions, WavePos{X: e.Pos.X, Y: e.Pos.Y, Hue: e.Hue.Value})
	}

	mmokit.SendEventToAll(s.Engine(), &msg)
}
