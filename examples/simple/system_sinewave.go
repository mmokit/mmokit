package main

import (
	"math"

	"github.com/zenion/mmoserver/pkg/mmokit"
)

// WavePos is one entity's published (x, y). Wire format follows the
// engine's reflective codec: two consecutive float32 LE.
type WavePos struct {
	X, Y float32
}

// WaveStateMsg is the per-tick world snapshot the server pushes to every
// connected client. Wire format = engine reflective codec; clients read
// the standard 0x00 typed-event frame and decode the slice of WavePos
// directly off the WebSocket. Registered via mmokit.RegisterEvent in main.
type WaveStateMsg struct {
	Positions []WavePos
}

// SineWaveSystem bobs every entity vertically with phase derived from its
// X position, producing a traveling wave through the field. After
// mutating positions it broadcasts a snapshot to every connected player
// via the engine's typed-event service.
type SineWaveSystem struct {
	mmokit.SystemBase
	entities mmokit.Query[struct {
		Pos *mmokit.Position
	}]
	elapsed float32
}

const (
	waveAmp    = 220.0 // peak vertical excursion
	waveFreqHz = 0.6   // temporal frequency
	waveSpread = 0.012 // radians per pixel of X — controls wavelength
)

func (s *SineWaveSystem) Init() {
	s.entities.With(mmokit.IncludeAll())
	const count = 60
	const span = 1200.0
	for i := range count {
		x := float32(i) * (span / float32(count-1))
		s.Stage().SpawnEntity(mmokit.Position{X: x, Y: 0})
	}
}

func (s *SineWaveSystem) Update(dt float32) {
	s.elapsed += dt
	t := 2 * math.Pi * waveFreqHz * float64(s.elapsed)

	msg := WaveStateMsg{Positions: make([]WavePos, 0, 64)}
	for _, e := range s.entities.Iter {
		e.Pos.Y = float32(math.Sin(t+float64(e.Pos.X)*waveSpread)) * waveAmp
		msg.Positions = append(msg.Positions, WavePos{X: e.Pos.X, Y: e.Pos.Y})
	}

	mmokit.SendEventToAll(s.Engine(), &msg)
}
