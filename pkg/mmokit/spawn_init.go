package mmokit

import (
	"fmt"
	"reflect"
	"unsafe"

	ecs "github.com/mlange-42/ark/ecs"
	"github.com/zenion/mmoserver/pkg/universe"
)

// Init populates a typed component bundle after the entity is spawned and all
// kind components are attached. T is inferred from the function argument's type.
//
//	stage.SpawnEntity(pos,
//	    mmokit.WithEntityKind(KindPlayer),
//	    mmokit.Init(func(c *PlayerComponents) { c.Name.Name = "alice" }),
//	)
//
// Reflection walks T's exported pointer-to-struct fields once per Init call;
// each field's (component type, struct offset) is captured in a closure. At
// spawn time the closure fetches component pointers via ark's
// world.Unsafe().Get(entity, id) and writes them into a freshly-allocated
// bundle before calling fn.
func Init[T any](fn func(*T)) universe.SpawnOption {
	bundleType := reflect.TypeFor[T]()
	if bundleType.Kind() != reflect.Struct {
		panic(fmt.Sprintf("mmokit.Init: T must be a struct, got %v", bundleType.Kind()))
	}

	type fieldMeta struct {
		compType reflect.Type
		offset   uintptr
	}
	var fields []fieldMeta
	for i := range bundleType.NumField() {
		f := bundleType.Field(i)
		if !f.IsExported() {
			continue
		}
		if f.Type.Kind() != reflect.Ptr || f.Type.Elem().Kind() != reflect.Struct {
			panic(fmt.Sprintf("mmokit.Init: bundle field %s.%s must be a pointer to a struct", bundleType.Name(), f.Name))
		}
		fields = append(fields, fieldMeta{compType: f.Type.Elem(), offset: f.Offset})
	}
	if len(fields) == 0 {
		panic(fmt.Sprintf("mmokit.Init: bundle struct %s has no exported pointer-to-struct fields", bundleType.Name()))
	}

	return universe.WithPostInit(func(w *ecs.World, e ecs.Entity) {
		bundle := new(T)
		base := unsafe.Pointer(bundle)
		u := w.Unsafe()
		for _, fm := range fields {
			id := ecs.TypeID(w, fm.compType)
			ptr := u.Get(e, id)
			*(*unsafe.Pointer)(unsafe.Add(base, fm.offset)) = ptr
		}
		fn(bundle)
	})
}
