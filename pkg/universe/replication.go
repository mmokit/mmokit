package universe

import (
	"encoding/binary"
	"math"

	"github.com/mlange-42/ark/ecs"

	"github.com/mmokit/mmokit/pkg/component"
)

// ComponentID is a game-assigned identifier for a replicated component type.
type ComponentID uint16

// ComponentReplicator handles one component type's replication.
// The Scan/Apply closures capture typed Ark ECS mappers at registration time,
// bridging compile-time generics with the runtime registry.
type ComponentReplicator struct {
	ID ComponentID

	// SkipOnTransfer marks components TransferFrame already carries as
	// top-level fields. HandoffDriver and SerializeEntity skip this
	// replicator when building frame.Components, and SpawnFromTransferCore
	// skips it when applying — so the tail cannot overwrite the authoritative
	// values written from frame.PosX/PosY and normalized for the destination.
	SkipOnTransfer bool

	// SkipOnBorder marks components the BORDER header already carries.
	//
	// Deliberately a different set from SkipOnTransfer, not a synonym. The
	// border header carries the collider's RADIUS alone while TransferFrame
	// carries the whole 18-byte collider, so Collider is SkipOnTransfer and
	// NOT SkipOnBorder — it has to ride the border tail or a neighbour-owned
	// entity arrives with no extents, no layer and no shape.
	SkipOnBorder bool

	// Scan serializes the component from an entity. Returns nil if the entity
	// lacks this component.
	Scan func(entity ecs.Entity) []byte

	// Apply deserializes component data onto an existing replica entity.
	// Called on both new and updated replicas.
	//
	// Returns a non-nil error when the checked decoder refuses data. Callers
	// skip THAT COMPONENT and keep the entity — see noteComponentDecodeDrop,
	// which is where the failure policy and its trade are written down. Apply
	// decodes in place, so a refused blob can leave the live component
	// partially updated; the next clean scan repairs it.
	Apply func(entity ecs.Entity, data []byte) error

	// Add adds the component (from serialized data) to a newly created replica entity.
	// Some components need Add (first time) vs Apply (update).
	// If nil, Apply is used for both. Same error contract as Apply.
	Add func(entity ecs.Entity, data []byte) error
}

// ReplicationRegistry tracks which components should be replicated across nodes.
type ReplicationRegistry struct {
	components []ComponentReplicator
	byID       map[ComponentID]*ComponentReplicator
	nextID     ComponentID
}

// NewReplicationRegistry creates an empty replication registry.
func NewReplicationRegistry() *ReplicationRegistry {
	return &ReplicationRegistry{
		byID: make(map[ComponentID]*ComponentReplicator),
	}
}

// Register adds a component replicator to the registry with an auto-assigned ID.
func (r *ReplicationRegistry) Register(c ComponentReplicator) {
	r.nextID++
	c.ID = r.nextID
	r.components = append(r.components, c)
	r.byID[c.ID] = &r.components[len(r.components)-1]
}

// Get returns the replicator for the given ID, or nil if not registered.
func (r *ReplicationRegistry) Get(id ComponentID) *ComponentReplicator {
	return r.byID[id]
}

// All returns all registered replicators in registration order.
func (r *ReplicationRegistry) All() []ComponentReplicator {
	return r.components
}

// Len returns the number of registered replicators.
func (r *ReplicationRegistry) Len() int {
	return len(r.components)
}

// ComponentSlice is a single replicated component's data within a transfer frame.
// Used by TransferFrame for cross-cell entity transfers.
type ComponentSlice struct {
	ID   ComponentID
	Data []byte
}

// ---------------------------------------------------------------------------
// Collider codec (component ID 0, used in TransferFrame)
// ---------------------------------------------------------------------------

// UnmarshalCollider decodes a Collider from binary data.
func UnmarshalCollider(data []byte) component.Collider {
	if len(data) < 14 {
		return component.Collider{}
	}
	return component.Collider{
		Radius: getFloat32(data[0:]),
		Width:  getFloat32(data[4:]),
		Height: getFloat32(data[8:]),
		Layer:  data[12],
		// A wire byte becoming the union arm. An out-of-range value is a peer
		// disagreeing about the protocol, not a shape, and spatial's narrow
		// phase would silently treat it as a degenerate box — so clamp to the
		// sphere arm rather than propagate it.
		Shape: validShape(data[13]),
	}
}

// validShape converts a wire byte to a ShapeKind, falling back to ShapeSphere
// for a value this build does not implement.
func validShape(b byte) component.ShapeKind {
	k := component.ShapeKind(b)
	if !k.Valid() {
		return component.ShapeSphere
	}
	return k
}

func getFloat32(buf []byte) float32 {
	return math.Float32frombits(binary.LittleEndian.Uint32(buf))
}
