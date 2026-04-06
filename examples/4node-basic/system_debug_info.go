package main

import (
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// DebugInfoSystem updates game-specific debug fields each tick.
type DebugInfoSystem struct {
	mmokit.SystemBase
	entities mmokit.Query[struct {
		DI *DebugInfo
	}]
}

func (s *DebugInfoSystem) Init() {
	s.entities.Init(s, mmokit.IncludeAll())
}

func (s *DebugInfoSystem) Update(dt float32) {
	for _, b := range s.entities.All() {
		b.DI.AoIRadius = AoIRadius
	}
}
