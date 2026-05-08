package main

import "github.com/zenion/mmoserver/pkg/mmokit"

// FieldSpawnerSystem seeds the demo with a horizontal row of entities at
// cell startup. Replaces the former Config.OnInit body.
type FieldSpawnerSystem struct{ mmokit.SystemBase }

func (s *FieldSpawnerSystem) Init() {
	const count = 60
	const span = 1200.0
	for i := range count {
		x := float32(i) * (span / float32(count-1))
		s.Stage().SpawnEntity(mmokit.Position{X: x, Y: 0})
	}
}
