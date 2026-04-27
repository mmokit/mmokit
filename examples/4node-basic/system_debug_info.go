package main

import "github.com/zenion/mmoserver/pkg/mmokit"

// DebugInfoSystem stamps the current AoI radius on every entity each tick
// so the client debug overlay can render it. The cluster-topology push is
// handled by mmokit.NewTopologyBroadcaster, registered separately.
type DebugInfoSystem struct {
	mmokit.SystemBase[*mmokit.WorldBase]
	entities mmokit.Query[struct {
		DI *DebugInfo
	}]
}

func (s *DebugInfoSystem) Update(dt float32) {
	for _, b := range s.entities.Iter {
		b.DI.AoIRadius = AoIRadius
	}
}
