package mmokit

import (
	"github.com/mlange-42/ark/ecs"
)

// EntityHandle is the raw ECS entity handle type — an alias for
// ark's ecs.Entity. Use this type when you need to store, pass, or
// compare-zero a bare entity reference without going through the
// richer mmokit.Entity wrapper (which carries a Stage and NetID).
//
// Most game code should use mmokit.Entity (the wrapper) for ECS
// operations — Get/Set/Has/Send all take Entity. EntityHandle exists
// for cases that have to interoperate with framework types that
// already use the raw handle, e.g. engine.PlayerSession.Entity.
type EntityHandle = ecs.Entity
