package universe

import (
	"reflect"
	"unsafe"

	"github.com/mlange-42/ark/ecs"

	"github.com/zenion/mmoserver/pkg/component"
)

// ErasedOpts is the type-erased internal representation that ComponentOption
// constructors populate. The transfer codec consumes only this form.
//
// Marshal / UnmarshalInto / PreMarshal drive the cross-cell serialization
// path. Binding and LocalOnly are bundle-walker fields — set by mmokit's
// WithBinding / LocalOnly options and consumed by mmokit.RegisterKind[T]'s
// reflection walker. The universe transfer codec ignores Binding+LocalOnly.
//
// Wire bytes are unchanged across this refactor: the typed paths that used
// to call marshal(*T) directly now go through Marshal(unsafe.Pointer(*T))
// — the adapter just casts and forwards, so output bytes are byte-identical.
type ErasedOpts struct {
	Marshal       func(p unsafe.Pointer) []byte
	UnmarshalInto func(b []byte, p unsafe.Pointer)
	PreMarshal    func(p unsafe.Pointer)
	Binding       any  // system.ComponentBinding — typed `any` to avoid an import cycle
	BindingFn     any  // func(*ecs.World) system.ComponentBinding — typed `any` to avoid an import cycle
	LocalOnly     bool
}

// ComponentOption configures per-component behavior at registration time.
// Built by the typed constructors below. Internally type-erased so the same
// option value flows through both the typed RegisterComponent[T] path and
// the type-erased RegisterComponentByID / KindComponentByID paths used by
// mmokit.RegisterKind[T]'s bundle walker.
type ComponentOption struct {
	Apply func(*ErasedOpts)
}

// WithMarshal overrides the default reflection-based marshal/unmarshal with
// custom functions. When provided, ValidateComponentType is not called.
func WithMarshal[T any](marshal func(*T) []byte, unmarshal func([]byte, *T)) ComponentOption {
	return ComponentOption{Apply: func(o *ErasedOpts) {
		o.Marshal = func(p unsafe.Pointer) []byte { return marshal((*T)(p)) }
		o.UnmarshalInto = func(b []byte, p unsafe.Pointer) { unmarshal(b, (*T)(p)) }
	}}
}

// WithPreMarshal registers a function that runs on a copy of the component
// before marshaling. Useful for clearing entity references or other fields
// that should not be sent over the wire.
func WithPreMarshal[T any](fn func(*T)) ComponentOption {
	return ComponentOption{Apply: func(o *ErasedOpts) {
		o.PreMarshal = func(p unsafe.Pointer) { fn((*T)(p)) }
	}}
}

// transferCoreTypes is the set of component types already serialized as
// top-level fields in TransferFrame (PosX/PosY, VelX/VelY, Rotation,
// CellCoord). Registering one of these in a ReplicationRegistry is valid
// for border-frame replication, but the redundant entry in frame.Components
// must not overwrite the authoritative normalized values during handoff.
// RegisterComponent detects membership here and sets IsTransferCore = true.
var transferCoreTypes = map[reflect.Type]bool{
	reflect.TypeFor[component.Position](): true,
	reflect.TypeFor[component.Velocity](): true,
	reflect.TypeFor[component.Rotation](): true,
	reflect.TypeFor[component.CellCoord](): true,
}

// IsTransferCore reports whether t is a framework-owned component already
// carried by TransferFrame's top-level fields (Position / Velocity / Rotation
// / CellCoord). RegisterKind rejects these as bundle fields because the
// framework owns their wire format end-to-end: TransferFrame serializes them
// across cells, EngineBindings replicates them to clients, and Stage.Spawn
// either requires them (Position) or defaults them (Velocity). Declaring one
// in a bundle does nothing useful and risks double-encoding on the wire.
func IsTransferCore(t reflect.Type) bool {
	return transferCoreTypes[t]
}

