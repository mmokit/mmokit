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
		entry.Z = pos.Z
	}
	if col != nil {
		entry.Radius = col.Radius
		entry.Width = col.Width
		entry.Height = col.Height
		entry.Depth = col.Depth
		entry.Layer = col.Layer
		entry.Shape = col.Shape
	}
	// Explicit identity when there is no Rotation, which is the COMMON case:
	// Stage.Spawn does not add one and a 2D game need never carry one.
	//
	// Redundant, strictly — component.Rotation's zero value is already
	// identity, because normalized() maps a zero-norm quaternion to
	// {0,0,0,1}. Written anyway, because that is a property of the accessors
	// and not of the bytes: ANY consumer that reads Rot's fields directly
	// instead of going through normalized() sees {0,0,0,0}, derives a basis
	// of three zero vectors, and a three-axis SAT built on it separates every
	// pair — silently, and only for entities that never set a rotation. Phase
	// 4b's narrow phase must renormalize; this line means the value it reads
	// is already right regardless.
	entry.Rot = component.RotationIdentity()
	if rot != nil {
		entry.Rot = *rot
	}
	return entry
}
