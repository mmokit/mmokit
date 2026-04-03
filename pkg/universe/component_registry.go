package universe

import (
	"reflect"

	"github.com/mlange-42/ark/ecs"
)

// ComponentOption configures how a component is marshaled during replication.
type ComponentOption[T any] func(*componentConfig[T])

type componentConfig[T any] struct {
	marshal    func(*T) []byte
	unmarshal  func([]byte, *T)
	preMarshal func(*T)
}

// WithMarshal overrides the default reflection-based marshal/unmarshal with
// custom functions. When provided, ValidateComponentType is not called.
func WithMarshal[T any](marshal func(*T) []byte, unmarshal func([]byte, *T)) ComponentOption[T] {
	return func(c *componentConfig[T]) {
		c.marshal = marshal
		c.unmarshal = unmarshal
	}
}

// WithPreMarshal registers a function that runs on a copy of the component
// before marshaling. Useful for clearing entity references or other fields
// that should not be sent over the wire.
func WithPreMarshal[T any](fn func(*T)) ComponentOption[T] {
	return func(c *componentConfig[T]) {
		c.preMarshal = fn
	}
}

// RegisterComponent registers an ECS component for automatic replication and
// transfer. It creates a ComponentReplicator with Scan, Apply, and Add closures
// that capture the typed *ecs.Map1[T].
//
// If no WithMarshal option is provided, the component type is validated at
// registration time and reflection-based marshal/unmarshal is used.
func RegisterComponent[T any](reg *ReplicationRegistry, id ComponentID, m *ecs.Map1[T], opts ...ComponentOption[T]) {
	var cfg componentConfig[T]
	for _, o := range opts {
		o(&cfg)
	}

	// Resolve marshal/unmarshal functions.
	marshalFn := cfg.marshal
	unmarshalFn := cfg.unmarshal
	if marshalFn == nil {
		// Validate at registration time — panics on unsupported field types.
		ValidateComponentType(reflect.TypeFor[T]())
		marshalFn = func(c *T) []byte { return ReflectMarshal(c) }
		unmarshalFn = func(data []byte, c *T) { ReflectUnmarshal(data, c) }
	}

	preMarshal := cfg.preMarshal

	reg.Register(ComponentReplicator{
		ID: id,
		Scan: func(entity ecs.Entity) []byte {
			if !m.HasAll(entity) {
				return nil
			}
			c := m.Get(entity)
			if preMarshal != nil {
				// Work on a copy to avoid mutating the original.
				tmp := *c
				preMarshal(&tmp)
				return marshalFn(&tmp)
			}
			return marshalFn(c)
		},
		Apply: func(entity ecs.Entity, data []byte) {
			if !m.HasAll(entity) {
				return
			}
			unmarshalFn(data, m.Get(entity))
		},
		Add: func(entity ecs.Entity, data []byte) {
			if m.HasAll(entity) {
				// Entity already has this component (e.g. from CreateReplica
				// or SpawnFromTransferCore) — update in place.
				unmarshalFn(data, m.Get(entity))
				return
			}
			var comp T
			unmarshalFn(data, &comp)
			m.Add(entity, &comp)
		},
	})
}
