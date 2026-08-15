package game

import (
	"github.com/mmokit/mmokit"
	gamecomp "github.com/mmokit/mmokit/examples/space/internal/component"
	"github.com/mmokit/mmokit/examples/space/internal/world"
	"github.com/mmokit/mmokit/pkg/spatial"
)

// DecorationBundle is the component bundle for hand-placed visual-only
// landmarks. The Decoration component carries the sprite family (Kind)
// and variant ("destroyer-01", etc.) so the client can render without
// per-tick traffic. PlacedID tags decorations spawned from world
// manifests so world.* verbs can despawn them cleanly; it's optional
// so non-manifest spawns can omit it.
type DecorationBundle struct {
	Decoration *gamecomp.Decoration
	PlacedID   *gamecomp.PlacedID `mmokit:"optional"`
}

// SpawnDecoration materializes a decoration entity at (localX, localY)
// inside the current cell. Decorations have no gameplay impact — they
// exist for client rendering only — so Collider.Layer is LayerNone.
// Returns the decoration entity's NetID.
func (gw *GameWorld) SpawnDecoration(localX, localY float32, def world.Decoration) uint32 {
	e := gw.stage.Spawn(
		mmokit.Position{X: localX, Y: localY},
		mmokit.EntityKind{Type: gamecomp.KindDecoration},
		mmokit.Collider{Radius: 0, Shape: spatial.ShapeCircle, Layer: 0},
		gamecomp.Decoration{Kind: def.Kind, Variant: def.Variant},
		gamecomp.PlacedID{ID: def.ID},
	)
	gw.eng.Log.Log(CatPlayerSpawn, "decoration spawned: id=%s kind=%s variant=%s netID=%d pos=(%.1f,%.1f)",
		def.ID, def.Kind, def.Variant, e.NetID(), localX, localY)
	return e.NetID()
}
