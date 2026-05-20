package game

import (
	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/pkg/mmokit"
	"github.com/zenion/mmoserver/pkg/spatial"
)

// DecorationBundle is the component bundle for hand-placed visual-only
// landmarks. The Decoration component carries the sprite family (Kind)
// and variant ("destroyer-01", etc.) so the client can render without
// per-tick traffic.
type DecorationBundle struct {
	Decoration *gamecomp.Decoration
}

// SpawnDecoration materializes a decoration entity at (localX, localY)
// inside the current cell. Decorations have no gameplay impact — they
// exist for client rendering only — so Collider.Layer is LayerNone.
func (gw *GameWorld) SpawnDecoration(localX, localY float32, def mmokit.WorldDecoration) {
	e := gw.stage.Spawn(
		mmokit.Position{X: localX, Y: localY},
		mmokit.EntityKind{Type: gamecomp.KindDecoration},
		mmokit.Collider{Radius: 0, Shape: spatial.ShapeCircle, Layer: 0},
		gamecomp.Decoration{Kind: def.Kind, Variant: def.Variant},
	)
	gw.eng.Log.Log(CatPlayerSpawn, "decoration spawned: id=%s kind=%s variant=%s netID=%d pos=(%.1f,%.1f)",
		def.ID, def.Kind, def.Variant, e.NetID(), localX, localY)
}
