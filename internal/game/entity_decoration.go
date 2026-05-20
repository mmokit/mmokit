package game

import "github.com/zenion/mmoserver/pkg/mmokit"

// SpawnDecoration is the world-manifest entry point. Task 10 replaces
// this stub with the real implementation that materializes decorative
// entities (visual-only props by def.Kind / def.Variant). The stub
// panics so the per-cell bootstrap loop fails loudly when
// world/decorations.json is non-empty before the implementation lands.
func (gw *GameWorld) SpawnDecoration(localX, localY float32, def mmokit.WorldDecoration) {
	_ = localX
	_ = localY
	_ = def
	panic("Task 10: implement SpawnDecoration(localX, localY, def mmokit.WorldDecoration)")
}
