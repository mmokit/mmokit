package game

import "github.com/zenion/mmokit/pkg/engine"

// Game-range debug flags. The engine reserves bits 0-15; games reserve 16-31
// (see pkg/engine/debug_flags.go). Grant them per session from the console.

// DebugInput enables per-player logging of movement commands the server did
// NOT apply — stale sequences, commands consumed while movement is disabled,
// and targets rejected as non-finite or out of world.
//
// Opt-in rather than always-on because a client controls how often it sends
// SetMoveTarget, so unconditional logging here is a log-volume amplifier. It
// is also exactly the kind of thing you want for one player at a time: from
// the client's side a rejected command and an applied one look identical, so
// this is the only way to tell rubber-banding caused by a rejected target
// from rubber-banding caused by predictor drift.
const DebugInput = engine.DebugFlag(1 << 16)

func init() {
	engine.RegisterDebugFlag("input", 16,
		"Log movement commands the server did not apply (stale, blocked, or rejected)")
}
