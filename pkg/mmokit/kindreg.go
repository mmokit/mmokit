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
	"github.com/zenion/mmoserver/pkg/universe"
)

// RegisterKind registers an entity kind on the Process. T is a struct of
// pointer-to-component fields; each field is added as a KindComponent on
// every cell's Stage during cell construction.
//
//	type PlayerComponents struct {
//	    Name       *PlayerName
//	    MoveTarget *mmokit.MoveTarget
//	}
//	mmokit.RegisterKind[PlayerComponents](mmo, KindPlayer, "Player", playerBindings)
func RegisterKind[T any](p *universe.Process, kind uint8, name string, bindings EngineBindingsConfig) {
	realize := buildKindSpec[T](kind, name, &bindings, nil)
	p.RegisterKindSpec(realize)
}

// buildKindSpec is the reflection core of RegisterKind. It validates T's
// fields once (cheap) and returns a closure that, given a *Stage,
// registers an EntityKindDef with one component per bundle field.
//
// The optional notify argument is for testing — called with each component
// reflect.Type as the spec is built, before any Stage exists.
func buildKindSpec[T any](kind uint8, name string, bindings *EngineBindingsConfig, notify func(reflect.Type)) func(*universe.Stage) {
	bundleType := reflect.TypeFor[T]()
	if bundleType.Kind() != reflect.Struct {
		panic(fmt.Sprintf("mmokit.RegisterKind: T must be a struct, got %v", bundleType.Kind()))
	}

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

	return func(base *universe.Stage) {
		def := universe.EntityKindDef{Kind: kind, Name: name}
		if bindings != nil {
			def.EngineBindings = bindings
		}
		w := base.ECSWorld()
		for _, ct := range compTypes {
			id := ecs.TypeID(w, ct)
			universe.AddKindComponentByID(&def, w, id)
		}
		base.RegisterEntityKind(def)
	}
}
