package component

// MoveMode selects how PhysicsSystem integrates an entity each tick.
//
// The zero value is MoveFly, which is exactly the behaviour every entity had
// before move modes existed: velocity integrates on every axis and nothing
// else touches it. That is what lets a 2D game — and any 3D entity that does
// not opt in — carry the Motion component or omit it with no difference.
type MoveMode uint8

const (
	// MoveFly integrates velocity and applies no gravity. Spaceships,
	// projectiles, cameras, anything in a 2D profile.
	MoveFly MoveMode = iota

	// MoveWalk applies gravity, then clamps to GroundZ and marks the entity
	// Grounded when it would fall through. A character on terrain.
	MoveWalk

	// MoveBallistic applies gravity and does NOT clamp. A thrown object,
	// which is expected to pass below the ground plane and be despawned or
	// handled by the game rather than resting on it.
	MoveBallistic
)

// String returns a stable name for diagnostics.
func (m MoveMode) String() string {
	switch m {
	case MoveFly:
		return "fly"
	case MoveWalk:
		return "walk"
	case MoveBallistic:
		return "ballistic"
	default:
		return "unknown"
	}
}

// Motion selects an entity's integration behaviour. It is OPTIONAL: an entity
// without it integrates as MoveFly, which is the pre-existing behaviour.
//
// It carries no net: tag. Like the other core components the framework owns
// its wire format end to end, and no client needs it — a client renders
// position and orientation, not the rule that produced them.
type Motion struct {
	// Mode selects the integration rule.
	Mode MoveMode

	// Grounded reports that the entity is resting on GroundZ. Set by
	// PhysicsSystem under MoveWalk; games read it to gate jumping.
	Grounded bool

	// GroundZ is the height MoveWalk clamps to. A plain scalar rather than a
	// terrain query because pkg/spatial is 2D until phase 4; a game with real
	// terrain writes this from its own height lookup before physics runs.
	GroundZ float32
}
