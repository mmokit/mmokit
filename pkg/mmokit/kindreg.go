// Package-internal kind registration. Game code uses mmokit.RegisterKind[T]
// to declare entity kinds via a typed component-bundle struct. Reflection
// enumerates the bundle's *ComponentType fields and registers each as a
// KindComponent on every cell's Stage via ark's type-erased TypeID +
// Unsafe.Add primitives.
//
// Pattern mirrors pkg/query/query.go: a generic bundle struct serves as
// both the kind spec (here) and the query iterator (there).

package mmokit

import (
	"fmt"
	"reflect"

	"github.com/mlange-42/ark/ecs"
	"github.com/zenion/mmoserver/pkg/system"
	"github.com/zenion/mmoserver/pkg/universe"
)

// KindFieldSpec is a per-component descriptor produced by Field[T]().
// Each Field call captures the compile-time component type T so that
// RegisterKind can wire cross-cell transfer + client replication without
// losing generic type information to reflection.
type KindFieldSpec struct {
	// compType is the reflect.Type of the component struct (not the pointer).
	compType reflect.Type
	// build creates the full registrar for one component given a live *ecs.World.
	// Called inside the realize closure (once per cell).
	build func(*ecs.World) kindFieldRegistrar
}

// kindFieldRegistrar holds closures for one component once it is bound to a
// specific *ecs.World (and therefore a specific *ecs.Map1[T] instance).
type kindFieldRegistrar struct {
	registerTransfer func(*universe.EntityKindDef)
	addNetworkBinding func(*universe.EntityKindDef)
}

// Field returns a KindFieldSpec for component type T.
// T must be a struct (the component value type, not a pointer).
// Pass one Field[T]() per exported field in the bundle struct, in the
// same order as the fields appear in the bundle.
//
// Example:
//
//	mmokit.RegisterKind[PlayerComponents](mmo, KindPlayer, "Player", bindings,
//	    mmokit.Field[PlayerName](),
//	    mmokit.Field[DebugInfo](),
//	    mmokit.Field[mmokit.MoveTarget](),
//	)
func Field[T any]() KindFieldSpec {
	ct := reflect.TypeFor[T]()
	if ct.Kind() != reflect.Struct {
		panic(fmt.Sprintf("mmokit.Field: T must be a struct, got %v", ct.Kind()))
	}
	return KindFieldSpec{
		compType: ct,
		build: func(w *ecs.World) kindFieldRegistrar {
			m := ecs.NewMap1[T](w)
			return kindFieldRegistrar{
				registerTransfer: func(def *universe.EntityKindDef) {
					universe.KindComponent(def, m)
				},
				addNetworkBinding: func(def *universe.EntityKindDef) {
					def.NetworkBindings = append(def.NetworkBindings, system.Component(m))
				},
			}
		},
	}
}

// FieldWithBinding is like Field[T] but uses a caller-supplied ComponentBinding
// for client replication instead of the default reflection-based binding. Use for
// components that need var-tail encoding or other non-reflection serialization.
func FieldWithBinding[T any](binding system.ComponentBinding) KindFieldSpec {
	ct := reflect.TypeFor[T]()
	if ct.Kind() != reflect.Struct {
		panic(fmt.Sprintf("mmokit.FieldWithBinding: T must be a struct, got %v", ct.Kind()))
	}
	return KindFieldSpec{
		compType: ct,
		build: func(w *ecs.World) kindFieldRegistrar {
			m := ecs.NewMap1[T](w)
			return kindFieldRegistrar{
				registerTransfer: func(def *universe.EntityKindDef) {
					universe.KindComponent(def, m)
				},
				addNetworkBinding: func(def *universe.EntityKindDef) {
					def.NetworkBindings = append(def.NetworkBindings, binding)
				},
			}
		},
	}
}

// FieldLocalOnly is like Field[T] but the component is never serialized for
// cross-cell transfer or client replication. Use for components like PlayerInput
// that are always created fresh on the receiving node.
func FieldLocalOnly[T any]() KindFieldSpec {
	ct := reflect.TypeFor[T]()
	if ct.Kind() != reflect.Struct {
		panic(fmt.Sprintf("mmokit.FieldLocalOnly: T must be a struct, got %v", ct.Kind()))
	}
	return KindFieldSpec{
		compType: ct,
		build: func(w *ecs.World) kindFieldRegistrar {
			m := ecs.NewMap1[T](w)
			return kindFieldRegistrar{
				registerTransfer: func(def *universe.EntityKindDef) {
					universe.KindComponentLocalOnly(def, m)
				},
				addNetworkBinding: nil, // not replicated to clients
			}
		},
	}
}

