package universe

import (
	"reflect"
	"unsafe"

	"github.com/mlange-42/ark/ecs"

	"github.com/zenion/mmokit/pkg/component"
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
	Binding       any // system.ComponentBinding — typed `any` to avoid an import cycle
	BindingFn     any // func(*ecs.World) system.ComponentBinding — typed `any` to avoid an import cycle
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
	reflect.TypeFor[component.Position]():  true,
	reflect.TypeFor[component.Velocity]():  true,
	reflect.TypeFor[component.Rotation]():  true,
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

// reflectMarshalOrDrop is the Scan-closure form of ReflectMarshal. Scan's
// signature is `func(entity ecs.Entity) []byte` with no error return, and nil
// already means "this entity does not carry the component", so an encoder-guard
// rejection degrades to exactly that: the component is omitted from the border
// or transfer frame and the rest of the entity still ships.
//
// That trade is deliberate. Failing the whole frame would turn one oversized
// string field into entity loss on the destination cell; the destination
// instead keeps a stale or absent copy of this one component, which the next
// clean scan repairs. The drop is counted and logged, never silent.
func reflectMarshalOrDrop(t reflect.Type, ptr any) []byte {
	b, err := ReflectMarshal(ptr)
	if err != nil {
		NoteMarshalDrop(CatMeshCell,
			"replication: component %s omitted from frame — %v", t, err)
		return nil
	}
	return b
}

// noteComponentDecodeDrop records a replicated component blob the checked
// decoder refused. It is the one place the receive-side failure policy is
// written down; the three consumers of ComponentReplicator.Apply/Add
// (applyEntityComponents, SpawnFromTransferCore, drainPendingPromotes) all
// route here and all keep going.
//
// The policy is per-COMPONENT, not per-entity. A malformed blob skips that
// component and the entity survives with everything else. Aborting the
// surrounding cross-cell transfer instead would turn one bad component into an
// entity-LOSS bug on the handoff path: the source has already demoted, so an
// entity the destination refuses to spawn exists nowhere in the cluster.
//
// State the trade plainly, because it is an authority-boundary decision rather
// than an oversight (docs/roadmap.md §6.8.4): a transfer can now COMPLETE with
// an entity carrying a stale, absent, or partially-updated copy of one
// component — state divergence between the two cells — instead of failing
// cleanly. Divergence that the next clean border scan repairs is the cheaper
// failure; entity loss is permanent. This mirrors the send-side policy in
// reflectMarshalOrDrop above.
//
// CatMeshCell because the blob arrives on the cell's mesh ingress, which is the
// category an operator investigating divergence between two cells already has
// enabled. The drop is counted (DecodeDrops) and throttled, never silent.
func noteComponentDecodeDrop(where string, id ComponentID, err error) {
	NoteDecodeDrop(CatMeshCell,
		"replication: %s skipped component %d — %v", where, id, err)
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

	// applyInPlace decodes into a scratch copy and commits it to the live
	// component only once the whole body has been accepted.
	//
	// Decoding straight into m.Get(entity) tears the component when a body is
	// refused partway: the fields the decoder already reached hold peer-supplied
	// values while the rest keep the previous tick's, and nothing downstream can
	// tell the difference from real data. That is reachable from anything that
	// can put bytes on the border-replication or handoff path, so a refused blob
	// must leave the component exactly as it found it.
	//
	// The scratch is SEEDED from the live value rather than zeroed, which keeps
	// two existing behaviours intact: a custom UnmarshalInto that merges into
	// prior state still sees it, and fields a short body never reaches keep
	// their current values instead of silently reverting to zero.
	applyInPlace := func(live *T, data []byte) error {
		scratch := *live
		if o.UnmarshalInto != nil {
			o.UnmarshalInto(data, unsafe.Pointer(&scratch))
			*live = scratch
			return nil
		}
		if err := ReflectUnmarshal(data, &scratch); err != nil {
			return err
		}
		*live = scratch
		return nil
	}

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
				return reflectMarshalOrDrop(reflect.TypeFor[T](), &tmp)
			}
			if o.Marshal != nil {
				return o.Marshal(unsafe.Pointer(c))
			}
			return reflectMarshalOrDrop(reflect.TypeFor[T](), c)
		},
		Apply: func(entity ecs.Entity, data []byte) error {
			if !m.HasAll(entity) {
				return nil
			}
			return applyInPlace(m.Get(entity), data)
		},
		Add: func(entity ecs.Entity, data []byte) error {
			if m.HasAll(entity) {
				// Entity already has this component (e.g. from CreateReplica
				// or SpawnFromTransferCore) — update in place.
				return applyInPlace(m.Get(entity), data)
			}
			var comp T
			if o.UnmarshalInto != nil {
				o.UnmarshalInto(data, unsafe.Pointer(&comp))
			} else if err := ReflectUnmarshal(data, &comp); err != nil {
				// Do not attach a half-decoded component to a fresh replica.
				// Leaving it absent lets EnsureEntityKindComponents zero-fill
				// a kind-registered component; attaching one whose trailing
				// fields are zero and whose leading fields came from a refused
				// body is indistinguishable from real data downstream.
				return err
			}
			m.Add(entity, &comp)
			return nil
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
	scratchPtr := reflect.New(t) // pointer to zero-valued T, PreMarshal staging

	// decodeScratch is the type-erased counterpart to RegisterComponent's
	// applyInPlace scratch: bodies are decoded here and copied into world
	// storage only once accepted, so a refused blob cannot tear a live
	// component. Kept separate from scratchPtr because Scan and Apply are
	// distinct phases and sharing one buffer would couple them for no gain.
	// Both are safe to capture: a ReplicationRegistry is per-Stage, so every
	// call arrives on that cell's own loop goroutine.
	decodeScratch := reflect.New(t)

	// applyInPlace mirrors the typed registrar's, seeding the scratch from the
	// live value so a merging UnmarshalInto and unreached fields both behave
	// exactly as they did when the decode wrote through to storage directly.
	applyInPlace := func(ptr unsafe.Pointer, data []byte) error {
		dst := unsafe.Pointer(decodeScratch.Pointer())
		reflect.NewAt(t, dst).Elem().Set(reflect.NewAt(t, ptr).Elem())
		if o.UnmarshalInto != nil {
			o.UnmarshalInto(data, dst)
			reflect.NewAt(t, ptr).Elem().Set(reflect.NewAt(t, dst).Elem())
			return nil
		}
		if err := ReflectUnmarshal(data, decodeScratch.Interface()); err != nil {
			return err
		}
		reflect.NewAt(t, ptr).Elem().Set(reflect.NewAt(t, dst).Elem())
		return nil
	}

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
				return reflectMarshalOrDrop(t, scratchPtr.Interface())
			}
			if o.Marshal != nil {
				return o.Marshal(ptr)
			}
			return reflectMarshalOrDrop(t, reflect.NewAt(t, ptr).Interface())
		},
		Apply: func(entity ecs.Entity, data []byte) error {
			if !u.Has(entity, id) {
				return nil
			}
			return applyInPlace(u.Get(entity, id), data)
		},
		Add: func(entity ecs.Entity, data []byte) error {
			if u.Has(entity, id) {
				return applyInPlace(u.Get(entity, id), data)
			}
			// Fresh component: decode into the scratch FIRST and attach only on
			// success, so a refused blob leaves the entity without the component
			// rather than with a zero-valued one. An attached zero component is
			// indistinguishable downstream from real data, while an absent one
			// still lets EnsureEntityKindComponents zero-fill deliberately.
			dst := unsafe.Pointer(decodeScratch.Pointer())
			reflect.NewAt(t, dst).Elem().SetZero()
			if o.UnmarshalInto != nil {
				o.UnmarshalInto(data, dst)
			} else if err := ReflectUnmarshal(data, decodeScratch.Interface()); err != nil {
				return err
			}
			u.Add(entity, id) // ark zero-initializes
			reflect.NewAt(t, u.Get(entity, id)).Elem().Set(reflect.NewAt(t, dst).Elem())
			return nil
		},
	})
}
