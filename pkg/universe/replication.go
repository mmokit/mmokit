package universe

import (
	"encoding/binary"
	"fmt"
	"math"

	"github.com/mlange-42/ark/ecs"
)

// ComponentID is a game-assigned identifier for a replicated component type.
type ComponentID uint16

// ComponentReplicator handles one component type's replication.
// The Scan/Apply closures capture typed Ark ECS mappers at registration time,
// bridging compile-time generics with the runtime registry.
type ComponentReplicator struct {
	ID ComponentID

	// Scan serializes the component from an entity. Returns nil if the entity
	// lacks this component.
	Scan func(entity ecs.Entity) []byte

	// Apply deserializes component data onto an existing replica entity.
	// Called on both new and updated replicas.
	Apply func(entity ecs.Entity, data []byte)

	// Add adds the component (from serialized data) to a newly created replica entity.
	// Some components need Add (first time) vs Apply (update).
	// If nil, Apply is used for both.
	Add func(entity ecs.Entity, data []byte)
}

// ReplicationRegistry tracks which components should be replicated across nodes.
type ReplicationRegistry struct {
	components []ComponentReplicator
	byID       map[ComponentID]*ComponentReplicator
}

// NewReplicationRegistry creates an empty replication registry.
func NewReplicationRegistry() *ReplicationRegistry {
	return &ReplicationRegistry{
		byID: make(map[ComponentID]*ComponentReplicator),
	}
}

// Register adds a component replicator to the registry. Panics on duplicate ID.
func (r *ReplicationRegistry) Register(c ComponentReplicator) {
	if _, exists := r.byID[c.ID]; exists {
		panic(fmt.Sprintf("replication: duplicate ComponentID %d", c.ID))
	}
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

// ---------------------------------------------------------------------------
// ReplicaFrame — generic wire format for replicated entities
// ---------------------------------------------------------------------------

// ReplicaFrame is the generic wire format for a replicated entity.
// Position and sector are always present (position is fundamental to replication).
// Components carries variable-length replicated component data.
type ReplicaFrame struct {
	NetworkID  uint32
	EntityType uint8
	PosX, PosY float32
	SectorX    int32
	SectorY    int32
	Components []ComponentSlice
}

// ComponentSlice is a single replicated component's data within a ReplicaFrame.
type ComponentSlice struct {
	ID   ComponentID
	Data []byte
}

// ---------------------------------------------------------------------------
// Binary codec for ReplicaFrame
// ---------------------------------------------------------------------------

// MarshalReplicaFrame encodes a ReplicaFrame to a compact binary format.
//
// Wire format:
//
//	[4] NetworkID
//	[1] EntityType
//	[4] PosX
//	[4] PosY
//	[4] SectorX (int32)
//	[4] SectorY (int32)
//	[2] component count
//	per component:
//	  [2] ComponentID
//	  [2] data length
//	  [N] data bytes
func MarshalReplicaFrame(f *ReplicaFrame) []byte {
	// Calculate total size
	size := 4 + 1 + 4 + 4 + 4 + 4 + 2 // header
	for _, c := range f.Components {
		size += 2 + 2 + len(c.Data) // id + len + data
	}

	buf := make([]byte, size)
	off := 0

	binary.LittleEndian.PutUint32(buf[off:], f.NetworkID)
	off += 4
	buf[off] = f.EntityType
	off++
	binary.LittleEndian.PutUint32(buf[off:], math.Float32bits(f.PosX))
	off += 4
	binary.LittleEndian.PutUint32(buf[off:], math.Float32bits(f.PosY))
	off += 4
	binary.LittleEndian.PutUint32(buf[off:], uint32(f.SectorX))
	off += 4
	binary.LittleEndian.PutUint32(buf[off:], uint32(f.SectorY))
	off += 4

	binary.LittleEndian.PutUint16(buf[off:], uint16(len(f.Components)))
	off += 2

	for _, c := range f.Components {
		binary.LittleEndian.PutUint16(buf[off:], uint16(c.ID))
		off += 2
		binary.LittleEndian.PutUint16(buf[off:], uint16(len(c.Data)))
		off += 2
		copy(buf[off:], c.Data)
		off += len(c.Data)
	}

	return buf
}

// UnmarshalReplicaFrame decodes a ReplicaFrame from binary data.
func UnmarshalReplicaFrame(data []byte) (*ReplicaFrame, error) {
	const headerSize = 4 + 1 + 4 + 4 + 4 + 4 + 2
	if len(data) < headerSize {
		return nil, fmt.Errorf("replica frame: need at least %d bytes, got %d", headerSize, len(data))
	}

	off := 0
	f := &ReplicaFrame{}

	f.NetworkID = binary.LittleEndian.Uint32(data[off:])
	off += 4
	f.EntityType = data[off]
	off++
	f.PosX = math.Float32frombits(binary.LittleEndian.Uint32(data[off:]))
	off += 4
	f.PosY = math.Float32frombits(binary.LittleEndian.Uint32(data[off:]))
	off += 4
	f.SectorX = int32(binary.LittleEndian.Uint32(data[off:]))
	off += 4
	f.SectorY = int32(binary.LittleEndian.Uint32(data[off:]))
	off += 4

	count := int(binary.LittleEndian.Uint16(data[off:]))
	off += 2

	f.Components = make([]ComponentSlice, 0, count)
	for i := 0; i < count; i++ {
		if off+4 > len(data) {
			return nil, fmt.Errorf("replica frame: truncated component header at index %d", i)
		}
		id := ComponentID(binary.LittleEndian.Uint16(data[off:]))
		off += 2
		dlen := int(binary.LittleEndian.Uint16(data[off:]))
		off += 2
		if off+dlen > len(data) {
			return nil, fmt.Errorf("replica frame: truncated component data at index %d", i)
		}
		f.Components = append(f.Components, ComponentSlice{
			ID:   id,
			Data: data[off : off+dlen],
		})
		off += dlen
	}

	return f, nil
}
