package universe

import (
	"reflect"

	"github.com/mlange-42/ark/ecs"

	"github.com/zenion/mmoserver/pkg/system"
)

// EntityKindDef describes an entity kind's components for transfer replication,
// client network replication, and schema export. Build one per entity type and
// pass it to Stage.RegisterEntityKind.
type EntityKindDef struct {
	Kind       uint8
	Name       string // human-readable name for schema export (e.g. "Player")
	components []kindComponent

	// requiredTypes lists the reflect.Type of every non-local Bundle field
	// declared by this kind. Stage.Spawn uses this under InvariantPanic to
	// verify callers attached every required component (catches the
	// "forgot to Set Health, NPC spawns dead-on-arrival" bug-class).
	// Fields tagged mmokit:"local" are excluded — they are transfer-local
	// state added on the receive side, not caller-required.
	requiredTypes []reflect.Type

	// NetworkBindings stores ComponentBinding values for the network
	// replication AutoReplicator. Populated by mmokit.RegisterKind[T]'s
	// bundle-walker when realizing each stage.
	NetworkBindings []system.ComponentBinding
}

// requiredFieldTypes returns the reflect.Types of every non-local Bundle
// field this kind declares — the set Spawn's debug invariant uses to verify
// callers attached every required component.
func (def *EntityKindDef) requiredFieldTypes() []reflect.Type {
	return def.requiredTypes
}

// kindComponent holds closures for one component's registration across subsystems.
type kindComponent struct {
	// registerTransfer registers this component with a ReplicationRegistry
	// for cross-cell entity transfers.
	registerTransfer func(reg *ReplicationRegistry)

	// ensureExists adds a zero-value component to an entity if it doesn't already
	// have one. Used for auto-filling on transfer receive and WithComponents().
	ensureExists func(entity ecs.Entity)
}

// KindComponentByID registers a component on an EntityKindDef from a
// pre-resolved ecs.ID + reflect.Type. The component is included in
// cross-cell transfers (unless localOnly=true) and auto-filled on
// transfer receive (and on WithComponents() spawn).
//
// Used by mmokit.RegisterKind[T] which walks a bundle struct via
// reflection and resolves each field's component type via ecs.TypeID.
// All access goes through World.Unsafe() — no typed Map1[T].
//
// localOnly=true skips transfer-codec registration but still ensures the
// component is added on transfer receive (for local-only state that must
// exist on the destination but doesn't carry serialized data over the wire).
//
// Internal API; game code uses mmokit.RegisterKind[T].
func KindComponentByID(
	def *EntityKindDef,
	w *ecs.World,
	id ecs.ID,
	t reflect.Type,
	localOnly bool,
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
	if !localOnly {
		kc.registerTransfer = func(reg *ReplicationRegistry) {
			RegisterComponentByID(reg, w, id, t, opts...)
		}
	}
	def.components = append(def.components, kc)
	if !localOnly {
		def.requiredTypes = append(def.requiredTypes, t)
	}
}

// Components returns the number of registered components.
func (def *EntityKindDef) Components() int {
	return len(def.components)
}