// RegisterComponent registers an ECS component for automatic replication and
// transfer. It creates a ComponentReplicator with Scan, Apply, and Add closures
// that capture the typed *ecs.Map1[T].
//
// If no WithMarshal option is provided, the component type is validated at
// registration time and reflection-based marshal/unmarshal is used.
//
// Components whose types appear in transferCoreTypes (Position, Velocity,
// Rotation, CellCoord) have IsTransferCore set to true — HandoffDriver and
// SerializeEntity skip them in frame.Components since those values are already
// carried by the frame's dedicated fields and normalized for the destination.
func RegisterComponent[T any](reg *ReplicationRegistry, m *ecs.Map1[T], opts ...ComponentOption) {
	var o ErasedOpts
	for _, opt := range opts {
		opt.Apply(&o)
	}

	// Resolve marshal/unmarshal functions. If no custom Marshal was supplied,
	// validate the type at registration time and fall back to reflection.
	if o.Marshal == nil {
		ValidateComponentType(reflect.TypeFor[T]())
	}

	isCore := transferCoreTypes[reflect.TypeFor[T]()]

	reg.Register(ComponentReplicator{
		IsTransferCore: isCore,
		Scan: func(entity ecs.Entity) []byte {
			if !m.HasAll(entity) {
				return nil
			}
			c := m.Get(entity)
			if o.PreMarshal != nil {
				// Work on a copy to avoid mutating the original.
				tmp := *c
				o.PreMarshal(unsafe.Pointer(&tmp))
				if o.Marshal != nil {
					return o.Marshal(unsafe.Pointer(&tmp))
				}
				return ReflectMarshal(&tmp)
			}
			if o.Marshal != nil {
				return o.Marshal(unsafe.Pointer(c))
			}
			return ReflectMarshal(c)
		},
		Apply: func(entity ecs.Entity, data []byte) {
			if !m.HasAll(entity) {
				return
			}
			if o.UnmarshalInto != nil {
				o.UnmarshalInto(data, unsafe.Pointer(m.Get(entity)))
				return
			}
			ReflectUnmarshal(data, m.Get(entity))
		},
		Add: func(entity ecs.Entity, data []byte) {
			if m.HasAll(entity) {
				// Entity already has this component (e.g. from CreateReplica
				// or SpawnFromTransferCore) — update in place.
				if o.UnmarshalInto != nil {
					o.UnmarshalInto(data, unsafe.Pointer(m.Get(entity)))
					return
				}
				ReflectUnmarshal(data, m.Get(entity))
				return
			}
			var comp T
			if o.UnmarshalInto != nil {
				o.UnmarshalInto(data, unsafe.Pointer(&comp))
			} else {
				ReflectUnmarshal(data, &comp)
			}
			m.Add(entity, &comp)
		},
	})
}

// RegisterComponentByID is the type-erased counterpart to RegisterComponent.
// All access goes through World.Unsafe() — no typed Map1[T]. Used by the
// reflection-driven bundle walker in mmokit.RegisterKind[T].
//
// Wire format is byte-identical to RegisterComponent: the same ReflectMarshal
// / ReflectUnmarshal helpers drive the default path, and the
// PreMarshal/Marshal/UnmarshalInto closures from ComponentOption are honored
// the same way.
func RegisterComponentByID(
	reg *ReplicationRegistry,
	w *ecs.World,
	id ecs.ID,
	t reflect.Type,
	opts ...ComponentOption,
) {
	var o ErasedOpts
	for _, opt := range opts {
		opt.Apply(&o)
	}

	// If no custom Marshal was supplied, validate the type at registration
	// time and fall back to reflection.
	if o.Marshal == nil {
		ValidateComponentType(t)
	}

	isCore := transferCoreTypes[t]
	u := w.Unsafe()

	// Allocate a scratch buffer of type t once per replicator, reused under
	// the PreMarshal-copy path. Cells use one ReplicationRegistry per Stage
	// and Scan/Apply run on the loop goroutine, so single-buffer reuse is safe.
	scratchPtr := reflect.New(t) // pointer to zero-valued T

	reg.Register(ComponentReplicator{
		IsTransferCore: isCore,
		Scan: func(entity ecs.Entity) []byte {
			if !u.Has(entity, id) {
				return nil
			}
			ptr := u.Get(entity, id)
			if o.PreMarshal != nil {
				// Copy the live component into the scratch buffer and run
				// PreMarshal there — matches RegisterComponent's "work on a
				// copy to avoid mutating the original" semantics.
				dst := unsafe.Pointer(scratchPtr.Pointer())
				reflect.NewAt(t, dst).Elem().Set(reflect.NewAt(t, ptr).Elem())
				o.PreMarshal(dst)
				if o.Marshal != nil {
					return o.Marshal(dst)
				}
				return ReflectMarshal(scratchPtr.Interface())
			}
			if o.Marshal != nil {
				return o.Marshal(ptr)
			}
			return ReflectMarshal(reflect.NewAt(t, ptr).Interface())
		},
		Apply: func(entity ecs.Entity, data []byte) {
			if !u.Has(entity, id) {
				return
			}
			ptr := u.Get(entity, id)
			if o.UnmarshalInto != nil {
				o.UnmarshalInto(data, ptr)
				return
			}
			ReflectUnmarshal(data, reflect.NewAt(t, ptr).Interface())
		},
		Add: func(entity ecs.Entity, data []byte) {
			if !u.Has(entity, id) {
				u.Add(entity, id) // ark zero-initializes
			}
			ptr := u.Get(entity, id)
			if o.UnmarshalInto != nil {
				o.UnmarshalInto(data, ptr)
				return
			}
			ReflectUnmarshal(data, reflect.NewAt(t, ptr).Interface())
		},
	})
}
