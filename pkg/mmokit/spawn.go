package mmokit

import (
	"fmt"
	"reflect"
	"unsafe"

	"github.com/mlange-42/ark/ecs"
	"github.com/zenion/mmoserver/pkg/component"
	pkguniverse "github.com/zenion/mmoserver/pkg/universe"
)

// KindID identifies an entity kind registered with the stage.
type KindID uint8

// Pos is the spawn position in cell-local coordinates.
type Pos struct{ X, Y float32 }

// Spawn creates a new entity of the given kind on the stage at pos, applying
// any supplied component overrides. Returns the resulting Entity.
//
// The kind's default component set is auto-attached (zero-valued); for each
// override in components, that component's data is copied onto the entity.
// Component override types must match those declared in the kind's
// EntityKindDef — passing an unknown type panics with a clear message.
func Spawn(stage *pkguniverse.Stage, kind KindID, pos Pos, components ...any) Entity {
	h := stage.SpawnEntity(
		component.Position{X: pos.X, Y: pos.Y},
		pkguniverse.WithEntityKind(uint8(kind)),
	)

	if h == (ecs.Entity{}) {
		return Entity{}
	}

	w := stage.ECSWorld()
	u := w.Unsafe()
	for _, c := range components {
		v := reflect.ValueOf(c)
		if v.Kind() == reflect.Pointer {
			v = v.Elem()
		}
		t := v.Type()
		id := ecs.TypeID(w, t)
		if !u.Has(h, id) {
			panic(fmt.Sprintf("mmokit.Spawn: component %s not registered on kind %d", t.String(), kind))
		}
		ptr := u.Get(h, id)
		reflect.NewAt(t, unsafe.Pointer(ptr)).Elem().Set(v)
	}

	return EntityFromECS(stage, h)
}

// Despawn marks the entity for removal. The actual ECS removal happens at
// the next FlushRemovals in the simulation tick. Safe on dead/zero entities
// (no-op).
func Despawn(e Entity) {
	if e.stage == nil {
		return
	}
	h := e.resolveHandle()
	if h == (ecs.Entity{}) {
		return
	}
	e.stage.MarkForRemoval(h)
}