// RegisterKind registers an entity kind on the Process. T is a struct of
// pointer-to-component fields; each field maps to a KindFieldSpec in fields
// (same order, same types). Each field is registered for cross-cell transfer
// and client replication.
//
//	type PlayerComponents struct {
//	    Name       *PlayerName
//	    MoveTarget *mmokit.MoveTarget
//	}
//	mmokit.RegisterKind[PlayerComponents](mmo, KindPlayer, "Player", playerBindings,
//	    mmokit.Field[PlayerName](),
//	    mmokit.Field[mmokit.MoveTarget](),
//	)
func RegisterKind[T any](p *universe.Process, kind uint8, name string, bindings EngineBindingsConfig, fields ...KindFieldSpec) {
	realize := buildKindSpec[T](kind, name, &bindings, fields, nil)
	p.RegisterKindSpec(realize)
}

// buildKindSpec is the reflection core of RegisterKind. It validates T's
// fields against the provided KindFieldSpecs once (at call time) and returns
// a closure that, given a *Stage, registers an EntityKindDef with full
// transfer + replication wiring.
//
// The optional notify argument is for testing — called with each component
// reflect.Type as the spec is validated, before any Stage exists.
func buildKindSpec[T any](kind uint8, name string, bindings *EngineBindingsConfig, fields []KindFieldSpec, notify func(reflect.Type)) func(*universe.Stage) {
	bundleType := reflect.TypeFor[T]()
	if bundleType.Kind() != reflect.Struct {
		panic(fmt.Sprintf("mmokit.RegisterKind: T must be a struct, got %v", bundleType.Kind()))
	}

	// Collect exported pointer-to-struct fields from the bundle.
	var compTypes []reflect.Type
	for i := range bundleType.NumField() {
		f := bundleType.Field(i)
		if !f.IsExported() {
			continue
		}
		if f.Type.Kind() != reflect.Pointer {
			panic(fmt.Sprintf("mmokit.RegisterKind: bundle field %s.%s must be a pointer (got %v)", bundleType.Name(), f.Name, f.Type.Kind()))
		}
		compType := f.Type.Elem()
		if compType.Kind() != reflect.Struct {
			panic(fmt.Sprintf("mmokit.RegisterKind: bundle field %s.%s must point to a struct (got *%v)", bundleType.Name(), f.Name, compType.Kind()))
		}
		compTypes = append(compTypes, compType)
		if notify != nil {
			notify(compType)
		}
	}

	if len(compTypes) == 0 {
		panic(fmt.Sprintf("mmokit.RegisterKind: bundle struct %s has no exported pointer-to-struct fields", bundleType.Name()))
	}

	// Validate that the provided KindFieldSpecs match the bundle fields.
	if len(fields) != len(compTypes) {
		panic(fmt.Sprintf("mmokit.RegisterKind: bundle %s has %d exported fields but %d Field() specs provided; each exported field needs exactly one Field[T]() call in the same order",
			bundleType.Name(), len(compTypes), len(fields)))
	}
	for i, ct := range compTypes {
		if fields[i].compType != ct {
			panic(fmt.Sprintf("mmokit.RegisterKind: bundle %s field %d type mismatch: bundle has %v but Field() spec has %v",
				bundleType.Name(), i, ct, fields[i].compType))
		}
	}

	// Snapshot the field build fns (the KindFieldSpec slice is caller-owned).
	buildFns := make([]func(*ecs.World) kindFieldRegistrar, len(fields))
	for i, f := range fields {
		buildFns[i] = f.build
	}

	return func(base *universe.Stage) {
		def := universe.EntityKindDef{Kind: kind, Name: name}
		if bindings != nil {
			def.EngineBindings = bindings
		}
		w := base.ECSWorld()
		for _, bf := range buildFns {
			reg := bf(w)
			if reg.registerTransfer != nil {
				reg.registerTransfer(&def)
			}
			if reg.addNetworkBinding != nil {
				reg.addNetworkBinding(&def)
			}
		}
		base.RegisterEntityKind(def)
	}
}
