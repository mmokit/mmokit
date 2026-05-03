package mmokit

import (
	"github.com/mlange-42/ark/ecs"
	pkguniverse "github.com/zenion/mmoserver/pkg/universe"
)

// Entity is the game-facing handle. Value type, cheap to pass.
// Wraps a NetID + lazily-resolved local ECS handle + stage ref.
// Methods are safe on zero/dead entities — they return zero values, never panic.
type Entity struct {
	netID  uint32
	cached ecs.Entity // resolved on first method call; zero means unresolved
	stage  *pkguniverse.Stage
}

// NetID returns the entity's stable cross-cell network ID.
// Returns 0 for zero-value Entity.
func (e Entity) NetID() uint32 { return e.netID }

// Alive reports whether the entity exists and is alive on its bound stage.
// Returns false for zero-value or cross-stage-stale Entity.
func (e Entity) Alive() bool {
	if e.netID == 0 || e.stage == nil {
		return false
	}
	h := e.resolveHandle()
	return h != (ecs.Entity{}) && e.stage.ECSWorld().Alive(h)
}

// resolveHandle returns the cached ECS handle, re-resolving from the stage's
// NetID index if the cache is stale or unset. Returns ecs.Entity{} if the
// entity is not currently known to the stage.
func (e Entity) resolveHandle() ecs.Entity {
	// Implementation in Task 1.3 once stage NetID lookup is wired.
	return e.cached
}
