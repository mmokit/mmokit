package spatial

import (
	"github.com/mlange-42/ark/ecs"

	"github.com/mmokit/mmokit/pkg/component"
)

// EntryFrom builds a grid entry from an entity's spatial components.
//
// This is the ONLY place components become an Entry. Three sites used to do it
// independently and they disagreed: SpatialSystem filled every field,
// Stage.Spawn filled four and left Layer and Shape at zero, and
// examples/space's collision system built a third variant by hand. Widening
// the entry then meant finding all three, and the one that was already wrong
// stayed wrong because nothing compared them.
//
// rot may be nil, which is treated as identity — Stage.Spawn does not add a
// Rotation and a 2D game need never carry one.
func EntryFrom(e ecs.Entity, pos *component.Position, col *component.Collider, rot *component.Rotation) Entry {
	entry := Entry{Entity: e}
	if pos != nil {
		entry.X = pos.X
		entry.Y = pos.Y
	}
	if col != nil {
		entry.Radius = col.Radius
		entry.Width = col.Width
		entry.Height = col.Height
		entry.Layer = col.Layer
		entry.Shape = col.Shape
	}
	if rot != nil {
		// Yaw only: the narrow phase is 2D until phase 4b, and carrying the
		// quaternion would cost an atan2 on the per-ray LOS path for a value
		// nothing reads yet.
		entry.Yaw = rot.Yaw()
	}
	return entry
}
