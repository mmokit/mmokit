package universe

import (
	"reflect"

	"github.com/mlange-42/ark/ecs"

	"github.com/zenion/mmoserver/pkg/system"
)

// KindComponentMode classifies a bundle field's registration semantics.
//
//   - KindComponentRequired: registered for cross-cell transfer; caller MUST pass
//     it at spawn time (Stage.Spawn enforces under InvariantPanic mode).
//   - KindComponentOptional: registered for cross-cell transfer; caller MAY omit
//     it at spawn time — auto_replicator's binding writes zeros when absent
//     (used for transfer-preserved server-side bookkeeping like PlacedID).
//   - KindComponentLocal: NOT registered for cross-cell transfer; auto-added with
//     zero value on transfer receive (used for transient host-local state).
type KindComponentMode uint8

const (
	KindComponentRequired KindComponentMode = iota
	KindComponentOptional
	KindComponentLocal
)

// EntityKindDef describes an entity kind's components for transfer replication,
// client network replication, and schema export. Build one per entity type and
// pass it to Stage.RegisterEntityKind.
type EntityKindDef struct {
	Kind       uint8
	Name       string // human-readable name for schema export (e.g. "Player")
	components []kindComponent

	// requiredTypes lists the reflect.Type of every Required Bundle field
	// declared by this kind. Stage.Spawn uses this under InvariantPanic to
	// verify callers attached every required component (catches the
	// "forgot to Set Health, NPC spawns dead-on-arrival" bug-class).
	// Optional and Local fields are excluded — Optional is auto-zeroed at
	// hash, Local is auto-added on transfer receive.
	requiredTypes []reflect.Type

	// NetworkBindings stores ComponentBinding values for the network
	// replication AutoReplicator. Populated by mmokit.RegisterKind[T]'s
	// bundle-walker when realizing each stage.
	NetworkBindings []system.ComponentBinding
}

// ComponentAccessor exposes one component on an entity for runtime inspection
// and mutation. Built during kind registration with the concrete component
// type in scope, so it can resolve the component without a typed Map1[T].
type ComponentAccessor struct {
	Name string       // component struct type name, e.g. "Health"
	Type reflect.Type // the component struct type
	// Get returns a *T (as any) pointing at the entity's live component
	// storage, or (nil, false) if the entity lacks the component. The returned
	// pointer aliases live ECS storage and is only valid for immediate,
	// synchronous use within a single on-loop call — a structural ECS mutation
	// (adding or removing any component on the same archetype) may relocate
	// storage and invalidate it. Read or copy the value out before yielding;
	// never store the pointer across ticks.
	Get func(entity ecs.Entity) (any, bool)
}

// kindComponent holds closures for one component's registration across subsystems.
type kindComponent struct {
	// registerTransfer registers this component with a ReplicationRegistry
	// for cross-cell entity transfers.
	registerTransfer func(reg *ReplicationRegistry)

	// ensureExists adds a zero-value component to an entity if it doesn't already
	// have one. Used for auto-filling on transfer receive (Stage.Spawn requires
	// the caller to pass kind components explicitly).
	ensureExists func(entity ecs.Entity)

	// buildAccessor constructs a ComponentAccessor for this component bound to
	// the registration-time world. nil only if registration predates accessors.
	buildAccessor func() ComponentAccessor
}

// KindComponentByID registers a component on an EntityKindDef from a
// pre-resolved ecs.ID + reflect.Type. See KindComponentMode for the
// transfer/spawn semantics of each mode.
//
// Used by mmokit.RegisterKind[T] which walks a bundle struct via
// reflection and resolves each field's component type via ecs.TypeID.
// All access goes through World.Unsafe() — no typed Map1[T].
//
// Internal API; game code uses mmokit.RegisterKind[T].
func KindComponentByID(
	def *EntityKindDef,
	w *ecs.World,
	id ecs.ID,
	t reflect.Type,
	mode KindComponentMode,
	opts ...ComponentOption,
) {
	u := w.Unsafe()
	kc := kindComponent{
		ensureExists: func(entity ecs.Entity) {
			if !u.Has(entity, id) {
				u.Add(entity, id)
			}
		},
	}
	kc.buildAccessor = func() ComponentAccessor {
		return ComponentAccessor{
			Name: t.Name(),
			Type: t,
			Get: func(entity ecs.Entity) (any, bool) {
				if !u.Has(entity, id) {
					return nil, false
				}
				return reflect.NewAt(t, u.Get(entity, id)).Interface(), true
			},
		}
	}
	if mode != KindComponentLocal {
		kc.registerTransfer = func(reg *ReplicationRegistry) {
			RegisterComponentByID(reg, w, id, t, opts...)
		}
	}
	def.components = append(def.components, kc)
	if mode == KindComponentRequired {
		def.requiredTypes = append(def.requiredTypes, t)
	}
}

// Components returns the number of registered components.
func (def *EntityKindDef) Components() int {
	return len(def.components)
}

// ComponentAccessors returns one accessor per registered component (including
// local-only components), in registration order. Used by entity.inspect /
// entity.modify to enumerate and mutate component fields generically.
func (def *EntityKindDef) ComponentAccessors() []ComponentAccessor {
	out := make([]ComponentAccessor, 0, len(def.components))
	for _, kc := range def.components {
		if kc.buildAccessor != nil {
			out = append(out, kc.buildAccessor())
		}
	}
	return out
}
