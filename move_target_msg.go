package mmokit

// MoveTargetMsg is the canonical click-to-move target update sent by
// the client. NewClickToMoveSystem registers a HandleClient handler for
// it as a side-effect of process.AddSystem, so games that want default
// click-to-move semantics get the wire type, the input handler, and the
// movement system from a single AddSystem call.
//
// Wire layout (reflect codec): X then Y, little-endian float32, no
// padding. Games that need richer semantics (input sequencing, stop
// gestures, target-entity IDs, waypoints) should skip NewClickToMoveSystem
// and wire ClickToMoveSystem + their own typed message manually.
type MoveTargetMsg struct {
	X float32
	Y float32
}
