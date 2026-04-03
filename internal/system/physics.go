package system

import "github.com/zenion/mmoserver/pkg/mmokit"

// PhysicsSystem integrates velocity into position each tick.
// Delegates to the generic engine physics system via embedding.
type PhysicsSystem = mmokit.PhysicsSystem
