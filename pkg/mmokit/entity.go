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

// EntityByNetID constructs an Entity bound to the given stage, resolving the
// local ECS handle on first method call. Use when you have a NetID and need
// to interact with the corresponding entity on this stage.
func EntityByNetID(stage *pkguniverse.Stage, netID uint32) Entity {
	return Entity{netID: netID, stage: stage}
}

// EntityFromECS wraps an ecs.Entity into an Entity by reading its NetworkID
// component. Used internally by the framework (e.g. when handing entities to
// system callbacks). Returns zero-value Entity if the handle is not alive or
// has no NetworkID.
func EntityFromECS(stage *pkguniverse.Stage, h ecs.Entity) Entity {
	if stage == nil || !stage.ECSWorld().Alive(h) {
		return Entity{}
	}
	netIDMap := stage.NetworkIDMap()
	if !netIDMap.HasAll(h) {
		return Entity{}
	}
	return Entity{netID: netIDMap.Get(h).ID, cached: h, stage: stage}
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
	if e.stage == nil {
		return ecs.Entity{}
	}
	if e.cached != (ecs.Entity{}) && e.stage.ECSWorld().Alive(e.cached) {
		return e.cached
	}
	h, _, ok := e.stage.LookupNetID(e.netID)
	if !ok {
		return ecs.Entity{}
	}
	return h
}
