package system

import (
	"math"

	"github.com/mlange-42/ark/ecs"

	gamepb "github.com/zenion/mmoserver/gen/go"
	"github.com/zenion/mmoserver/internal/game"
	"github.com/zenion/mmoserver/pkg/spatial"
)

// SnapshotHasher is an inline FNV-64a hasher for entity diff detection.
// No interface dispatch — all methods are value-type operations.
type SnapshotHasher struct {
	hash uint64
}

const (
	fnvOffset64 uint64 = 14695981039346656037
	fnvPrime64  uint64 = 1099511628211
)

// Reset reinitializes the hasher to the FNV offset basis.
func (h *SnapshotHasher) Reset() {
	h.hash = fnvOffset64
}

// Sum returns the current hash value.
func (h *SnapshotHasher) Sum() uint64 {
	return h.hash
}

// Float32 hashes a float32 value.
func (h *SnapshotHasher) Float32(v float32) {
	bits := math.Float32bits(v)
	h.hash ^= uint64(bits & 0xFF)
	h.hash *= fnvPrime64
	h.hash ^= uint64((bits >> 8) & 0xFF)
	h.hash *= fnvPrime64
	h.hash ^= uint64((bits >> 16) & 0xFF)
	h.hash *= fnvPrime64
	h.hash ^= uint64((bits >> 24) & 0xFF)
	h.hash *= fnvPrime64
}

// Uint32 hashes a uint32 value.
func (h *SnapshotHasher) Uint32(v uint32) {
	h.hash ^= uint64(v & 0xFF)
	h.hash *= fnvPrime64
	h.hash ^= uint64((v >> 8) & 0xFF)
	h.hash *= fnvPrime64
	h.hash ^= uint64((v >> 16) & 0xFF)
	h.hash *= fnvPrime64
	h.hash ^= uint64((v >> 24) & 0xFF)
	h.hash *= fnvPrime64
}

// Uint8 hashes a uint8 value.
func (h *SnapshotHasher) Uint8(v uint8) {
	h.hash ^= uint64(v)
	h.hash *= fnvPrime64
}

// Bool hashes a boolean value.
func (h *SnapshotHasher) Bool(v bool) {
	if v {
		h.Uint8(1)
	} else {
		h.Uint8(0)
	}
}

// Int64 hashes an int64 value.
func (h *SnapshotHasher) Int64(v int64) {
	u := uint64(v)
	h.hash ^= u & 0xFF
	h.hash *= fnvPrime64
	h.hash ^= (u >> 8) & 0xFF
	h.hash *= fnvPrime64
	h.hash ^= (u >> 16) & 0xFF
	h.hash *= fnvPrime64
	h.hash ^= (u >> 24) & 0xFF
	h.hash *= fnvPrime64
	h.hash ^= (u >> 32) & 0xFF
	h.hash *= fnvPrime64
	h.hash ^= (u >> 40) & 0xFF
	h.hash *= fnvPrime64
	h.hash ^= (u >> 48) & 0xFF
	h.hash *= fnvPrime64
	h.hash ^= (u >> 56) & 0xFF
	h.hash *= fnvPrime64
}

// NetworkContext holds precomputed per-tick shared data for handlers.
type NetworkContext struct {
	GW       *game.GameWorld
	LockedBy map[ecs.Entity]lockerInfo
}

// EntityNetHandler defines the per-entity-type network serialization interface.
type EntityNetHandler interface {
	// EntityType returns the component.Type* constant this handler covers.
	EntityType() uint8
	// HashSnapshot writes diff-relevant mutable fields into the hasher.
	// Base fields (pos, vel, rot, locked_by) are already hashed by NetworkSystem.
	HashSnapshot(h *SnapshotHasher, ctx *NetworkContext, entry spatial.Entry)
	// Serialize sets the type_data oneof on the EntityState.
	// Base fields are already populated by NetworkSystem.
	Serialize(state *gamepb.EntityState, ctx *NetworkContext, entry spatial.Entry)
}

// NetHandlerRegistry maps entity type constants to their handlers.
type NetHandlerRegistry struct {
	handlers map[uint8]EntityNetHandler
}

// NewNetHandlerRegistry creates an empty handler registry.
func NewNetHandlerRegistry() *NetHandlerRegistry {
	return &NetHandlerRegistry{
		handlers: make(map[uint8]EntityNetHandler),
	}
}

// Register adds a handler for its entity type.
func (r *NetHandlerRegistry) Register(h EntityNetHandler) {
	r.handlers[h.EntityType()] = h
}

// Get returns the handler for the given entity type, or nil if none registered.
func (r *NetHandlerRegistry) Get(entityType uint8) EntityNetHandler {
	return r.handlers[entityType]
}
