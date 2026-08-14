package mmokit

import "github.com/mmokit/mmokit/pkg/universe"

// Entity is the rich game-facing handle. Value type, cheap to pass.
// Methods are safe on zero/dead entities — they return zero values, never panic.
type Entity = universe.Entity

// EntityByNetID constructs an Entity bound to the given stage, resolving the
// local ECS handle on first method call.
var EntityByNetID = universe.EntityByNetID

// EntityFromECS wraps an ecs.Entity into an Entity by reading its NetworkID
// component. Returns zero-value Entity if the handle is not alive or has no NetworkID.
var EntityFromECS = universe.EntityFromECS
