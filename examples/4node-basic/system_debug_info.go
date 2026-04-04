package main

import (
	"github.com/mlange-42/ark/ecs"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// DebugInfoSystem updates game-specific debug fields each tick.
// Mesh state (local/replica/ghost, owner node) is handled by the engine's
// MeshState binding — this system only sets game-specific fields like AoIRadius.
type DebugInfoSystem struct {
	mmokit.SystemBase
	filter *ecs.Filter1[DebugInfo]
}

func (s *DebugInfoSystem) Init() {
	w := s.ECSWorld()
	s.filter = ecs.NewFilter1[DebugInfo](w)
}

func (s *DebugInfoSystem) Update(dt float32) {
	query := s.filter.Query()
	for query.Next() {
		di := query.Get()
		di.AoIRadius = AoIRadius
	}
}
