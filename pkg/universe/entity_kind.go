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
	Kind           uint8
	Name           string                       // human-readable name for schema export (e.g. "Player")
	EngineBindings *system.EngineBindingsConfig // nil = use defaults
	components     []kindComponent

	// NetworkBindings stores ComponentBinding values for the network
	// replication AutoReplicator. Set by mmokit.KindComponent.
	NetworkBindings []system.ComponentBinding
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

// KindComponent registers a component type on an EntityKindDef. The component
// will be included in cross-cell transfers and auto-filled on transfer receive.
//
// For network replication support, use the mmokit.KindComponent wrapper instead,
// which also registers a ComponentBinding for the AutoReplicator.
//
// This is a package-level function because Go does not support generic methods.
func KindComponent[T any](def *EntityKindDef, m *ecs.Map1[T], opts ...ComponentOption) {
	def.components = append(def.components, kindComponent{
		registerTransfer: func(reg *ReplicationRegistry) {
			RegisterComponent(reg, m, opts...)
		},
		ensureExists: func(entity ecs.Entity) {
			if !m.HasAll(entity) {
				m.Add(entity, new(T))
			}
		},
	})
}

// KindComponentLocalOnly registers a component that is added locally after transfer
// (via EnsureEntityKindComponents) but never serialized for cross-cell transfer.
// Use for components like PlayerInput that are always created fresh on the receiving node.
//
// This is a package-level function because Go does not support generic methods.
func KindComponentLocalOnly[T any](def *EntityKindDef, m *ecs.Map1[T]) {
	def.components = append(def.components, kindComponent{
		// registerTransfer is nil — not serialized for cross-cell transfer
		ensureExists: func(entity ecs.Entity) {
			if !m.HasAll(entity) {
				m.Add(entity, new(T))
			}
		},
	})
}

// KindComponentByID is the type-erased counterpart to KindComponent. Works
// from a pre-resolved ecs.ID and an *ecs.World rather than a typed
// *ecs.Map1[T] — used by mmokit.RegisterKind[T] which walks a bundle struct
// via reflection and resolves each field's component type via ecs.TypeID.
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
}

// Components returns the number of registered components.
func (def *EntityKindDef) Components() int {
	return len(def.components)
}
