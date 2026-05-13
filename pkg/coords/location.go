package coords

// Location is a world-space anchor for placing a player entity — used for
// initial spawn, respawn, and (later) teleport/warp. The coord frame is
// absolute world-space, NOT cell-local, so a Location survives cell
// split/merge: the gateway resolves which cell currently owns (X, Y) at
// dispatch time.
//
// Facing is in radians, 0 = +X axis. The engine does not auto-apply it —
// games opt in by passing mmokit.Rotation{Angle: loc.Facing} to Stage.Spawn.
//
// Tag is opaque to the engine. Games that want tagged destinations
// ("bank", "tutorial_start") populate it; games that don't leave it empty.
type Location struct {
	X, Y   float32 // world-space coordinates
	Facing float32 // radians, 0 = +X axis
	Tag    string  // game-defined; ignored by engine
}

// IsZero reports whether l is the zero value — useful as a "no preference"
// sentinel in callers that optionally override a default.
func (l Location) IsZero() bool { return l == (Location{}) }
